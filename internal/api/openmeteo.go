// Package api habla con Open-Meteo (https://open-meteo.com) y Rain Viewer.
//
// Go no tiene clases: este paquete expone tipos (structs) y funciones.
// Todo lo que empieza con Mayúscula es público (exportado); minúscula = privado
// al paquete. Equivale a public/private en Java, pero por convención de nombre.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Son variables (no constantes) para que los tests las apunten a un servidor
// falso con httptest. Es la forma más simple de "inyección de dependencias" en Go.
var (
	geocodingURL = "https://geocoding-api.open-meteo.com/v1/search"
	forecastURL  = "https://api.open-meteo.com/v1/forecast"
)

// Un único http.Client compartido (como un bean singleton).
// Siempre poné Timeout: el default de Go es "esperar para siempre".
var httpClient = &http.Client{Timeout: 10 * time.Second}

// ErrNotFound es un error "centinela": se compara con errors.Is(err, ErrNotFound).
var ErrNotFound = errors.New("ciudad no encontrada")

// Location es un resultado del geocoding.
// Los `json:"..."` son "struct tags": le dicen a encoding/json qué campo mapear
// (parecido a @JsonProperty de Jackson).
type Location struct {
	Name      string  `json:"name"`
	Admin1    string  `json:"admin1"` // provincia / estado
	Country   string  `json:"country"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// Label arma "Córdoba, Córdoba, Argentina" sin repetir vacíos.
// (l Location) es el "receiver": así se definen métodos en Go.
func (l Location) Label() string {
	parts := []string{l.Name}
	if l.Admin1 != "" && l.Admin1 != l.Name {
		parts = append(parts, l.Admin1)
	}
	if l.Country != "" {
		parts = append(parts, l.Country)
	}
	return strings.Join(parts, ", ")
}

// Geocode busca una ciudad por nombre. Acepta "Córdoba" o "Córdoba, AR"
// (código de país ISO de 2 letras después de la coma).
//
// Fijate la firma: devuelve (valor, error). En Go no hay excepciones;
// los errores son valores que se chequean explícitamente.
func Geocode(ctx context.Context, query string) (Location, error) {
	name, country := splitCountry(query)

	q := url.Values{}
	q.Set("name", name)
	q.Set("count", "1")
	q.Set("language", "es")
	q.Set("format", "json")
	if country != "" {
		q.Set("countryCode", country)
	}

	// Struct anónimo: sólo lo necesitamos acá para desarmar el JSON.
	var resp struct {
		Results []Location `json:"results"`
	}
	if err := getJSON(ctx, geocodingURL+"?"+q.Encode(), &resp); err != nil {
		return Location{}, fmt.Errorf("geocoding: %w", err) // %w "envuelve" el error original
	}
	if len(resp.Results) == 0 {
		return Location{}, fmt.Errorf("%w: %q", ErrNotFound, query)
	}
	return resp.Results[0], nil
}

// Current son las condiciones actuales.
type Current struct {
	Time          string  `json:"time"`
	Temperature   float64 `json:"temperature_2m"`
	ApparentTemp  float64 `json:"apparent_temperature"`
	Humidity      float64 `json:"relative_humidity_2m"`
	WeatherCode   int     `json:"weather_code"`
	WindSpeed     float64 `json:"wind_speed_10m"`
	WindDirection float64 `json:"wind_direction_10m"`
	Pressure      float64 `json:"surface_pressure"`
	IsDay         int     `json:"is_day"`
	CloudCover    float64 `json:"cloud_cover"`
	WindGusts     float64 `json:"wind_gusts_10m"`
	Precip        float64 `json:"precipitation"`
}

// Daily viene "columnar": un slice por variable, todos del mismo largo.
type Daily struct {
	Time        []string  `json:"time"`
	WeatherCode []int     `json:"weather_code"`
	TempMax     []float64 `json:"temperature_2m_max"`
	TempMin     []float64 `json:"temperature_2m_min"`
	PrecipProb  []float64 `json:"precipitation_probability_max"`
	Sunrise     []string  `json:"sunrise"`
	Sunset      []string  `json:"sunset"`
	UVMax       []float64 `json:"uv_index_max"`
}

// Hourly: próximas 24 horas, también columnar.
type Hourly struct {
	Time        []string  `json:"time"`
	Temperature []float64 `json:"temperature_2m"`
	PrecipProb  []float64 `json:"precipitation_probability"`
	WeatherCode []int     `json:"weather_code"`
}

// Forecast es la respuesta completa que nos interesa.
type Forecast struct {
	Timezone string  `json:"timezone"`
	Current  Current `json:"current"`
	Daily    Daily   `json:"daily"`
	Hourly   Hourly  `json:"hourly"`
}

// Hour es una fila del pronóstico horario.
type Hour struct {
	Time        time.Time
	Temperature float64
	PrecipProb  float64
	WeatherCode int
}

// Hours convierte el formato columnar en filas (descarta filas incompletas).
func (f *Forecast) Hours() []Hour {
	h := f.Hourly
	n := min(len(h.Time), len(h.Temperature), len(h.PrecipProb)) // min es built-in desde Go 1.21
	out := make([]Hour, 0, n)
	for i := range n {
		t, err := time.Parse("2006-01-02T15:04", h.Time[i])
		if err != nil {
			continue
		}
		hr := Hour{Time: t, Temperature: h.Temperature[i], PrecipProb: h.PrecipProb[i]}
		if i < len(h.WeatherCode) {
			hr.WeatherCode = h.WeatherCode[i]
		}
		out = append(out, hr)
	}
	return out
}

// SunTimes devuelve salida y puesta del sol de hoy como "HH:MM" ("" si no hay).
func (f *Forecast) SunTimes() (rise, set string) {
	hhmm := func(s []string) string {
		if len(s) == 0 || len(s[0]) < 16 {
			return ""
		}
		return s[0][11:16] // "2026-10-24T06:58" -> "06:58": slicing de strings por bytes
	}
	return hhmm(f.Daily.Sunrise), hhmm(f.Daily.Sunset)
}

// Day es una fila "armada" del pronóstico diario, más cómoda para la UI.
type Day struct {
	Date        time.Time
	WeatherCode int
	Max, Min    float64
	PrecipProb  float64
}

// Days convierte el formato columnar en filas.
func (f *Forecast) Days() []Day {
	days := make([]Day, 0, len(f.Daily.Time))
	for i, ds := range f.Daily.Time {
		d, err := time.Parse("2006-01-02", ds) // sí, el layout de fechas en Go es esa fecha mágica
		if err != nil {
			continue
		}
		day := Day{Date: d}
		if i < len(f.Daily.WeatherCode) {
			day.WeatherCode = f.Daily.WeatherCode[i]
		}
		if i < len(f.Daily.TempMax) {
			day.Max = f.Daily.TempMax[i]
		}
		if i < len(f.Daily.TempMin) {
			day.Min = f.Daily.TempMin[i]
		}
		if i < len(f.Daily.PrecipProb) {
			day.PrecipProb = f.Daily.PrecipProb[i]
		}
		days = append(days, day)
	}
	return days
}

// GetForecast trae condiciones actuales, 7 días y las próximas 24 horas.
func GetForecast(ctx context.Context, loc Location) (*Forecast, error) {
	q := url.Values{}
	q.Set("latitude", fmt.Sprintf("%.4f", loc.Latitude))
	q.Set("longitude", fmt.Sprintf("%.4f", loc.Longitude))
	q.Set("current", strings.Join([]string{
		"temperature_2m", "apparent_temperature", "relative_humidity_2m",
		"weather_code", "wind_speed_10m", "wind_direction_10m",
		"surface_pressure", "is_day", "cloud_cover", "wind_gusts_10m", "precipitation",
	}, ","))
	q.Set("daily", "weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max,sunrise,sunset,uv_index_max")
	q.Set("hourly", "temperature_2m,precipitation_probability,weather_code")
	q.Set("forecast_hours", "24") // 24 pasos horarios a partir de la hora actual
	q.Set("timezone", "auto")
	q.Set("forecast_days", "7")

	var f Forecast
	if err := getJSON(ctx, forecastURL+"?"+q.Encode(), &f); err != nil {
		return nil, fmt.Errorf("forecast: %w", err)
	}
	return &f, nil
}

// getJSON hace un GET y decodifica el cuerpo en `out` (que debe ser un puntero).
func getJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "chusmeteo/0.1 (hobby)")

	res, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close() // defer = "finally": corre al salir de la función

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// splitCountry separa "Córdoba, AR" en ("Córdoba", "AR").
func splitCountry(q string) (name, country string) {
	q = strings.TrimSpace(q)
	i := strings.LastIndex(q, ",")
	if i < 0 {
		return q, ""
	}
	cc := strings.TrimSpace(q[i+1:])
	if len(cc) == 2 {
		return strings.TrimSpace(q[:i]), strings.ToUpper(cc)
	}
	return q, "" // la parte después de la coma no era un código de país
}
