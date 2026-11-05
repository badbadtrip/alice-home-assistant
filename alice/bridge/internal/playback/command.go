// Package playback recognizes direct controls for the current music queue.
package playback

import (
	"regexp"
	"strings"
	"unicode"
)

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

var spokenDigits = strings.NewReplacer("один", "1", "первый", "1", "два", "2", "второй", "2", "три", "3", "третий", "3", "четыре", "4", "пять", "5")

// playlistName compares what was said with a panel name: "плейлист номер 1" equals "номер один".
func playlistName(name string) string {
	return spokenDigits.Replace(strings.TrimPrefix(normalize(name), "плейлист "))
}

func Command(text string) string {
	text = normalize(text)
	for _, c := range commands {
		if c.pattern.MatchString(text) {
			return c.action
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
