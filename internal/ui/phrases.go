package ui

import "math/rand/v2"

// phrase is a bilingual UI string (English / Spanish), so the playful splash and
// update lines are ready for the language toggle.
type phrase struct{ en, es string }

// pick returns the phrase in the given language ("es" → Spanish, else English).
func (p phrase) pick(lang string) string {
	if lang == "es" {
		return p.es
	}
	return p.en
}

// loadingPhrases are the tongue-in-cheek lines shown on the startup splash.
var loadingPhrases = []phrase{
	{"Pinging the cloud… hoping it pings back.", "Haciendo ping a la nube… a ver si contesta."},
	{"Asking the servers how they're feeling…", "Preguntándoles a los servidores cómo se sienten…"},
	{"Counting packets so you don't have to…", "Contando paquetes para que vos no tengas que hacerlo…"},
	{"Waking up the status pages…", "Despertando a las páginas de estado…"},
	{"Teaching the cloud to say 'I'm fine'…", "Enseñándole a la nube a decir 'estoy bien'…"},
	{"Checking if the internet is still there…", "Chequeando si internet sigue ahí…"},
	{"Negotiating with the API gods…", "Negociando con los dioses de la API…"},
	{"Hunting outages so you sleep better…", "Cazando caídas para que duermas tranquilo…"},
	{"Warming up the radar…", "Calentando el radar…"},
	{"Bribing the DNS for a quick answer…", "Sobornando al DNS para que conteste rápido…"},
	{"Tuning the heartbeat monitor…", "Afinando el monitor de latidos…"},
	{"Politely poking AWS…", "Pinchando a AWS con cariño…"},
	{"Reticulating splines…", "Reticulando splines…"},
	{"Convincing the servers to confess…", "Convenciendo a los servidores de que confiesen…"},
	{"Downloading more dashboard…", "Descargando más dashboard…"},
	{"Measuring the speed of light, roughly…", "Midiendo la velocidad de la luz, más o menos…"},
	{"Herding the status feeds…", "Arreando los feeds de estado…"},
	{"Sharpening the green checkmarks…", "Afilando los tildes verdes…"},
	{"Asking the cloud to keep it together…", "Pidiéndole a la nube que se mantenga entera…"},
	{"Almost ready to judge the uptime…", "Casi listo para juzgar el uptime…"},
}

// randomLoadingPhrase returns a random splash line in the given language.
func randomLoadingPhrase(lang string) string {
	return loadingPhrases[rand.IntN(len(loadingPhrases))].pick(lang)
}
