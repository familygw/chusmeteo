package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"chusmeteo/internal/api"
	"chusmeteo/internal/geo"
	"chusmeteo/internal/registro"
	"chusmeteo/internal/sdr"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// send pasa un mensaje por Update y devuelve el modelo concreto.
// (Update devuelve la interfaz tea.Model; `.(Model)` es una "type assertion".)
func send(m Model, msg tea.Msg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

// fakeModel arma un modelo con datos inventados, sin tocar la red.
func fakeModel(w, h int, opts Options) Model {
	m := New("Córdoba, AR", opts)
	m.view = viewWeather // los datos los cargamos a mano, no por ensureData
	m, _ = send(m, tea.WindowSizeMsg{Width: w, Height: h})

	fc := &api.Forecast{}
	fc.Current = api.Current{Temperature: 18.4, ApparentTemp: 17, Humidity: 62,
		WeatherCode: 2, WindSpeed: 14, WindDirection: 45, Pressure: 1012, IsDay: 1,
		CloudCover: 40, WindGusts: 31}
	fc.Daily = api.Daily{
		Time:        []string{"2026-10-24", "2026-10-25", "2026-10-26", "2026-10-27", "2026-10-28", "2026-10-29", "2026-10-30"},
		WeatherCode: []int{2, 0, 63, 95, 3, 61, 0},
		TempMax:     []float64{24, 27, 21, 31, 26, 19, 23},
		TempMin:     []float64{12, 14, 12, 18, 15, 11, 9},
		PrecipProb:  []float64{5, 0, 80, 60, 20, 70, 0},
		Sunrise:     []string{"2026-10-24T06:58"},
		Sunset:      []string{"2026-10-24T20:05"},
		UVMax:       []float64{9.5},
	}
	// 24 horas con una curva diaria y un chaparrón a la tarde.
	t0 := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	for i := range 24 {
		h := t0.Add(time.Duration(i) * time.Hour)
		fc.Hourly.Time = append(fc.Hourly.Time, h.Format("2006-01-02T15:04"))
		fc.Hourly.Temperature = append(fc.Hourly.Temperature, 17+8*math.Sin(float64(i-9)*math.Pi/12))
		prob := 0.0
		if i >= 15 && i <= 19 {
			prob = float64(40 + (i-15)*12)
		}
		fc.Hourly.PrecipProb = append(fc.Hourly.PrecipProb, prob)
	}
	m, _ = send(m, weatherMsg{
		loc: api.Location{Name: "Córdoba", Admin1: "Córdoba", Country: "Argentina", Latitude: -31.42, Longitude: -64.18},
		fc:  fc,
	})
	m, _ = send(m, gridMsg{fakeGrid()})
	m.grid.at = time.Now()
	m, _ = send(m, radarIndexMsg{fakeIndex()})
	for _, f := range m.radar.idx.Frames {
		m, _ = send(m, radarFrameMsg{fakeRadar(f)})
	}
	m, _ = send(m, alertsMsg{fakeAlerts()})
	m, _ = send(m, hailMsg{fakeHail(fc)})
	m.now = time.Date(2026, 10, 24, 0, 12, 0, 0, time.Local)
	m.view = opts.View
	m.rx = nil
	return m
}

// fakeAlerts: un aviso naranja con granizo sobre Córdoba y dos en otras provincias.
func fakeAlerts() *api.AlertsResult {
	mk := func(event, area, poly string) *api.Alert {
		xml := `<alert><status>Actual</status><msgType>Alert</msgType><info>
			<event>` + event + `</event><expires>2099-01-01T00:00:00-03:00</expires>
			<area><areaDesc>` + area + `</areaDesc><polygon>` + poly + `</polygon></area></info></alert>`
		a, err := api.ParseCAP([]byte(xml), area)
		if err != nil {
			panic(err)
		}
		return a
	}
	res := &api.AlertsResult{New: map[string]*api.Alert{}}
	for _, a := range []*api.Alert{
		mk("TORMENTAS FUERTES CON CAIDA DE GRANIZO - AVISO NARANJA", "Capital; Colón; Santa María; Punilla",
			"-31.0,-64.6 -31.0,-63.8 -31.8,-63.8 -31.8,-64.6"),
		mk("TORMENTAS - AVISO AMARILLO", "Rosario; San Lorenzo", "-32.5,-61.0 -32.5,-60.4 -33.2,-60.4 -33.2,-61.0"),
		mk("VIENTO ZONDA - AVISO AMARILLO", "Valle de Uco", "-33.3,-69.5 -33.3,-68.9 -34.0,-68.9 -34.0,-69.5"),
	} {
		res.Current = append(res.Current, a.URL)
		res.New[a.URL] = a
	}
	return res
}

// fakeHail: ICON con riesgo que sube a la tarde (justo cuando llueve en fakeModel).
func fakeHail(fc *api.Forecast) *api.HailOutlook {
	o := &api.HailOutlook{}
	for _, h := range fc.Hours() {
		cape := 0.0
		code := 3
		if hr := h.Time.Hour(); hr >= 14 && hr <= 20 {
			cape, code = float64(800+(hr-14)*400), 95
		}
		hh := api.HailHour{Time: h.Time, Code: code, CAPE: cape, FreezingLevel: 4100,
			Risk: api.EstimateHailRisk(code, cape, 4100)}
		o.Hours = append(o.Hours, hh)
		o.Max = max(o.Max, hh.Risk)
		o.MaxCAPE = max(o.MaxCAPE, cape)
		o.MinFZL = 4100
	}
	return o
}

// fakeGrid: más calor al norte, más frío al sur y en la cordillera.
func fakeGrid() []api.PointWeather {
	pts := geo.Grid(gridStep)
	for _, c := range geo.Cities {
		pts = append(pts, c.LatLon)
	}
	out := make([]api.PointWeather, len(pts))
	for i, p := range pts {
		t := 33 + 0.85*(p.Lat+22) - math.Max(0, (p.Lon+69)*-3)
		out[i] = api.PointWeather{LatLon: p, Temperature: t}
	}
	return out
}

func fakeIndex() *api.RadarIndex {
	idx := &api.RadarIndex{Host: "http://fake"}
	t0 := time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)
	for i := range 13 {
		idx.Frames = append(idx.Frames, api.RadarFrame{
			Time: t0.Add(time.Duration(i) * 10 * time.Minute),
			Path: "/v2/radar/f" + string(rune('a'+i)),
		})
	}
	return idx
}

// fakeRadar dibuja una tormenta que se mueve hacia el este frame a frame:
// núcleo intenso (rojo) rodeado de anillos amarillo, azul y celeste.
// Generar los tiles es lo más lento de los tests: los memorizamos por frame.
var radarCache = map[string]*api.RadarImage{}

func fakeRadar(f api.RadarFrame) *api.RadarImage {
	if img, ok := radarCache[f.Path]; ok {
		return img
	}
	img := buildFakeRadar(f)
	radarCache[f.Path] = img
	return img
}

func buildFakeRadar(f api.RadarFrame) *api.RadarImage {
	shift := float64(f.Path[len(f.Path)-1]-'a') * 0.25
	center := geo.LatLon{Lat: -32.5, Lon: -63.5 + shift}
	rings := []struct {
		r   float64
		col color.RGBA
	}{
		{0.6, color.RGBA{193, 0, 0, 255}},
		{1.2, color.RGBA{255, 238, 0, 255}},
		{2.0, color.RGBA{0, 85, 136, 255}},
		{3.0, color.RGBA{136, 221, 238, 255}},
	}
	tiles := map[[2]int]image.Image{}
	x0, y0, x1, y1 := api.TileRange()
	for tx := x0; tx <= x1; tx++ {
		for ty := y0; ty <= y1; ty++ {
			img := image.NewRGBA(image.Rect(0, 0, 256, 256))
			for py := 0; py < 256; py += 2 { // de a 2 píxeles: alcanza y es más rápido
				for px := 0; px < 256; px += 2 {
					p := api.TileLatLon(tx, ty, px, py)
					d := math.Hypot(p.Lat-center.Lat, (p.Lon-center.Lon)*0.8)
					for _, r := range rings {
						if d < r.r {
							for k := range 4 {
								img.SetRGBA(px+k%2, py+k/2, r.col)
							}
							break
						}
					}
				}
			}
			tiles[[2]int{tx, ty}] = img
		}
	}
	return api.NewRadarImage(f, tiles)
}

var sizes = [][2]int{{72, 24}, {80, 24}, {100, 30}, {120, 40}, {200, 60}, {270, 60}, {90, 70}, {300, 30}}

// Ninguna vista puede pasarse del tamaño de la terminal.
func TestViewsFitTerminal(t *testing.T) {
	for v := range viewCount {
		for _, size := range sizes {
			for _, rx := range []bool{false, true} {
				m := fakeModel(size[0], size[1], Options{View: v})
				if rx {
					m, _ = m.startReception()
					for range 10 {
						m, _ = send(m, rxTickMsg{m.rxGen})
					}
				}
				out := m.View()
				if w := lipgloss.Width(out); w > size[0] {
					t.Errorf("vista %d %dx%d rx=%v: ancho %d", v, size[0], size[1], rx, w)
				}
				if h := lipgloss.Height(out); h != size[1] {
					t.Errorf("vista %d %dx%d rx=%v: alto %d", v, size[0], size[1], rx, h)
				}
			}
		}
	}
}

func TestViewContents(t *testing.T) {
	want := map[View][]string{
		viewWeather: {"CÓRDOBA, ARGENTINA", "Parcialmente nublado", "PRÓXIMAS 24 HORAS", "RÁFAGAS", "GRANIZO", "ALTO", "gran"},
		viewMap:     {"TEMPERATURA ACTUAL", "MÁX", "CIUDADES", "Comodoro", "Open-Meteo"},
		viewRadar:   {"RADAR", "CUADRO 13/13", "Rain Viewer", "posible granizo", "LLUVIA POR CIUDAD"},
	}
	for v, words := range want {
		out := fakeModel(200, 50, Options{View: v}).View()
		for _, w := range words {
			if !strings.Contains(out, w) {
				t.Errorf("vista %d: falta %q", v, w)
			}
		}
	}
}

// Al entrar al mapa con datos arranca la recepción, y termina sola.
func TestReceptionFlow(t *testing.T) {
	m := fakeModel(80, 24, Options{})
	next, _ := m.switchView(viewMap)
	m = next.(Model)
	if m.rx == nil {
		t.Fatal("al cambiar al mapa debería arrancar la recepción")
	}
	for i := 0; m.rx != nil; i++ {
		if i > 500 {
			t.Fatal("la recepción no termina nunca")
		}
		m, _ = send(m, rxTickMsg{m.rxGen})
	}
	// Un tick viejo (de otra generación) se ignora.
	m, cmd := send(m, rxTickMsg{m.rxGen - 1})
	if cmd != nil || m.rx != nil {
		t.Error("un tick viejo no debería hacer nada")
	}
}

func TestNoFX(t *testing.T) {
	m := fakeModel(80, 24, Options{NoFX: true})
	next, _ := m.switchView(viewMap)
	if next.(Model).rx != nil {
		t.Error("con -nofx no debería haber recepción")
	}
}

// El radar: índice -> frames -> recepción -> animación.
func TestRadarFlow(t *testing.T) {
	m := New("Córdoba, AR", Options{View: viewRadar})
	m, _ = send(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.radar.loading {
		t.Fatal("sin ubicación todavía no se puede elegir el radar")
	}
	m, _ = send(m, weatherMsg{loc: api.Location{Name: "Córdoba", Latitude: -31.42, Longitude: -64.18}, fc: &api.Forecast{}})
	if !m.radar.loading {
		t.Fatal("con la ubicación lista debería pedir el índice")
	}
	idx := fakeIndex()
	m, _ = send(m, radarIndexMsg{idx})
	if m.radar.pending != len(idx.Frames) {
		t.Fatalf("pending = %d, esperaba %d", m.radar.pending, len(idx.Frames))
	}
	// Llegan en desorden: primero el último (dispara la recepción).
	m, _ = send(m, radarFrameMsg{fakeRadar(idx.Frames[12])})
	if m.rx == nil {
		t.Fatal("al llegar el frame más nuevo debería arrancar la recepción")
	}
	for _, f := range idx.Frames[:12] {
		m, _ = send(m, radarFrameMsg{fakeRadar(f)})
	}
	if m.radar.pending != 0 {
		t.Errorf("pending = %d", m.radar.pending)
	}
	// Esc saltea la recepción y arranca la animación.
	m, cmd := send(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.rx != nil || cmd == nil {
		t.Fatal("Esc debería saltear la recepción y arrancar el loop")
	}
	// En el último frame se queda animHold cuadros y después vuelve al primero.
	for range animHold + 1 {
		m, _ = send(m, animTickMsg{m.animGen})
	}
	if m.radar.pos != 0 {
		t.Errorf("después del hold esperaba volver al cuadro 0, está en %d", m.radar.pos)
	}
	// Espacio pausa; → avanza un cuadro.
	m, _ = send(m, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.radar.playing || m.radar.pos != 1 {
		t.Errorf("playing=%v pos=%d", m.radar.playing, m.radar.pos)
	}
}

// El popup se superpone sin romper el tamaño de la pantalla, y es modal.
func TestAlertsPopup(t *testing.T) {
	for _, size := range sizes {
		m := fakeModel(size[0], size[1], Options{View: viewMap})
		m, _ = send(m, tea.KeyMsg{Type: tea.KeyF4})
		if !m.showAlerts {
			t.Fatal("F4 debería abrir el popup")
		}
		out := m.View()
		if w, h := lipgloss.Width(out), lipgloss.Height(out); w != size[0] || h != size[1] {
			t.Errorf("%dx%d: con popup la pantalla mide %dx%d", size[0], size[1], w, h)
		}
		for _, want := range []string{"AVISOS Y GRANIZO", "NARANJA", "GRANIZO", "resto del país: 2", "ALTO", "Aceptar"} {
			if !strings.Contains(out, want) {
				t.Errorf("%dx%d: el popup no muestra %q", size[0], size[1], want)
			}
		}
		// Modal: con el popup abierto, "2" no cambia de vista; Esc lo cierra.
		m, _ = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
		m, _ = send(m, tea.KeyMsg{Type: tea.KeyEsc})
		if m.showAlerts || m.view != viewMap {
			t.Errorf("popup modal mal: abierto=%v vista=%d", m.showAlerts, m.view)
		}
	}
}

func TestAlertBadge(t *testing.T) {
	m := fakeModel(120, 30, Options{})
	if !strings.Contains(m.headerView(), "AVISO SMN") {
		t.Error("con un aviso sobre la ciudad, la barra debería mostrarlo")
	}
	m.alerts = alertState{}
	m.hail = hailState{}
	if strings.Contains(m.headerView(), "⚠") {
		t.Error("sin avisos ni riesgo no debería haber indicador")
	}
}

// Ver la pantalla sin abrir la app:
//
//	CHUSMETEO_DUMP=clima|mapa|radar|rx [CHUSMETEO_SIZE=120x40] go test ./internal/ui -run Dump -v
func TestDumpView(t *testing.T) {
	which := os.Getenv("CHUSMETEO_DUMP")
	if which == "" {
		t.Skip("seteá CHUSMETEO_DUMP=clima|mapa|radar|rx para imprimir la pantalla")
	}
	v, _ := ParseView(which)
	if which == "rx" || which == "popup" {
		v = viewMap
	}
	w, h := 80, 24
	fmt.Sscanf(os.Getenv("CHUSMETEO_SIZE"), "%dx%d", &w, &h) // opcional: CHUSMETEO_SIZE=120x40
	m := fakeModel(w, h, Options{View: v})
	if which == "rx" {
		m, _ = m.startReception()
		for range 22 {
			m, _ = send(m, rxTickMsg{m.rxGen})
		}
	}
	if which == "popup" {
		m.showAlerts = true
	}
	t.Log("\n" + m.View())
}

func TestSatView(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 200, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 200; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y / 3), 80, 255})
		}
	}
	for _, size := range sizes {
		m := fakeModel(size[0], size[1], Options{View: viewSat, Hardcore: true, SDR: sdr.DefaultConfig()})
		if out := m.View(); !strings.Contains(out, "SIN SEÑAL") && size[0] >= 100 {
			t.Errorf("%dx%d: falta SIN SEÑAL", size[0], size[1])
		}
		m, _ = send(m, satImgMsg{img: img, path: "/tmp/x.png", mod: time.Now()})
		out := m.View()
		if w := lipgloss.Width(out); w > size[0] {
			t.Errorf("%dx%d: ancho %d", size[0], size[1], w)
		}
		if h := lipgloss.Height(out); h != size[1] {
			t.Errorf("%dx%d: alto %d", size[0], size[1], h)
		}
	}
	m := fakeModel(120, 40, Options{Hardcore: true})
	if m, _ = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")}); m.view != viewSat {
		t.Error("la tecla 4 debería ir a la vista satélite")
	}
	m = fakeModel(120, 40, Options{})
	if m, _ = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")}); m.view == viewSat {
		t.Error("sin -hardcore no hay vista satélite")
	}
}

func TestCellThreat(t *testing.T) {
	m := fakeModel(140, 50, Options{})
	m.alerts = alertState{}
	m.cells.nc = &api.Nowcast{Time: m.now.Add(-8 * time.Minute), Frames: 3, Near: true, NearKm: 42, NearDir: "SO",
		NearLevel: 4, Moving: true, SpeedKmh: 50, Toward: "NE",
		Approaching: true, ETA: m.now.Add(35 * time.Minute), ETALevel: 4, FromDir: "SO"}
	if h := m.headerView(); !strings.Contains(h, "CELDA EN 35 MIN") {
		t.Errorf("la barra debería avisar de la celda: %q", h)
	}
	m.showAlerts = true
	out := m.View()
	for _, want := range []string{"a 42 km al SO", "va al NE a 50 km/h", "Llegaría uno intenso desde el SO", "en 35 min", "puede traer granizo"} {
		if !strings.Contains(out, want) {
			t.Errorf("popup: falta %q", want)
		}
	}
	m.cells.nc.NoCoverage = true // sin radar no se afirma nada, ni en la barra ni en el popup
	if out := m.View(); strings.Contains(m.headerView(), "CELDA") || !strings.Contains(out, "Sin radar") {
		t.Error("sin cobertura debería decirlo y no avisar de celdas")
	}
	m.cells.nc.NoCoverage = false
	m.cells.nc.ETA = m.now.Add(3 * time.Hour) // lejos: se informa, pero la barra no titila
	if strings.Contains(m.headerView(), "CELDA") {
		t.Error("una celda a 3 horas no debería ir a la barra")
	}
}

// Radar local: zoom a la zona del radar, niveles por dBZ y resumen de celdas.
func TestLocalRadarView(t *testing.T) {
	area := geo.Box{North: -29.3, South: -33.5, East: -61.6, West: -66.7}
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 60; y < 75; y++ { // una franja "intensa" al sur de la ciudad
		for x := 30; x < 70; x++ {
			img.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	level := func(c color.RGBA) int {
		if c.A == 0 {
			return 0
		}
		return 4
	}
	for _, size := range sizes {
		m := fakeModel(size[0], size[1], Options{View: viewRadar, NoFX: true})
		idx := &api.RadarIndex{Source: "RMA1 Cordoba", Area: &area}
		for i := range 3 {
			idx.Frames = append(idx.Frames, api.RadarFrame{Time: m.now.Add(time.Duration(i-3) * 9 * time.Minute), Path: fmt.Sprintf("ohmc:%d", i)})
		}
		m, _ = send(m, radarIndexMsg{idx})
		for _, f := range idx.Frames {
			m, _ = send(m, radarFrameMsg{api.NewLocalRadarImage(f, img, area, level)})
		}
		m.cells.nc = &api.Nowcast{Time: m.now.Add(-4 * time.Minute), Near: true, NearKm: 22, NearDir: "S", NearLevel: 4, DBZ: true}
		out := m.View()
		if w := lipgloss.Width(out); w > size[0] {
			t.Errorf("%dx%d: ancho %d", size[0], size[1], w)
		}
		if h := lipgloss.Height(out); h != size[1] {
			t.Errorf("%dx%d: alto %d", size[0], size[1], h)
		}
		if size[1] < 30 {
			continue
		}
		for _, want := range []string{"RADAR RMA1 CORDOBA", "dBZ", "22 km al S", "OHMC"} {
			if !strings.Contains(out, want) {
				t.Errorf("%dx%d: falta %q", size[0], size[1], want)
			}
		}
	}
	// El zoom cubre el área del radar, no el país.
	m := fakeModel(120, 40, Options{View: viewRadar})
	national := m.radarProjection()
	m.radar.idx = &api.RadarIndex{Area: &area}
	if local := m.radarProjection(); local.PixelsPerDegree() < 4*national.PixelsPerDegree() {
		t.Errorf("escala local %.1f vs nacional %.1f px/grado", local.PixelsPerDegree(), national.PixelsPerDegree())
	}
}

// Los tests no ejecutan los tea.Cmd, pero por las dudas el registro va a un
// directorio temporal y nunca al archivo real de la persona.
func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "chusmeteo-test")
	registro.Path = filepath.Join(dir, "registro.jsonl")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestRegistro(t *testing.T) {
	registro.Path = filepath.Join(t.TempDir(), "registro.jsonl")
	m := fakeModel(140, 50, Options{})
	run := func(cmd tea.Cmd) { // ejecuta el comando (y los de un Batch) como haría Bubble Tea
		if cmd == nil {
			return
		}
		if b, ok := cmd().(tea.BatchMsg); ok {
			for _, c := range b {
				if c != nil {
					c()
				}
			}
		}
	}
	// Llega un nowcast con una celda en camino: una entrada. El mismo estado de nuevo: ninguna.
	nc := &api.Nowcast{Time: m.now.Add(-5 * time.Minute), Near: true, NearKm: 22, NearDir: "S", NearLevel: 4,
		Moving: true, SpeedKmh: 45, Toward: "NE", Approaching: true, ETA: m.now.Add(13 * time.Minute), ETALevel: 4, FromDir: "S", DBZ: true}
	m.cells.loc = "Córdoba"
	var cmd tea.Cmd
	m, cmd = send(m, nowMsg{nc: nc, loc: "Córdoba"})
	run(cmd)
	m, cmd = send(m, nowMsg{nc: nc, loc: "Córdoba"})
	run(cmd)
	// La persona anota lo que vio desde el popup.
	m.showAlerts = true
	m, cmd = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	run(cmd)
	if out := m.View(); !strings.Contains(out, "lluvia fuerte en el registro") {
		t.Error("el popup debería confirmar lo anotado")
	}

	body, err := os.ReadFile(registro.Path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("esperaba 2 entradas, hay %d:\n%s", len(lines), body)
	}
	for i, want := range []string{`"tipo":"celda"`, `"tipo":"usuario"`} {
		if !strings.Contains(lines[i], want) || !strings.Contains(lines[i], `"isoterma_m":4100`) || !strings.Contains(lines[i], `"ciudad":"Córdoba"`) {
			t.Errorf("entrada %d: %s", i, lines[i])
		}
	}
	if !strings.Contains(lines[1], `"que":"lluvia fuerte"`) || !strings.Contains(lines[1], `"eta_nivel":4`) {
		t.Errorf("entrada del usuario: %s", lines[1])
	}
}

func TestBadgePriority(t *testing.T) {
	m := fakeModel(140, 50, Options{})
	// Alerta por zona sola: contexto del día. (El titileo es sólo de color y en
	// los tests no hay colores, así que acá se prueba el texto y la prioridad.)
	if a := m.alertBadge(false); !strings.Contains(a, "AVISO SMN") {
		t.Errorf("falta la alerta por zona: %q", a)
	}
	// Granizo observado cerca: urgente.
	m.obs.st = []api.Station{{ICAO: "SACO", DistKm: 16, HailAt: m.now.Add(-20 * time.Minute)}}
	if h := m.headerView(); !strings.Contains(h, "GRANIZO OBSERVADO") {
		t.Errorf("falta el granizo observado: %q", h)
	}
	// Aviso a corto plazo sobre la ciudad: le gana al observado.
	m.alerts.byURL[m.alerts.current[0]].ShortTerm = true
	if a := m.alertBadge(false); !strings.Contains(a, "CORTO PLAZO") {
		t.Errorf("falta el aviso a corto plazo: %q", a)
	}
}

func TestHailNoteUsesFreezingLevel(t *testing.T) {
	m := fakeModel(140, 50, Options{})
	m.cells.nc = &api.Nowcast{Time: m.now.Add(-5 * time.Minute), Near: true, NearKm: 3, NearDir: "S", NearLevel: 4,
		Overhead: true, OverLevel: 4, DBZ: true, Source: "radar RMA1 Cordoba"}
	m.showAlerts = true
	if out := m.View(); !strings.Contains(out, "puede traer granizo") {
		t.Error("con isoterma a 4100 m debería advertir por granizo")
	}
	m.hail.outlook.Hours[0].FreezingLevel = 5100
	if out := m.View(); !strings.Contains(out, "granizo poco probable (0 °C a 5100 m)") {
		t.Error("con isoterma a 5100 m debería decir que el granizo es poco probable")
	}
}

func TestHailSummarySeparatesOfficialAndModel(t *testing.T) {
	m := fakeModel(200, 50, Options{})
	s := m.hailSummary()
	if !strings.Contains(s, "SMN") || !strings.Contains(s, "MODELOS") {
		t.Errorf("falta SMN o MODELOS: %q", s)
	}
	if w := lipgloss.Width(s); w > 38 {
		t.Errorf("el resumen mide %d: se envolvería y dejaría un hueco negro", w)
	}
}
