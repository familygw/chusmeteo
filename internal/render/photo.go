package render

import (
	"fmt"
	"image"
)

// FromImage ajusta una foto al ancho del lienzo (w x h píxeles) y muestra su
// parte más NUEVA: las imágenes de satélite crecen hacia abajo mientras pasa.
func FromImage(img image.Image, w, h int) *Canvas {
	c := New(w, h, Black)
	b := img.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 || w <= 0 || h <= 0 {
		return c
	}
	scale := float64(b.Dx()) / float64(w)
	rows := min(h, int(float64(b.Dy())/scale))
	top := b.Max.Y - int(float64(rows)*scale) // alineado al borde inferior de la foto
	for y := 0; y < rows; y++ {
		sy := top + int((float64(y)+0.5)*scale)
		for x := 0; x < w; x++ {
			sx := b.Min.X + int((float64(x)+0.5)*scale)
			r, g, bl, _ := img.At(sx, min(sy, b.Max.Y-1)).RGBA()
			c.Set(x, y, Color(fmt.Sprintf("#%02X%02X%02X", r>>8, g>>8, bl>>8)))
		}
	}
	return c
}
