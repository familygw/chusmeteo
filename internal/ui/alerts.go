package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"chusmeteo/internal/api"
	"chusmeteo/internal/geo"
	"chusmeteo/internal/render"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	tea "github.com/charmbracelet/bubbletea"
)

// El índice del SMN es UN pedido y los XML se cachean: consultarlo seguido es
// barato (el límite es ~200 pedidos cada 10 min). Importa porque los avisos a
// corto plazo duran 1 a 3 horas: enterarse 10 minutos tarde es mucho.
const alertsTTL = 3 * time.Minute

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

// nowState: nowcast de celdas fuertes con el radar, alrededor de la ciudad elegida.
type nowState struct {
	nc      *api.Nowcast
	grids   map[string]*api.RadarGrid // caché por path del cuadro (son inmutables)
	loc     string
	at      time.Time
	tried   time.Time
	loading bool
	err     error
}

type nowMsg struct {
	nc    *api.Nowcast
	grids map[string]*api.RadarGrid
	loc   string
}
type nowErrMsg struct {
	err error
	loc string
}

const (
	nowTTL    = 5 * time.Minute  // hay cuadro nuevo cada 10: con 5 lo agarramos rápido
	nowWithin = 60 * time.Minute // la barra avisa si la celda llega antes de esto
)

func (m Model) ensureNow() (Model, tea.Cmd) {
	n := &m.cells
	now := time.Now()
	if m.state != stateReady || n.loading || now.Sub(n.at) < nowTTL || now.Sub(n.tried) < retryAfter {
		return m, nil
	}
	n.loading, n.tried = true, now
	loc, cache := m.loc, n.grids // el map no se modifica nunca: GetNowcast devuelve uno nuevo
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		nc, grids, err := api.GetNowcast(ctx, loc, cache, render.RadarLevel)
		if err != nil {
			return nowErrMsg{err, loc.Name}
		}
		return nowMsg{nc, grids, loc.Name}
	}
}

// cellThreat: núcleo fuerte encima o llegando en la próxima hora.
func (m Model) cellThreat() (level int, ok bool) {
	return m.cells.nc.Threat(m.now, nowWithin)
}

// officialHail: el nivel más alto entre los avisos del SMN con granizo que cubren la ciudad.
func (m Model) officialHail() (api.Level, bool) {
	city, _ := m.cityAlerts()
	lvl, ok := api.LevelNone, false
	for _, a := range city {
		if a.Hail {
			lvl, ok = max(lvl, a.Level), true
		}
	}
	return lvl, ok
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
	var c1, c2, c3 tea.Cmd
	m, c1 = m.ensureSMN()
	m, c2 = m.ensureObs()
	m, c3 = m.ensureNow()
	return m, tea.Batch(c1, c2, c3)
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

	// 1) URGENTE (titila): lo que habla de ESTE punto y de AHORA, de más a
	// menos confiable: la celda en el radar, el aviso a corto plazo del SMN
	// (sale del radar, polígono chico) y el granizo visto en un aeropuerto.
	urgent := ""
	if _, ok := m.cellThreat(); ok {
		nc := m.cells.nc
		urgent = " ⚠ CELDA ENCIMA F4 "
		if !nc.Overhead {
			urgent = fmt.Sprintf(" ⚠ CELDA EN %d MIN F4 ", max(int(nc.ETA.Sub(m.now).Minutes()), 0))
		}
	}
	if urgent == "" {
		for _, a := range city {
			if a.ShortTerm {
				urgent = " ⚠ AVISO A CORTO PLAZO F4 "
				break
			}
		}
	}
	if _, ok := m.hailObserved(); ok && urgent == "" {
		urgent = " ⚠ GRANIZO OBSERVADO CERCA F4 "
	}
	if urgent != "" {
		if short {
			urgent = " ⚠ F4 "
		}
		st := popupTitle
		if m.now.Second()%2 == 0 {
			st = popupText.Bold(true)
		}
		return st.Render(urgent)
	}

	// 2) CONTEXTO DEL DÍA (fijo, sin titilar): la alerta por zona del SMN y
	// los modelos dicen "hoy puede pasar", no "te está por pasar". Con
	// tormentas en la provincia están prendidos casi todo el día.
	ho := m.hail.outlook // la barra mira 24 h; lo de pasado mañana está en el popup
	calm := barStyle.Bold(true)
	switch {
	case len(city) > 0 && short:
		return calm.Render(" ⚠ F4 ")
	case len(city) > 0:
		return calm.Render(" ⚠ AVISO SMN F4 ")
	case ho != nil && ho.MaxWithin(24*time.Hour) >= api.HailModerate && short:
		return calm.Render(" ⚠ F4 ")
	case ho != nil && ho.MaxWithin(24*time.Hour) >= api.HailModerate:
		return calm.Render(" ⚠ GRANIZO? F4 ")
	case len(all) > 0 && !short:
		return barStyle.Foreground(vgaDarkGray).Render(fmt.Sprintf(" SMN: %d avisos ", len(all)))
	}
	return ""
}

// meltLevel: con la isoterma de 0 °C por encima de esto, el granizo chico
// suele derretirse antes de llegar al suelo (mismo umbral que api.EstimateHail).
const meltLevel = 4600.0

// freezingNow: altura de la isoterma de 0 °C en la hora actual, según los modelos.
func (m Model) freezingNow() (float64, bool) {
	o := m.hail.outlook
	if o == nil || len(o.Hours) == 0 || o.Hours[0].FreezingLevel <= 0 {
		return 0, false
	}
	return o.Hours[0].FreezingLevel, true
}

// hailNote: qué significa un núcleo intenso para el granizo, según la isoterma.
// (13/11/2026: 55+ dBZ sobre Córdoba con isoterma a ~5100 m: lluvia fuerte, sin granizo.)
func (m Model) hailNote(dbz bool) string {
	if fzl, ok := m.freezingNow(); ok && fzl > meltLevel {
		return fmt.Sprintf("  Lluvia muy intensa; granizo poco probable (0 °C a %.0f m).", fzl)
	}
	if dbz {
		return "  Más de 55 dBZ: puede traer granizo (no siempre llega al suelo)."
	}
	return "  Eco intenso: puede traer granizo (el radar no lo confirma)."
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
		// Las alertas por zona nombran el granizo casi siempre (texto de molde):
		// sólo resaltamos el de los avisos a corto plazo, que salen del radar.
		switch {
		case a.Hail && a.ShortTerm:
			hail = popupHi.Render(" ▲GRANIZO")
		case a.Hail:
			hail = popupDim.Render(" granizo posible")
		}
		ev := sentence(stripLevel(a.Event))
		room := w - lipgloss.Width(tag) - lipgloss.Width(hail) - 1
		lines = append(lines, pad(tag+popupText.Render(" "+truncate(ev, room))+hail))
		until := ""
		if a.ShortTerm {
			until = "A CORTO PLAZO · "
		}
		if !a.Expires.IsZero() {
			until += "hasta " + untilLabel(a.Expires.Local(), m.now) + " · "
		}
		// Las alertas por zona vienen sin descripción del área: sin el " · " colgando.
		lines = append(lines, dim("  "+strings.TrimSuffix(until+a.AreaDesc, " · ")))
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

	// Radar (nowcast)
	lines = append(lines, hi("RADAR · CELDAS FUERTES A MENOS DE 120 KM"))
	lvlName := func(l int) string {
		dbz := m.cells.nc != nil && m.cells.nc.DBZ
		switch {
		case l >= 4 && dbz:
			return "intenso (55+ dBZ)"
		case l >= 4:
			return "intenso"
		}
		return "fuerte"
	}
	src := "" // "radar RMA1 Cordoba 18:33"
	if nc := m.cells.nc; nc != nil {
		src = nc.Source + " " + nc.Time.Local().Format("15:04")
	}
	switch nc := m.cells.nc; {
	case nc == nil && m.cells.err != nil:
		lines = append(lines, text("Sin datos: "+m.cells.err.Error()))
	case nc == nil:
		lines = append(lines, text("Esperando el radar..."))
	case nc.NoCoverage:
		lines = append(lines,
			hi("Sin radar sobre tu zona (ni argentino ni de Rain Viewer)."),
			text("No se puede saber desde acá si viene una celda: guiate por"),
			text("los avisos de arriba."))
	case m.now.Sub(nc.Time) > 40*time.Minute:
		lines = append(lines, text("Último cuadro demasiado viejo ("+src+")."))
	case !nc.Near:
		lines = append(lines, text("Sin núcleos fuertes cerca ("+src+")."))
	default:
		l := fmt.Sprintf("Núcleo %s a %.0f km al %s", lvlName(nc.NearLevel), nc.NearKm, nc.NearDir)
		if nc.Overhead {
			l = "▲ Núcleo " + lvlName(nc.OverLevel) + " sobre tu zona"
		}
		if nc.Moving {
			l += fmt.Sprintf(" · va al %s a %.0f km/h", nc.Toward, nc.SpeedKmh)
		}
		if nc.Overhead {
			lines = append(lines, hi(l))
		} else {
			lines = append(lines, text(l))
		}
		switch {
		case nc.Approaching:
			mins := max(int(nc.ETA.Sub(m.now).Minutes()), 0)
			l := fmt.Sprintf("▲ Llegaría uno %s desde el %s ~%s (en %d min)",
				lvlName(nc.ETALevel), nc.FromDir, nc.ETA.Local().Format("15:04"), mins)
			lines = append(lines, hi(l))
			if nc.ETALevel >= 4 {
				lines = append(lines, text(m.hailNote(nc.DBZ)))
			}
		case nc.Overhead:
			if nc.OverLevel >= 4 {
				lines = append(lines, text(m.hailNote(nc.DBZ)))
			}
			lines = append(lines, dim("  "+src))
		case nc.Moving:
			lines = append(lines, text("Por ahora ninguno apunta a tu zona ("+src+")."))
		default:
			lines = append(lines, text("No se pudo estimar hacia dónde se mueven ("+src+")."))
		}
	}
	lines = append(lines, pad(""))

	// Modelos
	lines = append(lines, hi("MODELOS · PRÓXIMAS 48 H (estimación)"))
	o := m.hail.outlook
	switch {
	case o == nil && m.hail.err != nil:
		lines = append(lines, text("Sin datos: "+m.hail.err.Error()))
	case o == nil:
		lines = append(lines, text("Esperando el pronóstico..."))
	default:
		risk := popupText.Render("Riesgo según los modelos: ") + riskStyle[o.Max].Render(" "+o.Max.String()+" ")
		lines = append(lines, pad(risk))
		if o.Max > api.HailNone {
			ref := o.Hours[0].Time
			for i, e := range o.Episodes(api.HailLow) {
				if i == 3 {
					break
				}
				extra := ""
				if o.Members > 0 {
					extra = fmt.Sprintf(" %.0f%% del ensamble ve tormenta", e.Prob*100)
				}
				lines = append(lines, pad(popupText.Render("  "+e.Label(ref)+" ")+
					riskStyle[e.Peak].Render(" "+e.Peak.String()+" ")+popupDim.Render(extra)))
			}
			if cape := o.PeakCAPE(); cape > 0 {
				lines = append(lines, text(fmt.Sprintf("Tamaño probable: %s (chico no es inofensivo)", api.HailSize(cape, o.PeakSHIP()))))
			}
		}
		if len(o.Models) > 0 {
			var parts []string
			for _, mp := range o.Models {
				parts = append(parts, fmt.Sprintf("%s %.0f", mp.Name, mp.MaxCAPE))
			}
			lines = append(lines, text("CAPE máx (J/kg): "+strings.Join(parts, " · ")))
		}
		switch {
		case !o.Crossed:
		case o.Disagree:
			lines = append(lines, text("Los modelos no coinciden: vale el nivel que sostienen dos."))
		default:
			lines = append(lines, text(fmt.Sprintf("Los %d modelos coinciden.", len(o.Models))))
		}
		if lvl, ok := m.officialHail(); ok && o.Max < api.HailModerate {
			lines = append(lines, hi("Ojo: el SMN avisa "+strings.ToLower(lvl.String())+" con granizo y los modelos no lo ven."))
		}
		tail := fmt.Sprintf("SHIP máx %.1f · isoterma 0 °C mín %.0f m", o.MaxSHIP, o.MinFZL)
		if o.Explicit {
			tail += " · granizo explícito (96/99)"
		}
		lines = append(lines, text(tail))
	}
	lines = append(lines, pad(""),
		dim("Nadie acierta siempre con el granizo: orientación, no certeza."),
		pad(""))

	// Registro de aciertos: lo que viste vos es el dato que más vale.
	if m.lastReport != "" {
		lines = append(lines, hi("Anotado en el registro: "+m.lastReport+"."))
	}
	lines = append(lines, text("¿Qué pasó en tu zona? Tecla o (o F6) para anotarlo."))
	lines = append(lines, pad(""))

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
	// En terminales bajas no entra todo: primero sacamos los renglones en
	// blanco y, si aun así no alcanza, cortamos desde abajo (queda el botón).
	if room := m.height - 4; len(body) > room && room > 2 {
		tight := body[:0:0]
		for i, l := range body {
			if strings.TrimSpace(ansi.Strip(l)) != "" || i == len(body)-2 {
				tight = append(tight, l)
			}
		}
		body = tight
		if len(body) > room {
			body = append(body[:room-1:room-1], body[len(body)-1])
		}
	}

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
