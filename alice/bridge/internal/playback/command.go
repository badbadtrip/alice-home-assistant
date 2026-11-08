// Package playback recognizes direct controls for the current music queue.
package playback

import (
	"regexp"
	"strings"
	"sync/atomic"
	"unicode"
)

// Custom holds the owner's own words from the panel. They extend the built-in
// phrases and never replace them.
type Custom struct {
	Spotify  []string
	YouTube  []string
	Relay    []string
	Commands map[string][]string
	// Replace means the lists are the whole phrase base from the server: the built-in
	// patterns step aside, so a phrase switched off in the panel is off here too.
	Replace bool
}

var custom atomic.Pointer[Custom]

// SetCustom installs the owner's words, normalized the same way as speech.
func SetCustom(c Custom) {
	clean := func(list []string) []string {
		out := make([]string, 0, len(list))
		for _, item := range list {
			// "*" marks a word ending; normalize would drop it as punctuation.
			stem := strings.HasSuffix(item, "*")
			if item = normalize(strings.TrimSuffix(item, "*")); item != "" {
				if stem {
					item += "*"
				}
				out = append(out, item)
			}
		}
		return out
	}
	next := Custom{Spotify: clean(c.Spotify), YouTube: clean(c.YouTube), Relay: clean(c.Relay), Commands: map[string][]string{}, Replace: c.Replace}
	for action, list := range c.Commands {
		next.Commands[action] = clean(list)
	}
	custom.Store(&next)
}

var servicePrepositions = []string{"в", "на", "из", "через", "по"}

// wordsAt reports whether phrase matches text word by word from the start; a phrase word
// ending in "*" matches any ending.
func wordsAt(text, phrase []string) bool {
	if len(text) < len(phrase) {
		return false
	}
	for i, word := range phrase {
		if stem, ok := strings.CutSuffix(word, "*"); ok {
			if !strings.HasPrefix(text[i], stem) {
				return false
			}
		} else if text[i] != word {
			return false
		}
	}
	return true
}

func replacing() *Custom {
	if c := custom.Load(); c != nil && c.Replace {
		return c
	}
	return nil
}

var searchHead = regexp.MustCompile(`^(?:включи|поставь|запусти|сыграй|играй|найди)(?: мне)? `)

var commands = []struct {
	action  string
	pattern *regexp.Regexp
}{
	{"next", regexp.MustCompile(`^(?:(?:включи|поставь|переключи|перейди|переключись)(?: на)? )?(?:следующий(?: трек)?|следующую(?: (?:песню|композицию))?|следующая(?: (?:песня|композиция))?|дальше|вперед)$`)},
	{"previous", regexp.MustCompile(`^(?:(?:включи|поставь|верни|переключи|перейди|переключись)(?: на)? )?(?:предыдущий(?: трек)?|предыдущую(?: (?:песню|композицию))?|предыдущая(?: (?:песня|композиция))?|назад)$`)},
	{"pause", regexp.MustCompile(`^(?:пауза|(?:поставь|поставьте)(?: (?:музыку|песню|трек|воспроизведение))? на паузу|приостанови(?: (?:музыку|песню|трек|воспроизведение))?)$`)},
	{"resume", regexp.MustCompile(`^(?:(?:продолжи|продолжай|возобнови)(?: (?:музыку|песню|трек|воспроизведение))?|сними(?: (?:музыку|песню|трек))? с паузы)$`)},
	{"stop", regexp.MustCompile(`^(?:хватит|стоп|останови(?: (?:музыку|песню|трек|воспроизведение))?|выключи (?:музыку|песню|трек|радио))$`)},
	{"restart", regexp.MustCompile(`^(?:сначала|начни(?: (?:песню|трек|музыку))? сначала|включи(?: текущий)? (?:трек|песню) сначала|повтори (?:трек|песню))$`)},
}

var start = regexp.MustCompile(`^(?:включи|поставь|запусти)(?: мне| мою)? музыку$`)

var musicAsk = regexp.MustCompile(`^(?:включи|поставь|запусти|играй)(?: мне| мою| мой)? (?:музыку|плейлист|песню|трек|альбом|группу|исполнителя|артиста)(?: |$)`)

// MusicAsk recognizes any request to play music, named or not.
func MusicAsk(text string) bool { return musicAsk.MatchString(normalize(text)) }

// Start recognizes a plain request for music with nothing named.
func Start(text string) bool { return start.MatchString(normalize(text)) }

// Playlist is a Spotify source the user named in the panel.
type Playlist struct {
	Name string `json:"name"`
	URI  string `json:"uri"`
}

var (
	namedPlaylist = regexp.MustCompile(`^(?:включи|поставь|запусти)(?: мне| мой)? плейлист (.+)$`)
	playlistURI   = regexp.MustCompile(`^spotify:(?:playlist|album):[A-Za-z0-9]+$`)
)

// PickPlaylist finds the URI for a spoken request. Plain "включи музыку" takes the first entry.
func PickPlaylist(text string, playlists []Playlist) string {
	if len(playlists) == 0 {
		return ""
	}
	if Start(text) {
		if playlistURI.MatchString(playlists[0].URI) {
			return playlists[0].URI
		}
		return ""
	}
	match := namedPlaylist.FindStringSubmatch(normalize(text))
	if match == nil {
		return ""
	}
	for _, playlist := range playlists {
		if playlistName(playlist.Name) == playlistName(match[1]) && playlistURI.MatchString(playlist.URI) {
			return playlist.URI
		}
	}
	return ""
}

var (
	searchVerb  = `^(?:включи|поставь|запусти|сыграй|играй|найди)(?: мне)?`
	namedSearch = regexp.MustCompile(searchVerb + `(?: мою| мой)? (песню|трек|композицию|группу|исполнителя|артиста|альбом|плейлист) (.+)$`)
	likedSearch = regexp.MustCompile(searchVerb + ` (?:мои лайки|избранное|любимую музыку|мою любимую музыку|любимые (?:песни|треки)|понравившиеся(?: песни| треки)?)$`)
	bareSearch  = regexp.MustCompile(searchVerb + ` (.+)$`)
	searchKinds = map[string]string{
		"песню": "track", "трек": "track", "композицию": "track",
		"группу": "artist", "исполнителя": "artist", "артиста": "artist",
		"альбом": "album", "плейлист": "playlist",
	}
	serviceName = `(?:в|на|из|через|по) (?:(спотифа[йеяю]|спотик[еа]?|spotify)|(ют[ьу]+б[еау]?(?: музык[еау])?|youtube(?: music)?))`
	serviceTail = regexp.MustCompile(` ` + serviceName + `$`)
	serviceHead = regexp.MustCompile(searchVerb + ` ` + serviceName + ` `)
)

// service finds a spoken service at either end and returns the phrase without it.
func service(text string) (string, string) {
	provider := func(match []string) string {
		if match[1] != "" {
			return "spotify"
		}
		return "youtube"
	}
	if replacing() == nil {
		if match := serviceTail.FindStringSubmatch(text); match != nil {
			return serviceTail.ReplaceAllString(text, ""), provider(match)
		}
		if match := serviceHead.FindStringSubmatch(text); match != nil {
			return strings.Fields(text)[0] + " " + strings.TrimPrefix(text, match[0]), provider(match)
		}
	}
	if c := custom.Load(); c != nil {
		words := strings.Fields(text)
		head := len(strings.Fields(searchHead.FindString(text)))
		for _, named := range []struct {
			provider string
			names    []string
		}{{"spotify", c.Spotify}, {"youtube", c.YouTube}} {
			for _, name := range named.names {
				for _, preposition := range servicePrepositions {
					said := append([]string{preposition}, strings.Fields(name)...)
					if len(words) > len(said) && wordsAt(words[len(words)-len(said):], said) {
						return strings.Join(words[:len(words)-len(said)], " "), named.provider
					}
					if head > 0 && len(words) > head+len(said) && wordsAt(words[head:], said) {
						return words[0] + " " + strings.Join(words[head+len(said):], " "), named.provider
					}
				}
			}
		}
	}
	return text, ""
}

// Search reads a request to find music. A bare "включи кино" may name a lamp or a
// Yandex skill, so it counts only when a service was said out loud. The provider is
// empty unless named.
func Search(text string) (kind, query, provider string, ok bool) {
	text, provider = service(normalize(text))
	if likedSearch.MatchString(text) {
		return "liked", "", provider, true
	}
	if match := namedSearch.FindStringSubmatch(text); match != nil {
		return searchKinds[match[1]], match[2], provider, true
	}
	if match := bareSearch.FindStringSubmatch(text); provider != "" && match != nil {
		return "any", match[1], provider, true
	}
	return "", "", "", false
}

var relayAsk = regexp.MustCompile(`^(?:скажи|передай)(?: на| в| во) ([^ ]+) (.+)$`)

// Relay splits "скажи на кухню ужин готов" into the spoken room and the message.
// The message stays word for word: "что покушать" loses its meaning without "что".
func Relay(text string) (room, message string, ok bool) {
	text = normalize(text)
	if replacing() == nil {
		if match := relayAsk.FindStringSubmatch(text); match != nil {
			return match[1], match[2], true
		}
	}
	if c := custom.Load(); c != nil {
		words := strings.Fields(text)
		for _, verb := range c.Relay {
			said := strings.Fields(verb)
			if len(words) > len(said)+1 && wordsAt(words, said) {
				return words[len(said)], strings.Join(words[len(said)+1:], " "), true
			}
		}
	}
	return "", "", false
}

// RoomStem drops the case ending, the same way the server does.
func RoomStem(room string) string {
	return strings.TrimRight(room, "аеиоуыюя")
}

var spokenDigits = strings.NewReplacer("один", "1", "первый", "1", "два", "2", "второй", "2", "три", "3", "третий", "3", "четыре", "4", "пять", "5")

// playlistName compares what was said with a panel name: "плейлист номер 1" equals "номер один".
func playlistName(name string) string {
	return spokenDigits.Replace(strings.TrimPrefix(normalize(name), "плейлист "))
}

func Command(text string) string {
	text = normalize(text)
	full := replacing()
	for _, c := range commands {
		// With the server's base only "restart" stays built in: it has no list of its own.
		if (full == nil || c.action == "restart") && c.pattern.MatchString(text) {
			return c.action
		}
	}
	if c := custom.Load(); c != nil {
		words := strings.Fields(text)
		for action, phrases := range c.Commands {
			for _, phrase := range phrases {
				if said := strings.Fields(phrase); len(said) == len(words) && wordsAt(words, said) {
					return action
				}
			}
		}
	}
	return ""
}

func normalize(text string) string {
	text = strings.ToLower(strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) {
			return ' '
		}
		return r
	}, text))
	text = strings.ReplaceAll(text, "ё", "е")
	words := strings.Fields(text)
	if len(words) > 0 && words[0] == "алиса" {
		words = words[1:]
	}
	if len(words) > 0 && words[0] == "пожалуйста" {
		words = words[1:]
	}
	if len(words) > 0 && words[len(words)-1] == "пожалуйста" {
		words = words[:len(words)-1]
	}
	return strings.Join(words, " ")
}
