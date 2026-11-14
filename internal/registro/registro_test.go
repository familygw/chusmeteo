package registro

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppend(t *testing.T) {
	Path = filepath.Join(t.TempDir(), "sub", "registro.jsonl")
	for _, k := range []string{Celda, Usuario} {
		e := Entry{Time: time.Date(2026, 11, 13, 19, 8, 0, 0, time.UTC), Kind: k, City: "Córdoba",
			Data: map[string]any{"que": "lluvia fuerte"}}
		if err := Append(e); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.Open(Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var kinds []string
	for sc := bufio.NewScanner(f); sc.Scan(); {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("renglón inválido %q: %v", sc.Text(), err)
		}
		kinds = append(kinds, e.Kind)
	}
	if len(kinds) != 2 || kinds[0] != Celda || kinds[1] != Usuario {
		t.Errorf("entradas: %v", kinds)
	}
	Path = ""
	if err := Append(Entry{}); err != nil {
		t.Errorf("sin Path no debería fallar: %v", err)
	}
}
