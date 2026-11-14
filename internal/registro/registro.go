// Package registro guarda en disco lo que la app avisó y lo que pasó después,
// para poder medir aciertos y ajustar los umbrales con datos en vez de a ojo.
//
// El formato es JSON Lines: un objeto JSON por renglón. Se puede leer con
// cualquier editor, con `jq` o cargarlo en una planilla, y agregar al final
// nunca rompe lo anterior.
package registro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Tipos de entrada.
const (
	Celda   = "celda"   // el radar ve un núcleo encima o llegando
	Aviso   = "aviso"   // aviso a corto plazo del SMN sobre la ciudad
	Modelos = "modelos" // los modelos dan riesgo moderado o alto en 24 h
	Metar   = "metar"   // un aeropuerto cercano reportó granizo
	Usuario = "usuario" // lo que la persona vio: nada / lluvia fuerte / granizo
)

// Entry es un renglón del registro.
type Entry struct {
	Time  time.Time      `json:"hora"`
	Kind  string         `json:"tipo"`
	City  string         `json:"ciudad"`
	Lat   float64        `json:"lat"`
	Lon   float64        `json:"lon"`
	Data  map[string]any `json:"datos,omitempty"`  // lo propio de la entrada
	State map[string]any `json:"estado,omitempty"` // foto de lo que la app creía en ese momento
}

// Path es el archivo del registro ("" = no registrar). Es una variable para
// que los tests la apunten a un directorio temporal.
//
// Va en el directorio de configuración del usuario y no en el de caché:
// un caché se puede borrar sin perder nada, y esto no.
// En macOS: ~/Library/Application Support/chusmeteo/registro.jsonl
var Path = func() string {
	d, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "chusmeteo", "registro.jsonl")
}()

var mu sync.Mutex // varias goroutines pueden registrar a la vez

// Append agrega una entrada al final del archivo.
func Append(e Entry) error {
	if Path == "" {
		return nil
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(Path), 0o755); err != nil {
		return err
	}
	// O_APPEND: cada escritura va al final aunque haya otra instancia abierta.
	f, err := os.OpenFile(Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}
