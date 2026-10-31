// Package sdr es el "modo hardcore": recibe imágenes del satélite ruso
// Meteor-M (LRPT, ~137 MHz) con un RTL-SDR, usando SatDump como decodificador.
//
// No decodificamos nada nosotros: lanzamos `satdump live` y mostramos la
// imagen más nueva que deje en el directorio de salida.
package sdr

import (
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // registra el decoder JPEG
	_ "image/png"  // registra el decoder PNG
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Config son los parámetros de la recepción.
type Config struct {
	Dir     string        // donde SatDump deja los productos
	FreqMHz float64       // Meteor-M2 3: 137.9 · Meteor-M2 4: 137.1
	Gain    float64       // dB del RTL-SDR
	Timeout time.Duration // duración máxima de una pasada
}

// DefaultConfig usa el directorio de caché del usuario.
func DefaultConfig() Config {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return Config{Dir: filepath.Join(dir, "chusmeteo", "lrpt"), FreqMHz: 137.9, Gain: 38, Timeout: 15 * time.Minute}
}

// ErrNoSatDump: el decodificador no está instalado.
var ErrNoSatDump = errors.New("no encuentro `satdump` en el PATH (https://github.com/SatDump/SatDump)")

// Available dice si se puede capturar.
func Available() error {
	if _, err := exec.LookPath("satdump"); err != nil {
		return ErrNoSatDump
	}
	return nil
}

// Args arma la línea de comandos de SatDump (aislada para poder testearla).
func (c Config) Args() []string {
	return []string{
		"live", "meteor_m2-x_lrpt", c.Dir,
		"--source", "rtlsdr",
		"--samplerate", "1.024e6",
		"--frequency", fmt.Sprintf("%.4fe6", c.FreqMHz),
		"--gain", fmt.Sprintf("%.0f", c.Gain),
		"--timeout", fmt.Sprintf("%.0f", c.Timeout.Seconds()),
	}
}

// Capture corre SatDump hasta que termine o se cancele el contexto.
// Cancelar no es un error. Si falla, devuelve el final de su salida.
func Capture(ctx context.Context, c Config) error {
	if err := Available(); err != nil {
		return err
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	var out tail
	cmd := exec.CommandContext(ctx, "satdump", c.Args()...)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("satdump: %w: %s", err, strings.TrimSpace(out.String()))
	}
	return nil
}

// tail guarda sólo los últimos bytes escritos: SatDump loguea mucho.
type tail struct{ b []byte }

const tailMax = 300

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > tailMax {
		t.b = t.b[len(t.b)-tailMax:]
	}
	return len(p), nil
}

func (t *tail) String() string { return string(t.b) }

// Latest devuelve la imagen (png/jpg) más nueva bajo dir.
func Latest(dir string) (path string, mod time.Time, ok bool) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".png", ".jpg", ".jpeg":
			if info, err := d.Info(); err == nil && info.ModTime().After(mod) {
				path, mod, ok = p, info.ModTime(), true
			}
		}
		return nil
	})
	return path, mod, ok
}

// Load decodifica una imagen del disco.
func Load(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}
