package ui

import (
	"context"
	"fmt"
	"image"
	"time"

	"chusmeteo/internal/render"
	"chusmeteo/internal/sdr"

	tea "github.com/charmbracelet/bubbletea"
)

const satPoll = 5 * time.Second // cada cuánto buscamos una imagen nueva en disco

// satState: vista del modo hardcore (RTL-SDR + Meteor-M LRPT).
type satState struct {
	img     image.Image
	path    string
	mod     time.Time
	checked time.Time
	cancel  context.CancelFunc // != nil mientras SatDump está corriendo
	err     error
}

type satImgMsg struct {
	img  image.Image
	path string
	mod  time.Time
}
type satDoneMsg struct{ err error }

// numViews: las pestañas disponibles (la de satélite sólo con -hardcore).
func (m Model) numViews() View {
	if m.opts.Hardcore {
		return viewCount + 1
	}
	return viewCount
}

func (m Model) ensureSat() (Model, tea.Cmd) {
	s := &m.sat
	now := time.Now()
	if now.Sub(s.checked) < satPoll {
		return m, nil
	}
	s.checked = now
	dir, known := m.opts.SDR.Dir, s.mod
	return m, func() tea.Msg {
		path, mod, ok := sdr.Latest(dir)
		if !ok || !mod.After(known) {
			return nil
		}
		img, err := sdr.Load(path)
		if err != nil {
			return nil // puede estar escribiéndose todavía: reintentamos
		}
		return satImgMsg{img, path, mod}
	}
}

// toggleCapture arranca o detiene SatDump.
func (m Model) toggleCapture() (Model, tea.Cmd) {
	if m.sat.cancel != nil {
		m.sat.cancel()
		m.sat.cancel = nil
		return m, nil
	}
	if err := sdr.Available(); err != nil {
		m.sat.err = err
		return m, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.sat.cancel, m.sat.err = cancel, nil
	cfg := m.opts.SDR
	return m, func() tea.Msg { return satDoneMsg{sdr.Capture(ctx, cfg)} }
}

func (m Model) satView() string {
	p := m.mapProjection()
	var c *render.Canvas
	if m.sat.img != nil {
		c = render.FromImage(m.sat.img, p.W, p.H)
	} else {
		c = render.New(p.W, p.H, render.Black)
		c.Label(max(0, (p.W-9)/2), p.H/2, "SIN SEÑAL")
	}
	rightW := m.rightWidth(c)

	state := dimStyle.Render("■ EN ESPERA")
	if m.sat.cancel != nil {
		state = errStyle.Render("◄ CAPTURANDO")
	}
	info := []string{
		titleStyle.Render("SATÉLITE · METEOR-M LRPT"),
		state,
		labelStyle.Render(fmt.Sprintf("%.4f MHz · %.0f dB", m.opts.SDR.FreqMHz, m.opts.SDR.Gain)),
		"",
	}
	if m.sat.img != nil {
		b := m.sat.img.Bounds()
		info = append(info,
			labelStyle.Render("IMAGEN ")+valueStyle.Render(fmt.Sprintf("%dx%d", b.Dx(), b.Dy())),
			labelStyle.Render("RECIBIDA ")+valueStyle.Render(m.sat.mod.Format("02/01 15:04")),
			base.Width(rightW).Render(truncate(m.sat.path, rightW*2)), "")
	} else {
		info = append(info, base.Width(rightW).Render("Todavía no hay imágenes en"), base.Width(rightW).Render(truncate(m.opts.SDR.Dir, rightW)), "")
	}
	info = append(info, base.Render("S  iniciar / detener captura"))
	if m.sat.err != nil {
		info = append(info, "", errStyle.Width(rightW).Render("▲ "+m.sat.err.Error()))
	}
	info = append(info, "", dimStyle.Render("RTL-SDR + antena V-dipolo o QFH"), dimStyle.Render("Decodifica: SatDump"))
	return m.splitMap(c, info)
}
