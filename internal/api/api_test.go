package api

import "testing"

// Los tests en Go son funciones TestXxx(t *testing.T) en archivos *_test.go.
// Se corren con: go test ./...
//
// Este estilo de "tabla de casos" es el idiomático en Go.
func TestCompass(t *testing.T) {
	cases := []struct {
		deg  float64
		want string
	}{
		{0, "N"}, {10, "N"}, {44, "NE"}, {90, "E"}, {180, "S"},
		{225, "SO"}, {270, "O"}, {315, "NO"}, {350, "N"}, {360, "N"},
	}
	for _, c := range cases {
		if got := Compass(c.deg); got != c.want {
			t.Errorf("Compass(%v) = %q, want %q", c.deg, got, c.want)
		}
	}
}

func TestSplitCountry(t *testing.T) {
	cases := []struct{ in, name, cc string }{
		{"Córdoba", "Córdoba", ""},
		{"Córdoba, ar", "Córdoba", "AR"},
		{"Villa Carlos Paz, Córdoba", "Villa Carlos Paz, Córdoba", ""},
	}
	for _, c := range cases {
		name, cc := splitCountry(c.in)
		if name != c.name || cc != c.cc {
			t.Errorf("splitCountry(%q) = (%q, %q), want (%q, %q)", c.in, name, cc, c.name, c.cc)
		}
	}
}

func TestDescribe(t *testing.T) {
	if txt, sky := Describe(95); txt != "Tormenta" || sky != SkyStorm {
		t.Errorf("Describe(95) = %q, %v", txt, sky)
	}
}
