// Package geo tiene la geografía para dibujar el mapa: países, provincias
// argentinas y ciudades (de Natural Earth, dominio público), más la proyección
// de lat/lon a píxeles.
package geo

import (
	_ "embed" // necesario para usar //go:embed
	"encoding/json"
	"math"
	"sync"
)

// go:embed mete el archivo DENTRO del binario al compilar: no hay que
// distribuirlo aparte ni saber en qué carpeta está. Lo genera
// tools/nedata/extract.py a partir de Natural Earth 1:10m.
//
//go:embed data/sudamerica.json
var rawData []byte

// LatLon es una coordenada geográfica en grados decimales.
type LatLon struct{ Lat, Lon float64 }

// Polygon es un anillo de vértices; se cierra solo (el último une con el primero).
type Polygon []LatLon

// Region es un país o una provincia: uno o más anillos (islas, agujeros).
type Region struct {
	Name  string
	Code  string // código ISO del país (sólo países)
	Rings []Polygon
	box   bbox
}

type bbox struct{ minLat, maxLat, minLon, maxLon float64 }

func (b bbox) has(pt LatLon) bool {
	return pt.Lat >= b.minLat && pt.Lat <= b.maxLat && pt.Lon >= b.minLon && pt.Lon <= b.maxLon
}

// Contains usa la regla par/impar sobre TODOS los anillos: así los agujeros
// (lagos, enclaves) se restan solos, sin distinguir exterior de interior.
func (r *Region) Contains(pt LatLon) bool {
	if !r.box.has(pt) { // descarte rápido: casi todos los puntos caen acá
		return false
	}
	inside := false
	for _, ring := range r.Rings {
		if ring.crossings(pt)%2 == 1 {
			inside = !inside
		}
	}
	return inside
}

// crossings cuenta cuántos bordes cruza un rayo que sale del punto hacia el este.
func (p Polygon) crossings(pt LatLon) int {
	n := 0
	for i, j := 0, len(p)-1; i < len(p); j, i = i, i+1 {
		a, b := p[i], p[j]
		if (a.Lat > pt.Lat) != (b.Lat > pt.Lat) &&
			pt.Lon < (b.Lon-a.Lon)*(pt.Lat-a.Lat)/(b.Lat-a.Lat)+a.Lon {
			n++
		}
	}
	return n
}

// Contains para un anillo suelto (ray casting).
func (p Polygon) Contains(pt LatLon) bool { return p.crossings(pt)%2 == 1 }

// --- Carga de datos (una sola vez) ---

type world struct {
	countries []*Region // countries[0] es Argentina
	provinces []*Region
}

var (
	loadOnce sync.Once // como un singleton lazy thread-safe de Java, en una línea
	data     world
)

func load() *world {
	loadOnce.Do(func() {
		var raw struct {
			Countries []struct {
				Name  string      `json:"name"`
				Code  string      `json:"code"`
				Rings [][]float64 `json:"rings"`
			} `json:"countries"`
			Provinces []struct {
				Name  string      `json:"name"`
				Rings [][]float64 `json:"rings"`
			} `json:"provinces"`
		}
		if err := json.Unmarshal(rawData, &raw); err != nil {
			panic("geo: datos embebidos corruptos: " + err.Error()) // error de programación, no de runtime
		}
		for _, c := range raw.Countries {
			data.countries = append(data.countries, newRegion(c.Name, c.Code, c.Rings))
		}
		for _, p := range raw.Provinces {
			data.provinces = append(data.provinces, newRegion(p.Name, "", p.Rings))
		}
	})
	return &data
}

// newRegion convierte los anillos "planos" (lon,lat,lon,lat...) y calcula la caja.
func newRegion(name, code string, flat [][]float64) *Region {
	r := &Region{Name: name, Code: code, box: bbox{math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)}}
	for _, f := range flat {
		ring := make(Polygon, 0, len(f)/2)
		for i := 0; i+1 < len(f); i += 2 {
			pt := LatLon{Lat: f[i+1], Lon: f[i]}
			ring = append(ring, pt)
			r.box.minLat = math.Min(r.box.minLat, pt.Lat)
			r.box.maxLat = math.Max(r.box.maxLat, pt.Lat)
			r.box.minLon = math.Min(r.box.minLon, pt.Lon)
			r.box.maxLon = math.Max(r.box.maxLon, pt.Lon)
		}
		r.Rings = append(r.Rings, ring)
	}
	return r
}

// Countries devuelve los países (el primero es Argentina, con Malvinas).
func Countries() []*Region { return load().countries }

// Provinces devuelve las 24 jurisdicciones argentinas.
func Provinces() []*Region { return load().provinces }

// InArgentina chequea si un punto cae en territorio argentino.
func InArgentina(pt LatLon) bool { return load().countries[0].Contains(pt) }

// Locate dice en qué país y provincia cae un punto: índices en Countries() y
// Provinces(), o -1 si no cae en ninguno (mar o fuera de los datos).
func Locate(pt LatLon) (country, province int) {
	w := load()
	country, province = -1, -1
	for i, c := range w.countries {
		if c.Contains(pt) {
			country = i
			break
		}
	}
	if country == 0 {
		for i, p := range w.provinces {
			if p.Contains(pt) {
				province = i
				break
			}
		}
	}
	return country, province
}

// Bounds es la ventana geográfica que dibujamos.
var Bounds = struct{ North, South, West, East float64 }{
	North: -21.3, South: -55.5, West: -74.0, East: -53.0,
}

// City es una ciudad que mostramos en el mapa.
type City struct {
	Name  string
	Short string // abreviatura de 3 letras para el listado
	LatLon
}

// Cities son las ciudades de referencia (se piden junto con la grilla).
var Cities = []City{
	{"Salta", "SLA", LatLon{-24.79, -65.41}},
	{"Tucumán", "TUC", LatLon{-26.82, -65.22}},
	{"Resistencia", "RES", LatLon{-27.45, -58.99}},
	{"Posadas", "PSS", LatLon{-27.40, -55.92}},
	{"Córdoba", "COR", LatLon{-31.42, -64.18}},
	{"Rosario", "ROS", LatLon{-32.95, -60.65}},
	{"Mendoza", "MDZ", LatLon{-32.89, -68.84}},
	{"Buenos Aires", "BUE", LatLon{-34.61, -58.40}},
	{"Neuquén", "NQN", LatLon{-38.95, -68.06}},
	{"Bariloche", "BRC", LatLon{-41.13, -71.31}},
	{"Comodoro Riv.", "CRD", LatLon{-45.86, -67.50}},
	{"Ushuaia", "USH", LatLon{-54.78, -68.30}},
}

// Grid genera puntos cada `step` grados que caen dentro de Argentina.
// Son los que le pedimos a Open-Meteo para colorear el mapa.
func Grid(step float64) []LatLon {
	var pts []LatLon
	for lat := Bounds.North - step/2; lat > Bounds.South; lat -= step {
		for lon := Bounds.West + step/2; lon < Bounds.East; lon += step {
			pt := LatLon{lat, lon}
			if InArgentina(pt) {
				pts = append(pts, pt)
			}
		}
	}
	return pts
}

// Projection convierte lat/lon en píxeles de un lienzo de W x H.
// Es equirectangular con corrección por coseno de la latitud media, para que
// Argentina no salga "aplastada". Cada píxel es medio carácter de alto
// (usamos ▀), así que los píxeles son aproximadamente cuadrados.
type Projection struct {
	W, H  int
	scale float64 // píxeles por grado de latitud
	k     float64 // cos(latitud media)
}

// NewProjection elige la escala para que el mapa entre en maxW x maxH píxeles.
func NewProjection(maxW, maxH int) Projection {
	k := math.Cos((Bounds.North + Bounds.South) / 2 * math.Pi / 180)
	latSpan := Bounds.North - Bounds.South
	lonSpan := (Bounds.East - Bounds.West) * k
	scale := math.Min(float64(maxH)/latSpan, float64(maxW)/lonSpan)
	return Projection{
		W:     int(math.Round(lonSpan * scale)),
		H:     int(math.Round(latSpan * scale)),
		scale: scale,
		k:     k,
	}
}

// ToPixel: coordenada -> píxel (puede caer fuera del lienzo).
func (p Projection) ToPixel(pt LatLon) (x, y int) {
	x = int((pt.Lon - Bounds.West) * p.k * p.scale)
	y = int((Bounds.North - pt.Lat) * p.scale)
	return x, y
}

// ToLatLon: centro de un píxel -> coordenada.
func (p Projection) ToLatLon(x, y int) LatLon {
	return LatLon{
		Lat: Bounds.North - (float64(y)+0.5)/p.scale,
		Lon: Bounds.West + (float64(x)+0.5)/(p.scale*p.k),
	}
}

// DegPerPixel es el tamaño de un píxel en grados de latitud.
func (p Projection) DegPerPixel() float64 { return 1 / p.scale }

// PixelsPerDegree es la escala (útil para decidir cuánto detalle dibujar).
func (p Projection) PixelsPerDegree() float64 { return p.scale }
