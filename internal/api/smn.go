package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
	Limited bool              // quedaron avisos sin bajar (tope o HTTP 429)
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
	links = recentLinks(links, time.Now())
	res := &AlertsResult{Current: links, New: map[string]*Alert{}}

	// Primero lo que ya tenemos en disco (el XML de un aviso no cambia), después
	// la red, lo más nuevo primero y con tope: el SMN responde 429 si nos pasamos.
	var todo []string
	for _, link := range links {
		if known[link] {
			continue
		}
		if body, ok := capCacheGet(link); ok {
			if a, err := ParseCAP(body, link); err == nil {
				res.New[link] = a
				continue
			}
		}
		todo = append(todo, link)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(todo))) // los nombres llevan la fecha: AAAA_MM_DD_HHMM
	if len(todo) > maxFetchPerPass {
		todo = todo[:maxFetchPerPass]
		res.Limited = true
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		sem  = make(chan struct{}, 4) // amables con el SMN
		errs int
		stop bool // el SMN dijo 429: no insistimos
	)
	for _, link := range todo {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			mu.Lock()
			skip := stop
			mu.Unlock()
			if skip {
				return
			}
			body, err := getBytes(ctx, link)
			var a *Alert
			if err == nil {
				a, err = ParseCAP(body, link)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs++
				if errors.Is(err, ErrRateLimited) {
					stop, res.Limited = true, true
				}
				return // no lo guardamos: se reintenta en la próxima pasada
			}
			capCachePut(link, body)
			res.New[link] = a
		}()
	}
	wg.Wait()
	if len(res.New) == 0 && len(known) == 0 && len(todo) > 0 && errs > 0 {
		if res.Limited {
			return nil, fmt.Errorf("avisos SMN: %w (se reintenta en 10 min)", ErrRateLimited)
		}
		return nil, fmt.Errorf("avisos SMN: no se pudo leer ningún aviso (%d errores)", errs)
	}
	return res, nil
}

// ErrRateLimited: el SMN devolvió HTTP 429.
var ErrRateLimited = errors.New("el SMN limitó los pedidos (HTTP 429)")

const (
	maxFetchPerPass = 60
	maxAlertAge     = 3 * 24 * time.Hour
)

var linkDateRe = regexp.MustCompile(`(\d{4})_(\d{2})_(\d{2})_(\d{2})(\d{2})`)

// recentLinks descarta avisos de hace días (la fecha va en el nombre del archivo).
// Los links sin fecha en el nombre se conservan.
func recentLinks(links []string, now time.Time) []string {
	out := links[:0:0]
	for _, l := range links {
		if m := linkDateRe.FindStringSubmatch(l); m != nil {
			n := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
			t := time.Date(n(1), time.Month(n(2)), n(3), n(4), n(5), 0, 0, time.Local)
			if now.Sub(t) > maxAlertAge {
				continue
			}
		}
		out = append(out, l)
	}
	return out
}

// capCacheDir: caché en disco de los XML (vacío = sin caché). Sobrevive a reinicios.
var capCacheDir = func() string {
	d, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "chusmeteo", "cap")
}()

func capCachePath(link string) string {
	if capCacheDir == "" {
		return ""
	}
	h := sha1.Sum([]byte(link))
	return filepath.Join(capCacheDir, hex.EncodeToString(h[:8])+".xml")
}

func capCacheGet(link string) ([]byte, bool) {
	p := capCachePath(link)
	if p == "" {
		return nil, false
	}
	b, err := os.ReadFile(p)
	return b, err == nil
}

func capCachePut(link string, body []byte) {
	if p := capCachePath(link); p != "" && os.MkdirAll(capCacheDir, 0o755) == nil {
		_ = os.WriteFile(p, body, 0o644)
	}
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
	if res.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 2<<20)) // tope de 2 MB por las dudas
}
