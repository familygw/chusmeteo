package ui

import (
	"context"
	"time"

	"chusmeteo/internal/api"
	"chusmeteo/internal/fx"
	"chusmeteo/internal/geo"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	gridStep   = 2.5              // grados entre puntos de la grilla (~45 puntos)
	gridTTL    = 30 * time.Minute // cada coordenada gasta cupo: no refrescar seguido
	radarTTL   = 10 * time.Minute // Rain Viewer publica un frame cada 10 min
	retryAfter = time.Minute      // si falló, esperar antes de reintentar solo
	rxEvery    = 60 * time.Millisecond
	animEvery  = 450 * time.Millisecond
	animHold   = 4 // cuadros de pausa en el último frame del loop
)

// gridState: datos del mapa de temperaturas.
type gridState struct {
	pts     []api.PointWeather // grilla + ciudades (las ciudades van al final)
	off     int                // horas hacia adelante (0 = ahora)
	at      time.Time
	tried   time.Time
	loading bool
	err     error
}

// points devuelve los puntos como estarán dentro de `off` horas.
func (g gridState) points() []api.PointWeather {
	if g.off == 0 {
		return g.pts
	}
	out := make([]api.PointWeather, len(g.pts))
	for i, p := range g.pts {
		out[i] = p.At(g.off)
	}
	return out
}

// maxOff: última hora pronosticada disponible.
func (g gridState) maxOff() int {
	if len(g.pts) == 0 {
		return 0
	}
	return max(len(g.pts[0].Hourly)-1, 0)
}

// cities devuelve sólo los puntos de las ciudades de referencia.
func (g gridState) cities() []api.PointWeather {
	n := len(geo.Cities)
	if len(g.pts) < n {
		return nil
	}
	out := make([]api.PointWeather, n)
	for i, p := range g.pts[len(g.pts)-n:] {
		out[i] = p.At(g.off)
	}
	return out
}

// radarState: frames del radar y reproducción.
//
// Ojo con los maps dentro de un Model que se copia por valor: la copia
// comparte el mismo map. Acá está bien porque sólo lo tocamos en Update
// (siempre en la misma goroutine) y las copias viejas se descartan.
type radarState struct {
	idx     *api.RadarIndex
	frames  map[string]*api.RadarImage // clave: path del frame
	pos     int                        // frame que se muestra
	playing bool
	hold    int
	pending int // frames descargándose
	at      time.Time
	tried   time.Time
	loading bool // descargando el índice
	err     error
}

// current devuelve el frame que se muestra (o nil si todavía no llegó).
func (r radarState) current() *api.RadarImage {
	if r.idx == nil || r.pos < 0 || r.pos >= len(r.idx.Frames) {
		return nil
	}
	return r.frames[r.idx.Frames[r.pos].Path]
}

// --- Mensajes ---

type gridMsg struct{ pts []api.PointWeather }
type gridErrMsg struct{ err error }
type radarIndexMsg struct{ idx *api.RadarIndex }
type radarFrameMsg struct{ img *api.RadarImage }
type radarErrMsg struct {
	err   error
	frame bool // true: falló un frame; false: falló el índice
}
type rxTickMsg struct{ gen int }
type animTickMsg struct{ gen int }

// --- Comandos ---

func fetchGrid() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		pts := geo.Grid(gridStep)
		for _, c := range geo.Cities {
			pts = append(pts, c.LatLon)
		}
		res, err := api.GetGrid(ctx, pts)
		if err != nil {
			return gridErrMsg{err}
		}
		return gridMsg{res}
	}
}

func fetchRadarIndex() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		idx, err := api.GetRadarIndex(ctx)
		if err != nil {
			return radarErrMsg{err: err}
		}
		return radarIndexMsg{idx}
	}
}

// fetchRadarFrame: un comando por frame. Bubble Tea los corre en paralelo
// (cada tea.Cmd en su goroutine) y el semáforo de api limita las descargas.
func fetchRadarFrame(host string, f api.RadarFrame) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		img, err := api.FetchRadarFrame(ctx, host, f)
		if err != nil {
			return radarErrMsg{err: err, frame: true}
		}
		return radarFrameMsg{img}
	}
}

func rxTick(gen int) tea.Cmd {
	return tea.Tick(rxEvery, func(time.Time) tea.Msg { return rxTickMsg{gen} })
}

func animTick(gen int) tea.Cmd {
	return tea.Tick(animEvery, func(time.Time) tea.Msg { return animTickMsg{gen} })
}

// --- Lógica de datos (métodos con receiver por valor: devuelven el modelo nuevo) ---

// ensureData pide lo que falte o esté vencido para la vista actual.
func (m Model) ensureData() (Model, tea.Cmd) {
	now := time.Now()
	switch m.view {
	case viewMap:
		g := &m.grid // puntero a un campo: modificamos m.grid sin copiar
		stale := g.pts == nil || now.Sub(g.at) > gridTTL
		if stale && !g.loading && now.Sub(g.tried) > retryAfter {
			g.loading, g.tried = true, now
			return m, tea.Batch(fetchGrid(), m.spin.Tick)
		}
	case viewSat:
		return m.ensureSat()
	case viewRadar:
		r := &m.radar
		stale := r.idx == nil || now.Sub(r.at) > radarTTL
		if stale && !r.loading && now.Sub(r.tried) > retryAfter {
			r.loading, r.tried = true, now
			return m, tea.Batch(fetchRadarIndex(), m.spin.Tick)
		}
	}
	return m, nil
}

// forceRefresh es F5 en las vistas de mapa: ignora el TTL.
func (m Model) forceRefresh() (Model, tea.Cmd) {
	switch m.view {
	case viewMap:
		m.grid.at, m.grid.tried = time.Time{}, time.Time{} // fecha cero = "vencido"
	case viewRadar:
		m.radar.at, m.radar.tried = time.Time{}, time.Time{}
	}
	return m.ensureData()
}

// onRadarIndex procesa un índice nuevo: pide sólo los frames que no tenemos.
func (m Model) onRadarIndex(idx *api.RadarIndex) (Model, tea.Cmd) {
	r := &m.radar
	r.idx, r.at, r.loading, r.err = idx, time.Now(), false, nil
	r.pos = len(idx.Frames) - 1
	if r.frames == nil {
		r.frames = map[string]*api.RadarImage{}
	}

	keep := map[string]bool{}
	var cmds []tea.Cmd
	for _, f := range idx.Frames {
		keep[f.Path] = true
		if _, ok := r.frames[f.Path]; !ok {
			r.pending++
			cmds = append(cmds, fetchRadarFrame(idx.Host, f))
		}
	}
	for path := range r.frames {
		if !keep[path] {
			delete(r.frames, path) // borrar de un map mientras se recorre es válido en Go
		}
	}
	if r.pending == 0 && m.view == viewRadar {
		var cmd tea.Cmd
		m, cmd = m.startReception()
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

func (m Model) onRadarFrame(img *api.RadarImage) (Model, tea.Cmd) {
	r := &m.radar
	if r.pending > 0 {
		r.pending--
	}
	if r.idx == nil {
		return m, nil
	}
	r.frames[img.Frame.Path] = img
	// Cuando llega el frame más reciente, arranca la "pasada del satélite".
	latest := r.idx.Frames[len(r.idx.Frames)-1].Path
	if img.Frame.Path == latest && m.view == viewRadar {
		return m.startReception()
	}
	if r.pending == 0 {
		return m.startAnim()
	}
	return m, nil
}

// startReception arranca el efecto APT sobre la vista de mapa actual.
func (m Model) startReception() (Model, tea.Cmd) {
	m.rxGen++ // invalida cualquier tick de una recepción anterior
	if m.opts.NoFX || (m.view != viewMap && m.view != viewRadar) {
		m.rx = nil
		return m.startAnim()
	}
	m.rx = fx.NewReception(m.mapProjection().H, uint64(time.Now().UnixNano()))
	return m, rxTick(m.rxGen)
}

func (m Model) onRxTick(msg rxTickMsg) (Model, tea.Cmd) {
	if msg.gen != m.rxGen || m.rx == nil {
		return m, nil
	}
	// Copiamos antes de mutar: el modelo viejo sigue apuntando a la recepción
	// anterior. Es el estilo "inmutable" de Elm, aunque Go no lo obligue.
	rx := *m.rx
	rx.Step()
	if rx.Done() {
		m.rx = nil
		return m.startAnim()
	}
	m.rx = &rx
	return m, rxTick(m.rxGen)
}

// startAnim (re)arranca el loop del radar si corresponde.
func (m Model) startAnim() (Model, tea.Cmd) {
	if m.view != viewRadar || !m.radar.playing || m.rx != nil {
		return m, nil
	}
	m.animGen++
	return m, animTick(m.animGen)
}

func (m Model) onAnimTick(msg animTickMsg) (Model, tea.Cmd) {
	if msg.gen != m.animGen || m.view != viewRadar || !m.radar.playing || m.rx != nil {
		return m, nil // el loop se corta solo: no devolvemos otro tick
	}
	r := &m.radar
	if r.idx != nil && len(r.frames) > 1 {
		last := len(r.idx.Frames) - 1
		if r.pos == last && r.hold < animHold {
			r.hold++
		} else {
			r.hold = 0
			r.pos = m.radar.step(+1)
		}
	}
	return m, animTick(m.animGen)
}

// step devuelve la posición del próximo frame disponible en una dirección.
func (r radarState) step(dir int) int {
	if r.idx == nil {
		return r.pos
	}
	n := len(r.idx.Frames)
	pos := r.pos
	for range n { // range sobre un entero: Go 1.22+
		pos = (pos + dir + n) % n
		if r.frames[r.idx.Frames[pos].Path] != nil {
			return pos
		}
	}
	return r.pos
}

func (m Model) anyLoading() bool {
	return m.state == stateLoading || m.grid.loading || m.radar.loading || m.radar.pending > 0
}
