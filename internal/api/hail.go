package api

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"time"
)

// Granizo según el modelo ICON del servicio alemán (DWD), vía Open-Meteo.
//
// ICON es de los pocos modelos con pronóstico EXPLÍCITO de granizo: es el que
// reporta los códigos WMO 96/99 ("tormenta con granizo"). La mayoría sólo
// dice 95 ("tormenta"). Además pedimos dos variables de inestabilidad:
//   - CAPE: energía disponible para que una burbuja de aire suba (J/kg).
//     Más CAPE = corrientes ascendentes más fuertes = granizo más grande.
//   - Altura de la isoterma de 0 °C: cuanto más baja, menos se derrite el
//     granizo al caer.
// El "riesgo" combinando ambas es una ESTIMACIÓN nuestra, no un producto oficial.

// HailRisk va de 0 (nada) a 3 (alto).
type HailRisk int

const (
	HailNone HailRisk = iota
	HailLow
	HailModerate
	HailHigh
)

func (r HailRisk) String() string {
	return [...]string{"NULO", "BAJO", "MODERADO", "ALTO"}[r]
}

// HailHour es una hora del pronóstico de ICON.
type HailHour struct {
	Time          time.Time
	Code          int     // código WMO según ICON
	CAPE          float64 // J/kg
	FreezingLevel float64 // metros
	Explicit      bool    // ICON pronostica granizo (96/99)
	Risk          HailRisk
	GFSCAPE       float64 // -1 si no hay dato de GFS
	Downgraded    bool    // GFS no ve energía y bajamos el riesgo de ICON
}

// HailOutlook resume las próximas horas.
type HailOutlook struct {
	Hours    []HailHour
	Max      HailRisk
	MaxCAPE  float64
	MinFZL   float64 // isoterma 0 °C más baja del período
	Explicit bool
	Crossed  bool // se pudo contrastar con GFS
	Disagree bool // GFS contradice a ICON en alguna hora de riesgo
}

// Window devuelve la primera y la última hora con riesgo >= r (ok=false si no hay).
func (o *HailOutlook) Window(r HailRisk) (from, to time.Time, ok bool) {
	for _, h := range o.Hours {
		if h.Risk >= r {
			if !ok {
				from, ok = h.Time, true
			}
			to = h.Time
		}
	}
	return from, to, ok
}

// Episode es un tramo continuo de horas con riesgo, con su pico.
type Episode struct {
	From, To time.Time
	Peak     HailRisk
}

// Episodes agrupa las horas con riesgo >= r; une tramos separados por una sola hora.
func (o *HailOutlook) Episodes(r HailRisk) []Episode {
	var eps []Episode
	for _, h := range o.Hours {
		if h.Risk < r {
			continue
		}
		if n := len(eps); n > 0 && h.Time.Sub(eps[n-1].To) <= 2*time.Hour {
			eps[n-1].To = h.Time
			eps[n-1].Peak = max(eps[n-1].Peak, h.Risk)
			continue
		}
		eps = append(eps, Episode{From: h.Time, To: h.Time, Peak: h.Risk})
	}
	return eps
}

// Label da el tramo con el día explícito: "hoy 15–18 h", "mañana 14 h"
// o "hoy 22 h a mañana 02 h". ref es la hora actual del pronóstico.
func (e Episode) Label(ref time.Time) string {
	day := func(t time.Time) string {
		switch int(t.Truncate(24*time.Hour).Sub(ref.Truncate(24*time.Hour)) / (24 * time.Hour)) {
		case 0:
			return "hoy"
		case 1:
			return "mañana"
		}
		return t.Format("02/01")
	}
	from, to := e.From.Format("15"), e.To.Format("15")
	switch {
	case e.From.Equal(e.To):
		return fmt.Sprintf("%s %s h", day(e.From), from)
	case day(e.From) == day(e.To):
		return fmt.Sprintf("%s %s–%s h", day(e.From), from, to)
	}
	return fmt.Sprintf("%s %s h a %s %s h", day(e.From), from, day(e.To), to)
}

// HailInputs: variables del modelo. Shear y Precip en negativo = sin dato.
type HailInputs struct {
	Code          int
	CAPE          float64 // J/kg
	FreezingLevel float64 // m
	Shear         float64 // km/h, viento 500 hPa − viento 10 m (vectorial)
	Precip        float64 // mm/h
}

// EstimateHailRisk: versión sin cizalladura ni precipitación.
func EstimateHailRisk(code int, cape, freezingLevel float64) HailRisk {
	return EstimateHail(HailInputs{code, cape, freezingLevel, -1, -1})
}

// EstimateHail: la heurística, aislada para poder testearla.
func EstimateHail(in HailInputs) HailRisk {
	code, cape, freezingLevel := in.Code, in.CAPE, in.FreezingLevel
	if code == 96 || code == 99 {
		return HailHigh // ICON lo pronostica explícitamente
	}
	var r HailRisk
	switch {
	case cape < 500:
		return HailNone
	case cape < 1500:
		r = HailLow
	case cape < 2500:
		r = HailModerate
	default:
		r = HailHigh
	}
	// Isoterma alta: el granizo chico se derrite antes de llegar al suelo.
	if freezingLevel > 4600 && r > HailNone {
		r--
	}
	// Isoterma baja con algo de energía: el granizo chico llega al suelo.
	// (31/10/2026: cayó granizo en Córdoba con CAPE ~800 J/kg e isoterma ~3600 m.)
	if freezingLevel > 0 && freezingLevel < 3800 && cape >= 600 && r < HailHigh {
		r++
	}
	// Sin tormenta ni chaparrones en el pronóstico, como mucho "bajo".
	if code < 80 && r > HailLow {
		r--
	}
	// Poca cizalladura: tormentas de masa de aire, de vida corta y granizo chico.
	if in.Shear >= 0 && in.Shear < 30 && r > HailLow {
		r--
	}
	// Mucha cizalladura con energía: tormentas organizadas (supercelda).
	if in.Shear >= 60 && cape >= 1000 && code >= 95 && r < HailHigh {
		r++
	}
	// CAPE sin precipitación pronosticada: no se dispara la convección.
	if in.Precip == 0 && r > HailLow {
		r = HailLow
	}
	return r
}

// GetHailOutlook pide a Open-Meteo las próximas 24 h del modelo ICON.
func GetHailOutlook(ctx context.Context, loc Location) (*HailOutlook, error) {
	q := url.Values{}
	q.Set("latitude", fmt.Sprintf("%.4f", loc.Latitude))
	q.Set("longitude", fmt.Sprintf("%.4f", loc.Longitude))
	q.Set("hourly", "weather_code,cape,freezing_level_height,precipitation,"+
		"wind_speed_10m,wind_direction_10m,wind_speed_500hPa,wind_direction_500hPa")
	q.Set("models", "icon_seamless") // con un solo modelo, los campos no llevan sufijo
	q.Set("forecast_hours", "24")
	q.Set("timezone", "auto")

	var resp struct {
		Hourly struct {
			Time []string   `json:"time"`
			Code []*int     `json:"weather_code"` // punteros: la API manda null si falta el dato
			CAPE []*float64 `json:"cape"`
			FZL  []*float64 `json:"freezing_level_height"`
			Prec []*float64 `json:"precipitation"`
			WS10 []*float64 `json:"wind_speed_10m"`
			WD10 []*float64 `json:"wind_direction_10m"`
			WS5  []*float64 `json:"wind_speed_500hPa"`
			WD5  []*float64 `json:"wind_direction_500hPa"`
		} `json:"hourly"`
	}
	if err := getJSON(ctx, forecastURL+"?"+q.Encode(), &resp); err != nil {
		return nil, fmt.Errorf("ICON: %w", err)
	}

	h := resp.Hourly
	o := &HailOutlook{}
	gfs := gfsCAPE(ctx, loc) // opcional: si falla, seguimos sólo con ICON
	for i, ts := range h.Time {
		t, err := time.Parse("2006-01-02T15:04", ts)
		if err != nil {
			continue
		}
		hh := HailHour{Time: t,
			Code:          deref(h.Code, i),
			CAPE:          deref(h.CAPE, i),
			FreezingLevel: deref(h.FZL, i),
		}
		hh.Explicit = hh.Code == 96 || hh.Code == 99
		in := HailInputs{hh.Code, hh.CAPE, hh.FreezingLevel, -1, -1}
		if i < len(h.Prec) && h.Prec[i] != nil {
			in.Precip = *h.Prec[i]
		}
		if i < len(h.WS5) && h.WS5[i] != nil && i < len(h.WD5) && h.WD5[i] != nil &&
			i < len(h.WS10) && h.WS10[i] != nil && i < len(h.WD10) && h.WD10[i] != nil {
			in.Shear = windShear(*h.WS10[i], *h.WD10[i], *h.WS5[i], *h.WD5[i])
		}
		hh.Risk = EstimateHail(in)
		hh.GFSCAPE = -1
		if c, ok := gfs[t]; ok {
			o.Crossed = true
			hh.GFSCAPE = c
			// Explícito (96/99) no se toca: es decisión directa del modelo.
			if !hh.Explicit && hh.Risk >= HailModerate && c < 500 {
				hh.Risk--
				hh.Downgraded = true
				o.Disagree = true
			}
		}

		o.Hours = append(o.Hours, hh)
		o.Max = max(o.Max, hh.Risk)
		o.MaxCAPE = max(o.MaxCAPE, hh.CAPE)
		if hh.FreezingLevel > 0 && (o.MinFZL == 0 || hh.FreezingLevel < o.MinFZL) {
			o.MinFZL = hh.FreezingLevel
		}
		o.Explicit = o.Explicit || hh.Explicit
	}
	if len(o.Hours) == 0 {
		return nil, fmt.Errorf("ICON: respuesta sin datos horarios")
	}
	return o, nil
}

// gfsCAPE devuelve el CAPE horario de GFS por hora local (nil si falla).
func gfsCAPE(ctx context.Context, loc Location) map[time.Time]float64 {
	q := url.Values{}
	q.Set("latitude", fmt.Sprintf("%.4f", loc.Latitude))
	q.Set("longitude", fmt.Sprintf("%.4f", loc.Longitude))
	q.Set("hourly", "cape")
	q.Set("models", "gfs_seamless")
	q.Set("forecast_hours", "24")
	q.Set("timezone", "auto")
	var resp struct {
		Hourly struct {
			Time []string   `json:"time"`
			CAPE []*float64 `json:"cape"`
		} `json:"hourly"`
	}
	if err := getJSON(ctx, forecastURL+"?"+q.Encode(), &resp); err != nil {
		return nil
	}
	out := make(map[time.Time]float64, len(resp.Hourly.Time))
	for i, ts := range resp.Hourly.Time {
		t, err := time.Parse("2006-01-02T15:04", ts)
		if err != nil || i >= len(resp.Hourly.CAPE) || resp.Hourly.CAPE[i] == nil {
			continue
		}
		out[t] = *resp.Hourly.CAPE[i]
	}
	return out
}

// windShear: módulo de la diferencia vectorial entre dos vientos (velocidad, dirección de donde sopla).
func windShear(s1, d1, s2, d2 float64) float64 {
	rad := math.Pi / 180
	u := s2*math.Sin(d2*rad) - s1*math.Sin(d1*rad)
	v := s2*math.Cos(d2*rad) - s1*math.Cos(d1*rad)
	return math.Hypot(u, v)
}

// deref es genérica (Go 1.18+): sirve para []*int y []*float64.
// Devuelve el valor i-ésimo, o el "cero" del tipo si falta o es null.
func deref[T any](s []*T, i int) T {
	var zero T
	if i >= len(s) || s[i] == nil {
		return zero
	}
	return *s[i]
}

// PeakCAPE: el CAPE más alto entre las horas con riesgo (0 si no hay ninguna).
func (o *HailOutlook) PeakCAPE() float64 {
	var peak float64
	for _, h := range o.Hours {
		if h.Risk >= HailLow {
			peak = max(peak, h.CAPE)
		}
	}
	return peak
}

// HailSize estima el tamaño probable según la energía. Es orientativo: con CAPE
// baja el granizo suele ser chico (pero puede caer en cantidad); el grande
// necesita corrientes ascendentes fuertes.
func HailSize(cape float64) string {
	switch {
	case cape < 1500:
		return "chico"
	case cape < 2500:
		return "mediano"
	}
	return "grande"
}
