package main

import (
	"context"
	"encoding/json"
	"time"

	"quasarbridge/internal/metrics"
)

// Counters change on every request and are not news for the panel.
var volatileHealth = []string{"client_frames", "server_frames", "activity_dropped", "conversation_log_failures"}

// StateHeartbeat is how often an unchanged state is sent again. Missing three in a row
// tells the app the speaker is gone, and it then checks it itself.
const StateHeartbeat = 15 * time.Second

// watchState looks at the speaker every second and reports a change at once.
func watchState(ctx context.Context, health func(context.Context) map[string]any, report func(context.Context, map[string]any) error, changed <-chan struct{}) {
	var last string
	var sent time.Time
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		state := health(ctx)
		key := stateKey(state)
		if key != last || time.Since(sent) > StateHeartbeat {
			payload := map[string]any{"now_playing": state["now_playing"], "health": state, "system": metrics.Device.System()}
			if report(ctx, payload) == nil {
				last, sent = key, time.Now()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-changed:
		}
	}
}

func stateKey(state map[string]any) string {
	stable := make(map[string]any, len(state))
	for key, value := range state {
		stable[key] = value
	}
	for _, key := range volatileHealth {
		delete(stable, key)
	}
	data, _ := json.Marshal(stable)
	return string(data)
}
