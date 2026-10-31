// chusmeteo: el clima en tu terminal, con estética de los 90.
//
// Convención Go: los ejecutables viven en cmd/<nombre>/main.go y la lógica
// en internal/ (que el compilador impide importar desde otros módulos).
package main

import (
	"flag"
	"fmt"
	"os"

	"chusmeteo/internal/sdr"
	"chusmeteo/internal/ui"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	// flag es el parser de argumentos de la stdlib: chusmeteo -city "Rosario" -view radar
	city := flag.String("city", "Córdoba, AR", "ciudad a mostrar (\"Ciudad\" o \"Ciudad, CC\")")
	view := flag.String("view", "clima", "vista inicial: clima | mapa | radar")
	noFX := flag.Bool("nofx", false, "sin efecto de recepción del satélite")
	hardcore := flag.Bool("hardcore", false, "agrega la vista SAT: recibe Meteor-M LRPT con un RTL-SDR (requiere satdump)")
	cfg := sdr.DefaultConfig()
	flag.StringVar(&cfg.Dir, "lrpt-dir", cfg.Dir, "directorio donde SatDump guarda las imágenes")
	flag.Float64Var(&cfg.FreqMHz, "lrpt-freq", cfg.FreqMHz, "frecuencia en MHz (Meteor-M2 3: 137.9, Meteor-M2 4: 137.1)")
	flag.Float64Var(&cfg.Gain, "lrpt-gain", cfg.Gain, "ganancia del RTL-SDR en dB")
	flag.Parse()

	v, ok := ui.ParseView(*view)
	if !ok || (v == ui.ViewSat && !*hardcore) {
		fmt.Fprintf(os.Stderr, "vista desconocida %q (usá clima, mapa, radar o, con -hardcore, satelite)\n", *view)
		os.Exit(2)
	}

	// WithAltScreen usa la "pantalla alternativa": al salir, tu terminal
	// queda como estaba (igual que vim o htop).
	p := tea.NewProgram(ui.New(*city, ui.Options{View: v, NoFX: *noFX, Hardcore: *hardcore, SDR: cfg}), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
