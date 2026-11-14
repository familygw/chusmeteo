package api

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"chusmeteo/internal/geo"
)

func TestDBZLevel(t *testing.T) {
	for dbz, want := range map[float64]int{-5: 0, 19.9: 0, 20: 1, 40: 2, 45: 3, 54.9: 3, 55: 4, 68: 4} {
		if got := DBZLevel(dbz); got != want {
			t.Errorf("DBZLevel(%v) = %d, want %d", dbz, got, want)
		}
	}
}

// Un OHMC de mentira: un radar en Córdoba, tres cuadros y tiles todos rojos (60 dBZ).
func TestOHMCNowcast(t *testing.T) {
	var tile bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			img.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	_ = png.Encode(&tile, img)
	now := time.Now().UTC()
	ts := func(minAgo int) string { return now.Add(-time.Duration(minAgo) * time.Minute).Format(time.RFC3339) }
	var tiles atomic.Int32 // el servidor atiende en paralelo: contador atómico

	srv := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case p == "/radars":
			fmt.Fprint(w, `{"radars":[
				{"code":"RMA1","title":"Cordoba","center_lat":-31.44,"center_long":-64.19,"is_active":true},
				{"code":"RMA2","title":"Ezeiza","center_lat":-34.8,"center_long":-58.5,"is_active":true}]}`)
		case p == "/cogs":
			if r.URL.Query().Get("radar_code") != "RMA1" {
				t.Errorf("radar = %q", r.URL.Query().Get("radar_code"))
			}
			// El de 400 km (vigilancia) se descarta; el de hace 50 min queda afuera por viejo.
			fmt.Fprintf(w, `{"cogs":[
				{"id":4,"observation_time":%q,"radar_coverage_m":236460,"bbox":{"min_lon":-66.7,"min_lat":-33.5,"max_lon":-61.6,"max_lat":-29.3}},
				{"id":9,"observation_time":%q,"radar_coverage_m":446340},
				{"id":3,"observation_time":%q,"radar_coverage_m":236460,"bbox":{"min_lon":-66.7,"min_lat":-33.5,"max_lon":-61.6,"max_lat":-29.3}},
				{"id":2,"observation_time":%q,"radar_coverage_m":236460,"bbox":{"min_lon":-66.7,"min_lat":-33.5,"max_lon":-61.6,"max_lat":-29.3}},
				{"id":1,"observation_time":%q,"radar_coverage_m":236460,"bbox":{"min_lon":-66.7,"min_lat":-33.5,"max_lon":-61.6,"max_lat":-29.3}}]}`, ts(3), ts(4), ts(12), ts(21), ts(50))
		case strings.HasPrefix(p, "/colormap/info/"):
			fmt.Fprint(w, `{"vmin":0,"vmax":60,"colors":["#000001","#00ff00","#ff0000"]}`)
		case strings.HasPrefix(p, "/frames/"):
			w.Header().Set("Content-Type", "image/png")
			w.Write(tile.Bytes())
		case strings.HasPrefix(p, "/tiles/"):
			tiles.Add(1)
			w.Header().Set("Content-Type", "image/png")
			w.Write(tile.Bytes())
		default:
			t.Errorf("pedido inesperado: %s", r.URL)
		}
	})
	ohmcURL = srv.URL
	ohmcRadars, ohmcPalette = nil, nil // los cachés son globales: los vaciamos

	loc := Location{Latitude: -31.47, Longitude: -64.19}
	nc, cache, err := GetNowcast(context.Background(), loc, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if nc.Source != "radar RMA1 Cordoba" || !nc.DBZ || nc.NoCoverage || !nc.Overhead || nc.OverLevel != 4 {
		t.Errorf("nowcast: %+v", nc)
	}
	if len(cache) != 3 || tiles.Load() != 12 { // 3 cuadros x (2x2 tiles)
		t.Errorf("caché %d, tiles %d", len(cache), tiles.Load())
	}
	// Segunda pasada con el caché: no vuelve a bajar tiles.
	if _, _, err := GetNowcast(context.Background(), loc, cache, nil); err != nil || tiles.Load() != 12 {
		t.Errorf("con caché: err=%v tiles=%d", err, tiles.Load())
	}
	// La vista Radar: índice local + un cuadro como imagen única.
	idx, err := GetRadarIndexFor(context.Background(), &loc)
	if err != nil || idx.Area == nil || idx.Source != "RMA1 Cordoba" || len(idx.Frames) != 4 { // la vista usa hasta 100 min: entra también el de hace 50
		t.Fatalf("índice local: %+v %v", idx, err)
	}
	fr, err := FetchRadarFrame(context.Background(), idx, idx.Frames[3])
	if err != nil {
		t.Fatal(err)
	}
	in := geo.LatLon{Lat: -31.47, Lon: -64.19}
	out := geo.LatLon{Lat: -36, Lon: -64.19}
	if fr.Level(in, nil) != 4 || fr.Level(out, nil) != 0 {
		t.Errorf("niveles: adentro %d, afuera %d", fr.Level(in, nil), fr.Level(out, nil))
	}
	// Lejos de todo radar argentino no usa esta fuente.
	if _, _, err := ohmcNowcast(context.Background(), Location{Latitude: -41.1, Longitude: -71.3}, nil); err != errNoRadar {
		t.Errorf("Bariloche: err = %v", err)
	}
}
