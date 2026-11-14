package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"chusmeteo/internal/geo"
)

// capXML arma un CAP como los del SMN (estructura del aviso real del 29/03/2026).
func capXML(event, expires, polygon, msgType string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<alert xmlns="urn:oasis:names:tc:emergency:cap:1.2">
  <identifier>urn:oid:2.49.0.1.32.0.2026.10.24.17.00.00</identifier>
  <sender>smn@smn.gov.ar</sender>
  <sent>2026-10-24T17:00:00-03:00</sent>
  <status>Actual</status>
  <msgType>` + msgType + `</msgType>
  <scope>Public</scope>
  <info>
    <language>es-AR</language>
    <category>Met</category>
    <event>` + event + `</event>
    <urgency>Immediate</urgency>
    <severity>Severe</severity>
    <certainty>Observed</certainty>
    <expires>` + expires + `</expires>
    <senderName>Servicio Meteorológico Nacional</senderName>
    <headline>` + event + `</headline>
    <description> --- </description>
    <area>
      <areaDesc>Capital; Colón; Santa María</areaDesc>
      <polygon>` + polygon + `</polygon>
    </area>
  </info>
</alert>`
}

// Polígono alrededor de Córdoba capital (lat,lon como en CAP).
const cbaPoly = "-31.0,-64.6 -31.0,-63.8 -31.8,-63.8 -31.8,-64.6 -31.0,-64.6"

func TestParseCAP(t *testing.T) {
	a, err := ParseCAP([]byte(capXML("TORMENTAS FUERTES CON CAIDA DE GRANIZO - AVISO NARANJA",
		"2026-10-24T19:00:00-03:00", cbaPoly, "Alert")), "x")
	if err != nil {
		t.Fatal(err)
	}
	if !a.Hail || a.Level != LevelOrange || a.Desc != "" || len(a.Areas) != 1 {
		t.Errorf("aviso mal parseado: %+v", a)
	}
	if !a.Covers(geo.LatLon{Lat: -31.42, Lon: -64.18}) || a.Covers(geo.LatLon{Lat: -34.6, Lon: -58.4}) {
		t.Error("el polígono debería cubrir Córdoba y no Buenos Aires")
	}
	ref := time.Date(2026, 10, 24, 18, 0, 0, 0, time.FixedZone("ART", -3*3600))
	if !a.Active(ref) || a.Active(ref.Add(2*time.Hour)) {
		t.Error("vigencia mal calculada")
	}
}

func TestParseCAPLatin1(t *testing.T) {
	xml := strings.Replace(capXML("NEVADAS - AVISO AMARILLO", "2026-10-24T19:00:00-03:00", cbaPoly, "Alert"),
		`encoding="UTF-8"`, `encoding="ISO-8859-1"`, 1)
	xml = strings.Replace(xml, "Colón", "Col\xf3n", 1) // ó en latin1 es un solo byte: 0xF3
	a, err := ParseCAP([]byte(xml), "x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.AreaDesc, "Colón") || a.Level != LevelYellow || a.Hail {
		t.Errorf("latin1 mal decodificado: %q nivel=%v", a.AreaDesc, a.Level)
	}
}

func TestParseIndex(t *testing.T) {
	rss := `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>RSS CAP SMN</title>
	<item><title>Tormentas</title><link>https://ssl.smn.gob.ar/feeds/CAP/avisocortoplazo/a.xml</link><guid>a</guid></item>
	<item><title>Viento</title><link>/feeds/CAP/alerta/b.xml</link></item>
	<item><title>Ajeno</title><link>https://evil.example.com/c.xml</link></item>
	</channel></rss>`
	got := parseIndex([]byte(rss), "https://ssl.smn.gob.ar/CAP/AR.php")
	want := []string{
		"https://ssl.smn.gob.ar/feeds/CAP/avisocortoplazo/a.xml",
		"https://ssl.smn.gob.ar/feeds/CAP/alerta/b.xml",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("índice:\n got %v\nwant %v", got, want)
	}
	// Si viene HTML en vez de RSS, igual encontramos los links.
	html := `<html><a href="/feeds/CAP/alerta/z.xml">Ver XML CAP</a></html>`
	if got := parseIndex([]byte(html), "https://ssl.smn.gob.ar/CAP/AR.php"); len(got) != 1 {
		t.Errorf("fallback HTML: %v", got)
	}
}

func TestFetchAlertsIncremental(t *testing.T) {
	capCacheDir = ""
	var capHits atomic.Int32
	srv := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "chusmeteo") {
			t.Errorf("User-Agent = %q (con uno de navegador el SMN devuelve HTML)", ua)
		}
		switch r.URL.Path {
		case "/CAP/AR.php":
			fmt.Fprint(w, `<rss><channel>
			<item><link>/feeds/CAP/1.xml</link></item>
			<item><link>/feeds/CAP/2.xml</link></item>
			<item><link>/feeds/CAP/roto.xml</link></item></channel></rss>`)
		case "/feeds/CAP/roto.xml":
			capHits.Add(1)
			http.Error(w, "nope", http.StatusInternalServerError)
		default:
			capHits.Add(1)
			fmt.Fprint(w, capXML("TORMENTAS - AVISO AMARILLO", "2099-01-01T00:00:00-03:00", cbaPoly, "Alert"))
		}
	})
	smnIndexURL = srv.URL + "/CAP/AR.php"

	res, err := FetchAlerts(context.Background(), map[string]bool{srv.URL + "/feeds/CAP/1.xml": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Current) != 3 {
		t.Errorf("Current = %v", res.Current)
	}
	if capHits.Load() != 2 { // 1.xml ya lo teníamos: no se pide
		t.Errorf("pidió %d XML, esperaba 2", capHits.Load())
	}
	if len(res.New) != 1 || res.New[srv.URL+"/feeds/CAP/2.xml"] == nil {
		t.Errorf("New = %v (el roto no se guarda: se reintenta después)", res.New)
	}
}

func TestSortAlerts(t *testing.T) {
	cba := geo.LatLon{Lat: -31.42, Lon: -64.18}
	far := &Alert{Level: LevelRed, Areas: []geo.Polygon{parsePolygon("-40,-70 -40,-69 -41,-69")}}
	near := &Alert{Level: LevelYellow, Areas: []geo.Polygon{parsePolygon(cbaPoly)}}
	list := []*Alert{far, near}
	SortAlerts(list, cba)
	if list[0] != near {
		t.Error("el aviso que cubre la ciudad tiene que ir primero aunque sea de menor nivel")
	}
}

func TestEstimateHailRisk(t *testing.T) {
	cases := []struct {
		code      int
		cape, fzl float64
		want      HailRisk
	}{
		{99, 0, 0, HailHigh},      // ICON lo dice explícito
		{95, 300, 4000, HailNone}, // poca energía
		{95, 1200, 4200, HailLow}, // algo de energía
		{95, 2000, 4200, HailModerate},
		{95, 3000, 4200, HailHigh},
		{95, 3000, 4800, HailModerate}, // isoterma alta: se derrite
		{95, 1200, 3200, HailModerate}, // isoterma baja: sube
		{3, 3000, 4200, HailModerate},  // sin tormenta pronosticada: baja un escalón
	}
	for _, c := range cases {
		if got := EstimateHailRisk(c.code, c.cape, c.fzl); got != c.want {
			t.Errorf("EstimateHailRisk(%d, %.0f, %.0f) = %v, want %v", c.code, c.cape, c.fzl, got, c.want)
		}
	}
}

func TestGetHailOutlook(t *testing.T) {
	times := `"time":["2026-10-24T16:00","2026-10-24T17:00","2026-10-24T18:00"]`
	main := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("models"); !strings.HasPrefix(got, "icon_seamless,gfs_seamless") {
			t.Errorf("models = %q", got)
		}
		// 16 h: nadie ve nada. 17 h: sólo ICON ve mucho (no alcanza). 18 h: ICON y GFS coinciden.
		fmt.Fprint(w, `{"elevation":400,"hourly":{`+times+`,
			"weather_code_icon_seamless":[3,95,95],"cape_icon_seamless":[400,3000,3000],
			"freezing_level_height_icon_seamless":[4300,4100,3900],"precipitation_icon_seamless":[0,2,2],
			"weather_code_gfs_seamless":[3,3,95],"cape_gfs_seamless":[100,200,2100],
			"precipitation_gfs_seamless":[0,0,3],
			"weather_code_ecmwf_ifs025":[3,3,null],"cape_ecmwf_ifs025":[0,100,null]}}`)
	})
	ens := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		// 4 miembros; a las 18 h (y sus vecinas, por el ±1 h) 3 ven tormenta.
		fmt.Fprint(w, `{"hourly":{`+times+`,
			"cape_ncep_gefs_seamless":[0,0,900],"precipitation_ncep_gefs_seamless":[0,0,1],
			"cape_member01_ncep_gefs_seamless":[0,0,900],"precipitation_member01_ncep_gefs_seamless":[0,0,1],
			"cape_member01_ecmwf_ifs025_ensemble":[0,0,900],"precipitation_member01_ecmwf_ifs025_ensemble":[0,0,1],
			"cape_member02_ecmwf_ifs025_ensemble":[0,0,900],"precipitation_member02_ecmwf_ifs025_ensemble":[0,0,0],
			"cape_member01_icon_seamless_eps":[null,null,null],"precipitation_member01_icon_seamless_eps":[5,5,5]}}`)
	})
	forecastURL, ensembleURL = main.URL, ens.URL
	o, err := GetHailOutlook(context.Background(), Location{Latitude: -31.4, Longitude: -64.2})
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Hours) != 3 || o.Max != HailModerate || o.MaxCAPE != 3000 || o.MinFZL != 3900 {
		t.Errorf("outlook: %+v", o)
	}
	if r := o.Hours[1].Risk; r != HailNone {
		t.Errorf("17 h: sólo ICON ve riesgo, el consenso debería ser NULO y dio %v", r)
	}
	from, to, ok := o.Window(HailModerate)
	if !ok || from.Hour() != 18 || to.Hour() != 18 || !o.Crossed || !o.Disagree {
		t.Errorf("ventana %v-%v ok=%v cruce=%v/%v", from, to, ok, o.Crossed, o.Disagree)
	}
	if len(o.Models) != 3 || o.Models[0].Name != "ICON" || o.Models[1].MaxCAPE != 2100 {
		t.Errorf("modelos: %+v", o.Models)
	}
	if o.Members != 4 || o.MaxProb != 0.75 || o.Hours[0].Prob != 0 || o.Hours[2].Models != 2 {
		t.Errorf("ensamble: miembros=%d maxProb=%v horas=%+v", o.Members, o.MaxProb, o.Hours)
	}
}

func TestShortTermAlert(t *testing.T) {
	x := capXML("TORMENTAS FUERTES", "2099-01-01T00:00:00-03:00", cbaPoly, "Alert")
	a, err := ParseCAP([]byte(x), "https://ssl.smn.gob.ar/feeds/CAP/avisocortoplazo/2026_11_13_1838_cap_es.xml")
	if err != nil || !a.ShortTerm {
		t.Errorf("debería ser de corto plazo: %+v %v", a, err)
	}
	// El XML dice 17:00-03:00, pero en estos avisos esa hora es UTC (14:00 de Argentina).
	if want := time.Date(2026, 10, 24, 17, 0, 0, 0, time.UTC); !a.Sent.Equal(want) {
		t.Errorf("Sent = %v, want %v", a.Sent, want)
	}
	// Una alerta común vive en otra carpeta (y puede decir urgency=Immediate igual).
	b, _ := ParseCAP([]byte(x), "https://ssl.smn.gob.ar/feeds/CAP/xml_generados/CAP_1.xml")
	if b.ShortTerm {
		t.Error("una alerta común no es de corto plazo")
	}
	list := []*Alert{b, a}
	SortAlerts(list, geo.LatLon{Lat: -31.4, Lon: -64.2})
	if list[0] != a {
		t.Error("el aviso a corto plazo debería ir primero")
	}
}

func TestDedupAlerts(t *testing.T) {
	mk := func(event, expires, poly, link string) *Alert {
		a, err := ParseCAP([]byte(capXML(event, expires, poly, "Update")), link)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	exp := "2026-11-14T08:59:59-03:00"
	otherPoly := "-30.0,-65.0 -30.0,-63.0 -32.5,-63.0 -32.5,-65.0 -30.0,-65.0"
	a := mk("Tormentas - AVISO NARANJA", exp, cbaPoly, "https://x/Tormenta_Patagonia_6.xml")
	b := mk("Tormentas - AVISO NARANJA", exp, cbaPoly, "https://x/Tormenta_Llanura_6.xml")   // el mismo, en otro archivo
	c := mk("Tormentas - AVISO NARANJA", exp, otherPoly, "https://x/Tormenta_Llanura_7.xml") // mismo texto, otra área
	d := mk("Tormentas - AVISO AMARILLO", exp, cbaPoly, "https://x/Tormenta_Llanura_8.xml")  // otro nivel
	all := []*Alert{a, b, c, d}
	if got := DedupAlerts(all, true); len(got) != 3 || got[0] != a || got[1] != c || got[2] != d {
		t.Errorf("con área: %d avisos", len(got))
	}
	if got := DedupAlerts(all, false); len(got) != 2 || got[0] != a || got[1] != d {
		t.Errorf("sin área: %d avisos", len(got))
	}
	if len(all) != 4 || all[1] != b {
		t.Error("DedupAlerts no debería modificar el slice original")
	}
}

func TestConsensus(t *testing.T) {
	for _, c := range []struct {
		in   []HailRisk
		want HailRisk
	}{
		{[]HailRisk{HailHigh}, HailHigh},
		{[]HailRisk{HailHigh, HailNone}, HailNone},
		{[]HailRisk{HailHigh, HailLow, HailModerate, HailNone}, HailModerate},
		{[]HailRisk{HailHigh, HailHigh, HailNone, HailNone}, HailHigh},
	} {
		if got := consensus(c.in); got != c.want {
			t.Errorf("consensus(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSHIP(t *testing.T) {
	// Ambiente de granizo grande: mucha energía, aire frío arriba, buena cizalladura.
	big := SHIPInputs{CAPE: 3000, DewPoint: 20, Pressure: 960, T500: -14, T700: 6,
		Z500: 5700, Z700: 3100, ShearKmh: 80, FreezingAGL: 3600}
	if s := SHIP(big); s < 1.5 || s > 4 {
		t.Errorf("SHIP ambiente severo = %.2f", s)
	}
	// Lo mismo con poca energía: los ajustes lo aplastan.
	weak := big
	weak.CAPE = 400
	if s := SHIP(weak); s > 0.2 {
		t.Errorf("SHIP con CAPE 400 = %.2f", s)
	}
	if s := SHIP(SHIPInputs{}); s != 0 {
		t.Errorf("SHIP sin datos = %v", s)
	}
}

func TestEpisodeLabel(t *testing.T) {
	ref := time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC)
	o := &HailOutlook{}
	for i := 0; i < 24; i++ {
		h := HailHour{Time: ref.Add(time.Duration(i) * time.Hour)}
		if h.Time.Hour() == 15 || h.Time.Hour() == 16 || (i == 23) {
			h.Risk = HailLow
		}
		o.Hours = append(o.Hours, h)
	}
	eps := o.Episodes(HailLow)
	if len(eps) != 2 || eps[0].Label(ref) != "hoy 15–16 h" || eps[1].Label(ref) != "mañana 14 h" {
		t.Errorf("episodios: %+v", eps)
	}
}

func TestArrowAndPointAt(t *testing.T) {
	if Arrow(0) != "↓" || Arrow(270) != "→" || Arrow(225) != "↗" {
		t.Errorf("flechas: %s %s %s", Arrow(0), Arrow(270), Arrow(225))
	}
	p := PointWeather{Temperature: 10, Hourly: []PointHour{{Temperature: 10}, {Temperature: 15, Wind: 7}}}
	if got := p.At(1); got.Temperature != 15 || got.Wind != 7 {
		t.Errorf("At(1) = %+v", got)
	}
	if p.At(9).Temperature != 10 {
		t.Error("fuera de rango debe devolver el punto actual")
	}
}

func TestHasWx(t *testing.T) {
	for wx, want := range map[string]bool{"TSGR": true, "TSRA GR": true, "+TSRA": false, "VCTS": false, "": false, "GS": true} {
		if got := hasWx(wx, "GR") || hasWx(wx, "GS"); got != want {
			t.Errorf("hasWx(%q) hail = %v", wx, got)
		}
	}
	if !hasWx("VCTS", "TS") || hasWx("RA", "TS") {
		t.Error("TS")
	}
}

func TestGetStations(t *testing.T) {
	srv := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("bbox") == "" {
			t.Error("falta bbox")
		}
		fmt.Fprint(w, `[
		 {"icaoId":"SAOC","name":"Villa Reynolds, C, AR","obsTime":1793476800,"wxString":"TSRA GR","rawOb":"r1","lat":-33.1,"lon":-64.3},
		 {"icaoId":"SAOC","name":"Villa Reynolds, C, AR","obsTime":1793475900,"wxString":"TSGR RA","rawOb":"r0","lat":-33.1,"lon":-64.3},
		 {"icaoId":"SACO","name":"Cordoba, X, AR","obsTime":1793476800,"wxString":null,"rawOb":"r2","lat":-31.3,"lon":-64.2}]`)
	})
	old := metarURL
	t.Cleanup(func() { metarURL = old })
	metarURL = srv.URL
	st, err := GetStations(context.Background(), Location{Latitude: -31.4, Longitude: -64.2}, 2)
	if err != nil || len(st) != 2 {
		t.Fatalf("st=%+v err=%v", st, err)
	}
	if st[0].ICAO != "SACO" || st[0].Name != "Cordoba" || !st[0].HailAt.IsZero() {
		t.Errorf("la más cercana: %+v", st[0])
	}
	if st[1].Raw != "r1" || st[1].HailAt.IsZero() || st[1].ThunderAt.IsZero() {
		t.Errorf("la más lejana: %+v", st[1])
	}
}

func TestHailRiskLowFreezingLevel(t *testing.T) {
	// Caso real 31/10/2026 en Córdoba: chaparrones, CAPE ~800, isoterma ~3600 m. Cayó granizo.
	if got := EstimateHailRisk(80, 800, 3600); got != HailModerate {
		t.Errorf("chaparrón con isoterma baja = %v, quería MODERADO", got)
	}
	if got := EstimateHailRisk(80, 800, 4300); got != HailLow {
		t.Errorf("con isoterma normal sigue BAJO, got %v", got)
	}
}

func TestRecentLinks(t *testing.T) {
	now := time.Date(2026, 10, 31, 19, 0, 0, 0, time.Local)
	got := recentLinks([]string{
		"https://x/feeds/CAP/avisocortoplazo/2026_10_31_1808_cap_es.xml",
		"https://x/feeds/CAP/avisocortoplazo/2026_10_20_0800_cap_es.xml",
		"https://x/feeds/CAP/sin-fecha.xml",
	}, now)
	if len(got) != 2 || !strings.Contains(got[0], "10_31") || !strings.Contains(got[1], "sin-fecha") {
		t.Errorf("recentLinks = %v", got)
	}
}

func TestFetchAlertsRateLimitedAndDiskCache(t *testing.T) {
	old := capCacheDir
	capCacheDir = t.TempDir()
	t.Cleanup(func() { capCacheDir = old })

	var limited atomic.Bool
	srv := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/CAP/AR.php":
			fmt.Fprint(w, `<rss><channel><item><link>/feeds/CAP/2099_01_01_0000.xml</link></item></channel></rss>`)
		case limited.Load():
			http.Error(w, "slow down", http.StatusTooManyRequests)
		default:
			fmt.Fprint(w, capXML("TORMENTAS - AVISO AMARILLO", "2099-01-01T00:00:00-03:00", cbaPoly, "Alert"))
		}
	})
	smnIndexURL = srv.URL + "/CAP/AR.php"

	if res, err := FetchAlerts(context.Background(), map[string]bool{}); err != nil || len(res.New) != 1 {
		t.Fatalf("primera pasada: %v %v", res, err)
	}
	// Reinicio de la app (known vacío) con el SMN en 429: sale del disco.
	limited.Store(true)
	res, err := FetchAlerts(context.Background(), map[string]bool{})
	if err != nil || len(res.New) != 1 {
		t.Fatalf("desde el caché en disco: %v %v", res, err)
	}
	// Sin caché y con 429: error claro.
	capCacheDir = t.TempDir()
	if _, err := FetchAlerts(context.Background(), map[string]bool{}); !errors.Is(err, ErrRateLimited) {
		t.Errorf("esperaba ErrRateLimited, got %v", err)
	}
}
