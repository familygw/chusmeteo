package render

import "math"

// HourlyChart dibuja un gráfico de barras en pixel art de w x h píxeles:
// arriba la temperatura (barras "termómetro": cada fila con el color de la
// temperatura a esa altura, sobre una grilla punteada cada 5°) y abajo una
// franja con la probabilidad de lluvia.
//
// Devuelve también la escala usada (lo, hi) para que la UI rotule el eje.
func HourlyChart(w, h int, temps, probs []float64) (c *Canvas, lo, hi float64) {
	c = New(w, h, Black)
	n := len(temps)
	if n == 0 || w < n || h < 8 {
		return c, 0, 0
	}

	rainH := max(2, h/5) // franja de lluvia
	tempH := h - rainH - 1

	lo, hi = math.Inf(1), math.Inf(-1)
	for _, t := range temps {
		lo, hi = math.Min(lo, t), math.Max(hi, t)
	}
	lo, hi = math.Floor(lo)-1, math.Ceil(hi)+1
	yOf := func(t float64) int { // temperatura -> fila (0 = arriba)
		return tempH - 1 - int(math.Round((t-lo)/(hi-lo)*float64(tempH-1)))
	}
	tOf := func(y int) float64 { // y la inversa: fila -> temperatura
		return lo + float64(tempH-1-y)/float64(tempH-1)*(hi-lo)
	}

	// Grilla punteada cada 5 grados.
	for g := math.Ceil(lo/5) * 5; g <= hi; g += 5 {
		y := yOf(g)
		for x := 0; x < w; x += 2 {
			c.Set(x, y, Gray8(1))
		}
	}
	// Separador entre temperatura y lluvia.
	for x := 0; x < w; x++ {
		c.Set(x, tempH, Gray8(1))
	}

	for i, t := range temps {
		x0, x1 := i*w/n, (i+1)*w/n
		if x1-x0 >= 3 {
			x1-- // con espacio, dejamos 1 píxel de separación entre barras
		}
		top := yOf(t)
		for x := x0; x < x1; x++ {
			for y := top; y < tempH; y++ {
				col := TempColorDither(tOf(y), x, y)
				if y == top {
					col = White // "tapa" de la barra: se lee mejor la curva
				}
				c.Set(x, y, col)
			}
			if i < len(probs) {
				ph := int(math.Round(probs[i] / 100 * float64(rainH)))
				for y := h - ph; y < h; y++ {
					col := LCyan
					if probs[i] >= 60 {
						col = LBlue
					}
					c.Set(x, y, col)
				}
			}
		}
	}
	return c, lo, hi
}
