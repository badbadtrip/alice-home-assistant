package indicator

import (
	"log/slog"
	"sync"
	"time"
)

// Transition is one change of what the ring shows and why. "card" and "player" tell
// what the music check saw at that moment: the sound card and the bridge's players.
type Transition struct {
	At     string `json:"at"`
	From   string `json:"from"`
	To     string `json:"to"`
	HeldMS int64  `json:"held_ms"` // how long the previous look lasted
	Card   bool   `json:"card"`
	Player bool   `json:"player"`
}

const journalKeep = 50

// ringJournal remembers changes of look; a blink shows as a short-held look.
type ringJournal struct {
	mu      sync.Mutex
	current string
	since   time.Time
	items   []Transition
	logged  []time.Time
}

func (j *ringJournal) note(now time.Time, state string, card, player bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if state == j.current {
		return
	}
	t := Transition{At: now.UTC().Format(time.RFC3339Nano), From: j.current, To: state, Card: card, Player: player}
	if !j.since.IsZero() {
		t.HeldMS = now.Sub(j.since).Milliseconds()
	}
	j.current, j.since = state, now
	j.items = append(j.items, t)
	if len(j.items) > journalKeep {
		j.items = j.items[len(j.items)-journalKeep:]
	}
	// At most 30 lines a minute: a fast flicker must not flood the speaker's log.
	for len(j.logged) > 0 && now.Sub(j.logged[0]) > time.Minute {
		j.logged = j.logged[1:]
	}
	if len(j.logged) < 30 {
		j.logged = append(j.logged, now)
		slog.Info("ring_look", "from", t.From, "to", t.To, "held_ms", t.HeldMS, "card", card, "player", player)
	}
}

func (j *ringJournal) list() []Transition {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Transition(nil), j.items...)
}

// lookName names the look pick chose, for the journal.
func lookName(cfg Config, thinking, playing, ok bool) string {
	switch {
	case !ok:
		return "off"
	case thinking && cfg.Enabled && cfg.valid():
		return "thinking"
	case playing && cfg.Music != nil && cfg.Music.Enabled:
		return "music"
	default:
		return "lamp"
	}
}
