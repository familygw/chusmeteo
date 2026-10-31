package api

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"chusmeteo/internal/geo"
)

// httptest.NewServer levanta un servidor HTTP real en localhost para el test.
// Así probamos el parseo sin depender de internet (y sin gastar cupo).
func fakeServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close) // se cierra solo al terminar el test
	return srv
}

func TestGeocodeAndForecast(t *testing.T) {
	srv := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/geo":
			if got := r.URL.Query().Get("countryCode"); got != "AR" {
				t.Errorf("countryCode = %q", got)
			}
			fmt.Fprint(w, `{"results":[{"name":"Córdoba","admin1":"Córdoba","country":"Argentina","latitude":-31.41,"longitude":-64.18}]}`)
		case "/fc":
			fmt.Fprint(w, `{"timezone":"America/Argentina/Cordoba",
			 "current":{"time":"2026-10-24T00:00","temperature_2m":18.4,"relative_humidity_2m":62,"weather_code":2,"is_day":0},
			 "daily":{"time":["2026-10-24","2026-10-25"],"weather_code":[2,63],"temperature_2m_max":[27,21],"temperature_2m_min":[14,12],"precipitation_probability_max":[5,80],"sunrise":["2026-10-24T06:58"],"sunset":["2026-10-24T20:05"]},
			 "hourly":{"time":["2026-10-24T00:00","2026-10-24T01:00"],"temperature_2m":[18.4,17.9],"precipitation_probability":[0,10],"weather_code":[2,3]}}`)
		}
	})
	geocodingURL, forecastURL = srv.URL+"/geo", srv.URL+"/fc"

	loc, err := Geocode(context.Background(), "Córdoba, ar")
	if err != nil || loc.Label() != "Córdoba, Argentina" {
		t.Fatalf("Geocode = %+v, %v", loc, err)
	}
	fc, err := GetForecast(context.Background(), loc)
	if err != nil {
		t.Fatal(err)
	}
	if fc.Current.Temperature != 18.4 || len(fc.Days()) != 2 || fc.Days()[1].WeatherCode != 63 {
		t.Errorf("forecast mal parseado: %+v", fc)
	}
	if h := fc.Hours(); len(h) != 2 || h[1].PrecipProb != 10 || h[1].Time.Hour() != 1 {
		t.Errorf("horario mal parseado: %+v", h)
	}
	if rise, set := fc.SunTimes(); rise != "06:58" || set != "20:05" {
		t.Errorf("sol: %q %q", rise, set)
	}
}

func TestGeocodeNotFound(t *testing.T) {
	srv := fakeServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) })
	geocodingURL = srv.URL
	_, err := Geocode(context.Background(), "Macondo")
	if err == nil || !strings.Contains(err.Error(), "no encontrada") {
		t.Errorf("esperaba ErrNotFound, llegó %v", err)
	}
}

func TestGetGrid(t *testing.T) {
	srv := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := len(strings.Split(r.URL.Query().Get("latitude"), ","))
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf(`{"latitude":0,"current":{"temperature_2m":%d,"weather_code":%d}}`, 10+i, i)
		}
		fmt.Fprint(w, "["+strings.Join(parts, ",")+"]")
	})
	forecastURL = srv.URL

	pts := []geo.LatLon{{Lat: -31, Lon: -64}, {Lat: -34, Lon: -58}, {Lat: -54, Lon: -68}}
	got, err := GetGrid(context.Background(), pts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2].Temperature != 12 || got[2].Lat != -54 {
		t.Errorf("grilla: %+v", got)
	}
}

func TestGetGridSinglePoint(t *testing.T) {
	srv := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"current":{"temperature_2m":7.5,"weather_code":3}}`)
	})
	forecastURL = srv.URL
	got, err := GetGrid(context.Background(), []geo.LatLon{{Lat: -54, Lon: -68}})
	if err != nil || len(got) != 1 || got[0].Temperature != 7.5 {
		t.Errorf("punto único: %+v, %v", got, err)
	}
}

// Radar: el servidor falso sirve el índice y tiles PNG generados en memoria.
func TestRadar(t *testing.T) {
	rain := color.RGBA{R: 30, G: 90, B: 220, A: 255}
	var tileHits atomic.Int32 // contador seguro entre goroutines

	var srv *httptest.Server
	srv = fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index" {
			fmt.Fprintf(w, `{"host":%q,"radar":{"past":[{"time":1761264000,"path":"/v2/radar/abc"},{"time":1761264600,"path":"/v2/radar/def"}]}}`, srv.URL)
			return
		}
		tileHits.Add(1)
		if !strings.HasPrefix(r.URL.Path, "/v2/radar/def/256/4/") || !strings.HasSuffix(r.URL.Path, "/2/1_0.png") {
			t.Errorf("URL de tile inesperada: %s", r.URL.Path)
		}
		img := image.NewRGBA(image.Rect(0, 0, 256, 256))
		for y := 0; y < 256; y++ {
			for x := 0; x < 128; x++ { // mitad izquierda "llueve"
				img.Set(x, y, rain)
			}
		}
		var buf bytes.Buffer
		_ = png.Encode(&buf, img)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(buf.Bytes())
	})
	rainviewerIndexURL = srv.URL + "/index"

	idx, err := GetRadarIndex(context.Background())
	if err != nil || len(idx.Frames) != 2 {
		t.Fatalf("índice: %+v, %v", idx, err)
	}
	last := idx.Frames[len(idx.Frames)-1]
	ri, err := FetchRadarFrame(context.Background(), idx.Host, last)
	if err != nil {
		t.Fatal(err)
	}
	if n := tileHits.Load(); n != 6 {
		t.Errorf("esperaba 6 tiles (2x3), bajó %d", n)
	}
	// Tile x=4 cubre lon -90..-67.5; su mitad izquierda es -90..-78.75 (fuera del
	// mapa). Tile x=5 cubre -67.5..-45: mitad izquierda -67.5..-56.25.
	if c := ri.Sample(geo.LatLon{Lat: -31, Lon: -64}); c != rain {
		t.Errorf("Córdoba debería tener lluvia, color %v", c)
	}
	if c := ri.Sample(geo.LatLon{Lat: -31, Lon: -70}); c.A != 0 {
		t.Errorf("-70 cae en la mitad derecha del tile 4: sin lluvia, color %v", c)
	}
}
