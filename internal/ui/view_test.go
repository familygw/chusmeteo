package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"chusmeteo/internal/api"
	"chusmeteo/internal/geo"

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
	if !m.radar.loading {
		t.Fatal("arrancando en radar debería pedir el índice")
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
