package geo

import "testing"

func TestInArgentina(t *testing.T) {
	cases := []struct {
		name string
		pt   LatLon
		want bool
	}{
		{"Córdoba", LatLon{-31.42, -64.18}, true},
		{"Buenos Aires", LatLon{-34.70, -58.50}, true},
		{"Ushuaia", LatLon{-54.75, -68.0}, true},
		{"Malvinas", LatLon{-51.75, -58.8}, true},
		{"Salta", LatLon{-24.79, -65.41}, true},
		{"Posadas", LatLon{-27.45, -55.9}, true},
		{"Santiago de Chile", LatLon{-33.45, -70.66}, false},
		{"Montevideo", LatLon{-34.90, -56.16}, false},
		{"Asunción", LatLon{-25.26, -57.40}, false},
		{"Mar abierto", LatLon{-45.0, -60.0}, false},
	}
	for _, c := range cases {
		if got := InArgentina(c.pt); got != c.want {
			t.Errorf("%s: InArgentina = %v, want %v", c.name, got, c.want)
		}
	}
}

// Todas las ciudades de referencia tienen que caer adentro del contorno.
func TestCitiesInside(t *testing.T) {
	for _, c := range Cities {
		if !InArgentina(c.LatLon) {
			t.Errorf("%s quedó fuera del contorno", c.Name)
		}
	}
}

func TestLocate(t *testing.T) {
	cases := []struct {
		name, country, province string
		pt                      LatLon
	}{
		{"Córdoba", "Argentina", "Córdoba", LatLon{-31.42, -64.18}},
		{"Mendoza", "Argentina", "Mendoza", LatLon{-32.89, -68.84}},
		{"Malvinas", "Argentina", "Tierra del Fuego", LatLon{-51.75, -58.8}},
		{"Santiago de Chile", "Chile", "", LatLon{-33.45, -70.66}},
		{"Montevideo", "Uruguay", "", LatLon{-34.85, -56.17}},
		{"Asunción", "Paraguay", "", LatLon{-25.28, -57.63}},
		{"Mar abierto", "", "", LatLon{-45.0, -60.0}},
	}
	for _, c := range cases {
		ci, pi := Locate(c.pt)
		gotC, gotP := "", ""
		if ci >= 0 {
			gotC = Countries()[ci].Name
		}
		if pi >= 0 {
			gotP = Provinces()[pi].Name
		}
		if gotC != c.country || gotP != c.province {
			t.Errorf("%s: Locate = (%q, %q), want (%q, %q)", c.name, gotC, gotP, c.country, c.province)
		}
	}
}

func TestProvincesLoaded(t *testing.T) {
	if n := len(Provinces()); n != 24 {
		t.Errorf("esperaba 24 jurisdicciones, hay %d", n)
	}
}

func TestGridSize(t *testing.T) {
	// Lo que importa es no pasarnos del cupo de Open-Meteo (~10k "llamadas"/día,
	// y cada coordenada cuenta). Con 2.5° quedan ~40-55 puntos.
	n := len(Grid(2.5))
	if n < 30 || n > 70 {
		t.Errorf("Grid(2.5) = %d puntos, esperaba 30..70", n)
	}
}

func TestProjectionRoundTrip(t *testing.T) {
	p := NewProjection(40, 60)
	if p.W > 40 || p.H > 60 || p.W < 10 {
		t.Fatalf("proyección fuera de rango: %dx%d", p.W, p.H)
	}
	x, y := p.ToPixel(LatLon{-31.42, -64.18})
	back := p.ToLatLon(x, y)
	if d := back.Lat + 31.42; d > p.DegPerPixel() || d < -p.DegPerPixel() {
		t.Errorf("lat ida y vuelta: %v", back)
	}
}
