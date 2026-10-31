package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"chusmeteo/internal/geo"
)

// PointWeather es el clima actual en un punto de la grilla.
type PointWeather struct {
	geo.LatLon
	Temperature float64
	WeatherCode int
	Precip      float64     // mm en la última hora
	Wind        float64     // km/h
	WindDir     float64     // grados de donde sopla
	Hourly      []PointHour // desde la hora actual; vacío si no llegó
}

// PointHour es el pronóstico de un punto para una hora.
type PointHour struct {
	Temperature float64
	Precip      float64
	Wind        float64
	WindDir     float64
}

// At devuelve el punto como se verá dentro de h horas (h=0: ahora).
func (p PointWeather) At(h int) PointWeather {
	if h <= 0 || h >= len(p.Hourly) {
		return p
	}
	x := p.Hourly[h]
	p.Temperature, p.Precip, p.Wind, p.WindDir = x.Temperature, x.Precip, x.Wind, x.WindDir
	return p
}

// GetGrid pide el clima actual para muchas coordenadas en UNA sola llamada.
// Open-Meteo acepta latitude=a,b,c&longitude=x,y,z y devuelve un array JSON
// en el mismo orden. Ojo: para el cupo diario cada coordenada cuenta como
// una llamada, así que no te zarpes con la grilla.
func GetGrid(ctx context.Context, pts []geo.LatLon) ([]PointWeather, error) {
	if len(pts) == 0 {
		return nil, nil
	}
	lats := make([]string, len(pts))
	lons := make([]string, len(pts))
	for i, p := range pts {
		lats[i] = strconv.FormatFloat(p.Lat, 'f', 2, 64)
		lons[i] = strconv.FormatFloat(p.Lon, 'f', 2, 64)
	}
	q := url.Values{}
	q.Set("latitude", strings.Join(lats, ","))
	q.Set("longitude", strings.Join(lons, ","))
	q.Set("current", "temperature_2m,weather_code,precipitation,wind_speed_10m,wind_direction_10m")
	q.Set("hourly", "temperature_2m,precipitation,wind_speed_10m,wind_direction_10m")
	q.Set("forecast_hours", "24")
	q.Set("timezone", "auto")

	type item struct {
		Current struct {
			Temperature float64 `json:"temperature_2m"`
			WeatherCode int     `json:"weather_code"`
			Precip      float64 `json:"precipitation"`
			Wind        float64 `json:"wind_speed_10m"`
			WindDir     float64 `json:"wind_direction_10m"`
		} `json:"current"`
		Hourly struct {
			Temperature []*float64 `json:"temperature_2m"`
			Precip      []*float64 `json:"precipitation"`
			Wind        []*float64 `json:"wind_speed_10m"`
			WindDir     []*float64 `json:"wind_direction_10m"`
		} `json:"hourly"`
	}

	// Con una sola coordenada la API devuelve un objeto, no un array.
	// json.RawMessage nos deja mirar el JSON antes de decidir cómo leerlo.
	var raw json.RawMessage
	if err := getJSON(ctx, forecastURL+"?"+q.Encode(), &raw); err != nil {
		return nil, fmt.Errorf("grilla: %w", err)
	}
	var items []item
	if len(raw) > 0 && raw[0] == '{' {
		var one item
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, fmt.Errorf("grilla: %w", err)
		}
		items = []item{one}
	} else if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("grilla: %w", err)
	}
	if len(items) != len(pts) {
		return nil, fmt.Errorf("grilla: pedí %d puntos y llegaron %d", len(pts), len(items))
	}

	out := make([]PointWeather, len(pts))
	for i, it := range items {
		// Usamos NUESTRAS coordenadas: la API devuelve las de su celda de modelo,
		// que pueden estar corridas unos km.
		out[i] = PointWeather{
			LatLon:      pts[i],
			Temperature: it.Current.Temperature,
			WeatherCode: it.Current.WeatherCode,
			Precip:      it.Current.Precip,
			Wind:        it.Current.Wind,
			WindDir:     it.Current.WindDir,
		}
		h := it.Hourly
		for j := range h.Temperature {
			out[i].Hourly = append(out[i].Hourly, PointHour{
				Temperature: deref(h.Temperature, j),
				Precip:      deref(h.Precip, j),
				Wind:        deref(h.Wind, j),
				WindDir:     deref(h.WindDir, j),
			})
		}
	}
	return out, nil
}
