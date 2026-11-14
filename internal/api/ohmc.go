package api

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"chusmeteo/internal/geo"
)

// Radares argentinos (red SINARAME: RMA1 Córdoba, RMA2 Ezeiza, Paraná...) vía
// la API pública del visor WebMet del Observatorio Hidro-Meteorológico de
// Córdoba (OHMC, https://webmet.ohmc.ar). Es la misma que usa su página, sin
// clave. A diferencia de Rain Viewer:
//   - cubre el centro del país (Rain Viewer no tiene estos radares);
//   - da reflectividad REAL en dBZ, no colores que hay que adivinar;
//   - hay un cuadro cada ~9 minutos, con pocos minutos de demora.
//
// Usamos COLMAX: para cada punto, el eco más fuerte de toda la columna. Es el
// producto que mejor muestra los núcleos con granizo, que suelen estar altos.
var ohmcURL = "https://webmet.ohmc.ar/api/v1"

const (
	ohmcProduct  = "COLMAX"   // filtrado: sin los ecos fijos de las sierras
	ohmcColormap = "OHMC_dBZ" // pedimos siempre la misma paleta para poder invertirla
	ohmcMaxKm    = 200.0      // el radar ve ~240 km; más lejos que esto no nos sirve
	ohmcStale    = 40 * time.Minute
)

var errNoRadar = errors.New("no hay radar argentino cerca")

// DBZLevel pasa reflectividad a los niveles 0..4 del resto de la app.
// Regla de dedo de los radaristas: desde ~45 dBZ es tormenta fuerte y desde
// ~55 dBZ es muy probable que haya granizo en la nube.
func DBZLevel(dbz float64) int {
	switch {
	case dbz < 20:
		return 0
	case dbz < 35:
		return 1
	case dbz < 45:
		return 2
	case dbz < 55:
		return 3
	}
	return 4
}

type ohmcRadar struct {
	Code   string  `json:"code"`
	Title  string  `json:"title"`
	Lat    float64 `json:"center_lat"`
	Lon    float64 `json:"center_long"`
	Active bool    `json:"is_active"`
}

// Lo que casi no cambia (lista de radares, paleta) se pide una vez por
// ejecución. No usamos sync.Once porque si falla queremos reintentar.
var (
	ohmcMu      sync.Mutex
	ohmcRadars  []ohmcRadar
	ohmcPalette map[color.RGBA]float64 // color -> dBZ
)

func getOHMCRadars(ctx context.Context) ([]ohmcRadar, error) {
	ohmcMu.Lock()
	defer ohmcMu.Unlock()
	if ohmcRadars != nil {
		return ohmcRadars, nil
	}
	var resp struct {
		Radars []ohmcRadar `json:"radars"`
	}
	if err := getJSON(ctx, ohmcURL+"/radars?active_only=true", &resp); err != nil {
		return nil, err
	}
	ohmcRadars = resp.Radars
	return ohmcRadars, nil
}

// getOHMCPalette baja la paleta (256 colores repartidos entre vmin y vmax) y
// la da vuelta: de color a dBZ.
func getOHMCPalette(ctx context.Context) (map[color.RGBA]float64, error) {
	ohmcMu.Lock()
	defer ohmcMu.Unlock()
	if ohmcPalette != nil {
		return ohmcPalette, nil
	}
	var resp struct {
		VMin   float64  `json:"vmin"`
		VMax   float64  `json:"vmax"`
		Colors []string `json:"colors"`
	}
	u := fmt.Sprintf("%s/colormap/info/%s?colormap=%s", ohmcURL, ohmcProduct, ohmcColormap)
	if err := getJSON(ctx, u, &resp); err != nil {
		return nil, err
	}
	if len(resp.Colors) < 2 || resp.VMax <= resp.VMin {
		return nil, errors.New("paleta vacía")
	}
	pal := make(map[color.RGBA]float64, len(resp.Colors))
	for i, hex := range resp.Colors {
		v, err := strconv.ParseUint(hex[min(1, len(hex)):], 16, 32) // "#ff2a0c"
		if err != nil || len(hex) != 7 {
			continue
		}
		c := color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}
		// Si un color se repite (pasa en los valores bajos), vale el dBZ más alto.
		pal[c] = resp.VMin + (resp.VMax-resp.VMin)*float64(i)/float64(len(resp.Colors)-1)
	}
	ohmcPalette = pal
	return pal, nil
}

// nearestRadar: el radar activo más cercano, si está a distancia útil.
func nearestRadar(radars []ohmcRadar, pt geo.LatLon) (ohmcRadar, bool) {
	best, bestKm := ohmcRadar{}, math.Inf(1)
	for _, r := range radars {
		if d := haversineKm(pt.Lat, pt.Lon, r.Lat, r.Lon); r.Active && d < bestKm {
			best, bestKm = r, d
		}
	}
	return best, bestKm <= ohmcMaxKm
}

type ohmcFrame struct {
	ID   int64
	Time time.Time
	Area geo.Box
}

// getOHMCFrames devuelve los últimos cuadros de corto alcance (~240 km) de un
// radar, del más viejo al más nuevo: como mucho maxN y no más viejos que
// `window` respecto del último. Cada radar alterna barridos: los de vigilancia
// (400+ km) tienen menos detalle y los descartamos.
func getOHMCFrames(ctx context.Context, code string, maxN int, window time.Duration) ([]ohmcFrame, error) {
	q := url.Values{}
	q.Set("radar_code", code)
	q.Set("product_key", ohmcProduct)
	q.Set("page_size", strconv.Itoa(maxN*2+4)) // la mitad son de vigilancia
	var resp struct {
		Cogs []struct {
			ID       int64   `json:"id"`
			Time     string  `json:"observation_time"`
			Coverage float64 `json:"radar_coverage_m"`
			BBox     struct {
				W float64 `json:"min_lon"`
				S float64 `json:"min_lat"`
				E float64 `json:"max_lon"`
				N float64 `json:"max_lat"`
			} `json:"bbox"`
		} `json:"cogs"`
	}
	if err := getJSON(ctx, ohmcURL+"/cogs?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	var out []ohmcFrame
	for _, c := range resp.Cogs {
		t, err := time.Parse(time.RFC3339, c.Time)
		if err != nil || c.Coverage > 300_000 {
			continue
		}
		out = append(out, ohmcFrame{c.ID, t, geo.Box{North: c.BBox.N, South: c.BBox.S, East: c.BBox.E, West: c.BBox.W}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	if len(out) == 0 {
		return nil, errors.New("sin cuadros")
	}
	newest := out[len(out)-1].Time
	for len(out) > maxN || newest.Sub(out[0].Time) > window {
		out = out[1:]
	}
	if age := time.Since(newest); age > ohmcStale {
		return nil, fmt.Errorf("último cuadro de hace %.0f min", age.Minutes())
	}
	return out, nil
}

// ohmcLevel arma la función color -> nivel 0..4 a partir de la paleta.
func ohmcLevel(pal map[color.RGBA]float64) func(color.RGBA) int {
	return func(c color.RGBA) int {
		if c.A < 128 {
			return 0 // transparente: sin eco (o fuera del alcance del radar)
		}
		c.A = 255
		dbz, ok := pal[c]
		if !ok {
			return 0
		}
		return DBZLevel(dbz)
	}
}

// GetLocalRadar arma el índice de cuadros del radar argentino más cercano a
// loc (la última ~hora y media), para la vista Radar con zoom regional.
func GetLocalRadar(ctx context.Context, loc Location) (*RadarIndex, error) {
	radars, err := getOHMCRadars(ctx)
	if err != nil {
		return nil, err
	}
	radar, ok := nearestRadar(radars, geo.LatLon{Lat: loc.Latitude, Lon: loc.Longitude})
	if !ok {
		return nil, errNoRadar
	}
	frames, err := getOHMCFrames(ctx, radar.Code, 10, 100*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("radar %s: %w", radar.Code, err)
	}
	area := frames[len(frames)-1].Area
	if area.North <= area.South || area.East <= area.West {
		return nil, fmt.Errorf("radar %s: sin extensión", radar.Code)
	}
	idx := &RadarIndex{Source: radar.Code + " " + radar.Title, Area: &area}
	for _, f := range frames {
		idx.Frames = append(idx.Frames, RadarFrame{Time: f.Time, Path: fmt.Sprintf("ohmc:%d", f.ID)})
	}
	return idx, nil
}

// GetRadarIndexFor: el radar local si hay uno cerca de loc y anda; si no (o si
// loc es nil), el mosaico de Rain Viewer.
func GetRadarIndexFor(ctx context.Context, loc *Location) (*RadarIndex, error) {
	if loc != nil {
		if idx, err := GetLocalRadar(ctx, *loc); err == nil {
			return idx, nil
		}
	}
	return GetRadarIndex(ctx)
}

// fetchOHMCFrame baja un cuadro entero como UNA imagen (473x473 px, ~1 km por
// píxel): el mismo pedido que hace el visor del Observatorio.
func fetchOHMCFrame(ctx context.Context, idx *RadarIndex, f RadarFrame) (*RadarImage, error) {
	pal, err := getOHMCPalette(ctx)
	if err != nil {
		return nil, fmt.Errorf("paleta: %w", err)
	}
	id := strings.TrimPrefix(f.Path, "ohmc:")
	downloadSlots <- struct{}{}
	img, err := fetchImage(ctx, fmt.Sprintf("%s/frames/%s/image.png?colormap=%s&vmin=-30&vmax=70", ohmcURL, id, ohmcColormap))
	<-downloadSlots
	if err != nil || img == nil {
		return nil, fmt.Errorf("radar %s: cuadro no disponible (%v)", f.Time.Local().Format("15:04"), err)
	}
	return &RadarImage{Frame: f, single: img, area: *idx.Area, level: ohmcLevel(pal)}, nil
}

// ohmcNowcast arma el nowcast con el radar argentino más cercano. Devuelve
// error si no hay radar cerca, si está caído o si el último cuadro es viejo:
// en esos casos el que llama prueba con otra fuente.
func ohmcNowcast(ctx context.Context, loc Location, cache map[string]*RadarGrid) (*Nowcast, map[string]*RadarGrid, error) {
	center := geo.LatLon{Lat: loc.Latitude, Lon: loc.Longitude}
	radars, err := getOHMCRadars(ctx)
	if err != nil {
		return nil, nil, err
	}
	radar, ok := nearestRadar(radars, center)
	if !ok {
		return nil, nil, errNoRadar
	}
	frames, err := getOHMCFrames(ctx, radar.Code, 3, 25*time.Minute)
	if err != nil {
		return nil, nil, fmt.Errorf("radar %s: %w", radar.Code, err)
	}
	pal, err := getOHMCPalette(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("paleta: %w", err)
	}
	level := ohmcLevel(pal)

	fx, fy := tileXY(center, NowcastZoom)
	x0, y0 := int(math.Floor(fx-0.5)), int(math.Floor(fy-0.5))
	next := map[string]*RadarGrid{}
	var grids []*RadarGrid
	var lastErr error
	for _, f := range frames {
		key := fmt.Sprintf("ohmc:%d", f.ID)
		g := cache[key]
		if g == nil {
			img, err := fetchTiles(ctx, RadarFrame{Time: f.Time}, NowcastZoom, x0, y0, x0+1, y0+1, func(x, y int) string {
				return fmt.Sprintf("%s/tiles/%d/%d/%d/%d.png?colormap=%s&vmin=-30&vmax=70",
					ohmcURL, f.ID, NowcastZoom, x, y, ohmcColormap)
			})
			if err != nil {
				lastErr = err
				continue
			}
			g = gridFromImage(img, center, level)
		}
		next[key] = g
		grids = append(grids, g)
	}
	if len(grids) == 0 || !grids[len(grids)-1].Time.Equal(frames[len(frames)-1].Time) {
		return nil, next, fmt.Errorf("radar %s: %w", radar.Code, lastErr)
	}
	nc := Analyze(grids)
	nc.Source = fmt.Sprintf("radar %s %s", radar.Code, radar.Title)
	nc.DBZ = true
	return nc, next, nil
}
