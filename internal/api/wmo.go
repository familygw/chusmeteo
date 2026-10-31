package api

// Sky agrupa los ~30 códigos WMO en unos pocos "tipos de cielo" para dibujar.
// Go no tiene enums: se usa un tipo entero + constantes con iota (0, 1, 2...).
type Sky int

const (
	SkyClear Sky = iota
	SkyPartly
	SkyCloudy
	SkyFog
	SkyDrizzle
	SkyRain
	SkySnow
	SkyStorm
)

// Describe traduce un código WMO (el "weather_code" de Open-Meteo)
// a texto en castellano y a un tipo de cielo.
// Tabla oficial: https://open-meteo.com/en/docs (sección WMO Weather interpretation codes)
func Describe(code int) (string, Sky) {
	switch code {
	case 0:
		return "Despejado", SkyClear
	case 1:
		return "Mayormente despejado", SkyClear
	case 2:
		return "Parcialmente nublado", SkyPartly
	case 3:
		return "Nublado", SkyCloudy
	case 45, 48:
		return "Niebla", SkyFog
	case 51, 53, 55:
		return "Llovizna", SkyDrizzle
	case 56, 57:
		return "Llovizna helada", SkyDrizzle
	case 61:
		return "Lluvia leve", SkyRain
	case 63:
		return "Lluvia", SkyRain
	case 65:
		return "Lluvia fuerte", SkyRain
	case 66, 67:
		return "Lluvia helada", SkyRain
	case 71, 73, 75, 77:
		return "Nieve", SkySnow
	case 80, 81:
		return "Chaparrones", SkyRain
	case 82:
		return "Chaparrones fuertes", SkyRain
	case 85, 86:
		return "Chaparrones de nieve", SkySnow
	case 95:
		return "Tormenta", SkyStorm
	case 96, 99:
		return "Tormenta con granizo", SkyStorm
	default:
		return "Desconocido", SkyCloudy
	}
}

// Arrow dibuja hacia dónde VA el viento (deg es de dónde sopla, como en Compass).
func Arrow(deg float64) string {
	arrows := [...]string{"↑", "↗", "→", "↘", "↓", "↙", "←", "↖"}
	i := int((deg+180+22.5)/45) % 8
	if i < 0 {
		i += 8
	}
	return arrows[i]
}

// Compass convierte grados (0-360) en un punto cardinal en castellano.
func Compass(deg float64) string {
	dirs := [...]string{"N", "NE", "E", "SE", "S", "SO", "O", "NO"} // array de tamaño fijo
	// Cada sector mide 45°; sumamos 22.5 para centrar y usamos módulo 8.
	i := int((deg+22.5)/45) % 8
	if i < 0 {
		i += 8
	}
	return dirs[i]
}
