# chusmeteo ☼

El clima en tu terminal, con estética de DOS de los 90.
Hobby + excusa para aprender Go.

Todo es *responsive*: el panel ocupa la terminal entera y cada vista usa el
espacio que tenga. Cuanto más grande la ventana, más detalle en pixel art.

Tres vistas:

1. **Clima**: condiciones actuales, pronóstico de hasta 7 días y, si hay alto, un gráfico de las próximas 24 horas.
2. **Mapa**: Argentina coloreada por temperatura actual, con 12 ciudades de referencia.
3. **Radar**: precipitación de las últimas 2 horas, animada.

Y con **F4**, un popup al estilo de los diálogos de Norton Commander con todo lo
que hay sobre **granizo**: los avisos vigentes del SMN para tu ciudad (y cuántos hay
en el resto del país) y el riesgo para las próximas 24 h según el modelo ICON. Si hay
algo, la barra superior titila con `⚠ AVISO SMN` o `⚠ GRANIZO?`.

Las vistas de mapa "se reciben" como en una PC de los 90 conectada a una antena:
la imagen baja línea por línea, con estática, franjas de sincronismo a la izquierda,
cuña de grises de telemetría a la derecha y corrimiento Doppler en la frecuencia.
Es un homenaje al APT de los satélites NOAA: NOAA-15, el último que lo transmitía,
se apagó el 19/08/2025.

```
 ☼ CHUSMETEO v0.4         1 CLIMA  2 MAPA  3 RADAR        SÁB 24/10/2026 00:12:00
     ╔════════════════════════════════════════════════════════════════════╗
     ║ ▌▐ ▄▄███▄          ▓  TEMPERATURA ACTUAL                           ║
     ║ ▌▐ ███████▄ ▄█     ▓  ◄ RECIBIENDO NOAA-15                         ║
     ║ ▌▐ ████◘█████      ▓  137.6197 MHz ▮▮▮▮▮▮▮▯                        ║
     ║ ▌▐ ▓▒░▒▓░▒░▓▒      ▒                                               ║
     ║ ▌▐ ░ ▒  ░ ▒ ░      ░  SLA  31°  TUC  29°  RES  28° ...             ║
```
*(en la terminal es a color: correlo y mirá)*

## Arrancar

```bash
brew install go              # si no lo tenés (Go 1.24+)
go mod tidy                  # la primera vez
go run ./cmd/chusmeteo
go run ./cmd/chusmeteo -city "Mendoza" -view mapa
go run ./cmd/chusmeteo -view radar -nofx    # sin efecto de recepción
```

O con `make`: `make run`, `make test`, `make build`, `make build-all`.

| Tecla            | Acción                                              |
|------------------|-----------------------------------------------------|
| `F2` / `Tab`     | Cambiar de vista (`1` clima · `2` mapa · `3` radar)  |
| `F3` / `/`       | Buscar ciudad (`"Córdoba, ES"` filtra por país)     |
| `F4` / `!`       | Avisos del SMN y riesgo de granizo (popup)          |
| `F5` / `r`       | Refrescar ahora                                     |
| `a`              | Repetir la recepción del satélite                   |
| `Espacio`        | Radar: play / pausa                                 |
| `←` `→`          | Radar: cuadro anterior / siguiente                  |
| `Esc`            | Saltear la recepción · cerrar ayuda                 |
| `F1` / `?`       | Ayuda                                               |
| `F10` / `q`      | Salir                                               |

En la Terminal de macOS las F-keys pueden estar tomadas por el sistema: usá las alternativas.

**Para la estética completa:** terminal con truecolor (iTerm2, Ghostty, WezTerm, Kitty)
y una fuente VGA, por ejemplo *Px437 IBM VGA 8x16* de
[The Oldschool PC Font Resource](https://int10h.org/oldschool-pc-fonts/).
La paleta es de 16 colores VGA, así que en Terminal.app (256 colores) también se ve bien.

## Estructura

```
cmd/chusmeteo/main.go   flags y arranque de Bubble Tea
internal/
  api/      Open-Meteo (geocoding, pronóstico, grilla multi-coordenada), Rain Viewer
            (índice, tiles en paralelo, Web Mercator), códigos WMO
  geo/      países y provincias (Natural Earth, embebido con go:embed), ciudades, proyección
            data/sudamerica.json lo genera tools/nedata/extract.py
  render/   lienzo de medios bloques (▀), paletas, tramado Bayer, mapas, gráfico horario
  fx/       efecto de recepción APT: barrido, estática, sincronismo, Doppler
  ui/       modelo/Update (model.go), datos y comandos (data.go), vistas (view.go, mapview.go)
```

Cada paquete tiene sus tests. Los de `api` usan `httptest` (servidores HTTP falsos),
así se prueba el parseo sin red y sin gastar cupo. `go test -race ./...` también pasa.

Ver cualquier pantalla sin abrir la app (útil al tocar el layout):

```bash
make dump V=mapa             # clima | mapa | radar | rx (en plena recepción)
make dump V=radar SIZE=120x40
```

## Cómo funciona cada versión

**v0.2, mapa.** `geo.Grid(2.5)` genera ~45 puntos dentro del contorno (ray casting),
más 12 ciudades. Se piden en **una** llamada a Open-Meteo (`latitude=a,b,c&longitude=x,y,z`),
que devuelve un array JSON. Cada píxel se colorea por IDW (promedio pesado por 1/distancia²).
La máscara de tierra se cachea por tamaño de terminal. El mapa se pide al entrar a la vista
y vence a los 30 min: cada coordenada cuenta para el cupo diario de 10.000.

**v0.3, radar.** Se baja el índice `weather-maps.json` (13 frames, uno cada 10 min) y,
por cada frame, los 6 tiles de zoom 4 que cubren el país. Cada frame es un `tea.Cmd`
(corren en paralelo) y un channel con buffer hace de semáforo (máx. 6 descargas a la vez).
Los frames se cachean por `path`: al refrescar sólo se baja lo nuevo. Para cada píxel del
mapa se muestrean 3x3 puntos del tile (Web Mercator) y se queda el nivel más alto, así
no desaparecen las tormentas chicas.

**v0.4, recepción APT.** `fx.Apply` toma el mapa terminado y devuelve una copia
"a medio recibir": estática debajo de la línea, una línea brillante, ruido en las
recién llegadas y algunas líneas corridas un píxel. El ruido usa un hash determinístico
(splitmix64), así `View` sigue siendo una función pura del modelo. Los ticks llevan
una "generación" para descartar los de recepciones viejas (el mismo truco que el
autorefresco).

**v0.5, responsive y pixel art.** El tamaño de todo sale de la terminal (`contentW`,
`contentH`). La geografía viene de Natural Earth 1:10m: Argentina con Malvinas,
las 24 jurisdicciones y los países vecinos, recortados y simplificados a 123 KB y
metidos en el binario con `//go:embed`. Para cada tamaño de mapa se calcula una vez
un raster "país/provincia por píxel" y se cachea. Con escala suficiente (≥ 2,4 px por
grado) aparecen los límites provinciales y las etiquetas de ciudades. Los países
vecinos se dibujan con una trama al 25% y las bandas de temperatura pasan de una a
otra con tramado ordenado Bayer 4x4, como los gráficos de 16 colores de los 90.

**v0.6, granizo.** Dos fuentes, porque ninguna alcanza sola:

- *Avisos del SMN* (`api/smn.go`): el índice `ssl.smn.gob.ar/CAP/AR.php` es un RSS que
  apunta a un XML CAP 1.2 por aviso, con evento ("...CAIDA DE GRANIZO - AVISO NARANJA"),
  vencimiento y polígono. Se cachean por URL y sólo se bajan los nuevos (el SMN permite
  ~200 pedidos cada 10 min). Se manda un User-Agent propio: con uno de navegador el SMN
  devuelve HTML. Acepta XML en ISO-8859-1 (`CharsetReader`).
- *Modelo ICON* (`api/hail.go`): Open-Meteo con `models=icon_seamless`. ICON es de los
  pocos modelos que pronostican granizo explícito (códigos WMO 96/99). Además se piden
  CAPE e isoterma de 0 °C y se combinan en un riesgo NULO/BAJO/MODERADO/ALTO
  (`EstimateHailRisk`). Es una heurística propia, no un producto oficial.

En la vista de clima aparece una línea `GRANIZO` y, bajo el gráfico de 24 h, una franja con
el riesgo por hora. En el radar, el nivel más alto se llama "Intensa (posible granizo)":
el eco fuerte sugiere granizo pero no lo confirma.

## Go para alguien que viene de Java

| Java / Spring                 | Go (y dónde verlo acá)                                         |
|-------------------------------|----------------------------------------------------------------|
| `public` / `private`          | Mayúscula / minúscula en el nombre (`Geocode` vs `getJSON`)    |
| Clase + métodos               | `struct` + funciones con receiver: `func (l Location) Label()` |
| Excepciones                   | `(valor, error)` y `if err != nil` (`openmeteo.go`)            |
| `throw new X(cause)`          | `fmt.Errorf("contexto: %w", err)` + `errors.Is`                |
| `finally`                     | `defer res.Body.Close()`                                       |
| `@JsonProperty`               | struct tags: `` `json:"temperature_2m"` ``                     |
| `enum`                        | `type Sky int` + `const ( ... iota )` (`wmo.go`, `View`)       |
| `instanceof` / pattern match  | type switch: `switch msg := msg.(type)` (`model.go`)           |
| `ExecutorService` + `Future`  | goroutines + `sync.WaitGroup` (`rainviewer.go`)                |
| `Semaphore`                   | `make(chan struct{}, 6)` (`downloadSlots`)                     |
| `synchronized`                | `sync.Mutex` (`FetchRadarFrame`)                               |
| recurso en el classpath       | `//go:embed data/sudamerica.json` (`geo/argentina.go`)         |
| singleton lazy                | `sync.Once` (`geo.load`)                                       |
| genéricos `<T>`               | `func deref[T any](s []*T, i int) T` (`api/hail.go`)           |
| `Optional` / campos null      | punteros en el JSON: `[]*float64` (`api/hail.go`)              |
| `@MockBean` / WireMock        | `httptest.NewServer` + URLs en variables (`http_test.go`)      |
| JUnit parametrizado           | tests de tabla: slice de casos + `for` (`geo_test.go`)         |
| Maven / Gradle                | `go.mod`; `go build`, `go test ./...`, `go vet ./...`          |

Detalles de Go que aparecen en el código y conviene mirar:
- **Shadowing con `:=`** (`model.go`, caso `clockMsg`): dentro de un bloque nuevo, `:=` crea otra variable.
- **Slices comparten memoria** (`gridState.cities`): un sub-slice no copia.
- **Maps dentro de structs copiados por valor** (`radarState`): la copia comparte el map.
- **`json.RawMessage`** (`grid.go`): mirar el JSON antes de decidir cómo leerlo.
- **Import en blanco** `_ "image/png"`: registra el decoder PNG.

## Roadmap

- [x] **v0.1** condiciones actuales + pronóstico de 4 días, búsqueda de ciudad, autorefresco
- [x] **v0.2** mapa de Argentina en bloques `▀` con grilla de temperaturas
- [x] **v0.3** radar de Rain Viewer animado (últimas 2 h)
- [x] **v0.4** efecto de recepción APT
- [x] **v0.5** responsive, geografía real (provincias y vecinos), tramado, gráfico de 24 h
- [x] **v0.6** granizo: avisos del SMN (CAP) + riesgo según ICON, popup con F4
- [ ] Ideas: zoom a una región en terminales grandes, viento con flechas, pronóstico en el mapa
  (`hourly` en vez de `current`), alertas del SMN, modo "hardcore" con RTL-SDR + Meteor-M LRPT.

## Datos y atribución

- Geografía: [Natural Earth](https://www.naturalearthdata.com) (dominio público)
- Avisos: [Servicio Meteorológico Nacional](https://ssl.smn.gob.ar/CAP/AR.php) (CAP)
- Pronóstico y grilla: [Open-Meteo](https://open-meteo.com) (CC BY 4.0; gratis para uso no comercial, sin API key, ~10.000 llamadas/día)
- Radar: [Rain Viewer](https://www.rainviewer.com/api.html) (uso personal/educativo; atribución visible obligatoria; zoom máx. 7)
- Los umbrales de intensidad del radar se infieren del color (esquema 2, "Universal Blue") y son aproximados: ver `render.RadarLevel`.
