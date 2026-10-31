package render

import (
	"image/color"
	"strings"
	"testing"

	"chusmeteo/internal/api"
	"chusmeteo/internal/geo"

	"github.com/charmbracelet/lipgloss"
)

func TestCanvasString(t *testing.T) {
	c := New(4, 3, Black) // 3 píxeles de alto = 2 filas de terminal
	c.Set(0, 0, White)
	c.Mark(3, 2, Mark{Rune: '•', FG: Yellow})
	out := c.String()
	if lipgloss.Height(out) != 2 || lipgloss.Width(out) != 4 {
		t.Fatalf("tamaño %dx%d", lipgloss.Width(out), lipgloss.Height(out))
	}
	if !strings.Contains(out, "▀") || !strings.Contains(out, "•") {
		t.Errorf("faltan medio bloque o marca: %q", out)
	}
}

func TestIDW(t *testing.T) {
	pts := []api.PointWeather{
		{LatLon: geo.LatLon{Lat: 0, Lon: 0}, Temperature: 10},
		{LatLon: geo.LatLon{Lat: 0, Lon: 2}, Temperature: 20},
	}
	if v := IDW(pts, geo.LatLon{Lat: 0, Lon: 1}); v < 14.9 || v > 15.1 {
		t.Errorf("punto medio = %.2f, esperaba 15", v)
	}
	if v := IDW(pts, geo.LatLon{Lat: 0, Lon: 0}); v != 10 {
		t.Errorf("sobre el punto = %.2f", v)
	}
}

func TestTempColor(t *testing.T) {
	if TempColor(-3) != White || TempColor(18) != LGreen || TempColor(40) != LMagenta {
		t.Error("bandas mal asignadas")
	}
}

func TestRadarLevel(t *testing.T) {
	cases := []struct {
		c    color.RGBA
		want int
	}{
		{color.RGBA{0, 0, 0, 0}, 0},
		{color.RGBA{136, 221, 238, 255}, 1}, // celeste
		{color.RGBA{0, 85, 136, 255}, 2},    // azul
		{color.RGBA{255, 238, 0, 255}, 3},   // amarillo
		{color.RGBA{255, 170, 0, 255}, 3},   // naranja
		{color.RGBA{193, 0, 0, 255}, 4},     // rojo
		{color.RGBA{255, 170, 255, 255}, 4}, // rosa
	}
	for _, c := range cases {
		if got := RadarLevel(c.c); got != c.want {
			t.Errorf("RadarLevel(%v) = %d, want %d", c.c, got, c.want)
		}
	}
}

func TestTempColorDither(t *testing.T) {
	// Lejos del borde de banda: color puro.
	if TempColorDither(17, 0, 0) != LGreen {
		t.Error("17° debería ser verde liso")
	}
	// Cerca del borde (19.8°): mezcla de las dos bandas en el patrón 4x4.
	seen := map[Color]bool{}
	for y := range 4 {
		for x := range 4 {
			seen[TempColorDither(19.8, x, y)] = true
		}
	}
	if !seen[LGreen] || !seen[Yellow] || len(seen) != 2 {
		t.Errorf("19.8° debería tramar verde y amarillo, vi %v", seen)
	}
}

func TestDarken(t *testing.T) {
	if Darken(White, 0.5) != "#7F7F7F" || Darken(Black, 0.5) != "#000000" {
		t.Errorf("Darken: %s", Darken(White, 0.5))
	}
}

func TestHourlyChart(t *testing.T) {
	temps := []float64{12, 14, 18, 22, 25, 24, 20, 16}
	probs := []float64{0, 0, 10, 30, 80, 60, 20, 0}
	c, lo, hi := HourlyChart(40, 20, temps, probs)
	if lo >= 12 || hi <= 25 {
		t.Errorf("escala %v..%v no contiene los datos", lo, hi)
	}
	// La barra más alta (25°, índice 4) tiene que llegar más arriba que la primera.
	top := func(i int) int {
		x := i*40/len(temps) + 1
		for y := 0; y < c.H; y++ {
			if c.At(x, y) == White {
				return y
			}
		}
		return -1
	}
	if top(4) >= top(0) {
		t.Errorf("barra de 25° (fila %d) no supera a la de 12° (fila %d)", top(4), top(0))
	}
}

func TestLabel(t *testing.T) {
	c := New(10, 4, Black)
	c.Label(2, 0, "COR")
	c.Label(3, 0, "XYZ") // se pisa: no se dibuja
	c.Label(8, 0, "BUE") // no entra: no se dibuja
	out := c.String()
	if !strings.Contains(out, "C") || strings.Contains(out, "X") || strings.Contains(out, "B") {
		t.Errorf("etiquetas mal: %q", out)
	}
}
