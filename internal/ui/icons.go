package ui

import "chusmeteo/internal/api"

// Arte ASCII de 5 líneas x 13 columnas, al estilo wttr.in.
// Son raw strings (entre backticks): no procesan escapes, ideales para ASCII art.
// Ojo: dentro de un raw string no se puede usar el backtick.
var art = map[api.Sky]string{
	api.SkyClear: `
    \   /
     .-.
  - (   ) -
     '-'
    /   \    `,
	api.SkyPartly: `
   \  /
 _ /"".-.
   \_(   ).
   /(___(__)
             `,
	api.SkyCloudy: `

     .--.
  .-(    ).
 (___.__)__)
             `,
	api.SkyFog: `

 _ - _ - _ -
  _ - _ - _
 _ - _ - _ -
             `,
	api.SkyDrizzle: `
     .-.
    (   ).
   (___(__)
    ' ' ' '
             `,
	api.SkyRain: `
     .-.
    (   ).
   (___(__)
   ,',',','
   ,',',','  `,
	api.SkySnow: `
     .-.
    (   ).
   (___(__)
    *  *  *
   *  *  *   `,
	api.SkyStorm: `
     .-.
    (   ).
   (___(__)
    ,/_,/_
     /  /    `,
}

const moonArt = `
     _..._
   .'   .-'
  /    /
  \    '-.
   '-...-'   `

// smallIcon es un glifo de 1 carácter para las cajitas de pronóstico.
// Casi todos existen en la página de códigos 437 de la IBM PC.
func smallIcon(s api.Sky) string {
	switch s {
	case api.SkyClear:
		return "☼"
	case api.SkyPartly:
		return "☼☁"
	case api.SkyCloudy:
		return "☁"
	case api.SkyFog:
		return "≡"
	case api.SkyDrizzle, api.SkyRain:
		return "☂"
	case api.SkySnow:
		return "*"
	case api.SkyStorm:
		return "‼"
	}
	return "?"
}

// bigIcon devuelve el arte grande; de noche y despejado, luna.
func bigIcon(s api.Sky, isDay bool) string {
	if s == api.SkyClear && !isDay {
		return moonArt[1:] // [1:] saca el salto de línea inicial del raw string
	}
	return art[s][1:]
}
