package render

import (
	"math"

	"chusmeteo/internal/api"
	"chusmeteo/internal/geo"
)

// Colores del mapa base (fondo de CRT: mar negro, vecinos apagados).
const (
	Water    Color = Black
	Neighbor Color = DarkGray // se dibuja tramado: 1 de cada 4 píxeles
	LandDim  Color = DarkGray // Argentina sin datos
	Border   Color = "#3A3A3A"
)

// DetailScale: desde cuántos píxeles por grado vale la pena dibujar límites
// provinciales y etiquetas. Con menos, las líneas taparían todo.
const DetailScale = 2.4

// Regions es el "raster" de regiones: para cada píxel, en qué país y
// provincia cae. Calcularlo es lo caro (punto-en-polígono contra miles de
// vértices), así que se hace una vez por tamaño de mapa y se cachea.
type Regions struct {
	W, H     int
	Country  []int8 // -1 = mar; 0 = Argentina; >0 = vecinos
	Province []int8 // -1 = fuera de Argentina
}

func (r *Regions) arg(i int) bool { return r.Country[i] == 0 }

// border dice si el píxel i está en el límite entre dos provincias
// (miramos sólo derecha y abajo: así la línea sale de 1 píxel de grosor).
func (r *Regions) border(x, y int) bool {
	i := y*r.W + x
	if !r.arg(i) {
		return false
	}
	p := r.Province[i]
	if x+1 < r.W && r.arg(i+1) && r.Province[i+1] != p {
		return true
	}
	if y+1 < r.H && r.arg(i+r.W) && r.Province[i+r.W] != p {
		return true
	}
	return false
}

var regionCache = map[[2]int]*Regions{}

// RegionsFor devuelve (y cachea) el raster para una proyección.
func RegionsFor(p geo.Projection) *Regions {
	k := [2]int{p.W, p.H}
	if r, ok := regionCache[k]; ok {
		return r
	}
	r := &Regions{W: p.W, H: p.H, Country: make([]int8, p.W*p.H), Province: make([]int8, p.W*p.H)}
	for y := 0; y < p.H; y++ {
		for x := 0; x < p.W; x++ {
			c, pr := geo.Locate(p.ToLatLon(x, y))
			r.Country[y*p.W+x], r.Province[y*p.W+x] = int8(c), int8(pr)
		}
	}
	regionCache[k] = r
	return r
}

// paint arma un mapa: `land` decide el color de cada píxel argentino.
func paint(p geo.Projection, land func(x, y int) Color) *Canvas {
	r := RegionsFor(p)
	c := New(p.W, p.H, Water)
	detail := p.PixelsPerDegree() >= DetailScale
	for y := 0; y < p.H; y++ {
		for x := 0; x < p.W; x++ {
			i := y*p.W + x
			switch {
			case r.Country[i] < 0:
				// mar: queda el fondo
			case !r.arg(i):
				// Países vecinos: grilla de puntos, el "gris al 25%" de los
				// gráficos de 16 colores. Se ve igual en cualquier terminal.
				if x%2 == 0 && y%2 == 0 {
					c.Pix[i] = Neighbor
				}
			case detail && r.border(x, y):
				c.Pix[i] = Darken(land(x, y), 0.45)
			default:
				c.Pix[i] = land(x, y)
			}
		}
	}
	return c
}

// Outline dibuja el mapa sin datos: Argentina en gris, vecinos apagados.
func Outline(p geo.Projection) *Canvas {
	return paint(p, func(int, int) Color { return LandDim })
}

// TempMap colorea Argentina interpolando la grilla de temperaturas,
// con tramado (dithering) entre bandas, como los gráficos de 16 colores.
func TempMap(p geo.Projection, pts []api.PointWeather) *Canvas {
	if len(pts) == 0 {
		return Outline(p)
	}
	return paint(p, func(x, y int) Color {
		return TempColorDither(IDW(pts, p.ToLatLon(x, y)), x, y)
	})
}

// IDW ("inverse distance weighting") estima la temperatura en un punto como
// un promedio de todos los puntos medidos, pesado por 1/distancia².
// Simple y suficiente para un mapa de este tamaño.
func IDW(pts []api.PointWeather, at geo.LatLon) float64 {
	k := math.Cos(at.Lat * math.Pi / 180)
	var num, den float64
	for _, p := range pts {
		dLat := p.Lat - at.Lat
		dLon := (p.Lon - at.Lon) * k
		d2 := dLat*dLat + dLon*dLon
		if d2 < 1e-6 {
			return p.Temperature // estamos justo sobre el punto
		}
		w := 1 / d2
		num += w * p.Temperature
		den += w
	}
	return num / den
}

// RadarMap dibuja el mapa base y encima la lluvia de un frame del radar.
// Cada píxel del mapa abarca varios píxeles del tile, así que muestreamos
// una sub-grilla de 3x3 y nos quedamos con el nivel más alto: si no, las
// celdas de tormenta chicas desaparecen al achicar.
func RadarMap(p geo.Projection, img *api.RadarImage) *Canvas {
	c := Outline(p)
	if img == nil {
		return c
	}
	step := p.DegPerPixel() / 3
	for y := 0; y < p.H; y++ {
		for x := 0; x < p.W; x++ {
			center := p.ToLatLon(x, y)
			k := math.Cos(center.Lat * math.Pi / 180)
			best := 0
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					pt := geo.LatLon{Lat: center.Lat + float64(dy)*step, Lon: center.Lon + float64(dx)*step/k}
					best = max(best, RadarLevel(img.Sample(pt)))
				}
			}
			if best > 0 {
				c.Set(x, y, RadarBands[best].Color)
			}
		}
	}
	return c
}

// MarkCity pone un carácter (y opcionalmente una etiqueta) en una coordenada.
// La etiqueta sólo se dibuja con escala suficiente y si no se pisa con otra.
func MarkCity(c *Canvas, p geo.Projection, pt geo.LatLon, m Mark, label string) {
	x, y := p.ToPixel(pt)
	c.Mark(x, y, m)
	if label != "" && p.PixelsPerDegree() >= DetailScale {
		c.Label(x+1, y, label)
	}
}
