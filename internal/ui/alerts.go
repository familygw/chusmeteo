package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"chusmeteo/internal/api"
	"chusmeteo/internal/geo"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	tea "github.com/charmbracelet/bubbletea"
)

const alertsTTL = 10 * time.Minute // el SMN limita a ~200 pedidos cada 10 min

// alertState: avisos del SMN. byURL es el caché (sólo se bajan los nuevos).
type alertState struct {
	byURL   map[string]*api.Alert
	current []string // URLs presentes en el último índice
	at      time.Time
	tried   time.Time
	loading bool
	err     error
}

// active devuelve los avisos vigentes, ordenados (los de la ciudad primero).
func (a alertState) active(now time.Time, pt geo.LatLon) []*api.Alert {
	var out []*api.Alert
	for _, u := range a.current {
		if al := a.byURL[u]; al != nil && al.Active(now) {
			out = append(out, al)
		}
	}
	api.SortAlerts(out, pt)
	return out
}

// hailState: pronóstico de granizo de ICON para la ciudad elegida.
type hailState struct {
	outlook *api.HailOutlook
	err     error
}

type alertsMsg struct{ res *api.AlertsResult }
type alertsErrMsg struct{ err error }
type hailMsg struct{ o *api.HailOutlook }
type hailErrMsg struct{ err error }

func fetchAlerts(known map[string]bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		res, err := api.FetchAlerts(ctx, known)
		if err != nil {
			return alertsErrMsg{err}
		}
		return alertsMsg{res}
	}
}

func fetchHail(loc api.Location) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		o, err := api.GetHailOutlook(ctx, loc)
		if err != nil {
			return hailErrMsg{err}
		}
		return hailMsg{o}
	}
}

// obsState: observaciones METAR de aeropuertos cercanos a la ciudad elegida.
type obsState struct {
	st      []api.Station
	loc     string // ciudad de la que son las observaciones
	at      time.Time
	tried   time.Time
	loading bool
	err     error
}

type obsMsg struct{ st []api.Station }
type obsErrMsg struct{ err error }

const (
	obsTTL    = 10 * time.Minute
	obsRadius = 2.0 // grados (~200 km)
	hailNear  = 100 // km: hasta dónde un granizo observado cuenta como "cerca"
	hailFresh = 90 * time.Minute
)

func (m Model) ensureObs() (Model, tea.Cmd) {
	o := &m.obs
	now := time.Now()
	if m.state != stateReady || o.loading || now.Sub(o.at) < obsTTL || now.Sub(o.tried) < retryAfter {
		return m, nil
	}
	o.loading, o.tried = true, now
	loc := m.loc
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		st, err := api.GetStations(ctx, loc, obsRadius)
		if err != nil {
			return obsErrMsg{err}
		}
		return obsMsg{st}
	}
}

// hailOutlook: el pronóstico de ICON subido con lo que el modelo no ve
// (avisos del SMN y observaciones de aeropuertos cercanos).
func (m Model) hailOutlook() *api.HailOutlook {
	o := m.hail.outlook
	if o == nil {
		return nil
	}
	var boosts []api.HailBoost
	city, _ := m.cityAlerts()
	for _, a := range city {
		if !a.Hail {
			continue
		}
		floor := api.HailModerate
		if a.Level >= api.LevelOrange {
			floor = api.HailHigh
		}
		until := a.Expires
		if until.IsZero() {
			until = m.now.Add(3 * time.Hour)
		}
		boosts = append(boosts, api.HailBoost{Floor: floor, Until: until, Reason: "aviso SMN " + strings.ToLower(a.Level.String())})
		break // con el más grave alcanza (cityAlerts ya viene ordenado)
	}
	for _, s := range m.obs.st {
		if s.DistKm > hailNear {
			continue
		}
		switch {
		case !s.HailAt.IsZero() && m.now.Sub(s.HailAt) <= hailFresh:
			boosts = append(boosts, api.HailBoost{Floor: api.HailHigh, Until: m.now.Add(2 * time.Hour), Reason: "granizo observado en " + s.ICAO})
		case !s.ThunderAt.IsZero() && m.now.Sub(s.ThunderAt) <= hailFresh:
			boosts = append(boosts, api.HailBoost{Floor: api.HailModerate, Until: m.now.Add(2 * time.Hour), Reason: "tormenta observada en " + s.ICAO})
		}
	}
	return o.Boosted(m.now, boosts)
}

// hailObserved: estación cercana que reportó granizo hace poco.
func (m Model) hailObserved() (api.Station, bool) {
	for _, s := range m.obs.st {
		if s.DistKm <= hailNear && !s.HailAt.IsZero() && m.now.Sub(s.HailAt) <= hailFresh {
			return s, true
		}
	}
	return api.Station{}, false
}

// ensureAlerts: los avisos se actualizan en segundo plano, en cualquier vista.
func (m Model) ensureAlerts() (Model, tea.Cmd) {
	var c1, c2 tea.Cmd
	m, c1 = m.ensureSMN()
	m, c2 = m.ensureObs()
	return m, tea.Batch(c1, c2)
}

func (m Model) ensureSMN() (Model, tea.Cmd) {
	a := &m.alerts
	now := time.Now()
	if a.loading || now.Sub(a.at) < alertsTTL || now.Sub(a.tried) < retryAfter {
		return m, nil
	}
	a.loading, a.tried = true, now
	known := make(map[string]bool, len(a.byURL)) // copia: la goroutine no comparte nuestro map
	for u := range a.byURL {
		known[u] = true
	}
	return m, fetchAlerts(known)
}

// onAlerts arma un caché NUEVO (en vez de mutar el viejo): sólo quedan los
// avisos que siguen en el índice, más los recién bajados.
func (m Model) onAlerts(res *api.AlertsResult) Model {
	next := make(map[string]*api.Alert, len(res.Current))
	for _, u := range res.Current {
		if a, ok := res.New[u]; ok {
			next[u] = a
		} else if a, ok := m.alerts.byURL[u]; ok {
			next[u] = a
		}
	}
	at := time.Now()
	if res.Limited { // quedaron avisos por bajar: reintentamos en 3 min, no en 10
		at = at.Add(-alertsTTL + 3*time.Minute)
	}
	m.alerts = alertState{byURL: next, current: res.Current, at: at, tried: m.alerts.tried}
	return m
}

// cityPoint: la ubicación elegida (o Córdoba si todavía no llegó).
func (m Model) cityPoint() geo.LatLon {
	if m.state == stateReady {
		return geo.LatLon{Lat: m.loc.Latitude, Lon: m.loc.Longitude}
	}
	return geo.Cities[4].LatLon
}

// cityAlerts: avisos vigentes que cubren la ciudad elegida.
func (m Model) cityAlerts() (city, all []*api.Alert) {
	all = m.alerts.active(m.now, m.cityPoint())
	for _, a := range all {
		if a.Covers(m.cityPoint()) {
			city = append(city, a)
		}
	}
	return city, all
}

// alertBadge: indicador para la barra superior ("" si no hay nada que avisar).
func (m Model) alertBadge(short bool) string {
	city, all := m.cityAlerts()
	ho := m.hailOutlook()
	hailRisk := ho != nil && ho.Max >= api.HailModerate
	if countHail(city) > 0 {
		hailRisk = true
	}
	if _, ok := m.hailObserved(); ok {
		hailRisk = true
	}
	blink := m.now.Second()%2 == 0
	switch {
	case len(city) > 0 || hailRisk:
		text := " ⚠ GRANIZO? F4 "
		if len(city) > 0 {
			text = " ⚠ AVISO SMN F4 "
		}
		if short {
			text = " ⚠ F4 "
		}
		st := popupTitle
		if blink {
			st = popupText.Bold(true)
		}
		return st.Render(text)
	case len(all) > 0 && !short:
		return barStyle.Foreground(vgaDarkGray).Render(fmt.Sprintf(" SMN: %d avisos ", len(all)))
	}
	return ""
}

// --- Popup al estilo de los diálogos de error de Norton Commander ---

var (
	popupBG    = lipgloss.Color("#AA0000")
	popupText  = lipgloss.NewStyle().Background(popupBG).Foreground(vgaWhite)
	popupHi    = popupText.Foreground(vgaYellow).Bold(true)
	popupDim   = popupText.Foreground(vgaGray)
	popupTitle = lipgloss.NewStyle().Background(vgaWhite).Foreground(popupBG).Bold(true)
	popupBtn   = lipgloss.NewStyle().Background(vgaCyan).Foreground(vgaBlack)
	shadow     = lipgloss.NewStyle().Background(vgaBlack)
)

var levelStyle = map[api.Level]lipgloss.Style{
	api.LevelYellow: lipgloss.NewStyle().Background(vgaYellow).Foreground(vgaBlack),
	api.LevelOrange: lipgloss.NewStyle().Background(lipgloss.Color("#FF8800")).Foreground(vgaBlack),
	api.LevelRed:    lipgloss.NewStyle().Background(vgaRed).Foreground(vgaWhite).Bold(true),
	api.LevelNone:   lipgloss.NewStyle().Background(vgaGray).Foreground(vgaBlack),
}

var riskStyle = map[api.HailRisk]lipgloss.Style{
	api.HailNone:     popupText.Foreground(vgaLGreen).Bold(true),
	api.HailLow:      popupText.Foreground(vgaWhite).Bold(true),
	api.HailModerate: popupText.Foreground(vgaYellow).Bold(true),
	api.HailHigh:     lipgloss.NewStyle().Background(vgaYellow).Foreground(vgaRed).Bold(true),
}

// popupLines arma el contenido (sin bordes) de ancho w.
func (m Model) popupLines(w int) []string {
	pad := func(s string) string { // completa la línea con fondo rojo
		if gap := w - lipgloss.Width(s); gap > 0 {
			s += popupText.Render(strings.Repeat(" ", gap))
		}
		return s
	}
	text := func(s string) string { return pad(popupText.Render(truncate(s, w))) }
	dim := func(s string) string { return pad(popupDim.Render(truncate(s, w))) }
	hi := func(s string) string { return pad(popupHi.Render(truncate(s, w))) }

	name := "Córdoba"
	if m.state == stateReady {
		name = m.loc.Name
	}
	city, all := m.cityAlerts()
	var lines []string

	// SMN
	lines = append(lines, hi("AVISOS SMN · "+strings.ToUpper(name)))
	switch {
	case m.alerts.byURL == nil && m.alerts.loading:
		lines = append(lines, text("Consultando al SMN..."))
	case m.alerts.byURL == nil && m.alerts.err != nil:
		lines = append(lines, text("No se pudo consultar: "+m.alerts.err.Error()))
	case len(city) == 0:
		lines = append(lines, text("Sin avisos vigentes para "+name+"."))
	}
	for _, a := range city {
		tag := levelStyle[a.Level].Render(fmt.Sprintf(" %-8s ", a.Level))
		hail := ""
		if a.Hail {
			hail = popupHi.Render(" ▲GRANIZO")
		}
		ev := sentence(stripLevel(a.Event))
		room := w - lipgloss.Width(tag) - lipgloss.Width(hail) - 1
		lines = append(lines, pad(tag+popupText.Render(" "+truncate(ev, room))+hail))
		until := ""
		if !a.Expires.IsZero() {
			until = "hasta " + untilLabel(a.Expires.Local(), m.now) + " · "
		}
		lines = append(lines, dim("  "+until+a.AreaDesc))
	}
	hailAll := 0
	for _, a := range all {
		if a.Hail {
			hailAll++
		}
	}
	if others := len(all) - len(city); others > 0 {
		lines = append(lines, text(fmt.Sprintf("En el resto del país: %d avisos (%d con granizo).", others, hailAll-countHail(city))))
	}
	lines = append(lines, pad(""))

	// METAR
	lines = append(lines, hi("OBSERVADO · AEROPUERTOS CERCANOS (METAR)"))
	switch {
	case len(m.obs.st) == 0 && m.obs.err != nil:
		lines = append(lines, text("Sin datos: "+m.obs.err.Error()))
	case len(m.obs.st) == 0:
		lines = append(lines, text("Sin estaciones cercanas en las últimas horas."))
	default:
		for i, s := range m.obs.st {
			if i == 3 {
				break
			}
			wx := s.Weather
			if wx == "" {
				wx = "sin fenómenos"
			}
			hail := ""
			if !s.HailAt.IsZero() {
				hail = popupHi.Render(" ▲GRANIZO " + ago(m.now, s.HailAt))
			} else if !s.ThunderAt.IsZero() {
				hail = popupText.Render(" tormenta " + ago(m.now, s.ThunderAt))
			}
			head := fmt.Sprintf("%s %s · %.0f km · %s · %s", s.ICAO, s.Name, s.DistKm, s.Time.Format("15:04"), wx)
			lines = append(lines, pad(popupText.Render(truncate(head, w-lipgloss.Width(hail)))+hail))
		}
	}
	lines = append(lines, pad(""))

	// ICON
	lines = append(lines, hi("MODELO ICON · PRÓXIMAS 24 H (estimación)"))
	o := m.hailOutlook()
	switch {
	case o == nil && m.hail.err != nil:
		lines = append(lines, text("Sin datos: "+m.hail.err.Error()))
	case o == nil:
		lines = append(lines, text("Esperando el pronóstico..."))
	default:
		risk := popupText.Render("Riesgo de granizo: ") + riskStyle[o.Max].Render(" "+o.Max.String()+" ")
		lines = append(lines, pad(risk))
		if o.Max > api.HailNone {
			ref := o.Hours[0].Time
			for i, e := range o.Episodes(api.HailLow) {
				if i == 3 {
					break
				}
				lines = append(lines, pad(popupText.Render("  "+e.Label(ref)+" ")+riskStyle[e.Peak].Render(" "+e.Peak.String()+" ")))
			}
			if cape := o.PeakCAPE(); cape > 0 {
				lines = append(lines, text(fmt.Sprintf("Tamaño probable: %s (CAPE %.0f J/kg; chico no es inofensivo)", api.HailSize(cape), cape)))
			}
		}
		explicit := "no"
		if o.Explicit {
			explicit = "SÍ"
		}
		switch {
		case !o.Crossed:
		case o.Disagree:
			lines = append(lines, text("GFS ve menos energía que ICON: bajamos el riesgo."))
		default:
			lines = append(lines, text("Contrastado con GFS: no lo contradice."))
		}
		if city, _ := m.cityAlerts(); o.Max < api.HailModerate && countHail(city) > 0 {
			lines = append(lines, hi("Ojo: aviso SMN con granizo en tu ciudad; el modelo lo subestima."))
		}
		if len(o.Notes) > 0 {
			lines = append(lines, hi("Subido por: "+strings.Join(o.Notes, ", ")))
		}
		lines = append(lines,
			text("Granizo explícito en ICON (códigos 96/99): "+explicit),
			text(fmt.Sprintf("CAPE máx %.0f J/kg · isoterma 0 °C mín %.0f m", o.MaxCAPE, o.MinFZL)))
	}
	lines = append(lines, pad(""),
		dim("Ni los avisos ni los modelos aciertan siempre con el granizo:"),
		dim("tomalo como orientación, no como certeza."),
		pad(""))

	btn := popupBtn.Render("  Aceptar  ")
	left := (w - lipgloss.Width(btn)) / 2
	lines = append(lines, pad(popupText.Render(strings.Repeat(" ", left))+btn))
	return lines
}

func countHail(as []*api.Alert) int {
	n := 0
	for _, a := range as {
		if a.Hail {
			n++
		}
	}
	return n
}

// stripLevel saca el " - AVISO NARANJA" final (ya lo muestra la etiqueta).
func stripLevel(ev string) string {
	if i := strings.LastIndex(ev, " - "); i > 0 {
		return ev[:i]
	}
	return ev
}

// ago: "hace 25 min" / "hace 2 h".
func ago(now, t time.Time) string {
	d := max(now.Sub(t), 0)
	if d < time.Hour {
		return fmt.Sprintf("hace %d min", int(d.Minutes()))
	}
	return fmt.Sprintf("hace %d h", int(d.Hours()))
}

// untilLabel: "15:04", "mañana 08:59" o "02/11 08:59" según el día respecto de now.
func untilLabel(t, now time.Time) string {
	y, mo, d := now.Date()
	today := time.Date(y, mo, d, 0, 0, 0, 0, now.Location())
	t = t.In(now.Location())
	days := int(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, now.Location()).Sub(today).Hours() / 24)
	switch {
	case days <= 0:
		return t.Format("15:04")
	case days == 1:
		return "mañana " + t.Format("15:04")
	}
	return t.Format("02/01 15:04")
}

// sentence: "TORMENTAS FUERTES" -> "Tormentas fuertes" (el SMN escribe en mayúsculas).
func sentence(s string) string {
	r := []rune(strings.ToLower(s))
	if len(r) > 0 {
		r[0] = []rune(strings.ToUpper(string(r[0])))[0]
	}
	return string(r)
}

// popupBox dibuja el diálogo con doble borde y título centrado en el borde.
func (m Model) popupBox() string {
	w := min(72, m.width-10) // ancho interior
	body := m.popupLines(w - 2)

	title := " AVISOS Y GRANIZO "
	side := w - lipgloss.Width(title)
	top := popupText.Render("╔"+strings.Repeat("═", side/2)) + popupTitle.Render(title) +
		popupText.Render(strings.Repeat("═", side-side/2)+"╗")
	out := []string{top}
	for _, l := range body {
		out = append(out, popupText.Render("║ ")+l+popupText.Render(" ║"))
	}
	out = append(out, popupText.Render("╚"+strings.Repeat("═", w)+"╝"))
	return strings.Join(out, "\n")
}

// overlay pega `fg` sobre `bg` en (x, y), con sombra negra de 2 columnas a la
// derecha y 1 fila abajo, como en los diálogos de DOS. ansi.Truncate y
// ansi.TruncateLeft cortan texto CON colores sin romper las secuencias de escape.
func overlay(bg, fg string, x, y int) string {
	bgLines := strings.Split(bg, "\n")
	fgLines := strings.Split(fg, "\n")
	w := lipgloss.Width(fg)
	put := func(row int, s string, col, width int) {
		if row < 0 || row >= len(bgLines) {
			return
		}
		line := bgLines[row]
		bgLines[row] = ansi.Truncate(line, col, "") + s + ansi.TruncateLeft(line, col+width, "")
	}
	for i, l := range fgLines {
		put(y+i, l, x, w)
		if i > 0 {
			put(y+i, shadow.Render("  "), x+w, 2)
		}
	}
	put(y+len(fgLines), shadow.Render(strings.Repeat(" ", w)), x+2, w)
	return strings.Join(bgLines, "\n")
}
