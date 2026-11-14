package api

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"sync"
	"time"

	"chusmeteo/internal/geo"
)

// Nowcast: "¿viene una celda fuerte hacia acá en la próxima hora?".
//
// Los modelos dicen si el ambiente da para granizo; el radar dice dónde están
// las tormentas AHORA. Acá miramos los últimos cuadros de Rain Viewer alrededor
// de la ubicación (del radar argentino más cercano o, si no hay, de Rain
// Viewer), con más detalle que el mapa nacional (zoom 7: ~1 km por píxel), y
// hacemos lo que hace cualquier nowcasting simple:
//  1. pasar cada cuadro a una grilla en km centrada en el usuario;
//  2. estimar el desplazamiento entre dos cuadros (correlación cruzada);
//  3. extrapolar en línea recta cada núcleo fuerte y ver si pasa por encima.
//
// Límites: las tormentas nacen, mueren y giran, así que la extrapolación sirve
// para ~1 hora; y un eco intenso SUGIERE granizo, no lo confirma.

const (
	// NowcastZoom es el máximo del tier libre de Rain Viewer.
	NowcastZoom = 7

	ncCellKm   = 2.0 // lado de cada celda de la grilla
	ncHalf     = 60  // celdas a cada lado del centro: 120 km
	ncN        = 2*ncHalf + 1
	ncHitKm    = 8.0  // "sobre tu zona": a menos de esto
	ncMaxKmh   = 110  // velocidad máxima de búsqueda del desplazamiento
	ncMinWet   = 12   // celdas con lluvia necesarias para estimar movimiento
	ncMinScore = 0.35 // correlación mínima para creerle al movimiento
	ncMaxETA   = 120  // minutos: más lejos que esto no extrapolamos

	// NowcastStrong es el nivel de radar (ver render.RadarLevel) desde el que
	// un eco cuenta como "núcleo fuerte"; el 4 es "intenso (posible granizo)".
	NowcastStrong = 3
)

// RadarGrid es un cuadro del radar reducido a niveles 0..4 en una grilla de
// 2 km centrada en un punto. Es inmutable: se puede compartir entre goroutines.
type RadarGrid struct {
	Time  time.Time
	cells [ncN * ncN]uint8 // fila 0 = norte, columna 0 = oeste
}

func (g *RadarGrid) at(i, j int) uint8 {
	if i < 0 || j < 0 || i >= ncN || j >= ncN {
		return 0
	}
	return g.cells[j*ncN+i]
}

// NewRadarGrid arma una grilla a partir de una función nivel(este km, norte km).
// Sirve para tests y para armar la grilla real desde los tiles.
func NewRadarGrid(t time.Time, level func(eastKm, northKm float64) int) *RadarGrid {
	g := &RadarGrid{Time: t}
	for j := range ncN {
		for i := range ncN {
			e, n := float64(i-ncHalf)*ncCellKm, float64(ncHalf-j)*ncCellKm
			g.cells[j*ncN+i] = uint8(max(0, min(level(e, n), 4)))
		}
	}
	return g
}

// Nowcast es el resultado del análisis.
type Nowcast struct {
	Time   time.Time // hora del último cuadro del radar
	Frames int       // cuadros usados (1 = sin movimiento estimado)
	Source string    // de dónde salió: "radar RMA1 Cordoba", "Rain Viewer"
	DBZ    bool      // los niveles salen de dBZ reales (no de colores adivinados)

	// NoCoverage: ningún radar de Rain Viewer llega a la ubicación. Entonces
	// "no veo ecos" NO significa "no hay tormentas": no se puede afirmar nada.
	// (Al 13/11/2026 Rain Viewer no incluye los radares argentinos del SMN:
	// en el país sólo se ve lo que alcanzan los de Brasil y Paraguay.)
	NoCoverage bool

	// Núcleo fuerte más cercano (Near=false si no hay ninguno a menos de 120 km).
	Near      bool
	NearKm    float64
	NearDir   string // dónde está, visto desde el usuario: "SO"
	NearLevel int    // 3 fuerte, 4 intenso

	// Movimiento del conjunto de ecos.
	Moving   bool
	SpeedKmh float64
	Toward   string // hacia dónde va: "NE"

	// Sobre la ubicación ahora (a menos de ncHitKm).
	Overhead  bool
	OverLevel int

	// Núcleo que, extrapolado, pasa por encima.
	Approaching bool
	ETA         time.Time
	ETALevel    int
	FromDir     string
}

// Threat: hay un núcleo fuerte encima o llegando antes de `within`.
// level es el más alto entre lo que hay encima y lo que viene.
func (n *Nowcast) Threat(now time.Time, within time.Duration) (level int, ok bool) {
	if n == nil || n.NoCoverage || now.Sub(n.Time) > 40*time.Minute { // sin radar o radar viejo: no afirmamos nada
		return 0, false
	}
	if n.Overhead {
		level, ok = n.OverLevel, true
	}
	if n.Approaching && n.ETA.Sub(now) <= within {
		level, ok = max(level, n.ETALevel), true
	}
	return level, ok
}

// GetNowcast baja (o reusa de `cache`, por path) los últimos cuadros del radar
// alrededor de loc y los analiza. Devuelve también el caché nuevo, que sólo
// contiene los cuadros que siguen sirviendo. `level` clasifica un color del
// radar en 0..4: se inyecta porque la tabla de colores vive en render, y
// render ya importa api (Go no permite imports circulares).
//
// Primero prueba con el radar argentino más cercano (ver ohmc.go); si no hay,
// está caído o falla, cae a Rain Viewer.
func GetNowcast(ctx context.Context, loc Location, cache map[string]*RadarGrid,
	level func(color.RGBA) int) (*Nowcast, map[string]*RadarGrid, error) {
	if nc, next, err := ohmcNowcast(ctx, loc, cache); err == nil {
		return nc, next, nil
	}
	idx, err := GetRadarIndex(ctx)
	if err != nil {
		return nil, nil, err
	}
	frames := idx.Frames
	if len(frames) > 3 {
		frames = frames[len(frames)-3:] // 20 minutos de historia alcanzan
	}
	center := geo.LatLon{Lat: loc.Latitude, Lon: loc.Longitude}
	fx, fy := tileXY(center, NowcastZoom)
	// 2x2 tiles elegidos para que el punto quede lo más al medio posible:
	// así hay al menos medio tile (~130 km en Córdoba) hacia cada lado.
	x0, y0 := int(math.Floor(fx-0.5)), int(math.Floor(fy-0.5))

	next := map[string]*RadarGrid{}
	var grids []*RadarGrid
	var lastErr error
	for _, f := range frames {
		g := cache[f.Path]
		if g == nil {
			img, err := fetchTiles(ctx, f, NowcastZoom, x0, y0, x0+1, y0+1, rainviewerTile(idx.Host, f, NowcastZoom))
			if err != nil {
				lastErr = err
				continue
			}
			g = gridFromImage(img, center, level)
		}
		next[f.Path] = g
		grids = append(grids, g)
	}
	newest := frames[len(frames)-1]
	if next[newest.Path] == nil {
		if lastErr == nil {
			lastErr = errors.New("sin cuadros")
		}
		return nil, next, fmt.Errorf("nowcast: %w", lastErr)
	}
	nc := Analyze(grids)
	nc.Source = "Rain Viewer"
	nc.NoCoverage = !covered(ctx, idx.Host, center)
	return nc, next, nil
}

// coverageCache: la cobertura casi no cambia; la guardamos por tile para no
// pedirla cada 5 minutos. Mapa compartido entre goroutines => mutex.
var (
	coverageMu    sync.Mutex
	coverageCache = map[[2]int]image.Image{}
)

// covered dice si algún radar de Rain Viewer alcanza el punto. El tile de
// cobertura es una máscara pensada para oscurecer el mapa: negro opaco donde
// NO hay radar, transparente donde sí.
// Si no se puede bajar, suponemos que sí (no rompemos el nowcast por esto).
func covered(ctx context.Context, host string, pt geo.LatLon) bool {
	const zoom = 5 // alcanza: los radares cubren cientos de km
	fx, fy := tileXY(pt, zoom)
	tx, ty := int(math.Floor(fx)), int(math.Floor(fy))
	key := [2]int{tx, ty}

	coverageMu.Lock()
	t, ok := coverageCache[key]
	coverageMu.Unlock()
	if !ok {
		var err error
		t, err = fetchImage(ctx, fmt.Sprintf("%s/v2/coverage/0/%d/%d/%d/%d/0/0_0.png", host, tileSize, zoom, tx, ty))
		if err != nil {
			return true
		}
		coverageMu.Lock()
		coverageCache[key] = t
		coverageMu.Unlock()
	}
	if t == nil { // 404: sin datos de cobertura para ese tile
		return true
	}
	b := t.Bounds()
	px := b.Min.X + int((fx-float64(tx))*float64(b.Dx()))
	py := b.Min.Y + int((fy-float64(ty))*float64(b.Dy()))
	c := color.RGBAModel.Convert(t.At(px, py)).(color.RGBA)
	return c.A < 128 // transparente = cubierto
}

func gridFromImage(img *RadarImage, center geo.LatLon, level func(color.RGBA) int) *RadarGrid {
	kmLat := 111.2
	kmLon := 111.2 * math.Cos(center.Lat*math.Pi/180)
	return NewRadarGrid(img.Frame.Time, func(e, n float64) int {
		return level(img.Sample(geo.LatLon{Lat: center.Lat + n/kmLat, Lon: center.Lon + e/kmLon}))
	})
}

// Analyze recibe los cuadros del más viejo al más nuevo (al menos uno).
func Analyze(grids []*RadarGrid) *Nowcast {
	cur := grids[len(grids)-1]
	nc := &Nowcast{Time: cur.Time, Frames: 1}

	// Movimiento: contra el cuadro más viejo que tengamos (más separación en
	// el tiempo = mejor resolución de velocidad con celdas de 2 km).
	var vx, vy float64 // km/min, este y norte
	if len(grids) > 1 {
		old := grids[0]
		if dt := cur.Time.Sub(old.Time).Minutes(); dt >= 5 && dt <= 45 {
			if sx, sy, ok := bestShift(old, cur, dt); ok {
				nc.Frames = len(grids)
				vx, vy = float64(sx)*ncCellKm/dt, -float64(sy)*ncCellKm/dt // las filas crecen hacia el sur
				nc.SpeedKmh = math.Hypot(vx, vy) * 60
				if nc.SpeedKmh >= 5 {
					nc.Moving = true
					nc.Toward = bearing(vx, vy)
				}
			}
		}
	}

	nc.NearKm = math.Inf(1)
	eta := math.Inf(1)
	for j := range ncN {
		for i := range ncN {
			lv := int(cur.at(i, j))
			if lv < NowcastStrong || !clustered(cur, i, j) {
				continue
			}
			x, y := float64(i-ncHalf)*ncCellKm, float64(ncHalf-j)*ncCellKm
			d := math.Hypot(x, y)
			if d > ncHalf*ncCellKm {
				continue
			}
			if d < nc.NearKm || (d == nc.NearKm && lv > nc.NearLevel) {
				nc.Near, nc.NearKm, nc.NearLevel, nc.NearDir = true, d, lv, bearing(x, y)
			}
			if d <= ncHitKm {
				nc.Overhead, nc.OverLevel = true, max(nc.OverLevel, lv)
				continue
			}
			if !nc.Moving {
				continue
			}
			// Máximo acercamiento de un punto que se mueve en línea recta:
			// t* = -(p·v)/|v|², distancia mínima = |p + v·t*|.
			v2 := vx*vx + vy*vy
			t := -(x*vx + y*vy) / v2
			if t <= 0 {
				continue // se aleja
			}
			miss := math.Hypot(x+vx*t, y+vy*t)
			if miss > ncHitKm {
				continue
			}
			t -= math.Sqrt(ncHitKm*ncHitKm-miss*miss) / math.Sqrt(v2) // cuándo entra al radio
			if t > ncMaxETA {
				continue
			}
			switch {
			case t < eta-10: // llega claramente antes: manda este
				eta, nc.ETALevel, nc.FromDir = t, lv, bearing(x, y)
			case t < eta+10: // parte del mismo frente: nos quedamos con el peor nivel
				eta, nc.ETALevel = min(eta, t), max(nc.ETALevel, lv)
			}
			nc.Approaching = true
		}
	}
	if !nc.Near {
		nc.NearKm = 0
	}
	if nc.Approaching {
		nc.ETA = cur.Time.Add(time.Duration(eta * float64(time.Minute)))
	}
	return nc
}

// clustered: la celda tiene al menos 2 vecinas fuertes. Un píxel suelto suele
// ser ruido del radar (o un cerro), no una tormenta.
func clustered(g *RadarGrid, i, j int) bool {
	n := 0
	for dj := -1; dj <= 1; dj++ {
		for di := -1; di <= 1; di++ {
			if (di != 0 || dj != 0) && g.at(i+di, j+dj) >= NowcastStrong {
				n++
			}
		}
	}
	return n >= 2
}

// bestShift busca el corrimiento (en celdas) que mejor superpone `old` sobre
// `cur`: prueba todos los posibles y se queda con el de mayor correlación
// normalizada. Fuerza bruta: ~35x35 corrimientos x 121x121 celdas, unos ms.
func bestShift(old, cur *RadarGrid, dtMin float64) (sx, sy int, ok bool) {
	wet := func(g *RadarGrid) int {
		n := 0
		for _, c := range g.cells {
			if c > 0 {
				n++
			}
		}
		return n
	}
	if wet(old) < ncMinWet || wet(cur) < ncMinWet {
		return 0, 0, false
	}
	reach := int(math.Ceil(ncMaxKmh * dtMin / 60 / ncCellKm))
	best := -1.0
	for dy := -reach; dy <= reach; dy++ {
		for dx := -reach; dx <= reach; dx++ {
			var ab, aa, bb float64
			for j := max(0, -dy); j < min(ncN, ncN-dy); j++ {
				for i := max(0, -dx); i < min(ncN, ncN-dx); i++ {
					a, b := float64(old.cells[j*ncN+i]), float64(cur.cells[(j+dy)*ncN+i+dx])
					ab += a * b
					aa += a * a
					bb += b * b
				}
			}
			if aa == 0 || bb == 0 {
				continue
			}
			// Empate: preferimos el corrimiento más chico (lo más simple).
			score := ab/math.Sqrt(aa*bb) - 1e-4*math.Hypot(float64(dx), float64(dy))
			if score > best {
				best, sx, sy = score, dx, dy
			}
		}
	}
	return sx, sy, best >= ncMinScore
}

// bearing: rumbo de un vector (este, norte) como punto cardinal.
func bearing(east, north float64) string {
	deg := math.Atan2(east, north) * 180 / math.Pi // 0 = norte, 90 = este
	if deg < 0 {
		deg += 360
	}
	return Compass(deg)
}
