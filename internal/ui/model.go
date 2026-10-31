// Package ui es la interfaz de terminal, hecha con Bubble Tea.
//
// Bubble Tea sigue "The Elm Architecture":
//   - Model:  el estado de la app (un struct).
//   - Update: recibe un mensaje (tecla, respuesta HTTP, tick) y devuelve
//     el nuevo modelo + un comando opcional para hacer algo asincrónico.
//   - View:   función pura que convierte el modelo en un string.
//
// Nunca hacés I/O dentro de Update: devolvés un tea.Cmd (una función) y
// Bubble Tea la corre en otra goroutine; cuando termina, su resultado vuelve
// como mensaje a Update. Es como un reducer de Redux/NgRx con efectos.
//
// Archivos del paquete:
//   - model.go   estado, Init, Update y teclas
//   - data.go    estado y comandos del mapa y del radar
//   - view.go    pantalla general, barras y vista de clima
//   - mapview.go vistas de mapa de temperaturas y radar
package ui

import (
	"context"
	"strings"
	"time"

	"chusmeteo/internal/api"
	"chusmeteo/internal/fx"
	"chusmeteo/internal/sdr"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

const autoRefreshEvery = 15 * time.Minute

type state int

const (
	stateLoading state = iota
	stateReady
	stateError
)

// View es la pantalla activa.
type View int

const (
	viewWeather View = iota
	viewMap
	viewRadar
	viewCount // truco: la última constante cuenta cuántas hay

	viewSat = viewCount // sólo con -hardcore; fuera de la cuenta para no afectar al resto
)

// ViewSat es la vista del modo hardcore (para el flag -view).
const ViewSat = viewSat

// ParseView traduce el flag -view. Devuelve false si no lo reconoce.
func ParseView(s string) (View, bool) {
	switch strings.ToLower(s) {
	case "clima", "weather", "1":
		return viewWeather, true
	case "mapa", "map", "2":
		return viewMap, true
	case "radar", "3":
		return viewRadar, true
	case "satelite", "sat", "4":
		return viewSat, true
	}
	return viewWeather, false
}

// Options son las opciones de línea de comandos.
type Options struct {
	View     View
	NoFX     bool // sin efecto de recepción APT
	Hardcore bool // RTL-SDR + Meteor-M LRPT
	SDR      sdr.Config
}

// --- Mensajes del clima puntual ---

type weatherMsg struct {
	loc api.Location
	fc  *api.Forecast
}

type errMsg struct{ err error }

type clockMsg time.Time

// refreshMsg lleva una "generación" para descartar timers viejos.
// Sin esto, cada refresco manual (F5) dejaría otro timer de 15 min vivo
// y se irían acumulando. Usamos el mismo truco para la recepción (rxGen)
// y la animación del radar (animGen).
type refreshMsg struct{ gen int }

// Model es el estado completo de la app.
type Model struct {
	opts Options
	view View

	// clima puntual (vista 1)
	city    string
	loc     api.Location
	fc      *api.Forecast
	err     error
	state   state
	updated time.Time
	gen     int

	// mapa y radar (vistas 2 y 3)
	grid  gridState
	radar radarState
	sat   satState

	// granizo: avisos del SMN y pronóstico del modelo ICON
	alerts     alertState
	hail       hailState
	showAlerts bool

	// efectos
	rx      *fx.Reception // nil = no hay recepción en curso
	rxGen   int
	animGen int

	now      time.Time
	showHelp bool

	searching bool
	input     textinput.Model
	spin      spinner.Model

	width, height int
}

// New arma el modelo inicial. En Go los "constructores" son funciones comunes,
// por convención llamadas New o NewXxx.
func New(city string, opts Options) Model {
	ti := textinput.New()
	ti.Placeholder = "Ej: Mendoza  o  Córdoba, ES"
	ti.CharLimit = 60
	ti.Width = 40
	ti.Prompt = "CIUDAD> "
	ti.PromptStyle = labelStyle
	ti.TextStyle = valueStyle
	ti.PlaceholderStyle = dimStyle
	ti.Cursor.Style = valueStyle

	sp := spinner.New(
		spinner.WithSpinner(spinner.Line), // | / - \  bien noventoso
		spinner.WithStyle(titleStyle),
	)

	return Model{
		opts:  opts,
		view:  opts.View,
		city:  city,
		state: stateLoading,
		radar: radarState{playing: true},
		input: ti,
		spin:  sp,
		now:   time.Now(),
	}
}

// Init devuelve los comandos que corren al arrancar.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, fetchWeather(m.city), clockTick())
}

// Update es el corazón de la app: un switch por tipo de mensaje.
// `msg.(type)` es un "type switch": pregunta qué tipo concreto hay
// dentro de la interfaz tea.Msg.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		first := m.width == 0
		m.width, m.height = msg.Width, msg.Height
		if first { // recién ahora sabemos el tamaño: si arrancamos en un mapa, cargarlo
			var c1, c2 tea.Cmd
			m, c1 = m.ensureData()
			m, c2 = m.ensureAlerts()
			return m, tea.Batch(c1, c2)
		}
		return m, nil

	case clockMsg:
		m.now = time.Time(msg)
		// Ojo: `m, cmd := ...` acá crearía OTRA variable m (este case es un bloque
		// nuevo) que "tapa" a la de afuera. Declaramos cmd aparte y asignamos con =.
		var cmd, acmd tea.Cmd
		m, cmd = m.ensureData() // ¿venció algo de la vista actual?
		m, acmd = m.ensureAlerts()
		return m, tea.Batch(cmd, acmd, clockTick())

	case spinner.TickMsg:
		if !m.anyLoading() {
			return m, nil // no devolver otro tick = el spinner se detiene
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case weatherMsg:
		m.loc, m.fc = msg.loc, msg.fc
		m.state = stateReady
		m.err = nil
		m.updated = time.Now()
		m.gen++
		// Con la ubicación confirmada, pedimos el granizo de ICON para ese punto.
		return m, tea.Batch(scheduleRefresh(m.gen), fetchHail(msg.loc))

	case errMsg:
		m.err = msg.err
		m.state = stateError
		return m, nil

	case refreshMsg:
		if msg.gen != m.gen || m.state == stateLoading {
			return m, nil // timer viejo: lo ignoramos
		}
		return m.reload()

	case gridMsg:
		m.grid.pts, m.grid.at, m.grid.loading, m.grid.err = msg.pts, time.Now(), false, nil
		if m.view == viewMap {
			return m.startReception()
		}
		return m, nil

	case gridErrMsg:
		m.grid.loading, m.grid.err = false, msg.err
		return m, nil

	case radarIndexMsg:
		return m.onRadarIndex(msg.idx)

	case radarFrameMsg:
		return m.onRadarFrame(msg.img)

	case radarErrMsg:
		if msg.frame {
			if m.radar.pending > 0 {
				m.radar.pending--
			}
		} else {
			m.radar.loading = false
		}
		m.radar.err = msg.err
		return m, nil

	case alertsMsg:
		return m.onAlerts(msg.res), nil

	case satImgMsg:
		m.sat.img, m.sat.path, m.sat.mod = msg.img, msg.path, msg.mod
		return m, nil

	case satDoneMsg:
		m.sat.cancel, m.sat.err = nil, msg.err
		return m, nil

	case alertsErrMsg:
		m.alerts.loading, m.alerts.err = false, msg.err
		return m, nil

	case hailMsg:
		m.hail = hailState{outlook: msg.o}
		return m, nil

	case hailErrMsg:
		m.hail.err = msg.err // conservamos el último outlook bueno, si había
		return m, nil

	case rxTickMsg:
		return m.onRxTick(msg)

	case animTickMsg:
		return m.onAnimTick(msg)

	case tea.KeyMsg:
		if m.searching {
			return m.updateSearch(msg)
		}
		if m.showAlerts { // el popup es "modal": se come las teclas
			switch msg.String() {
			case "esc", "enter", " ", "f4", "!", "q":
				m.showAlerts = false
			case "ctrl+c":
				return m, tea.Quit
			}
			return m, nil
		}
		return m.updateKeys(msg)
	}
	return m, nil
}

// updateKeys maneja las teclas en modo normal.
func (m Model) updateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "f10", "ctrl+c":
		return m, tea.Quit
	case "esc":
		if m.rx != nil { // saltear la recepción
			m.rx = nil
			m.rxGen++
			return m.startAnim()
		}
		m.showHelp = false
	case "f1", "?":
		m.showHelp = !m.showHelp
	case "f4", "!":
		m.showAlerts = true
	case "f2", "tab":
		return m.switchView((m.view + 1) % m.numViews())
	case "shift+tab":
		return m.switchView((m.view + m.numViews() - 1) % m.numViews())
	case "1", "2", "3", "4":
		if v := View(msg.String()[0] - '1'); v < m.numViews() { // '2' - '1' = 1: aritmética de bytes
			return m.switchView(v)
		}
	case "s":
		if m.view == viewSat {
			return m.toggleCapture()
		}
	case "f3", "/", "c":
		m.searching = true
		m.showHelp = false
		m.input.SetValue("")
		return m, m.input.Focus()
	case "f5", "r":
		if m.view == viewWeather {
			return m.reload()
		}
		return m.forceRefresh()
	case "a": // repetir la recepción
		if m.view != viewWeather {
			return m.startReception()
		}
	case " ", "p":
		if m.view == viewRadar {
			m.radar.playing = !m.radar.playing
			return m.startAnim()
		}
	case "left", "right", "h", "l":
		dir := 1
		if s := msg.String(); s == "left" || s == "h" {
			dir = -1
		}
		if m.view == viewMap && m.rx == nil {
			m.grid.off = max(0, min(m.grid.off+dir, m.grid.maxOff()))
		}
		if m.view == viewRadar && m.rx == nil {
			m.radar.playing = false
			m.radar.pos = m.radar.step(dir)
		}
	}
	return m, nil
}

// switchView cambia de vista, carga datos si hace falta y lanza la recepción.
func (m Model) switchView(v View) (tea.Model, tea.Cmd) {
	if v == m.view {
		return m, nil
	}
	m.view = v
	m.showHelp = false
	m.rx = nil
	m.rxGen++

	var cmds []tea.Cmd
	m, cmd := m.ensureData()
	cmds = append(cmds, cmd)

	// Si ya hay datos, "recibimos" la imagen de nuevo: es parte del encanto.
	hasData := (v == viewMap && m.grid.pts != nil) || (v == viewRadar && m.radar.current() != nil)
	if hasData {
		m, cmd = m.startReception()
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// updateSearch maneja las teclas mientras el usuario escribe una ciudad.
func (m Model) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		city := strings.TrimSpace(m.input.Value())
		m.searching = false
		m.input.Blur()
		if city == "" {
			return m, nil
		}
		m.city = city
		return m.reload()
	case "esc":
		m.searching = false
		m.input.Blur()
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	}
	// Cualquier otra tecla se la pasamos al componente de texto.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) reload() (tea.Model, tea.Cmd) {
	m.state = stateLoading
	return m, tea.Batch(m.spin.Tick, fetchWeather(m.city))
}

// --- Comandos (efectos asincrónicos) ---

// fetchWeather devuelve un tea.Cmd: una función que Bubble Tea ejecuta
// en otra goroutine. Lo que devuelve vuelve a Update como mensaje.
func fetchWeather(city string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		loc, err := api.Geocode(ctx, city)
		if err != nil {
			return errMsg{err}
		}
		fc, err := api.GetForecast(ctx, loc)
		if err != nil {
			return errMsg{err}
		}
		return weatherMsg{loc: loc, fc: fc}
	}
}

func clockTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return clockMsg(t) })
}

func scheduleRefresh(gen int) tea.Cmd {
	return tea.Tick(autoRefreshEvery, func(time.Time) tea.Msg { return refreshMsg{gen: gen} })
}
