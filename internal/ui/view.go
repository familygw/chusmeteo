package ui

import (
	"fmt"
	"strings"
	"time"

	"chusmeteo/internal/api"
	"chusmeteo/internal/render"

	"github.com/charmbracelet/lipgloss"
)

const (
	version = "v0.6"
	minW    = 72
	minH    = 24
)

var weekdays = [...]string{"DOM", "LUN", "MAR", "MIÉ", "JUE", "VIE", "SÁB"}

// --- Tamaños: todo se calcula a partir de la terminal (responsive) ---

// contentW es el ancho útil dentro del panel (pantalla - bordes - padding).
func (m Model) contentW() int { return m.width - 4 }

// contentH es el alto útil para la vista (pantalla - barras - bordes - búsqueda).
func (m Model) contentH() int {
	h := m.height - 4
	if m.searching {
		h -= 2
	}
	return h
}

// View dibuja TODA la pantalla en cada frame. Bubble Tea se encarga de
// redibujar sólo lo que cambió, así que no hay que optimizar nada a mano.
func (m Model) View() string {
	if m.width == 0 {
		return "" // todavía no llegó el tamaño de la terminal
	}
	if m.width < minW || m.height < minH {
		msg := fmt.Sprintf("Agrandá la terminal (mín %dx%d, ahora %dx%d)", minW, minH, m.width, m.height)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
			valueStyle.Render(msg), lipgloss.WithWhitespaceBackground(vgaBlue))
	}

	var content string
	switch {
	case m.showHelp:
		content = m.helpView()
	case m.view == viewMap:
		content = m.tempMapView()
	case m.view == viewRadar:
		content = m.radarView()
	case m.view == viewSat:
		content = m.satView()
	case m.state == stateLoading:
		content = m.loadingView()
	case m.state == stateError:
		content = m.errorView()
	default:
		content = m.weatherView()
	}
	content = clampLines(content, m.contentH())
	if m.searching {
		content += "\n" + dimStyle.Render(strings.Repeat("─", m.contentW())) + "\n" + m.input.View()
	}

	// El panel ocupa toda la pantalla, como una app de DOS a pantalla completa.
	// Width incluye el padding; los bordes suman 2 más.
	panel := panelStyle.Width(m.width - 2).Height(m.height - 4).Render(content)
	screen := lipgloss.JoinVertical(lipgloss.Left, m.headerView(), panel, m.footerView())
	if m.showAlerts {
		box := m.popupBox()
		x := (m.width - lipgloss.Width(box)) / 2
		y := max(1, (m.height-lipgloss.Height(box))/2)
		screen = overlay(screen, box, x, y)
	}
	return screen
}

// clampLines recorta un bloque a n líneas: red de seguridad para que ninguna
// vista empuje las barras fuera de la pantalla.
func clampLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n")
}

// --- Barras superior e inferior ---

var tabNames = [viewCount]string{"CLIMA", "MAPA", "RADAR"}

func (m Model) headerView() string {
	left := barStyle.Render(" ☼ CHUSMETEO " + version + " ")
	right := barStyle.Render(weekdays[m.now.Weekday()] + " " + m.now.Format("02/01/2006 15:04:05") + " ")
	// Indicador de avisos/granizo: versión corta si no entra.
	tabsW := 26
	if m.opts.Hardcore {
		tabsW += 12
	}
	badge := m.alertBadge(false)
	fits := func() bool {
		return lipgloss.Width(left)+lipgloss.Width(badge)+tabsW+lipgloss.Width(right)+2 <= m.width
	}
	if !fits() {
		badge = m.alertBadge(true)
	}
	if !fits() { // terminal angosta: sólo la hora
		right = barStyle.Render(" " + m.now.Format("15:04:05") + " ")
	}
	left += badge

	var tabs strings.Builder
	names := tabNames[:]
	if m.opts.Hardcore {
		names = append(names, "SAT")
	}
	for i, name := range names {
		label := fmt.Sprintf(" %d %s ", i+1, name)
		if View(i) == m.view {
			tabs.WriteString(barKeyStyle.Render(label))
		} else {
			tabs.WriteString(barStyle.Render(label))
		}
	}

	// Centramos las pestañas en el espacio libre entre izquierda y derecha.
	free := m.width - lipgloss.Width(left) - lipgloss.Width(right) - lipgloss.Width(tabs.String())
	if free < 2 {
		free = 2
	}
	return left + barStyle.Render(strings.Repeat(" ", free/2)) + tabs.String() +
		barStyle.Render(strings.Repeat(" ", free-free/2)) + right
}

func (m Model) footerView() string {
	keys := []struct{ k, label string }{
		{"F1", "Ayuda"}, {"F2", "Vista"}, {"F3", "Ciudad"}, {"F4", "Granizo"}, {"F5", "Refrescar"}, {"F10", "Salir"},
	}
	var b strings.Builder // como StringBuilder de Java
	for _, k := range keys {
		b.WriteString(barKeyStyle.Render(k.k))
		b.WriteString(barStyle.Render(" " + k.label + "  "))
	}
	line := b.String()
	if pad := m.width - lipgloss.Width(line); pad > 0 {
		line += barStyle.Render(strings.Repeat(" ", pad))
	}
	return line
}

// --- Vista 1: clima ---

func (m Model) weatherView() string {
	cw, ch := m.contentW(), m.contentH()
	c := m.fc.Current
	desc, sky := api.Describe(c.WeatherCode)

	// Arriba: dibujo | datos principales | (si hay ancho) datos extra.
	const artW, topH, dataW = 15, 8, 50
	art := base.Width(artW).Height(topH).PaddingTop(1).
		Render(artStyle.Render(bigIcon(sky, c.IsDay == 1)))

	row := func(label, value string) string {
		return labelStyle.Render(fmt.Sprintf("%-12s", label)) + value
	}
	main := []string{
		titleStyle.Render(truncate(strings.ToUpper(m.loc.Label()), dataW)),
		base.Render(desc),
		"",
		row("TEMPERATURA", tempStyle(c.Temperature).Render(fmt.Sprintf("%.1f°C", c.Temperature))+
			dimStyle.Render(fmt.Sprintf("  (ST %.1f°C)", c.ApparentTemp))),
		row("HUMEDAD", valueStyle.Render(fmt.Sprintf("%.0f %%", c.Humidity))),
		row("VIENTO", valueStyle.Render(fmt.Sprintf("%s %s %.0f km/h", api.Arrow(c.WindDirection), api.Compass(c.WindDirection), c.WindSpeed))),
		row("PRESIÓN", valueStyle.Render(fmt.Sprintf("%.0f hPa", c.Pressure))),
		row("GRANIZO", m.hailSummary()),
	}
	cols := []string{art}
	if cw >= artW+dataW+34 { // hay lugar para la tercera columna
		cols = append(cols, base.Width(dataW).Height(topH).Render(strings.Join(main, "\n")))
		rise, set := m.fc.SunTimes()
		uv := 0.0
		if len(m.fc.Daily.UVMax) > 0 {
			uv = m.fc.Daily.UVMax[0]
		}
		extra := []string{
			"", "", "",
			row("RÁFAGAS", valueStyle.Render(fmt.Sprintf("%.0f km/h", c.WindGusts))),
			row("NUBOSIDAD", valueStyle.Render(fmt.Sprintf("%.0f %%", c.CloudCover))),
			row("UV MÁX", valueStyle.Render(fmt.Sprintf("%.1f", uv))),
			row("SOL", artStyle.Render("↑ ")+valueStyle.Render(rise)+artStyle.Render("  ↓ ")+valueStyle.Render(set)),
		}
		cols = append(cols, base.Width(cw-artW-dataW).Height(topH).Render(strings.Join(extra, "\n")))
	} else {
		cols = append(cols, base.Width(cw-artW).Height(topH).Render(strings.Join(main, "\n")))
	}
	top := lipgloss.JoinHorizontal(lipgloss.Top, cols...)

	sep := dimStyle.Render(strings.Repeat("─", cw))
	parts := []string{top, sep, m.forecastRow(cw)}

	// Si sobra alto, el gráfico de las próximas 24 horas.
	used := topH + 1 + 6 + 1              // arriba + separador + pronóstico + pie
	if rows := ch - used - 3; rows >= 6 { // el gráfico se estira con la terminal
		parts = append(parts, sep, m.hourlyChart(cw, rows))
	}
	foot := dimStyle.Render(fmt.Sprintf("Actualizado %s  ·  Datos: Open-Meteo.com", m.updated.Format("15:04")))
	parts = append(parts, base.Width(cw).Render(foot))
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// forecastRow: tantas cajas de días como entren (hasta 7), centradas.
func (m Model) forecastRow(cw int) string {
	days := m.fc.Days()
	n := min(len(days), max(1, cw/16))
	days = days[:n]
	boxW := min(22, cw/n-2) // ancho interior; +2 de borde
	boxes := make([]string, 0, n)
	for i, d := range days {
		name := weekdays[d.Date.Weekday()] + " " + d.Date.Format("02")
		if i == 0 {
			name = "HOY"
		}
		_, sky := api.Describe(d.WeatherCode)
		content := strings.Join([]string{
			labelStyle.Render(name),
			artStyle.Render(smallIcon(sky)),
			tempStyle(d.Max).Render(fmt.Sprintf("%.0f°", d.Max)) + dimStyle.Render(" / ") +
				tempStyle(d.Min).Render(fmt.Sprintf("%.0f°", d.Min)),
			dimStyle.Render(fmt.Sprintf("☂ %.0f%%", d.PrecipProb)),
		}, "\n")
		boxes = append(boxes, dayStyle.Width(boxW).Render(content))
	}
	return base.Width(cw).Align(lipgloss.Center).Render(lipgloss.JoinHorizontal(lipgloss.Top, boxes...))
}

// hourlyChart: título + gráfico en pixel art + eje de horas.
func (m Model) hourlyChart(cw, rows int) string {
	hours := m.fc.Hours()
	if len(hours) == 0 {
		return base.Width(cw).Render(dimStyle.Render("Sin pronóstico horario"))
	}
	const axisW = 6
	plotW := cw - axisW
	temps := make([]float64, len(hours))
	probs := make([]float64, len(hours))
	for i, h := range hours {
		temps[i], probs[i] = h.Temperature, h.PrecipProb
	}
	// Si hay pronóstico de ICON, reservamos una fila para la franja de riesgo.
	risk := m.riskStrip(hours, plotW)
	if risk != "" {
		rows--
	}
	chart, lo, hi := render.HourlyChart(plotW, rows*2, temps, probs)

	// Eje izquierdo: máximo arriba, mínimo al pie de las barras, ☂ en la franja de lluvia.
	axis := make([]string, rows)
	rainRows := (max(2, rows*2/5) + 1) / 2
	axis[0] = fmt.Sprintf("%4.0f°", hi)
	axis[rows-rainRows-1] = fmt.Sprintf("%4.0f°", lo)
	axis[rows-1] = "   ☂%"
	left := dimStyle.Width(axisW).Render(strings.Join(axis, "\n"))

	// Eje de abajo: la hora cada 3 (o cada 6 si no entra).
	every := 3
	if plotW/len(hours)*every < 3 {
		every = 6
	}
	ticks := []rune(strings.Repeat(" ", plotW))
	for i := 0; i < len(hours); i += every {
		pos := i * plotW / len(hours)
		label := []rune(hours[i].Time.Format("15"))
		if pos+len(label) <= plotW {
			copy(ticks[pos:], label)
		}
	}

	title := titleStyle.Render("PRÓXIMAS 24 HORAS  ") +
		dimStyle.Render("barras: temperatura · abajo: probabilidad de lluvia")
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, chart.String())
	parts := []string{base.Width(cw).Render(title), body}
	if risk != "" {
		parts = append(parts, dimStyle.Width(axisW).Render(" gran")+risk)
	}
	parts = append(parts, dimStyle.Width(cw).Render(strings.Repeat(" ", axisW)+string(ticks)))
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m Model) loadingView() string {
	lines := []string{
		"",
		m.spin.View() + titleStyle.Render("  SINTONIZANDO SATÉLITE..."),
		"",
		labelStyle.Render("Buscando: ") + valueStyle.Render(m.city),
		"",
	}
	return base.Width(m.contentW()).Render(strings.Join(lines, "\n"))
}

func (m Model) errorView() string {
	cw := m.contentW()
	lines := []string{
		"",
		errStyle.Render("▲ ERROR DE RECEPCIÓN"),
		"",
		base.Render(truncate(m.err.Error(), cw)),
		"",
		dimStyle.Render("F5 reintentar  ·  F3 probar otra ciudad"),
	}
	return base.Width(cw).Render(strings.Join(lines, "\n"))
}

func (m Model) helpView() string {
	key := func(k, d string) string {
		return valueStyle.Render(fmt.Sprintf("  %-12s", k)) + base.Render(d)
	}
	lines := []string{
		titleStyle.Render("AYUDA"),
		"",
		key("F1  ?", "Mostrar / ocultar esta ayuda"),
		key("F2  Tab", "Cambiar de vista (o 1 clima · 2 mapa · 3 radar)"),
		key("F3  /  c", "Buscar otra ciudad (\"Ciudad, AR\" filtra por país)"),
		key("F4  !", "Avisos del SMN y riesgo de granizo (modelo ICON)"),
		key("F5  r", "Refrescar ahora (clima 15 min, mapa 30, radar 10)"),
		key("a", "Repetir la recepción del satélite"),
		key("Esp  p", "Radar: play / pausa"),
		key("←  →", "Mapa: hora del pronóstico · Radar: cuadro anterior / siguiente"),
	}
	if m.opts.Hardcore {
		lines = append(lines, key("4  s", "Satélite: vista Meteor-M · s inicia / detiene la captura"))
	}
	lines = append(lines,
		key("Esc", "Saltear recepción · cerrar ayuda · cancelar"),
		key("F10 q", "Salir"),
		"",
		dimStyle.Render("Todo se adapta al tamaño de la terminal: agrandala para ver más detalle."),
		dimStyle.Render("Si tu terminal no manda F1..F10, usá las teclas alternativas."),
		dimStyle.Render("La recepción homenajea a NOAA-15, último satélite con APT (1998-2025)."),
	)
	return base.Width(m.contentW()).Render(strings.Join(lines, "\n"))
}

// truncate corta por runas (no por bytes): "Córdoba" tiene 7 runas pero 8 bytes.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return string(r[:n-1]) + "…"
}

// hailSummary: una línea con el riesgo de granizo según ICON.
func (m Model) hailSummary() string {
	o := m.hail.outlook
	if o == nil {
		if m.hail.err != nil {
			return base.Render("sin datos de ICON")
		}
		return base.Render("consultando ICON...")
	}
	styles := map[api.HailRisk]lipgloss.Style{
		api.HailNone:     valueStyle.Foreground(vgaLGreen),
		api.HailLow:      valueStyle,
		api.HailModerate: valueStyle.Foreground(vgaYellow),
		api.HailHigh:     valueStyle.Foreground(vgaLRed).Blink(true),
	}
	out := styles[o.Max].Render(o.Max.String())
	if eps := o.Episodes(max(o.Max, api.HailLow)); len(eps) > 0 && o.Max > api.HailNone {
		out += valueStyle.Render(" " + eps[0].Label(o.Hours[0].Time))
	}
	return out + base.Render(" · F4")
}

// riskStrip: una fila de colores con el riesgo de granizo por hora, alineada
// con las barras del gráfico ("" si no hay datos de ICON).
func (m Model) riskStrip(hours []api.Hour, w int) string {
	o := m.hail.outlook
	if o == nil || len(hours) == 0 {
		return ""
	}
	byTime := make(map[time.Time]api.HailRisk, len(o.Hours))
	for _, h := range o.Hours {
		byTime[h.Time] = h.Risk
	}
	colors := [...]render.Color{render.Gray8(1), render.Gray, render.Yellow, render.LRed}
	c := render.New(w, 2, render.Black)
	n := len(hours)
	for i, h := range hours {
		r, ok := byTime[h.Time]
		if !ok {
			continue
		}
		x0, x1 := i*w/n, (i+1)*w/n
		if x1-x0 >= 3 {
			x1--
		}
		for x := x0; x < x1; x++ {
			c.Set(x, 0, colors[r])
			c.Set(x, 1, colors[r])
		}
	}
	return c.String()
}
