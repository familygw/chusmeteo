// chusmeteo: el clima en tu terminal, con estética de los 90.
//
// Convención Go: los ejecutables viven en cmd/<nombre>/main.go y la lógica
// en internal/ (que el compilador impide importar desde otros módulos).
package main

import (
	"flag"
	"fmt"
	"os"

	"chusmeteo/internal/ui"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	// flag es el parser de argumentos de la stdlib: chusmeteo -city "Rosario" -view radar
	city := flag.String("city", "Córdoba, AR", "ciudad a mostrar (\"Ciudad\" o \"Ciudad, CC\")")
	view := flag.String("view", "clima", "vista inicial: clima | mapa | radar")
	noFX := flag.Bool("nofx", false, "sin efecto de recepción del satélite")
	flag.Parse()

	v, ok := ui.ParseView(*view)
	if !ok {
		fmt.Fprintf(os.Stderr, "vista desconocida %q (usá clima, mapa o radar)\n", *view)
		os.Exit(2)
	}

	// WithAltScreen usa la "pantalla alternativa": al salir, tu terminal
	// queda como estaba (igual que vim o htop).
	p := tea.NewProgram(ui.New(*city, ui.Options{View: v, NoFX: *noFX}), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
