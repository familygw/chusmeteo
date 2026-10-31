// Package locate obtiene la ubicación de la compu con CoreLocation (macOS).
//
// Las Mac no tienen GPS: CoreLocation estima con Wi-Fi, así que sirve tener el
// Wi-Fi prendido. Usamos CoreLocationCLI porque un binario suelto de Go no puede
// pedir el permiso de ubicación: lo hereda de la terminal desde la que corre.
package locate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"chusmeteo/internal/api"
)

// Token es el "nombre de ciudad" que significa "usá mi ubicación".
const Token = "@gps"

// ErrUnavailable: no se puede ubicar (sistema o herramienta faltante).
var ErrUnavailable = errors.New("ubicación no disponible: instalá CoreLocationCLI (brew install --cask corelocationcli) " +
	"y dale permiso a tu terminal en Ajustes > Privacidad > Localización")

// num acepta números JSON tanto como strings ("-31.4" o -31.4).
type num float64

func (n *num) UnmarshalJSON(b []byte) error {
	f, err := strconv.ParseFloat(strings.Trim(string(b), `"`), 64)
	*n = num(f)
	return err
}

// Here devuelve la ubicación actual.
func Here(ctx context.Context) (api.Location, error) {
	if runtime.GOOS != "darwin" {
		return api.Location{}, errors.New("ubicación automática sólo en macOS")
	}
	bin, err := exec.LookPath("CoreLocationCLI")
	if err != nil {
		return api.Location{}, ErrUnavailable
	}
	out, err := exec.CommandContext(ctx, bin, "--json").Output()
	if err != nil {
		return api.Location{}, fmt.Errorf("CoreLocationCLI: %w (¿permiso de localización para tu terminal?)", err)
	}
	return parse(out)
}

func parse(out []byte) (api.Location, error) {
	var r struct {
		Lat      num    `json:"latitude"`
		Lon      num    `json:"longitude"`
		Locality string `json:"locality"`
		Admin    string `json:"administrativeArea"`
		Country  string `json:"country"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return api.Location{}, fmt.Errorf("respuesta de CoreLocationCLI: %w", err)
	}
	if r.Lat == 0 && r.Lon == 0 {
		return api.Location{}, errors.New("CoreLocationCLI no devolvió coordenadas")
	}
	name := r.Locality
	if name == "" {
		name = "Mi ubicación"
	}
	return api.Location{Name: name, Admin1: r.Admin, Country: r.Country, Latitude: float64(r.Lat), Longitude: float64(r.Lon)}, nil
}
