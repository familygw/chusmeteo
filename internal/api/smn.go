package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"chusmeteo/internal/geo"
)

// Avisos del Servicio Meteorológico Nacional en formato CAP 1.2
// (Common Alerting Protocol, el estándar internacional de alertas).
//
// El índice es un RSS con un ítem por aviso; cada ítem apunta a un XML CAP.
// Dos cuidados:
//   - el SMN permite ~200 pedidos cada 10 minutos: cacheamos por URL y sólo
//     bajamos los avisos nuevos;
//   - con un User-Agent de navegador devuelve HTML: mandamos el nuestro.
var smnIndexURL = "https://ssl.smn.gob.ar/CAP/AR.php"

// Level es el color del aviso.
type Level int

const (
	LevelNone   Level = iota
	LevelYellow       // amarillo
	LevelOrange       // naranja
	LevelRed          // rojo
)

func (l Level) String() string {
	return [...]string{"-", "AMARILLO", "NARANJA", "ROJO"}[l]
}

// Alert es un aviso ya procesado.
type Alert struct {
	URL      string
	Event    string // "TORMENTAS FUERTES CON ... CAIDA DE GRANIZO - AVISO NARANJA"
	Headline string
	Desc     string
	AreaDesc string
	Level    Level
	Hail     bool // menciona granizo
	Sent     time.Time
	Expires  time.Time
	Areas    []geo.Polygon
	status   string
	msgType  string
}

// Active: vigente, real y no es una cancelación.
func (a *Alert) Active(now time.Time) bool {
	if a.status != "Actual" || a.msgType == "Cancel" {
		return false
	}
	return a.Expires.IsZero() || a.Expires.After(now)
}

// Covers dice si el aviso incluye un punto (por sus polígonos).
func (a *Alert) Covers(pt geo.LatLon) bool {
	for _, p := range a.Areas {
		if p.Contains(pt) {
			return true
		}
	}
	return false
}

// AlertsResult es lo que devuelve una actualización.
type AlertsResult struct {
	Current []string          // URLs que hoy están en el índice
	New     map[string]*Alert // avisos que no teníamos (nil = falló ese XML)
}

// FetchAlerts lee el índice y baja sólo los avisos que no están en `known`.
// `known` es una COPIA del conjunto de URLs que la UI ya tiene: así esta
// función (que corre en otra goroutine) no toca estado compartido.
func FetchAlerts(ctx context.Context, known map[string]bool) (*AlertsResult, error) {
	body, err := getBytes(ctx, smnIndexURL)
	if err != nil {
		return nil, fmt.Errorf("avisos SMN: %w", err)
	}
	links := parseIndex(body, smnIndexURL)
	res := &AlertsResult{Current: links, New: map[string]*Alert{}}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		sem  = make(chan struct{}, 4) // amables con el SMN
		errs int
	)
	for _, link := range links {
		if known[link] {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			a, err := fetchAlert(ctx, link)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs++
				return // no lo guardamos: se reintenta en la próxima pasada
			}
			res.New[link] = a
		}()
	}
	wg.Wait()
	if errs > 0 && len(res.New) == 0 && len(links) > 0 && len(known) == 0 {
		return nil, fmt.Errorf("avisos SMN: no se pudo leer ningún aviso (%d errores)", errs)
	}
	return res, nil
}

// parseIndex saca los links a XML CAP del RSS. Si el formato cambia (o viene
// Atom), cae a buscar URLs .xml con una expresión regular. Sólo aceptamos
// links del mismo host que el índice.
func parseIndex(body []byte, base string) []string {
	var rss struct {
		Items []struct {
			Link string `xml:"link"`
			GUID string `xml:"guid"`
		} `xml:"channel>item"`
	}
	var raw []string
	if err := decodeXML(body, &rss); err == nil {
		for _, it := range rss.Items {
			if strings.HasSuffix(strings.TrimSpace(it.Link), ".xml") {
				raw = append(raw, it.Link)
			} else if strings.HasSuffix(strings.TrimSpace(it.GUID), ".xml") {
				raw = append(raw, it.GUID)
			}
		}
	}
	if len(raw) == 0 {
		raw = xmlLinkRe.FindAllString(string(body), -1)
	}

	b, _ := url.Parse(base)
	seen := map[string]bool{}
	var out []string
	for _, l := range raw {
		u, err := b.Parse(strings.TrimSpace(l)) // resuelve links relativos
		if err != nil || u.Host != b.Host || seen[u.String()] {
			continue
		}
		seen[u.String()] = true
		out = append(out, u.String())
	}
	return out
}

var xmlLinkRe = regexp.MustCompile(`(https?://[^\s"'<>]+|/feeds/[^\s"'<>]+)\.xml`)

// capDoc refleja la parte del XML CAP que usamos. Los nombres de los tags
// no llevan namespace: encoding/xml compara sólo el nombre local.
type capDoc struct {
	Status  string `xml:"status"`
	MsgType string `xml:"msgType"`
	Sent    string `xml:"sent"`
	Info    []struct {
		Event       string `xml:"event"`
		Severity    string `xml:"severity"`
		Expires     string `xml:"expires"`
		Headline    string `xml:"headline"`
		Description string `xml:"description"`
		Areas       []struct {
			Desc     string   `xml:"areaDesc"`
			Polygons []string `xml:"polygon"`
		} `xml:"area"`
	} `xml:"info"`
}

func fetchAlert(ctx context.Context, link string) (*Alert, error) {
	body, err := getBytes(ctx, link)
	if err != nil {
		return nil, err
	}
	return ParseCAP(body, link)
}

// ParseCAP convierte un XML CAP en un Alert.
func ParseCAP(body []byte, link string) (*Alert, error) {
	var doc capDoc
	if err := decodeXML(body, &doc); err != nil {
		return nil, fmt.Errorf("CAP %s: %w", link, err)
	}
	if len(doc.Info) == 0 {
		return nil, fmt.Errorf("CAP %s: sin <info>", link)
	}
	info := doc.Info[0] // el SMN publica una sola <info> en castellano
	a := &Alert{
		URL:      link,
		Event:    strings.TrimSpace(info.Event),
		Headline: strings.TrimSpace(info.Headline),
		Desc:     cleanDesc(info.Description),
		status:   strings.TrimSpace(doc.Status),
		msgType:  strings.TrimSpace(doc.MsgType),
	}
	a.Sent, _ = time.Parse(time.RFC3339, strings.TrimSpace(doc.Sent))
	a.Expires, _ = time.Parse(time.RFC3339, strings.TrimSpace(info.Expires))

	var descs []string
	for _, ar := range info.Areas {
		if d := strings.TrimSpace(ar.Desc); d != "" {
			descs = append(descs, d)
		}
		for _, p := range ar.Polygons {
			if poly := parsePolygon(p); len(poly) >= 3 {
				a.Areas = append(a.Areas, poly)
			}
		}
	}
	a.AreaDesc = strings.Join(descs, "; ")

	text := strings.ToUpper(a.Event + " " + a.Headline + " " + a.Desc)
	a.Hail = strings.Contains(text, "GRANIZO")
	a.Level = levelOf(strings.ToUpper(a.Event+" "+a.Headline), info.Severity)
	return a, nil
}

// levelOf: el color viene escrito en el evento ("AVISO NARANJA"); si no,
// usamos la severidad CAP (Moderate/Severe/Extreme).
func levelOf(text, severity string) Level {
	switch {
	case strings.Contains(text, "ROJ"):
		return LevelRed
	case strings.Contains(text, "NARANJA"):
		return LevelOrange
	case strings.Contains(text, "AMARILL"):
		return LevelYellow
	}
	switch strings.TrimSpace(severity) {
	case "Extreme":
		return LevelRed
	case "Severe":
		return LevelOrange
	case "Moderate":
		return LevelYellow
	}
	return LevelNone
}

// parsePolygon: CAP usa "lat,lon lat,lon ..." (¡latitud primero!).
func parsePolygon(s string) geo.Polygon {
	var p geo.Polygon
	for _, pair := range strings.Fields(s) {
		lat, lon, ok := strings.Cut(pair, ",") // strings.Cut: Go 1.18+, muy cómodo
		if !ok {
			continue
		}
		la, err1 := strconv.ParseFloat(lat, 64)
		lo, err2 := strconv.ParseFloat(lon, 64)
		if err1 == nil && err2 == nil {
			p = append(p, geo.LatLon{Lat: la, Lon: lo})
		}
	}
	return p
}

// cleanDesc descarta las descripciones "de relleno" (el SMN a veces manda " --- ").
func cleanDesc(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if strings.Trim(s, "- ") == "" {
		return ""
	}
	return s
}

// SortAlerts ordena: primero los que cubren el punto, después por nivel y granizo.
func SortAlerts(alerts []*Alert, pt geo.LatLon) {
	sort.SliceStable(alerts, func(i, j int) bool {
		a, b := alerts[i], alerts[j]
		if ca, cb := a.Covers(pt), b.Covers(pt); ca != cb {
			return ca
		}
		if a.Level != b.Level {
			return a.Level > b.Level
		}
		return a.Hail && !b.Hail
	})
}

// decodeXML acepta XML en UTF-8 o ISO-8859-1/Windows-1252 (habitual en sitios
// del Estado). encoding/xml sólo entiende UTF-8 de fábrica: CharsetReader es
// el gancho para enchufarle otro decodificador.
func decodeXML(body []byte, v any) error {
	d := xml.NewDecoder(bytes.NewReader(body))
	d.CharsetReader = func(charset string, in io.Reader) (io.Reader, error) {
		switch strings.ToLower(charset) {
		case "iso-8859-1", "latin1", "windows-1252", "cp1252":
			return latin1Reader{bufio.NewReader(in)}, nil
		}
		return nil, fmt.Errorf("charset no soportado: %s", charset)
	}
	return d.Decode(v)
}

// latin1Reader convierte ISO-8859-1 a UTF-8: en latin1 cada byte ES el código
// Unicode del carácter, así que alcanza con re-codificar byte por byte.
type latin1Reader struct{ r *bufio.Reader }

func (l latin1Reader) Read(p []byte) (int, error) {
	n := 0
	for n+2 <= len(p) { // cada carácter ocupa a lo sumo 2 bytes en UTF-8
		b, err := l.r.ReadByte()
		if err != nil {
			return n, err
		}
		if b < 0x80 {
			p[n] = b
			n++
		} else {
			p[n], p[n+1] = 0xC0|b>>6, 0x80|b&0x3F
			n += 2
		}
	}
	return n, nil
}

// getBytes: GET genérico con nuestro User-Agent.
func getBytes(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "chusmeteo/0.6 (hobby; terminal)")
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 2<<20)) // tope de 2 MB por las dudas
}
