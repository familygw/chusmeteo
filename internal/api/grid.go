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
	Precip      float64 // mm en la última hora
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
	q.Set("current", "temperature_2m,weather_code,precipitation")
	q.Set("timezone", "auto")

	type item struct {
		Current struct {
			Temperature float64 `json:"temperature_2m"`
			WeatherCode int     `json:"weather_code"`
			Precip      float64 `json:"precipitation"`
		} `json:"current"`
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
		}
	}
	return out, nil
}
