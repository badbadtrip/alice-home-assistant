package playback

import "testing"

func TestVoicePlaybackCommands(t *testing.T) {
	for _, tc := range []struct{ text, action string }{
		{"Следующий трек", "next"}, {"Алиса, включи следующую песню!", "next"}, {"Переключи на следующий трек", "next"}, {"Дальше", "next"},
		{"Следующая песня", "next"}, {"Вперёд", "next"}, {"Предыдущая песня", "previous"}, {"Назад", "previous"}, {"Сначала", "restart"},
		{"Предыдущий трек", "previous"}, {"Включи предыдущую песню", "previous"}, {"Верни предыдущий трек", "previous"},
		{"Поставь музыку на паузу", "pause"}, {"Пауза", "pause"}, {"Приостанови воспроизведение", "pause"},
		{"Продолжи", "resume"}, {"Продолжай музыку", "resume"}, {"Сними с паузы", "resume"}, {"Возобнови воспроизведение", "resume"},
		{"Хватит", "stop"}, {"Стоп", "stop"}, {"Останови музыку", "stop"}, {"Выключи музыку", "stop"},
		{"Начни песню сначала", "restart"}, {"Включи текущий трек сначала", "restart"},
	} {
		if got := Command(tc.text); got != tc.action {
			t.Errorf("%q: got %q want %q", tc.text, got, tc.action)
		}
	}
	for _, text := range []string{"Не переключай трек", "Как включить следующий трек", "Расскажи про предыдущую песню", "Продолжи объяснять код", "Поставь таймер на паузу", "Включи музыку с сайта audio.example/music", "Следующий вопрос", "Назад в историю", "Хватит ли памяти", "Включи свет", "Громче", "Не продолжай музыку"} {
		if got := Command(text); got != "" {
			t.Errorf("false action %q: %s", text, got)
		}
	}
}

func TestMusicAsk(t *testing.T) {
	for text, want := range map[string]bool{"Включи группу Кино": true, "Алиса, поставь песню": true, "Включи плейлист бег": true, "Включи свет": false, "Включи следующий трек": false} {
		if MusicAsk(text) != want {
			t.Errorf("%q", text)
		}
	}
}

func TestCommandsAsPeopleSayThem(t *testing.T) {
	for _, tc := range []struct{ text, action string }{
		{"Ну, Алиса, пауза", "pause"}, {"Слушай, дальше", "next"}, {"А продолжи", "resume"}, {"Окей, назад", "previous"},
		{"Пауза, плиз", "pause"}, {"Алиса, дальше пожалуйста", "next"}, {"Ладно, хватит", "stop"}, {"Эй, стоп сейчас", "stop"},
		{"Харэ", "stop"}, {"Стопэ", "stop"}, {"Играй", "resume"}, {"Переключи трек", "next"}, {"Переключи песню", "next"},
	} {
		if got := Command(tc.text); got != tc.action {
			t.Errorf("%q: got %q want %q", tc.text, got, tc.action)
		}
	}
	// Fillers alone or around something else are not commands.
	for _, text := range []string{"Ну", "Слушай", "Ну назад в будущее", "Стоп-кран", "Играй в шахматы", "Так держать", "Окей гугл"} {
		if got := Command(text); got != "" {
			t.Errorf("false action %q: %s", text, got)
		}
	}
	if got := normalize("ну"); got != "ну" {
		t.Errorf("a lone filler stays: %q", got)
	}
}
