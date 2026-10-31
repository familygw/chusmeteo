package fx

import (
	"testing"

	"chusmeteo/internal/render"
)

func TestReceptionLifecycle(t *testing.T) {
	r := NewReception(40, 42)
	if r.Done() || r.FreqMHz() <= baseMHz {
		t.Fatal("al arrancar: no terminada y frecuencia por encima de la base (se acerca)")
	}
	steps := 0
	for !r.Done() {
		r.Step()
		steps++
	}
	if steps < 30 || steps > 60 {
		t.Errorf("pasada de %d cuadros, esperaba ~40-50", steps)
	}
	if r.FreqMHz() >= baseMHz {
		t.Error("al final la frecuencia tiene que estar por debajo (se aleja)")
	}
}

func TestApplyKeepsSizeAndOriginal(t *testing.T) {
	img := render.New(10, 20, render.Green)
	img.Mark(5, 18, render.Mark{Rune: '•', FG: render.White})
	r := NewReception(20, 1)
	r.Line = 5

	out := Apply(img, r)
	if out.W != 10 || out.H != 20 {
		t.Fatalf("tamaño %dx%d", out.W, out.H)
	}
	if img.At(3, 15) != render.Green {
		t.Error("Apply modificó el original")
	}
	if out.At(3, 15) == render.Green {
		t.Error("debajo de la línea de recepción debería haber estática")
	}
	// Mismo estado => mismo resultado (determinístico).
	if Apply(img, r).String() != out.String() {
		t.Error("Apply no es determinístico")
	}
}

// Sin recepción, la imagen queda casi igual: sólo un poco de ruido fijo
// (nada de línea brillante en el borde, que fue un bug).
func TestApplyWithoutReception(t *testing.T) {
	img := render.New(30, 40, render.Green)
	out := Apply(img, nil)
	changed := 0
	for i := range img.Pix {
		if out.Pix[i] != img.Pix[i] {
			changed++
		}
	}
	if changed > len(img.Pix)/10 {
		t.Errorf("cambiaron %d de %d píxeles", changed, len(img.Pix))
	}
	lastRow := 0
	for x := 0; x < img.W; x++ {
		if out.At(x, img.H-1) != render.Green {
			lastRow++
		}
	}
	if lastRow > img.W/3 {
		t.Errorf("la última fila quedó pintada (%d/%d píxeles)", lastRow, img.W)
	}
}

func TestFrame(t *testing.T) {
	f := Frame(render.New(10, 16, render.Green))
	if f.W != 10+FrameW || f.At(0, 0) != render.White || f.At(f.W-1, 15) != render.Gray8(7) {
		t.Errorf("marco APT mal armado: %dx%d", f.W, f.H)
	}
}
