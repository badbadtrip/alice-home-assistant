package aliceipc

import (
	"strings"
	"unicode"
)

// command accepts whole utterances only. Stop intentionally pauses in this
// prototype so that a subsequent "продолжай" can retain the same AirPlay sender.
func command(text string) (requested, action string) {
	text = strings.ToLower(strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) {
			return ' '
		}
		return r
	}, text))
	text = strings.ReplaceAll(text, "ё", "е")
	text = strings.Join(strings.Fields(text), " ")
	text = strings.TrimPrefix(text, "алиса ")
	text = strings.TrimPrefix(text, "пожалуйста ")
	text = strings.TrimSuffix(text, " пожалуйста")
	switch text {
	case "пауза", "на паузу", "поставь на паузу", "поставь музыку на паузу", "поставь песню на паузу", "поставь трек на паузу", "приостанови", "приостанови музыку", "приостанови воспроизведение":
		return "pause", "pause"
	case "продолжи", "продолжай", "возобнови", "продолжи музыку", "продолжай музыку", "возобнови музыку", "продолжи воспроизведение", "продолжай воспроизведение", "сними с паузы", "сними музыку с паузы":
		return "resume", "resume"
	case "стоп", "хватит", "останови", "останови музыку", "останови песню", "останови трек", "останови воспроизведение", "выключи музыку", "выключи песню", "выключи трек", "выключи радио", "харэ", "стопэ", "завали":
		return "stop", "pause"
	default:
		return "", ""
	}
}
