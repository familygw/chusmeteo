package ui

import (
	"time"

	"chusmeteo/internal/api"
	"chusmeteo/internal/registro"

	tea "github.com/charmbracelet/bubbletea"
)

// Registro de aciertos: cada vez que la app avisa algo (o que vos contás qué
// pasó) se agrega un renglón a registro.Path. Update no hace I/O: arma la
// entrada y devuelve un tea.Cmd que la escribe en otra goroutine.
//
// Para no repetir, cada cosa se anota una vez por ejecución (m.logged). Si
// cerrás y abrís la app puede repetirse un aviso: cada entrada lleva un "id"
// para poder descartar duplicados al analizar.

// logCmd arma la entrada con la foto del estado actual.
func (m Model) logCmd(kind string, data map[string]any) tea.Cmd {
	e := registro.Entry{Time: m.now, Kind: kind, City: m.loc.Name,
		Lat: m.loc.Latitude, Lon: m.loc.Longitude, Data: data, State: m.snapshot()}
	return func() tea.Msg {
		_ = registro.Append(e) // si no se puede escribir, la app sigue igual
		return nil
	}
}

// once dice si es la primera vez que se ve esta clave (y la marca).
// El map se comparte entre copias del modelo: sólo se toca desde Update.
func (m Model) once(key string) bool {
	if m.logged[key] {
		return false
	}
	m.logged[key] = true
	return true
}

// snapshot: lo que la app "creía" en este momento. Con esto cada renglón se
// entiende solo, sin cruzarlo con otros.
func (m Model) snapshot() map[string]any {
	s := map[string]any{}
	if nc := m.cells.nc; nc != nil {
		c := map[string]any{"fuente": nc.Source, "radar": nc.Time, "sin_cobertura": nc.NoCoverage,
			"encima": nc.Overhead, "llega": nc.Approaching}
		if nc.Near {
			c["cerca_km"], c["cerca_dir"], c["cerca_nivel"] = round1(nc.NearKm), nc.NearDir, nc.NearLevel
		}
		if nc.Overhead {
			c["encima_nivel"] = nc.OverLevel
		}
		if nc.Approaching {
			c["eta"], c["eta_nivel"], c["desde"] = nc.ETA, nc.ETALevel, nc.FromDir
		}
		if nc.Moving {
			c["hacia"], c["kmh"] = nc.Toward, round1(nc.SpeedKmh)
		}
		s["celdas"] = c
	}
	if o := m.hail.outlook; o != nil && len(o.Hours) > 0 {
		h := o.Hours[0]
		s["modelos"] = map[string]any{"riesgo_24h": o.MaxWithin(24 * time.Hour).String(), "riesgo_ahora": h.Risk.String(),
			"cape": h.CAPE, "isoterma_m": h.FreezingLevel, "ship": round1(h.SHIP), "ensamble": round1(h.Prob),
			"modelos_con_riesgo": h.Votes, "modelos": h.Models}
	}
	city, _ := m.cityAlerts()
	var zone, short api.Level
	for _, a := range city {
		if a.ShortTerm {
			short = max(short, a.Level)
		} else {
			zone = max(zone, a.Level)
		}
	}
	s["smn"] = map[string]any{"zona": zone.String(), "corto_plazo": short.String()}
	if st, ok := m.hailObserved(); ok {
		s["metar_granizo"] = st.ICAO
	}
	if len(m.obs.st) > 0 {
		s["metar_cercano"] = m.obs.st[0].ICAO + " " + m.obs.st[0].Weather
	}
	return s
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

// logCells: anota cuando una celda pasa a estar "llegando" o "encima".
func (m Model) logCells() (Model, tea.Cmd) {
	state := ""
	if _, ok := m.cellThreat(); ok {
		state = "llega"
		if m.cells.nc.Overhead {
			state = "encima"
		}
	}
	prev := m.logCell
	m.logCell = state
	if state == "" || state == prev {
		return m, nil
	}
	return m, m.logCmd(registro.Celda, map[string]any{"que": state})
}

// logAlerts: avisos a corto plazo del SMN que cubren la ciudad.
func (m Model) logAlerts() tea.Cmd {
	var cmds []tea.Cmd
	city, _ := m.cityAlerts()
	for _, a := range city {
		if a.ShortTerm && m.once("aviso:"+a.URL) {
			cmds = append(cmds, m.logCmd(registro.Aviso, map[string]any{"id": a.URL, "nivel": a.Level.String(),
				"granizo": a.Hail, "titulo": a.Headline, "enviado": a.Sent, "vence": a.Expires}))
		}
	}
	return tea.Batch(cmds...)
}

// logModels: una vez por día y por nivel, cuando los modelos dan moderado o alto.
func (m Model) logModels() tea.Cmd {
	o := m.hail.outlook
	if o == nil || len(o.Hours) == 0 {
		return nil
	}
	risk := o.MaxWithin(24 * time.Hour)
	if risk < api.HailModerate || !m.once("modelos:"+m.loc.Name+m.now.Format("2006-01-02")+risk.String()) {
		return nil
	}
	data := map[string]any{"riesgo": risk.String(), "ship_max": round1(o.MaxSHIP), "ensamble_max": round1(o.MaxProb),
		"explicito": o.Explicit, "no_coinciden": o.Disagree}
	for _, e := range o.Episodes(api.HailModerate) {
		data["desde"], data["hasta"] = e.From.Format("2006-01-02T15:04"), e.To.Format("2006-01-02T15:04")
		break // el primer tramo
	}
	return m.logCmd(registro.Modelos, data)
}

// logObs: granizo reportado por un aeropuerto cercano.
func (m Model) logObs() tea.Cmd {
	st, ok := m.hailObserved()
	if !ok || !m.once("metar:"+st.ICAO+st.HailAt.Format(time.RFC3339)) {
		return nil
	}
	return m.logCmd(registro.Metar, map[string]any{"estacion": st.ICAO, "km": round1(st.DistKm), "hora": st.HailAt, "reporte": st.Raw})
}

// openReport abre el formulario "¿qué pasó?" con los valores por defecto.
func (m Model) openReport() Model {
	if m.state != stateReady { // sin ubicación no hay qué anotar
		return m
	}
	m.showReport, m.showAlerts, m.report = true, false, reportForm{}
	m.report.val[fieldWhat] = 2 // lo más común de anotar: lluvia fuerte
	return m
}
