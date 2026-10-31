// Package fx: el efecto de "recepción APT".
//
// Los satélites NOAA transmitían la imagen en vivo (APT, 137 MHz), una línea
// por vez, mientras pasaban sobre tu antena. En la PC de los 90 la veías
// "bajar" de arriba a abajo, con ruido, franjas de sincronismo a la izquierda
// y cuñas de calibración en gris a la derecha. El último satélite con APT,
// NOAA-15, se apagó el 19/08/2025. Este paquete es un homenaje.
package fx

import (
	"math"

	"chusmeteo/internal/render"
)

const (
	SyncW = 4 // columnas de sincronismo a la izquierda
	TeleW = 2 // columnas de telemetría a la derecha
	// FrameW es cuánto más ancho queda el mapa con el marco APT.
	FrameW = SyncW + TeleW

	Satellite = "NOAA-15"
	baseMHz   = 137.620 // frecuencia APT de NOAA-15
	dopplerHz = 3500    // corrimiento Doppler máximo (±3.5 kHz en un pase)
)

// Frame agrega las franjas de sincronismo (rayas verticales) y la cuña de
// telemetría (8 escalones de gris) alrededor de la imagen.
func Frame(img *render.Canvas) *render.Canvas {
	c := render.New(img.W+FrameW, img.H, render.Black)
	sync := [SyncW]render.Color{render.White, render.Black, render.Gray, render.Black}
	for y := 0; y < c.H; y++ {
		for x, col := range sync {
			c.Set(x, y, col)
		}
		wedge := y * 8 / c.H // 0..7
		for x := c.W - TeleW; x < c.W; x++ {
			c.Set(x, y, render.Gray8(wedge))
		}
	}
	c.Paste(img, SyncW, 0)
	return c
}

// Reception es el estado de una "pasada": qué línea va llegando.
type Reception struct {
	H    int    // alto en píxeles de la imagen que se recibe
	Line int    // próxima línea a recibir (0..H)
	Tick int    // contador de cuadros (para que el ruido "hierva")
	Seed uint64 // ruido fijo de esta pasada
}

// NewReception arranca una pasada nueva para una imagen de alto h.
func NewReception(h int, seed uint64) *Reception {
	return &Reception{H: h, Seed: seed}
}

// Step avanza un cuadro. La velocidad escala con el alto: siempre ~3 s.
func (r *Reception) Step() {
	r.Tick++
	r.Line += max(1, (r.H+49)/50) // max es built-in desde Go 1.21
}

// Done indica si ya llegó toda la imagen.
func (r *Reception) Done() bool { return r.Line >= r.H }

// Progress va de 0 a 1.
func (r *Reception) Progress() float64 {
	if r.H == 0 {
		return 1
	}
	return math.Min(1, float64(r.Line)/float64(r.H))
}

// FreqMHz simula el corrimiento Doppler: el satélite se acerca (frecuencia
// más alta), pasa por arriba y se aleja (más baja).
func (r *Reception) FreqMHz() float64 {
	return baseMHz + dopplerHz/1e6*(1-2*r.Progress())
}

// Signal devuelve la fuerza de señal 0..8: máxima a mitad de pasada.
func (r *Reception) Signal() int {
	s := 8*math.Sin(math.Pi*r.Progress()) + float64(hash(uint64(r.Tick), 7, r.Seed)%3) - 1
	return int(math.Max(1, math.Min(8, math.Round(s))))
}

// Apply devuelve una copia del lienzo "como se está recibiendo" ahora.
// Con r == nil (o pasada terminada) deja sólo un poco de suciedad fija.
// No toca el original: View tiene que poder llamarse muchas veces igual.
func Apply(img *render.Canvas, r *Reception) *render.Canvas {
	out := img.Clone()
	// Sin recepción: la "línea" queda más allá del final, así ninguna fila
	// cae en la estática ni en la línea brillante.
	line, tick, seed := img.H+2, 0, uint64(1998) // 1998: año de lanzamiento de NOAA-15
	if r != nil {
		line, tick, seed = r.Line, r.Tick, r.Seed
	}
	for y := 0; y < img.H; y++ {
		jitter := hash(uint64(y), 1, seed)%100 < 4 // algunas líneas llegan corridas
		for x := 0; x < img.W; x++ {
			h := hash(uint64(x), uint64(y), seed)
			switch {
			case y > line+1: // todavía no llegó: estática que cambia en cada cuadro
				out.Set(x, y, static(hash(uint64(x), uint64(y)^uint64(tick)<<16, seed)))
			case y >= line-1: // la línea que está entrando: brillante
				out.Set(x, y, render.Gray8(5+int(h%3)))
			default:
				noise := uint64(1) // % de píxeles con ruido
				if line-y < 4 && line < img.H {
					noise = 12 // recién llegadas: más ruido
				}
				if h%100 < noise {
					out.Set(x, y, render.Gray8(2+int(h>>8%5)))
				} else if jitter && x > 0 {
					out.Set(x, y, img.At(x-1, y))
				}
			}
		}
	}
	// Las ciudades aparecen recién cuando llega su línea.
	for row := 0; row < img.Rows(); row++ {
		if row*2 > line {
			for x := 0; x < img.W; x++ {
				out.Unmark(x, row)
			}
		}
	}
	return out
}

func static(h uint64) render.Color {
	switch v := h % 100; {
	case v < 68:
		return render.Black
	case v < 84:
		return render.Gray8(1)
	case v < 95:
		return render.Gray8(2)
	default:
		return render.Gray8(4)
	}
}

// hash es splitmix64: barato y determinístico. Con la misma entrada da el
// mismo "ruido", así la imagen no titila al redibujar si no cambió nada.
func hash(a, b, seed uint64) uint64 {
	z := a*0x9E3779B97F4A7C15 ^ b*0xBF58476D1CE4E5B9 ^ seed
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}
