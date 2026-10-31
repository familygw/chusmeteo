package sdr

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestArgs(t *testing.T) {
	c := Config{Dir: "/tmp/x", FreqMHz: 137.9, Gain: 38, Timeout: 15 * time.Minute}
	got := c.Args()
	for _, want := range []string{"meteor_m2-x_lrpt", "/tmp/x", "137.9000e6", "900"} {
		if !slices.Contains(got, want) {
			t.Errorf("faltan %q en %v", want, got)
		}
	}
}

func TestLatest(t *testing.T) {
	dir := t.TempDir()
	if _, _, ok := Latest(dir); ok {
		t.Fatal("directorio vacío no debería tener imágenes")
	}
	sub := filepath.Join(dir, "MSU-MR")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, age time.Duration) string {
		p := filepath.Join(sub, name)
		f, _ := os.Create(p)
		img := image.NewRGBA(image.Rect(0, 0, 2, 2))
		img.Set(0, 0, color.RGBA{255, 0, 0, 255})
		_ = png.Encode(f, img)
		f.Close()
		old := time.Now().Add(-age)
		_ = os.Chtimes(p, old, old)
		return p
	}
	write("viejo.png", time.Hour)
	nuevo := write("nuevo.png", time.Minute)
	_ = os.WriteFile(filepath.Join(sub, "log.txt"), []byte("x"), 0o644)

	p, _, ok := Latest(dir)
	if !ok || p != nuevo {
		t.Fatalf("Latest = %q ok=%v, quería %q", p, ok, nuevo)
	}
	if img, err := Load(p); err != nil || img.Bounds().Dx() != 2 {
		t.Errorf("Load: %v", err)
	}
}
