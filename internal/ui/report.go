package ui

import (
	"strings"
	"time"

	"chusmeteo/internal/registro"

	"github.com/charmbracelet/lipgloss"

	tea "github.com/charmbracelet/bubbletea"
)

// Formulario "¿qué pasó en tu zona?" (F6 / o): un diálogo aparte, al estilo
// de los de configuración de DOS. Lo que se anota va al registro de aciertos.
//
// Cada campo es una lista de opciones; el estado es sólo "en qué campo estoy"
// y "qué opción tiene cada campo". Así agregar un campo es agregar un renglón
// a reportFields.

type reportField struct {
	Key     string   // nombre en el registro
	Label   string   // nombre en pantalla
	Options []string // lo que se muestra
	Values  []string // lo que se guarda (si es nil, lo mismo que Options)
}

const (
	fieldWhat = iota
	fieldSize
	fieldWind
	fieldWhen
)

var reportFields = []reportField{
	fieldWhat: {Key: "que", Label: "Qué pasó",
		Options: []string{"nada", "lluvia ligera", "lluvia fuerte", "lluvia y granizo", "solo granizo"}},
	fieldSize: {Key: "granizo_tamano", Label: "Granizo",
		Options: []string{"pequeño (arveja, < 1 cm)", "mediano (moneda, 1–3 cm)", "grande (pelota de golf o más)"},
		Values:  []string{"pequeño", "mediano", "grande"}},
	fieldWind: {Key: "viento", Label: "Viento",
		Options: []string{"calmo", "fuerte", "muy fuerte (ramas caídas)"},
		Values:  []string{"calmo", "fuerte", "muy fuerte"}},
	fieldWhen: {Key: "hace_min", Label: "Cuándo",
		Options: []string{"ahora", "hace 15 min", "hace 30 min", "hace 1 hora", "hace 2 horas"},
		Values:  []string{"0", "15", "30", "60", "120"}},
}

var whenMinutes = []int{0, 15, 30, 60, 120}

// reportForm es el estado del formulario.
type reportForm struct {
	field int                // campo con el foco
	val   [fieldWhen + 1]int // opción elegida en cada campo
}

// hail: ¿lo elegido en "Qué pasó" incluye granizo?
func (f reportForm) hail() bool { return f.val[fieldWhat] >= 3 }

// visible: el tamaño del granizo sólo se pregunta si cayó granizo.
func (f reportForm) visible(i int) bool { return i != fieldSize || f.hail() }

// move pasa al campo visible siguiente o anterior.
func (f reportForm) move(dir int) reportForm {
	n := len(reportFields)
	for range n {
		f.field = (f.field + dir + n) % n
		if f.visible(f.field) {
			break
		}
	}
	return f
}

// change cambia la opción del campo con el foco (sin dar la vuelta).
func (f reportForm) change(dir int) reportForm {
	n := len(reportFields[f.field].Options)
	f.val[f.field] = max(0, min(f.val[f.field]+dir, n-1))
	return f
}

// updateReport maneja las teclas del formulario (es modal).
func (m Model) updateReport(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.report
	switch k := msg.String(); k {
	case "esc", "q", "f6":
		m.showReport = false
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	// Las opciones están apiladas, así que ↑↓ elige la opción; ←→ (o Tab)
	// cambia de grupo.
	case "up", "k":
		f = f.change(-1)
	case "down", "j", " ":
		f = f.change(+1)
	case "left", "h", "shift+tab":
		f = f.move(-1)
	case "right", "l", "tab":
		f = f.move(+1)
	case "enter":
		m.showReport = false
		return m.saveReport()
	default:
		// Un número elige directo la opción del campo actual (1 = la primera).
		if len(k) == 1 && k[0] >= '1' && int(k[0]-'1') < len(reportFields[f.field].Options) {
			f.val[f.field] = int(k[0] - '1')
		}
	}
	if !f.visible(f.field) {
		f.field = fieldWhat
	}
	m.report = f
	return m, nil
}

// saveReport arma la entrada del registro con lo elegido.
func (m Model) saveReport() (Model, tea.Cmd) {
	f := m.report
	data := map[string]any{}
	for i, fld := range reportFields {
		if !f.visible(i) || i == fieldWhen {
			continue
		}
		v := fld.Options[f.val[i]]
		if fld.Values != nil {
			v = fld.Values[f.val[i]]
		}
		data[fld.Key] = v
	}
	// "Cuándo" se guarda como minutos y como hora: es lo que se compara con
	// la hora de llegada que estimó el radar.
	mins := whenMinutes[f.val[fieldWhen]]
	when := m.now.Add(-time.Duration(mins) * time.Minute)
	data["hace_min"], data["cuando"] = mins, when

	m.lastReport = when.Format("15:04") + " " + reportFields[fieldWhat].Options[f.val[fieldWhat]]
	if f.hail() {
		m.lastReport += " " + reportFields[fieldSize].Values[f.val[fieldSize]]
	}
	return m, m.logCmd(registro.Usuario, data)
}

var (
	formBG    = vgaGray
	formText  = lipgloss.NewStyle().Background(formBG).Foreground(vgaBlack)
	formDim   = formText.Foreground(vgaDarkGray)
	formTitle = lipgloss.NewStyle().Background(vgaBlue).Foreground(vgaWhite).Bold(true)
	formFocus = lipgloss.NewStyle().Background(vgaBlue).Foreground(vgaYellow).Bold(true)
	formSel   = formText.Bold(true)
)

// reportBox dibuja el formulario.
func (m Model) reportBox() string {
	w := min(64, m.width-10) // ancho interior
	in := w - 2
	pad := func(s string) string {
		if gap := in - lipgloss.Width(s); gap > 0 {
			s += formText.Render(strings.Repeat(" ", gap))
		}
		return s
	}
	f := m.report
	var body []string
	body = append(body, pad(""))
	for i, fld := range reportFields {
		if !f.visible(i) {
			continue
		}
		focus := i == f.field
		label := formText.Render(fld.Label + ":")
		if focus {
			label = formSel.Render("► " + fld.Label + ":")
		} else {
			label = formText.Render("  " + fld.Label + ":")
		}
		body = append(body, pad(label))
		for j, opt := range fld.Options {
			box := "( ) "
			if j == f.val[i] {
				box = "(•) "
			}
			line := truncate("     "+box+opt, in)
			switch {
			case focus && j == f.val[i]:
				body = append(body, pad(formFocus.Render(line)))
			case j == f.val[i]:
				body = append(body, pad(formSel.Render(line)))
			case focus:
				body = append(body, pad(formText.Render(line)))
			}
		}
		// Los campos sin foco muestran sólo lo elegido: el diálogo queda corto.
	}
	body = append(body, pad(""),
		pad(formDim.Render(truncate("↑↓ opción · ←→ grupo · Enter anota · Esc cancela", in))),
		pad(""))

	title := " ¿QUÉ PASÓ EN TU ZONA? "
	side := w - lipgloss.Width(title)
	out := []string{formText.Render("╔"+strings.Repeat("═", side/2)) + formTitle.Render(title) +
		formText.Render(strings.Repeat("═", side-side/2)+"╗")}
	for _, l := range body {
		out = append(out, formText.Render("║ ")+l+formText.Render(" ║"))
	}
	out = append(out, formText.Render("╚"+strings.Repeat("═", w)+"╝"))
	return strings.Join(out, "\n")
}
