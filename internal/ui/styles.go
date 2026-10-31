package ui

import "github.com/charmbracelet/lipgloss"

// Paleta VGA de 16 colores (la de DOS). Usamos los hex exactos.
const (
	vgaBlack    = lipgloss.Color("#000000")
	vgaBlue     = lipgloss.Color("#0000AA")
	vgaCyan     = lipgloss.Color("#00AAAA")
	vgaRed      = lipgloss.Color("#AA0000")
	vgaGray     = lipgloss.Color("#AAAAAA")
	vgaDarkGray = lipgloss.Color("#555555")
	vgaLBlue    = lipgloss.Color("#5555FF")
	vgaLGreen   = lipgloss.Color("#55FF55")
	vgaLCyan    = lipgloss.Color("#55FFFF")
	vgaLRed     = lipgloss.Color("#FF5555")
	vgaYellow   = lipgloss.Color("#FFFF55")
	vgaWhite    = lipgloss.Color("#FFFFFF")
)

// Truco importante con lipgloss: TODO estilo que se dibuje sobre el fondo azul
// tiene que tener Background(vgaBlue). Si no, ese texto "agujerea" el fondo
// y se ve el color default de tu terminal. Por eso todos derivan de `base`.
//
// Los estilos de lipgloss son valores (no punteros): cada método devuelve
// una copia modificada, así que derivar de `base` no la altera.
var (
	base = lipgloss.NewStyle().Background(vgaBlue).Foreground(vgaGray)

	labelStyle = base.Foreground(vgaLCyan)
	valueStyle = base.Foreground(vgaWhite).Bold(true)
	dimStyle   = base.Foreground(vgaDarkGray)
	artStyle   = base.Foreground(vgaYellow)
	titleStyle = base.Foreground(vgaYellow).Bold(true)
	errStyle   = base.Foreground(vgaLRed).Bold(true)

	// Ventana principal con borde doble, como los diálogos de Norton/Turbo Pascal.
	panelStyle = base.
			Border(lipgloss.DoubleBorder()).
			BorderForeground(vgaWhite).
			BorderBackground(vgaBlue).
			Padding(0, 1)

	// Cajitas de pronóstico con borde simple.
	dayStyle = base.
			Border(lipgloss.NormalBorder()).
			BorderForeground(vgaLBlue).
			BorderBackground(vgaBlue).
			Align(lipgloss.Center)

	// Barra superior (cyan, texto negro) e inferior de teclas F.
	barStyle    = lipgloss.NewStyle().Background(vgaCyan).Foreground(vgaBlack)
	barKeyStyle = lipgloss.NewStyle().Background(vgaBlack).Foreground(vgaWhite)
)

// tempStyle pinta la temperatura según qué tan frío o caliente está.
func tempStyle(t float64) lipgloss.Style {
	switch {
	case t < 5:
		return valueStyle.Foreground(vgaLCyan)
	case t < 18:
		return valueStyle.Foreground(vgaWhite)
	case t < 28:
		return valueStyle.Foreground(vgaYellow)
	default:
		return valueStyle.Foreground(vgaLRed)
	}
}
