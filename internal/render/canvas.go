// Package render dibuja "píxeles" en la terminal usando el carácter ▀
// (medio bloque superior): el color de texto pinta la mitad de arriba y el
// fondo la de abajo, así cada celda de la terminal son 2 píxeles verticales.
package render

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Color es un color hex ("#RRGGBB"). Cadena vacía = transparente / fondo.
type Color = lipgloss.Color // alias de tipo: mismo tipo, otro nombre

// Mark es un carácter que se dibuja encima de una celda (ej. una ciudad).
// Si FG es "", se elige negro o blanco según el fondo.
type Mark struct {
	Rune rune
	FG   Color
}

// Contrast devuelve negro o blanco, el que más contraste con col.
func Contrast(col Color) Color {
	var r, g, b int
	if _, err := fmt.Sscanf(string(col), "#%02x%02x%02x", &r, &g, &b); err != nil {
		return White
	}
	// Luminancia percibida (fórmula clásica de la TV: el verde "pesa" más).
	if 299*r+587*g+114*b > 140_000 { // los _ separan dígitos, como en Java
		return Black
	}
	return White
}

// Canvas es una grilla de W x H píxeles. Pix es un slice "plano":
// el píxel (x, y) vive en Pix[y*W+x]. Es más rápido y simple que [][]Color.
type Canvas struct {
	W, H  int
	Pix   []Color
	BG    Color
	marks map[[2]int]Mark // clave: celda {columna, fila de celda}
}

// New crea un lienzo lleno con el color de fondo.
func New(w, h int, bg Color) *Canvas {
	c := &Canvas{W: w, H: h, Pix: make([]Color, w*h), BG: bg, marks: map[[2]int]Mark{}}
	for i := range c.Pix {
		c.Pix[i] = bg
	}
	return c
}

// Clone hace una copia profunda (los slices y maps se comparten si no copiás).
func (c *Canvas) Clone() *Canvas {
	n := &Canvas{W: c.W, H: c.H, Pix: make([]Color, len(c.Pix)), BG: c.BG, marks: make(map[[2]int]Mark, len(c.marks))}
	copy(n.Pix, c.Pix)
	for k, v := range c.marks {
		n.marks[k] = v
	}
	return n
}

func (c *Canvas) in(x, y int) bool { return x >= 0 && y >= 0 && x < c.W && y < c.H }

// Set pinta un píxel; fuera de rango se ignora (cómodo para no chequear afuera).
func (c *Canvas) Set(x, y int, col Color) {
	if c.in(x, y) {
		c.Pix[y*c.W+x] = col
	}
}

// At lee un píxel (fuera de rango devuelve el fondo).
func (c *Canvas) At(x, y int) Color {
	if !c.in(x, y) {
		return c.BG
	}
	return c.Pix[y*c.W+x]
}

// Mark pone un carácter en la celda que contiene el píxel (x, y).
func (c *Canvas) Mark(x, y int, m Mark) {
	if c.in(x, y) {
		c.marks[[2]int{x, y / 2}] = m
	}
}

// Label escribe un texto corto a partir del píxel (x, y), una letra por celda.
// No pisa marcas existentes ni se sale del lienzo: si no entra, no se dibuja.
func (c *Canvas) Label(x, y int, text string) {
	row := y / 2
	runes := []rune(text)
	if x < 0 || x+len(runes) > c.W || !c.in(x, y) {
		return
	}
	for i := range runes {
		if _, taken := c.marks[[2]int{x + i, row}]; taken {
			return
		}
	}
	for i, r := range runes {
		c.marks[[2]int{x + i, row}] = Mark{Rune: r}
	}
}

// Unmark saca la marca de una celda.
func (c *Canvas) Unmark(x, cellRow int) { delete(c.marks, [2]int{x, cellRow}) }

// Paste copia otro lienzo en la posición (ox, oy) — píxeles y marcas.
func (c *Canvas) Paste(src *Canvas, ox, oy int) {
	for y := 0; y < src.H; y++ {
		for x := 0; x < src.W; x++ {
			c.Set(ox+x, oy+y, src.At(x, y))
		}
	}
	for k, m := range src.marks {
		// Sólo alineamos bien marcas si oy es par (una celda = 2 píxeles).
		c.Mark(ox+k[0], oy+k[1]*2, m)
	}
}

// Rows es la cantidad de filas de terminal que ocupa el lienzo.
func (c *Canvas) Rows() int { return (c.H + 1) / 2 }

// String convierte el lienzo en texto con colores ANSI.
// Agrupamos celdas consecutivas con los mismos colores en un solo Render:
// lipgloss es cómodo pero no es gratis, y así hacemos ~10x menos llamadas.
func (c *Canvas) String() string {
	var out strings.Builder
	for row := 0; row < c.Rows(); row++ {
		var (
			run   strings.Builder
			style lipgloss.Style
			key   string
		)
		flush := func() { // closure: captura run, style y out
			if run.Len() > 0 {
				out.WriteString(style.Render(run.String()))
				run.Reset()
			}
		}
		for x := 0; x < c.W; x++ {
			top := c.At(x, row*2)
			bot := c.At(x, row*2+1)

			var ch string
			var st lipgloss.Style
			var k string
			if m, ok := c.marks[[2]int{x, row}]; ok {
				if m.FG == "" {
					m.FG = Contrast(top) // sin color: el que mejor se lea sobre el fondo
				}
				ch = string(m.Rune)
				st = lipgloss.NewStyle().Foreground(m.FG).Background(top)
				k = "m" + string(m.FG) + string(top) + ch
			} else if top == bot {
				ch = " "
				st = lipgloss.NewStyle().Background(top)
				k = "s" + string(top)
			} else {
				ch = "▀"
				st = lipgloss.NewStyle().Foreground(top).Background(bot)
				k = "h" + string(top) + string(bot)
			}
			if k != key {
				flush()
				key, style = k, st
			}
			run.WriteString(ch)
		}
		flush()
		if row < c.Rows()-1 {
			out.WriteByte('\n')
		}
	}
	return out.String()
}
