package home

import (
	"context"
	"errors"
	"testing"
)

func TestMatchDeviceNeedsVerbAndKnownName(t *testing.T) {
	f := &Functions{}
	f.voice.snapshot.Store(&voicePhraseSnapshot{Names: []string{"компьютер", "свет на балконе"}})
	for text, want := range map[string]bool{
		"включи компьютер":                      true,
		"Алиса, выключи, пожалуйста, компьютер": true,
		"компьютер включи":                      true,
		"какой компьютер купить":                true,
		"расскажи про компьютер":                false,
		"включи кондиционер":                    false,
		"включи песню про компьютер":            false,
		"поставь таймер на компьютер":           false,
		"включи компьютерный":                   false,
	} {
		if got := f.MatchDevice(context.Background(), text); got != want {
			t.Errorf("%q: got %v", text, got)
		}
	}
}

func TestPolitenessDoesNotChangeVoicePhrase(t *testing.T) {
	if got := normalizeVoicePhrase("Алиса, включи, пожалуйста, свет!"); got != "включи свет" {
		t.Fatal(got)
	}
}

func TestPublishedRoutingAppliedOnceAndRejectionKept(t *testing.T) {
	calls := 0
	f := &Functions{OnRouting: func([]byte) error { calls++; return errors.New("rejected") }}
	f.applyRouting([]byte(`{"a":1}`))
	f.applyRouting([]byte(`{"a":1}`))
	f.applyRouting(nil)
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	f.applyRouting([]byte(`{"a":2}`))
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}
