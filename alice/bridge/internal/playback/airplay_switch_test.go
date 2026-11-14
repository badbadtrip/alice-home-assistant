package playback

import "testing"

func TestAirPlaySwitch(t *testing.T) {
	cases := map[string][2]bool{
		"Алиса, включи эйрплей":          {true, true},
		"выключи AirPlay":                {false, true},
		"ну отключи эйр плей пожалуйста": {false, true},
		"включи аирплей на колонке":      {true, true},
		"включи музыку":                  {false, false},
	}
	for text, want := range cases {
		on, ok := AirPlaySwitch(text)
		if on != want[0] || ok != want[1] {
			t.Errorf("%q: on=%v ok=%v, want %v", text, on, ok, want)
		}
	}
}
