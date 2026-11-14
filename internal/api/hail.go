package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Granizo según los modelos globales (ICON, GFS, ECMWF, GEM), vía Open-Meteo.
//
// ICON es de los pocos que pronostican granizo EXPLÍCITO: los códigos WMO 96/99
// ("tormenta con granizo"); los demás sólo dicen 95. Aparecen también en
// Argentina (visto el 13/11/2026 en Córdoba), pero de a ratos: el grueso del
// riesgo sale de los ingredientes:
//   - CAPE: energía disponible para que una burbuja de aire suba (J/kg).
//     Más CAPE = corrientes ascendentes más fuertes = granizo más grande.
//   - Altura de la isoterma de 0 °C: cuanto más baja, menos se derrite el
//     granizo al caer.
//   - Cizalladura: el viento que cambia con la altura organiza las tormentas.
//   - SHIP: un índice del servicio de EE.UU. que junta todo lo anterior.
// Cada modelo da su riesgo y nos quedamos con el que sostienen al menos dos;
// el ensamble (80 corridas) dice qué tan firme es. El "riesgo" es una
// ESTIMACIÓN nuestra, no un producto oficial.

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
	SHIP          float64 // Significant Hail Parameter más alto entre los modelos (0 si no se pudo calcular)
	Models        int     // modelos con datos para esta hora
	Votes         int     // de esos, cuántos ven algún riesgo
	Prob          float64 // fracción del ensamble que ve tormenta (-1 sin dato)
	Downgraded    bool    // el ensamble casi no ve tormenta y bajamos un escalón
}

// ModelPeak resume lo que vio un modelo en todo el período.
type ModelPeak struct {
	Name    string
	Max     HailRisk
	MaxCAPE float64
}

// HailOutlook resume las próximas horas.
type HailOutlook struct {
	Hours    []HailHour
	Max      HailRisk
	MaxCAPE  float64
	MinFZL   float64 // isoterma 0 °C más baja del período
	Explicit bool
	Crossed  bool // hubo más de un modelo para contrastar
	Disagree bool // algún modelo vio riesgo moderado o más y el consenso no lo acompañó
	Models   []ModelPeak
	Members  int     // miembros del ensamble (0 = sin ensamble)
	MaxProb  float64 // mayor fracción del ensamble con tormenta (-1 sin dato)
	MaxSHIP  float64
}

// MaxWithin: el riesgo más alto en las próximas d horas (desde la primera del pronóstico).
func (o *HailOutlook) MaxWithin(d time.Duration) HailRisk {
	var r HailRisk
	for _, h := range o.Hours {
		if h.Time.Sub(o.Hours[0].Time) < d {
			r = max(r, h.Risk)
		}
	}
	return r
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
	Prob     float64 // mayor fracción del ensamble con tormenta en el tramo (sólo vale si Outlook.Members > 0)
	SHIP     float64 // SHIP más alto del tramo
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
			eps[n-1].Prob = max(eps[n-1].Prob, h.Prob)
			eps[n-1].SHIP = max(eps[n-1].SHIP, h.SHIP)
			continue
		}
		eps = append(eps, Episode{From: h.Time, To: h.Time, Peak: h.Risk, Prob: h.Prob, SHIP: h.SHIP})
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

// Modelos globales que se contrastan. Ninguno "ve" una tormenta individual
// (resuelven ~10-25 km), y entre sí difieren mucho: el 13/11/2026 daban para
// Córdoba, a la misma hora, CAPE de 1170 (ECMWF) a 4040 J/kg (GEM). Por eso no
// le creemos a uno solo.
var hailModels = []struct{ ID, Name string }{
	{"icon_seamless", "ICON"},
	{"gfs_seamless", "GFS"},
	{"ecmwf_ifs025", "ECMWF"},
	{"gem_seamless", "GEM"},
}

var ensembleURL = "https://ensemble-api.open-meteo.com/v1/ensemble"

const (
	hailHorizon = 48 // horas de pronóstico
	// Un miembro del ensamble "ve tormenta" si en la hora (±1 h) tiene energía y lluvia.
	stormCAPE   = 500.0
	stormPrecip = 0.3
)

// columns es la respuesta "columnar" de Open-Meteo con claves dinámicas:
// con varios modelos, cada variable viene como "cape_icon_seamless".
// json.RawMessage posterga el parseo hasta saber qué columna queremos.
type columns map[string]json.RawMessage

func (c columns) floats(key string) []*float64 {
	var out []*float64
	if raw, ok := c[key]; ok {
		_ = json.Unmarshal(raw, &out) // columna rota = sin datos
	}
	return out
}

func (c columns) times() []time.Time {
	var raw []string
	_ = json.Unmarshal(c["time"], &raw)
	out := make([]time.Time, len(raw))
	for i, ts := range raw {
		out[i], _ = time.Parse("2006-01-02T15:04", ts)
	}
	return out
}

// val: valor i de una columna (ok=false si falta o es null).
func val(s []*float64, i int) (float64, bool) {
	if i < 0 || i >= len(s) || s[i] == nil {
		return 0, false
	}
	return *s[i], true
}

// GetHailOutlook pide a Open-Meteo las próximas 48 h de varios modelos (una
// sola llamada) y, en paralelo, el ensamble (opcional: si falla, seguimos).
func GetHailOutlook(ctx context.Context, loc Location) (*HailOutlook, error) {
	q := url.Values{}
	q.Set("latitude", fmt.Sprintf("%.4f", loc.Latitude))
	q.Set("longitude", fmt.Sprintf("%.4f", loc.Longitude))
	q.Set("hourly", "weather_code,cape,freezing_level_height,precipitation,"+
		"wind_speed_10m,wind_direction_10m,wind_speed_500hPa,wind_direction_500hPa,"+
		"temperature_500hPa,temperature_700hPa,geopotential_height_500hPa,geopotential_height_700hPa,"+
		"dew_point_2m,surface_pressure")
	ids := make([]string, len(hailModels))
	for i, m := range hailModels {
		ids[i] = m.ID
	}
	q.Set("models", strings.Join(ids, ","))
	q.Set("forecast_hours", strconv.Itoa(hailHorizon))
	q.Set("timezone", "auto")

	// El ensamble corre en otra goroutine; el channel con buffer 1 deja que
	// termine aunque nadie lo espere (si la llamada principal falla).
	ensCh := make(chan *ensemble, 1)
	go func() { ensCh <- getEnsemble(ctx, loc) }()

	var resp struct {
		Elevation float64 `json:"elevation"`
		Hourly    columns `json:"hourly"`
	}
	if err := getJSON(ctx, forecastURL+"?"+q.Encode(), &resp); err != nil {
		return nil, fmt.Errorf("modelos: %w", err)
	}
	ens := <-ensCh

	times := resp.Hourly.times()
	// Una "vista" por modelo con sus columnas ya parseadas.
	type view struct {
		name                              string
		code, cape, fzl, prec             []*float64
		ws10, wd10, ws5, wd5              []*float64
		t500, t700, z500, z700, dew, psfc []*float64
		max                               HailRisk
		maxCAPE                           float64
		any                               bool
	}
	views := make([]*view, len(hailModels))
	for i, m := range hailModels {
		col := func(v string) []*float64 { return resp.Hourly.floats(v + "_" + m.ID) }
		views[i] = &view{name: m.Name,
			code: col("weather_code"), cape: col("cape"), fzl: col("freezing_level_height"), prec: col("precipitation"),
			ws10: col("wind_speed_10m"), wd10: col("wind_direction_10m"),
			ws5: col("wind_speed_500hPa"), wd5: col("wind_direction_500hPa"),
			t500: col("temperature_500hPa"), t700: col("temperature_700hPa"),
			z500: col("geopotential_height_500hPa"), z700: col("geopotential_height_700hPa"),
			dew: col("dew_point_2m"), psfc: col("surface_pressure")}
	}

	o := &HailOutlook{MaxProb: -1}
	if ens != nil {
		o.Members = ens.members
	}
	for i, t := range times {
		if t.IsZero() {
			continue
		}
		hh := HailHour{Time: t, Prob: -1}
		// La isoterma sólo la dan ICON y GFS: los demás usan la primera disponible.
		fzl := 0.0
		for _, v := range views {
			if f, ok := val(v.fzl, i); ok && f > 0 {
				fzl = f
				break
			}
		}
		var risks []HailRisk
		for _, v := range views {
			cape, okC := val(v.cape, i)
			codeF, okW := val(v.code, i)
			if !okC && !okW {
				continue // este modelo no tiene datos para esta hora
			}
			code := int(codeF)
			in := HailInputs{Code: code, CAPE: cape, FreezingLevel: fzl, Shear: -1, Precip: -1}
			if p, ok := val(v.prec, i); ok {
				in.Precip = p
			}
			s10, ok1 := val(v.ws10, i)
			d10, ok2 := val(v.wd10, i)
			s5, ok3 := val(v.ws5, i)
			d5, ok4 := val(v.wd5, i)
			if ok1 && ok2 && ok3 && ok4 {
				in.Shear = windShear(s10, d10, s5, d5)
			}
			r := EstimateHail(in)

			// SHIP: sólo si el modelo trae todo lo necesario.
			ship := -1.0
			t500, a1 := val(v.t500, i)
			t700, a2 := val(v.t700, i)
			z500, a3 := val(v.z500, i)
			z700, a4 := val(v.z700, i)
			dew, a5 := val(v.dew, i)
			psfc, a6 := val(v.psfc, i)
			if okC && a1 && a2 && a3 && a4 && a5 && a6 && in.Shear >= 0 {
				ship = SHIP(SHIPInputs{CAPE: cape, DewPoint: dew, Pressure: psfc, T500: t500, T700: t700,
					Z500: z500, Z700: z700, ShearKmh: in.Shear, FreezingAGL: fzl - resp.Elevation})
				// SHIP mide el AMBIENTE; hace falta además que el modelo dispare lluvia.
				if in.Precip != 0 {
					switch {
					case ship >= 1 && r < HailHigh:
						r = HailHigh
					case ship >= 0.5 && r < HailModerate:
						r = HailModerate
					}
				}
			}
			explicit := code == 96 || code == 99

			if len(risks) == 0 { // ICON va primero y es la referencia; si falta, el siguiente
				hh.Code, hh.CAPE, hh.FreezingLevel = code, cape, fzl
			}
			hh.Explicit = hh.Explicit || explicit
			hh.SHIP = max(hh.SHIP, ship)
			hh.Models++
			if r >= HailLow {
				hh.Votes++
			}
			risks = append(risks, r)
			v.any = true
			v.max = max(v.max, r)
			v.maxCAPE = max(v.maxCAPE, cape)
		}
		if len(risks) == 0 {
			continue
		}
		hh.Risk = consensus(risks)
		if hh.Explicit {
			hh.Risk = HailHigh // decisión directa de un modelo: no se discute
		}
		if top := slices.Max(risks); top >= HailModerate && hh.Risk < top {
			o.Disagree = true
		}
		if len(risks) > 1 {
			o.Crossed = true
		}
		// Ensamble: si casi ningún miembro ve tormenta a esa hora, bajamos un escalón.
		if p, ok := ens.prob(t); ok {
			hh.Prob = p
			o.MaxProb = max(o.MaxProb, p)
			if !hh.Explicit && hh.Risk >= HailModerate && p < 0.10 && ens.members >= 20 {
				hh.Risk--
				hh.Downgraded = true
			}
		}

		o.Hours = append(o.Hours, hh)
		o.Max = max(o.Max, hh.Risk)
		o.MaxCAPE = max(o.MaxCAPE, hh.CAPE)
		o.MaxSHIP = max(o.MaxSHIP, hh.SHIP)
		if hh.FreezingLevel > 0 && (o.MinFZL == 0 || hh.FreezingLevel < o.MinFZL) {
			o.MinFZL = hh.FreezingLevel
		}
		o.Explicit = o.Explicit || hh.Explicit
	}
	if len(o.Hours) == 0 {
		return nil, fmt.Errorf("modelos: respuesta sin datos horarios")
	}
	for _, v := range views {
		if v.any {
			o.Models = append(o.Models, ModelPeak{Name: v.name, Max: v.max, MaxCAPE: v.maxCAPE})
		}
	}
	return o, nil
}

// consensus: el segundo riesgo más alto, o sea el nivel que sostienen al menos
// DOS modelos. Con uno solo, lo que diga ese.
func consensus(risks []HailRisk) HailRisk {
	if len(risks) == 1 {
		return risks[0]
	}
	s := slices.Clone(risks) // no desordenar el slice del que llama
	slices.Sort(s)
	return s[len(s)-2]
}

// SHIPInputs: lo que necesita el Significant Hail Parameter.
type SHIPInputs struct {
	CAPE        float64 // J/kg
	DewPoint    float64 // °C a 2 m
	Pressure    float64 // hPa en superficie
	T500, T700  float64 // °C
	Z500, Z700  float64 // m (altura geopotencial)
	ShearKmh    float64 // superficie a 500 hPa
	FreezingAGL float64 // m sobre el terreno (<= 0: sin dato)
}

// SHIP es el "Significant Hail Parameter" del Storm Prediction Center (EE.UU.):
//
//	SHIP = CAPE · w · Γ · (−T500) · cizalladura / 42.000.000
//
// con w = relación de mezcla (g/kg), Γ = gradiente térmico 700–500 hPa (°C/km)
// y la cizalladura en m/s. Cerca de 1 o más, el ambiente favorece granizo
// grande (≥ 5 cm); por debajo de ~0.3, no. Es un índice de AMBIENTE: no dice
// si la tormenta se forma.
//
// Aproximaciones nuestras: usamos el CAPE del modelo (no el "más inestable"),
// la humedad de superficie y el viento de 500 hPa (~5,5 km) en vez de 6 km.
func SHIP(in SHIPInputs) float64 {
	if in.CAPE <= 0 || in.Z500 <= in.Z700 {
		return 0
	}
	// Presión de vapor (Bolton 1980) y relación de mezcla.
	e := 6.112 * math.Exp(17.67*in.DewPoint/(in.DewPoint+243.5))
	w := 622 * e / (in.Pressure - e)
	lapse := (in.T700 - in.T500) / ((in.Z500 - in.Z700) / 1000)
	shear := in.ShearKmh / 3.6

	// Topes de la fórmula oficial: evitan que un solo ingrediente la dispare.
	w = min(max(w, 11), 13.6)
	shear = min(max(shear, 7), 27)
	t500 := min(in.T500, -5.5)

	ship := in.CAPE * w * lapse * -t500 * shear / 42e6
	if in.CAPE < 1300 {
		ship *= in.CAPE / 1300
	}
	if lapse < 5.8 {
		ship *= lapse / 5.8
	}
	if in.FreezingAGL > 0 && in.FreezingAGL < 2400 {
		ship *= in.FreezingAGL / 2400
	}
	return max(ship, 0)
}

// ensemble: fracción de miembros que ven tormenta, por hora.
type ensemble struct {
	members int
	byHour  map[time.Time]float64
}

// prob funciona también con receiver nil (sin ensamble): en Go se puede
// llamar un método sobre un puntero nil mientras no se lo desreferencie.
func (e *ensemble) prob(t time.Time) (float64, bool) {
	if e == nil {
		return 0, false
	}
	p, ok := e.byHour[t]
	return p, ok
}

// getEnsemble pide los ensambles GEFS (30 miembros) y ECMWF (50). Cada miembro
// es una corrida del modelo con condiciones iniciales apenas distintas: si la
// mayoría coincide, el pronóstico es firme. (El ensamble de ICON no trae CAPE.)
// Devuelve nil si falla: es un extra.
func getEnsemble(ctx context.Context, loc Location) *ensemble {
	q := url.Values{}
	q.Set("latitude", fmt.Sprintf("%.4f", loc.Latitude))
	q.Set("longitude", fmt.Sprintf("%.4f", loc.Longitude))
	q.Set("hourly", "cape,precipitation")
	q.Set("models", "gfs_seamless,ecmwf_ifs025")
	q.Set("forecast_hours", strconv.Itoa(hailHorizon))
	q.Set("timezone", "auto")
	var resp struct {
		Hourly columns `json:"hourly"`
	}
	if err := getJSON(ctx, ensembleURL+"?"+q.Encode(), &resp); err != nil {
		return nil
	}
	times := resp.Hourly.times()
	hits := make([]int, len(times))
	e := &ensemble{byHour: map[time.Time]float64{}}
	for key := range resp.Hourly {
		// "cape_member07_ecmwf_ifs025_ensemble" -> su par es "precipitation_member07_...".
		id, ok := strings.CutPrefix(key, "cape")
		if !ok {
			continue
		}
		cape, prec := resp.Hourly.floats(key), resp.Hourly.floats("precipitation"+id)
		if _, ok := val(cape, 0); !ok || len(prec) == 0 {
			continue
		}
		e.members++
		for i := range times {
			var c, p float64
			for k := i - 1; k <= i+1; k++ { // ±1 h: los miembros no coinciden al minuto
				if v, ok := val(cape, k); ok {
					c = max(c, v)
				}
				if v, ok := val(prec, k); ok {
					p = max(p, v)
				}
			}
			if c >= stormCAPE && p >= stormPrecip {
				hits[i]++
			}
		}
	}
	if e.members == 0 {
		return nil
	}
	for i, t := range times {
		if !t.IsZero() {
			e.byHour[t] = float64(hits[i]) / float64(e.members)
		}
	}
	return e
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

// PeakSHIP: el SHIP más alto entre las horas con riesgo.
func (o *HailOutlook) PeakSHIP() float64 {
	var peak float64
	for _, h := range o.Hours {
		if h.Risk >= HailLow {
			peak = max(peak, h.SHIP)
		}
	}
	return peak
}

// HailSize estima el tamaño probable. Con SHIP (ship > 0) usamos sus umbrales;
// si no, la energía sola. Es orientativo: con poca energía el granizo suele ser
// chico (pero puede caer en cantidad); el grande necesita corrientes
// ascendentes fuertes y tormentas organizadas.
func HailSize(cape, ship float64) string {
	if ship > 0 {
		switch {
		case ship < 0.5:
			return "chico"
		case ship < 1:
			return "mediano"
		}
		return "grande"
	}
	switch {
	case cape < 1500:
		return "chico"
	case cape < 2500:
		return "mediano"
	}
	return "grande"
}
