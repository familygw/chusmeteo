package api

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/png" // import "en blanco": registra el decoder PNG para image.Decode
	"math"
	"net/http"
	"sync"
	"time"

	"chusmeteo/internal/geo"
)

// Rain Viewer: radar de precipitación, gratis para uso personal/educativo.
// Exige atribución visible ("Radar: Rain Viewer") — la mostramos en la vista.
// Desde 2026 el tier libre tiene zoom máximo 7 y un único esquema de color.
var rainviewerIndexURL = "https://api.rainviewer.com/public/weather-maps.json"

const (
	// RadarZoom 4: cada tile cubre 22.5° de longitud; Argentina entra en 2x3 tiles.
	// Para un mapa de ~1 píxel por grado sobra resolución.
	RadarZoom   = 4
	tileSize    = 256
	colorScheme = 2     // "Universal Blue", el único disponible en el tier libre
	tileOptions = "1_0" // suavizado sí, nieve en color aparte no
)

// RadarFrame es una "foto" del radar (una cada 10 minutos, últimas 2 horas).
type RadarFrame struct {
	Time time.Time
	Path string
}

// RadarIndex es la lista de frames disponibles.
type RadarIndex struct {
	Host   string
	Frames []RadarFrame // ordenados del más viejo al más nuevo
	// Source y Area sólo los llena un radar local (ver ohmc.go): Area nil
	// quiere decir "mosaico de Rain Viewer, todo el país".
	Source string
	Area   *geo.Box
}

// GetRadarIndex trae el índice de frames.
func GetRadarIndex(ctx context.Context) (*RadarIndex, error) {
	var resp struct {
		Host  string `json:"host"`
		Radar struct {
			Past []struct {
				Time int64  `json:"time"`
				Path string `json:"path"`
			} `json:"past"`
		} `json:"radar"`
	}
	if err := getJSON(ctx, rainviewerIndexURL, &resp); err != nil {
		return nil, fmt.Errorf("radar: %w", err)
	}
	if resp.Host == "" || len(resp.Radar.Past) == 0 {
		return nil, errors.New("radar: índice vacío")
	}
	idx := &RadarIndex{Host: resp.Host}
	for _, p := range resp.Radar.Past {
		idx.Frames = append(idx.Frames, RadarFrame{Time: time.Unix(p.Time, 0), Path: p.Path})
	}
	return idx, nil
}

// RadarImage son los tiles de un frame, listos para muestrear por lat/lon.
type RadarImage struct {
	Frame RadarFrame
	tiles map[[2]int]image.Image // clave {x, y} del tile; nil = tile sin datos
	zoom  int                    // 0 = RadarZoom (el mapa nacional)

	// Radar local: una sola imagen que cubre `area`, con su propia tabla
	// color -> nivel (dBZ reales).
	single image.Image
	area   geo.Box
	level  func(color.RGBA) int
}

// Level clasifica el eco en un punto en 0..4. `def` es la tabla para los
// colores de Rain Viewer; un radar local trae la suya.
func (r *RadarImage) Level(pt geo.LatLon, def func(color.RGBA) int) int {
	if r.level != nil {
		return r.level(r.Sample(pt))
	}
	return def(r.Sample(pt))
}

// NewRadarImage arma un frame a partir de tiles ya decodificados (útil en tests).
func NewRadarImage(f RadarFrame, tiles map[[2]int]image.Image) *RadarImage {
	return &RadarImage{Frame: f, tiles: tiles}
}

// NewLocalRadarImage arma un cuadro de radar local a partir de una imagen que
// cubre `area` (útil en tests).
func NewLocalRadarImage(f RadarFrame, img image.Image, area geo.Box, level func(color.RGBA) int) *RadarImage {
	return &RadarImage{Frame: f, single: img, area: area, level: level}
}

// TileRange devuelve el rango de tiles {x0, y0, x1, y1} que cubre el mapa.
func TileRange() (x0, y0, x1, y1 int) {
	fx0, fy0 := tileXY(geo.LatLon{Lat: geo.Bounds.North, Lon: geo.Bounds.West}, RadarZoom)
	fx1, fy1 := tileXY(geo.LatLon{Lat: geo.Bounds.South, Lon: geo.Bounds.East}, RadarZoom)
	return int(fx0), int(fy0), int(fx1), int(fy1)
}

// TileLatLon es la inversa de tileXY: coordenada de un píxel de un tile.
func TileLatLon(tx, ty, px, py int) geo.LatLon {
	n := math.Exp2(RadarZoom)
	x := float64(tx) + (float64(px)+0.5)/tileSize
	y := float64(ty) + (float64(py)+0.5)/tileSize
	return geo.LatLon{
		Lon: x/n*360 - 180,
		Lat: math.Atan(math.Sinh(math.Pi*(1-2*y/n))) * 180 / math.Pi,
	}
}

// Semáforo global: como máximo 6 descargas a la vez, sin importar cuántos
// frames pida la UI en paralelo. Un channel con buffer es el semáforo
// idiomático en Go: mandar = tomar un lugar, recibir = liberarlo.
var downloadSlots = make(chan struct{}, 6)

// FetchRadarFrame baja un cuadro: la imagen del radar local, o todos los
// tiles de Rain Viewer que cubren Argentina, en paralelo.
func FetchRadarFrame(ctx context.Context, idx *RadarIndex, f RadarFrame) (*RadarImage, error) {
	if idx.Area != nil {
		return fetchOHMCFrame(ctx, idx, f)
	}
	x0, y0, x1, y1 := TileRange()
	return fetchTiles(ctx, f, RadarZoom, x0, y0, x1, y1, rainviewerTile(idx.Host, f, RadarZoom))
}

// rainviewerTile arma la URL de un tile de Rain Viewer. Devuelve una función
// (un "closure" que recuerda host, frame y zoom): así fetchTiles sirve para
// cualquier servidor de tiles.
func rainviewerTile(host string, f RadarFrame, zoom int) func(x, y int) string {
	return func(x, y int) string {
		return fmt.Sprintf("%s%s/%d/%d/%d/%d/%d/%s.png", host, f.Path, tileSize, zoom, x, y, colorScheme, tileOptions)
	}
}

// fetchTiles baja el rectángulo de tiles [x0..x1] x [y0..y1] a un zoom dado.
func fetchTiles(ctx context.Context, f RadarFrame, zoom, x0, y0, x1, y1 int, tileURL func(x, y int) string) (*RadarImage, error) {
	img := &RadarImage{Frame: f, tiles: map[[2]int]image.Image{}, zoom: zoom}
	var (
		wg       sync.WaitGroup // espera a que terminen todas las goroutines
		mu       sync.Mutex     // protege el map y firstErr (los maps no son thread-safe)
		firstErr error
	)
	for x := x0; x <= x1; x++ {
		for y := y0; y <= y1; y++ {
			wg.Add(1)
			go func() { // desde Go 1.22 cada iteración tiene su propia x, y: no hace falta copiarlas
				defer wg.Done()
				downloadSlots <- struct{}{}
				defer func() { <-downloadSlots }()

				t, err := fetchImage(ctx, tileURL(x, y))

				mu.Lock()
				defer mu.Unlock()
				if err != nil && firstErr == nil {
					firstErr = err
				}
				img.tiles[[2]int{x, y}] = t
			}()
		}
	}
	wg.Wait()
	if firstErr != nil {
		return nil, fmt.Errorf("radar %s: %w", f.Time.Format("15:04"), firstErr)
	}
	return img, nil
}

// Sample devuelve el color del radar en una coordenada (A=0 si no hay lluvia).
func (r *RadarImage) Sample(pt geo.LatLon) color.RGBA {
	if r.single != nil {
		if !r.area.Has(pt) {
			return color.RGBA{}
		}
		b := r.single.Bounds()
		px := b.Min.X + int((pt.Lon-r.area.West)/(r.area.East-r.area.West)*float64(b.Dx()))
		py := b.Min.Y + int((r.area.North-pt.Lat)/(r.area.North-r.area.South)*float64(b.Dy()))
		return color.RGBAModel.Convert(r.single.At(min(px, b.Max.X-1), min(py, b.Max.Y-1))).(color.RGBA)
	}
	zoom := r.zoom
	if zoom == 0 {
		zoom = RadarZoom
	}
	fx, fy := tileXY(pt, zoom)
	tx, ty := int(math.Floor(fx)), int(math.Floor(fy))
	t := r.tiles[[2]int{tx, ty}]
	if t == nil {
		return color.RGBA{}
	}
	px := int((fx - float64(tx)) * tileSize)
	py := int((fy - float64(ty)) * tileSize)
	b := t.Bounds()
	// Algunos servidores devuelven tiles de 512: escalamos por las dudas.
	px = b.Min.X + px*b.Dx()/tileSize
	py = b.Min.Y + py*b.Dy()/tileSize
	// color.RGBAModel.Convert pasa cualquier formato (paleta, NRGBA...) a RGBA.
	return color.RGBAModel.Convert(t.At(px, py)).(color.RGBA)
}

// tileXY convierte lat/lon a coordenadas de tile "slippy map" (Web Mercator).
// La parte entera es el tile; la fraccionaria, la posición dentro de él.
func tileXY(pt geo.LatLon, zoom int) (x, y float64) {
	n := math.Exp2(float64(zoom))
	lat := pt.Lat * math.Pi / 180
	x = (pt.Lon + 180) / 360 * n
	y = (1 - math.Asinh(math.Tan(lat))/math.Pi) / 2 * n
	return x, y
}

func fetchImage(ctx context.Context, url string) (image.Image, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "chusmeteo/0.4 (hobby)")
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, nil // tile sin cobertura: no es un error
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tile HTTP %d", res.StatusCode)
	}
	img, _, err := image.Decode(res.Body)
	return img, err
}
