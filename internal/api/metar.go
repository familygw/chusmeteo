package api

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Observaciones reales de aeropuertos (METAR/SPECI) vía aviationweather.gov,
// sin clave. Son lo único "en vivo" que dice GR (granizo) y TS (tormenta).
var metarURL = "https://aviationweather.gov/api/data/metar"

// Station es una estación con lo observado en las últimas horas.
type Station struct {
	ICAO, Name string
	DistKm     float64
	Time       time.Time // último reporte
	Raw        string
	Weather    string    // fenómenos del último reporte ("TSRA GR"; "" si nada)
	HailAt     time.Time // último reporte con granizo (cero si no hubo)
	ThunderAt  time.Time // último reporte con tormenta (cero si no hubo)
}

// HasWeather dice si el código de tiempo presente incluye tok como grupo propio
// ("TSGR" incluye GR; "GRAVEL" no existe en METAR, así que alcanza con buscar subcadena
// sobre grupos separados).
func hasWx(wx, tok string) bool {
	for _, g := range strings.Fields(wx) {
		g = strings.TrimLeft(g, "+-")
		for i := 0; i+len(tok) <= len(g); i += 2 { // los códigos van de a 2 letras
			if g[i:i+len(tok)] == tok {
				return true
			}
		}
	}
	return false
}

// GetStations devuelve las estaciones a menos de ~radiusDeg grados, de la más cercana a la más lejana.
func GetStations(ctx context.Context, loc Location, radiusDeg float64) ([]Station, error) {
	q := url.Values{}
	q.Set("bbox", fmt.Sprintf("%.2f,%.2f,%.2f,%.2f",
		loc.Latitude-radiusDeg, loc.Longitude-radiusDeg, loc.Latitude+radiusDeg, loc.Longitude+radiusDeg))
	q.Set("format", "json")
	q.Set("hours", "3")

	var resp []struct {
		ICAO    string  `json:"icaoId"`
		Name    string  `json:"name"`
		ObsTime int64   `json:"obsTime"`
		Wx      string  `json:"wxString"`
		Raw     string  `json:"rawOb"`
		Lat     float64 `json:"lat"`
		Lon     float64 `json:"lon"`
	}
	if err := getJSON(ctx, metarURL+"?"+q.Encode(), &resp); err != nil {
		return nil, fmt.Errorf("METAR: %w", err)
	}

	by := map[string]*Station{}
	for _, r := range resp {
		t := time.Unix(r.ObsTime, 0)
		s := by[r.ICAO]
		if s == nil {
			name, _, _ := strings.Cut(r.Name, ",")
			s = &Station{ICAO: r.ICAO, Name: name, DistKm: haversineKm(loc.Latitude, loc.Longitude, r.Lat, r.Lon)}
			by[r.ICAO] = s
		}
		if hasWx(r.Wx, "GR") || hasWx(r.Wx, "GS") {
			if t.After(s.HailAt) {
				s.HailAt = t
			}
		}
		if hasWx(r.Wx, "TS") && t.After(s.ThunderAt) {
			s.ThunderAt = t
		}
		if t.After(s.Time) {
			s.Time, s.Raw, s.Weather = t, r.Raw, r.Wx
		}
	}
	out := make([]Station, 0, len(by))
	for _, s := range by {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DistKm < out[j].DistKm })
	return out, nil
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 6371 * 2 * math.Asin(math.Sqrt(a))
}
