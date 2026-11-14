package api

import (
	"math"
	"testing"
	"time"
)

// storm arma un cuadro con una celda redonda: núcleo intenso de 6 km de radio
// rodeado de lluvia moderada hasta 14 km.
func storm(t time.Time, east, north float64) *RadarGrid {
	return NewRadarGrid(t, func(e, n float64) int {
		switch d := math.Hypot(e-east, n-north); {
		case d <= 6:
			return 4
		case d <= 14:
			return 2
		}
		return 0
	})
}

func TestAnalyzeApproaching(t *testing.T) {
	t0 := time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)
	// La celda está al sudoeste y se mueve al noreste a 60 km/h (10 km cada 10 min por eje... ~85 km/h).
	nc := Analyze([]*RadarGrid{
		storm(t0, -60, -60),
		storm(t0.Add(10*time.Minute), -50, -50),
		storm(t0.Add(20*time.Minute), -40, -40),
	})
	if !nc.Near || nc.NearDir != "SO" || nc.NearLevel != 4 || nc.Overhead {
		t.Fatalf("núcleo: %+v", nc)
	}
	if !nc.Moving || nc.Toward != "NE" || math.Abs(nc.SpeedKmh-84.9) > 3 {
		t.Errorf("movimiento: %s a %.1f km/h", nc.Toward, nc.SpeedKmh)
	}
	// Centro a 56,6 km; el borde del núcleo (6 km) entra al radio de 8 km tras ~42,6 km: ~30 min.
	eta := nc.ETA.Sub(nc.Time).Minutes()
	if !nc.Approaching || nc.FromDir != "SO" || nc.ETALevel != 4 || eta < 24 || eta > 36 {
		t.Errorf("llegada: %+v (en %.0f min)", nc, eta)
	}
	if lvl, ok := nc.Threat(nc.Time.Add(5*time.Minute), time.Hour); !ok || lvl != 4 {
		t.Errorf("Threat = %d, %v", lvl, ok)
	}
	if _, ok := nc.Threat(nc.Time.Add(2*time.Hour), time.Hour); ok {
		t.Error("con el radar viejo no debería afirmar nada")
	}
}

func TestAnalyzeMissesAndLeaves(t *testing.T) {
	t0 := time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)
	// Pasa de largo: va hacia el este, 60 km al sur.
	nc := Analyze([]*RadarGrid{storm(t0, -40, -60), storm(t0.Add(20*time.Minute), -20, -60)})
	if !nc.Near || !nc.Moving || nc.Toward != "E" || nc.Approaching {
		t.Errorf("pasa de largo: %+v", nc)
	}
	// Se aleja.
	nc = Analyze([]*RadarGrid{storm(t0, 20, 20), storm(t0.Add(20*time.Minute), 40, 40)})
	if nc.Approaching || nc.Overhead {
		t.Errorf("se aleja: %+v", nc)
	}
	// Encima.
	nc = Analyze([]*RadarGrid{storm(t0, 2, -2)})
	if lvl, ok := nc.Threat(t0, time.Hour); !nc.Overhead || !ok || lvl != 4 || nc.Moving {
		t.Errorf("encima: %+v", nc)
	}
}

func TestAnalyzeIgnoresNoise(t *testing.T) {
	t0 := time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)
	// Cielo limpio, y un píxel intenso suelto (ruido): no es un núcleo.
	for _, g := range []*RadarGrid{
		NewRadarGrid(t0, func(e, n float64) int { return 0 }),
		NewRadarGrid(t0, func(e, n float64) int {
			if e == 10 && n == 10 {
				return 4
			}
			return 0
		}),
	} {
		if nc := Analyze([]*RadarGrid{g}); nc.Near || nc.Approaching || nc.Overhead {
			t.Errorf("no debería haber núcleos: %+v", nc)
		}
	}
}
