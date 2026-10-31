package render

import (
	"fmt"
	"image/color"
	"math"
)

// Paleta VGA (la misma de la UI) + algunos grises extra para el efecto APT.
const (
	Black    Color = "#000000"
	Blue     Color = "#0000AA"
	Green    Color = "#00AA00"
	Cyan     Color = "#00AAAA"
	Red      Color = "#AA0000"
	Magenta  Color = "#AA00AA"
	Brown    Color = "#AA5500"
	Gray     Color = "#AAAAAA"
	DarkGray Color = "#555555"
	LBlue    Color = "#5555FF"
	LGreen   Color = "#55FF55"
	LCyan    Color = "#55FFFF"
	LRed     Color = "#FF5555"
	LMagenta Color = "#FF55FF"
	Yellow   Color = "#FFFF55"
	White    Color = "#FFFFFF"
)

// TempBand es un rango de temperatura con su color.
type TempBand struct {
	Max   float64 // límite superior (exclusivo)
	Color Color
	Label string
}

// TempBands: 9 bandas con colores VGA puros. Evitamos el marrón (#AA5500):
// en terminales de 16 colores (Terminal.app) se ve igual que el rojo.
var TempBands = []TempBand{
	{0, White, "< 0°"},
	{5, LBlue, "0-5°"},
	{10, Cyan, "5-10°"},
	{15, LCyan, "10-15°"},
	{20, LGreen, "15-20°"},
	{25, Yellow, "20-25°"},
	{30, LRed, "25-30°"},
	{35, Red, "30-35°"},
	{999, LMagenta, "> 35°"},
}

// TempColor devuelve el color de la banda de una temperatura.
func TempColor(t float64) Color {
	for _, b := range TempBands {
		if t < b.Max {
			return b.Color
		}
	}
	return TempBands[len(TempBands)-1].Color
}

// bayer4 es la matriz de tramado ordenado 4x4 (la de los juegos y GIFs de los 90).
// Cada valor 0..15 es un umbral: comparando la parte fraccionaria contra él,
// los píxeles de una zona de transición alternan entre dos colores en patrón fijo.
var bayer4 = [4][4]float64{
	{0, 8, 2, 10},
	{12, 4, 14, 6},
	{3, 11, 1, 9},
	{15, 7, 13, 5},
}

// TempColorDither es TempColor con transición tramada: en el último cuarto de
// cada banda de 5°, algunos píxeles ya toman el color de la banda siguiente.
func TempColorDither(t float64, x, y int) Color {
	const width, zone = 5.0, 0.25
	frac := t/width - math.Floor(t/width)
	if frac > 1-zone {
		threshold := (bayer4[y%4][x%4] + 0.5) / 16
		if (frac-(1-zone))/zone > threshold {
			return TempColor(t + width*zone)
		}
	}
	return TempColor(t)
}

// Darken mezcla un color con negro (f = 0 nada, 1 negro).
func Darken(c Color, f float64) Color {
	var r, g, b int
	if _, err := fmt.Sscanf(string(c), "#%02x%02x%02x", &r, &g, &b); err != nil {
		return c
	}
	k := 1 - f
	return Color(fmt.Sprintf("#%02X%02X%02X", int(float64(r)*k), int(float64(g)*k), int(float64(b)*k)))
}

// RadarLevel clasifica un píxel del radar en 0 (nada) .. 4 (intensa).
// Rain Viewer manda colores ya "pintados" (esquema 2, Universal Blue: celestes ->
// azules -> amarillo -> naranja -> rojo -> rosa), así que lo inferimos del color.
// La tabla exacta está en un CSV de rainviewer.com/api/color-schemes.html; estos
// umbrales son aproximados: si con datos reales la intensidad se ve corrida,
// este es el lugar para ajustarlos.
func RadarLevel(c color.RGBA) int {
	if c.A < 40 {
		return 0
	}
	r, g, b := int(c.R), int(c.G), int(c.B)
	switch {
	case r > 180 && b > 180 && g < 200: // rosa / magenta
		return 4
	case r > 150 && g < 90: // rojo
		return 4
	case r > 180 && b < 130: // amarillo / naranja
		return 3
	case g >= 140: // celeste
		return 1
	default: // azul
		return 2
	}
}

// RadarBands: colores y nombres de los niveles 1..4.
var RadarBands = [...]struct {
	Color Color
	Label string
}{
	{"", ""},
	{LCyan, "Leve"},
	{LBlue, "Moderada"},
	{Yellow, "Fuerte"},
	{LRed, "Intensa (posible granizo)"},
}

// Gray8 devuelve un gris de 0 (negro) a 7 (blanco), para ruido y cuñas APT.
func Gray8(level int) Color {
	if level < 0 {
		level = 0
	}
	if level > 7 {
		level = 7
	}
	v := level * 255 / 7
	return Color(fmt.Sprintf("#%02X%02X%02X", v, v, v))
}
