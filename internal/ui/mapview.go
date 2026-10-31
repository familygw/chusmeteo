package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"chusmeteo/internal/api"
	"chusmeteo/internal/fx"
	"chusmeteo/internal/geo"
	"chusmeteo/internal/render"

	"github.com/charmbracelet/lipgloss"
)

const (
	gapW      = 2  // separación entre mapa y columna de datos
	minRightW = 34 // ancho mínimo de la columna de datos
	maxRightW = 60 // ...y máximo: más ancho no aporta, mejor centrar
)

// frameW: columnas extra del marco APT (si los efectos están activos).
func (m Model) frameW() int {
	if m.opts.NoFX {
		return 0
	}
	return fx.FrameW
}

// mapProjection: el mapa usa todo el alto y el ancho que deje la columna de datos.
// Argentina es alta y flaca, así que casi siempre manda el alto.
func (m Model) mapProjection() geo.Projection {
	maxW := m.contentW() - m.frameW() - gapW - minRightW
	return geo.NewProjection(max(maxW, 10), max(m.contentH(), 8)*2)
}

// detailed: ¿hay lugar para la columna de datos extendida?
func (m Model) detailed(rightW int) bool { return rightW >= 44 && m.contentH() >= 30 }

// mapCanvas compone: mapa base -> ciudades -> efecto APT -> marco.
func (m Model) mapCanvas(base *render.Canvas, p geo.Projection) *render.Canvas {
	for _, c := range geo.Cities {
		render.MarkCity(base, p, c.LatLon, render.Mark{Rune: '•'}, c.Short)
	}
	if m.state == stateReady {
		sel := geo.LatLon{Lat: m.loc.Latitude, Lon: m.loc.Longitude}
		x, y := p.ToPixel(sel)
		if x >= 0 && y >= 0 && x < p.W && y < p.H {
			render.MarkCity(base, p, sel, render.Mark{Rune: '◘'}, "")
		}
	}
	if m.opts.NoFX {
		return base
	}
	return fx.Frame(fx.Apply(base, m.rx))
}

// rightWidth: el ancho de la columna de datos para un mapa de c.W columnas.
func (m Model) rightWidth(c *render.Canvas) int {
	return min(m.contentW()-c.W-gapW, maxRightW)
}

// splitMap arma la vista: mapa + columna de datos, centrados en el panel.
func (m Model) splitMap(c *render.Canvas, info []string) string {
	rightW := m.rightWidth(c)
	rows := max(c.Rows(), len(info))
	// Todo lo que va sobre el panel lleva fondo azul y alto completo: si no,
	// lipgloss rellena con espacios "sin estilo" y se ve el fondo de la terminal.
	right := base.Width(rightW).Height(rows).Render(strings.Join(info, "\n"))
	gap := base.Width(gapW).Height(rows).Render("")
	block := lipgloss.JoinHorizontal(lipgloss.Top, c.String(), gap, right)
	return base.Width(m.contentW()).Align(lipgloss.Center).Render(block)
}

// receptionLines son las 2 líneas de estado de la "pasada del satélite".
func (m Model) receptionLines(done string) (string, string) {
	if m.rx == nil {
		return done, ""
	}
	sig := m.rx.Signal()
	bars := strings.Repeat("▮", sig) + strings.Repeat("▯", 8-sig)
	blink := "◄"
	if m.rx.Tick/4%2 == 0 { // parpadea cada 4 cuadros
		blink = " "
	}
	l1 := errStyle.Render(blink+" RECIBIENDO ") + valueStyle.Render(fx.Satellite)
	l2 := labelStyle.Render(fmt.Sprintf("%.4f MHz ", m.rx.FreqMHz())) + artStyle.Render(bars)
	return l1, l2
}

// bar dibuja una barra horizontal proporcional (con medio bloque de precisión).
func bar(v, lo, hi float64, width int, col lipgloss.Color) string {
	f := math.Max(0, math.Min(1, (v-lo)/(hi-lo))) * float64(width)
	full := int(f)
	s := strings.Repeat("█", full)
	if f-float64(full) >= 0.5 && full < width {
		s += "▌"
	}
	return base.Foreground(col).Render(s)
}

// --- Vista 2: mapa de temperaturas ---

func (m Model) tempMapView() string {
	p := m.mapProjection()
	var baseMap *render.Canvas
	if m.grid.pts != nil {
		baseMap = render.TempMap(p, m.grid.points())
	} else {
		baseMap = render.Outline(p)
	}
	c := m.mapCanvas(baseMap, p)
	rightW := m.rightWidth(c)

	title := titleStyle.Render("TEMPERATURA ACTUAL")
	if m.grid.off > 0 {
		t := time.Now().Truncate(time.Hour).Add(time.Duration(m.grid.off) * time.Hour)
		title = titleStyle.Render(fmt.Sprintf("PRONÓSTICO +%d h · %s", m.grid.off, t.Format("15:04")))
	}
	var info []string
	switch {
	case m.grid.pts == nil && m.grid.loading:
		info = []string{title, "", m.spin.View() + base.Render(" Descargando grilla...")}
	case m.grid.pts == nil && m.grid.err != nil:
		info = []string{title, "", errStyle.Render("▲ ERROR DE RECEPCIÓN"),
			base.Width(rightW).Render(m.grid.err.Error()), "", dimStyle.Render("F5 reintentar")}
	case m.grid.pts == nil:
		info = []string{title}
	default:
		l1, l2 := m.receptionLines(dimStyle.Render("■ RECEPCIÓN OK · " + m.grid.at.Format("15:04")))
		if l2 == "" {
			l2 = dimStyle.Render(fmt.Sprintf("Grilla %.1f° · %d puntos", gridStep, len(m.grid.pts)))
		}
		info = []string{title, l1, l2, ""}
		info = append(info, tempLegend()...)
		info = append(info, "")
		if m.detailed(rightW) {
			info = append(info, m.cityTable(rightW)...)
		} else {
			info = append(info, m.cityTemps()...)
		}
		info = append(info, "")
		info = append(info, m.extremes()...)
		if m.grid.loading {
			info = append(info, "", m.spin.View()+dimStyle.Render(" actualizando..."))
		}
		info = append(info, "", dimStyle.Render("• ciudades  ◘ "+truncate(m.loc.Name, 16)), dimStyle.Render("←/→ hora · Datos: Open-Meteo.com"))
	}
	return m.splitMap(c, info)
}

// swatch es un cuadradito de color (2 caracteres) para las leyendas.
func swatch(col render.Color) string {
	return lipgloss.NewStyle().Background(col).Render("  ")
}

func tempLegend() []string {
	var lines []string
	var cur strings.Builder
	for i, b := range render.TempBands {
		cur.WriteString(swatch(b.Color))
		cur.WriteString(base.Render(fmt.Sprintf(" %-7s", b.Label)))
		if i%3 == 2 || i == len(render.TempBands)-1 {
			lines = append(lines, cur.String())
			cur.Reset()
		}
	}
	return lines
}

// cityTemps: versión compacta, 3 ciudades por línea.
func (m Model) cityTemps() []string {
	var lines []string
	var cur strings.Builder
	for i, pw := range m.grid.cities() {
		c := geo.Cities[i]
		cur.WriteString(labelStyle.Render(c.Short))
		cur.WriteString(base.Render(" "))
		cur.WriteString(tempStyle(pw.Temperature).Render(fmt.Sprintf("%3.0f°", pw.Temperature)))
		cur.WriteString(artStyle.Render(api.Arrow(pw.WindDir)))
		cur.WriteString(base.Render(" "))
		if i%3 == 2 {
			lines = append(lines, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

// cityTable: versión extendida, una ciudad por línea con barra de temperatura.
func (m Model) cityTable(rightW int) []string {
	lines := []string{titleStyle.Render("CIUDADES")}
	barW := rightW - 36
	for i, pw := range m.grid.cities() {
		c := geo.Cities[i]
		lines = append(lines,
			labelStyle.Render(c.Short)+base.Render(fmt.Sprintf(" %-13s", c.Name))+
				tempStyle(pw.Temperature).Render(fmt.Sprintf("%4.0f° ", pw.Temperature))+
				artStyle.Render(api.Arrow(pw.WindDir))+base.Render(fmt.Sprintf("%3.0f ", pw.Wind))+
				bar(pw.Temperature, -5, 40, barW, render.TempColor(pw.Temperature)))
	}
	return lines
}

func (m Model) extremes() []string {
	cities := m.grid.cities()
	if len(cities) == 0 {
		return nil
	}
	hi, lo := 0, 0
	for i, c := range cities {
		if c.Temperature > cities[hi].Temperature {
			hi = i
		}
		if c.Temperature < cities[lo].Temperature {
			lo = i
		}
	}
	line := func(label string, i int) string {
		t := cities[i].Temperature
		return labelStyle.Render(label) + tempStyle(t).Render(fmt.Sprintf("%5.1f°C ", t)) + base.Render(geo.Cities[i].Name)
	}
	return []string{line("MÁX ", hi), line("MÍN ", lo)}
}

// --- Vista 3: radar ---

func (m Model) radarView() string {
	p := m.mapProjection()
	c := m.mapCanvas(render.RadarMap(p, m.radar.current()), p)
	rightW := m.rightWidth(c)
	r := m.radar

	info := []string{titleStyle.Render("RADAR · PRECIPITACIÓN")}
	switch {
	case r.idx == nil && r.loading:
		info = append(info, "", m.spin.View()+base.Render(" Buscando frames..."))
	case r.idx == nil && r.err != nil:
		info = append(info, "", errStyle.Render("▲ ERROR DE RECEPCIÓN"),
			base.Width(rightW).Render(r.err.Error()), "", dimStyle.Render("F5 reintentar"))
	case r.idx != nil:
		state := valueStyle.Render("▶ ANIMANDO")
		if !r.playing {
			state = artStyle.Render("❚❚ PAUSA")
		}
		l1, l2 := m.receptionLines(state)
		if l2 == "" {
			f := r.idx.Frames[r.pos]
			l2 = labelStyle.Render(fmt.Sprintf("CUADRO %2d/%d ", r.pos+1, len(r.idx.Frames))) +
				valueStyle.Render(f.Time.Local().Format("15:04"))
		}
		info = append(info, l1, l2, m.timeline(), "")
		for lvl := 1; lvl < len(render.RadarBands); lvl++ {
			b := render.RadarBands[lvl]
			info = append(info, swatch(b.Color)+base.Render(" "+b.Label))
		}
		info = append(info, "")
		if m.detailed(rightW) {
			info = append(info, m.cityRain()...)
			info = append(info, "")
		} else if s := m.rainSummary(); s != "" {
			info = append(info, base.Render(s), "")
		}
		if r.pending > 0 {
			info = append(info, m.spin.View()+dimStyle.Render(fmt.Sprintf(" bajando %d cuadros...", r.pending)))
		} else {
			info = append(info, dimStyle.Render(fmt.Sprintf("%d cuadros · cada 10 min", len(r.frames))))
		}
		if r.err != nil {
			info = append(info, errStyle.Render(truncate("▲ "+r.err.Error(), rightW)))
		}
		info = append(info,
			dimStyle.Render("ESPACIO play/pausa · ←/→"),
			"",
			labelStyle.Render("Radar: Rain Viewer"), // atribución obligatoria
			dimStyle.Render("rainviewer.com"))
	}
	return m.splitMap(c, info)
}

// timeline dibuja una barrita con un carácter por frame.
func (m Model) timeline() string {
	r := m.radar
	var b strings.Builder
	for i, f := range r.idx.Frames {
		switch {
		case i == r.pos:
			b.WriteString(valueStyle.Render("█"))
		case r.frames[f.Path] == nil:
			b.WriteString(dimStyle.Render("░"))
		default:
			b.WriteString(labelStyle.Render("▒"))
		}
	}
	return b.String()
}

// rainAt devuelve el nivel de lluvia más alto en ~25 km alrededor de un punto.
func (m Model) rainAt(pt geo.LatLon) int {
	img := m.radar.current()
	if img == nil {
		return 0
	}
	best := 0
	const d = 0.25
	k := math.Cos(pt.Lat * math.Pi / 180)
	for dy := -1.0; dy <= 1; dy++ {
		for dx := -1.0; dx <= 1; dx++ {
			s := img.Sample(geo.LatLon{Lat: pt.Lat + dy*d, Lon: pt.Lon + dx*d/k})
			best = max(best, render.RadarLevel(s))
		}
	}
	return best
}

func rainText(level int) string {
	if level == 0 {
		return "sin ecos"
	}
	return strings.ToLower(render.RadarBands[level].Label)
}

// cityRain: lluvia en cada ciudad de referencia, para el frame actual.
func (m Model) cityRain() []string {
	lines := []string{titleStyle.Render("LLUVIA POR CIUDAD")}
	for _, c := range geo.Cities {
		lvl := m.rainAt(c.LatLon)
		sw := base.Render("  ")
		if lvl > 0 {
			sw = swatch(render.RadarBands[lvl].Color)
		}
		text := dimStyle.Render(rainText(lvl))
		if lvl > 0 {
			text = valueStyle.Render(rainText(lvl))
		}
		lines = append(lines, labelStyle.Render(c.Short)+base.Render(fmt.Sprintf(" %-14s", c.Name))+sw+base.Render(" ")+text)
	}
	return lines
}

// rainSummary: ¿llueve en la ciudad elegida según el frame actual?
func (m Model) rainSummary() string {
	if m.radar.current() == nil || m.state != stateReady {
		return ""
	}
	pt := geo.LatLon{Lat: m.loc.Latitude, Lon: m.loc.Longitude}
	if pt.Lat > geo.Bounds.North || pt.Lat < geo.Bounds.South || pt.Lon < geo.Bounds.West || pt.Lon > geo.Bounds.East {
		return ""
	}
	return "◘ " + truncate(m.loc.Name, 14) + ": " + rainText(m.rainAt(pt))
}
