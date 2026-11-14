// Modified for Alice Gemini Bridge: native acknowledgement, persona and microphone control fixes.
package main

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"quasarbridge/internal/assistant"
	"quasarbridge/internal/bridge"
	"quasarbridge/internal/dialogue"
	"quasarbridge/internal/home"
	"quasarbridge/internal/indicator"
	"quasarbridge/internal/journal"
	"quasarbridge/internal/llm"
	"quasarbridge/internal/localstate"
	"quasarbridge/internal/metrics"
	"quasarbridge/internal/nowplaying"
	"quasarbridge/internal/sendspin"

	airplay "github.com/skrashevich/station-airplay"
)

func main() {
	path := flag.String("config", "config.json", "configuration file")
	query := flag.String("query", "", "run one text query for backend diagnostics")
	check := flag.Bool("check-backends", false, "check LLM and configured Home Assistant connectivity")
	checkMCP := flag.Bool("check-mcp", false, "connect to configured MCP servers and list tools without executing them")
	route := flag.String("route", "", "explain a route without executing home actions or generating an answer")
	validate := flag.Bool("check-config", false, "validate configuration without contacting backends")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)
	cfg, err := readConfig(*path)
	if err != nil {
		slog.Error("configuration rejected", "error", err)
		os.Exit(1)
	}

	diagnostics := 0
	if *query != "" {
		diagnostics++
	}
	if *route != "" {
		diagnostics++
	}
	if *check {
		diagnostics++
	}
	if *checkMCP {
		diagnostics++
	}
	if *validate {
		diagnostics++
	}
	if diagnostics > 1 {
		slog.Error("diagnostic flags are mutually exclusive")
		os.Exit(2)
	}
	if *validate {
		fmt.Println("configuration is valid")
		return
	}
	if *checkMCP {
		ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
		defer cancel()
		status, err := assistant.CheckMCP(ctx, cfg.AssistantTools.MCPServers)
		data, _ := json.Marshal(status)
		fmt.Println(string(data))
		if err != nil {
			slog.Error("MCP check failed", "error", err)
			os.Exit(1)
		}
		return
	}
	var clientCount, serverCount atomic.Int64
	// Every frame used to be written to the log; on the speaker's flash that is constant
	// writing. Only the counters stay; QUASAR_DEBUG_FRAMES=1 brings the frame log back.
	debugFrames := os.Getenv("QUASAR_DEBUG_FRAMES") == "1"
	proxy := &bridge.Proxy{Upstream: cfg.Upstream, Observer: frameObserver(&clientCount, &serverCount, debugFrames)}
	logs := new(journal.Writer)
	lc := &llm.Client{
		BaseURL: cfg.LLMBaseURL, Model: cfg.LLMModel, APIKey: os.Getenv("QUASAR_LLM_API_KEY"),
		API: cfg.LLMAPI, ReasoningEffort: cfg.LLMReasoningEffort,
		MaxOutputTokens: cfg.LLMMaxOutputTokens, HostedTools: cfg.LLMHostedTools,
		ChatParameters: cfg.LLMChatParameters,
	}
	var planner home.Planner = lc
	var ring *indicator.Controller
	if diagnostics == 0 {
		ring = indicator.New(*path)
		go ring.Serve(context.Background())
		lc.OnRequest = ring.Request
	}
	llmDisabled := os.Getenv("QUASAR_LLM_DISABLED") == "1"
	if llmDisabled {
		planner = noLLMPlanner{}
	}
	engine := &bridge.Engine{LLM: planner, Dialogue: dialogue.New(cfg.DialogueContext), SystemPrompt: cfg.SystemPrompt}
	var hf *home.Functions
	if endpoint := os.Getenv("QUASAR_HOME_FUNCTIONS_URL"); endpoint != "" {
		hf = &home.Functions{SpeakerID: os.Getenv("QUASAR_HOME_SPEAKER_ID"), Area: os.Getenv("QUASAR_HOME_AREA"), URL: endpoint, Token: os.Getenv("QUASAR_HOME_FUNCTIONS_TOKEN"), Planner: planner}
		hf.OnRouting = func(raw []byte) error {
			next := bridge.DefaultRoutingConfig()
			if err := json.Unmarshal(raw, &next, json.RejectUnknownMembers(true)); err != nil {
				return errors.New("invalid routing JSON")
			}
			return engine.SetRouting(withoutModelFallback(next, llmDisabled))
		}
		hf.OnReplies = bridge.SetReplies
		engine.Home = hf
	}
	if cfg.AssistantTools.Enabled {
		source := func() journal.Config {
			c := logs.Config()
			if c.Path == "" {
				return cfg.ConversationLog
			}
			return c
		}
		reader := assistant.NewWebReader(cfg.AssistantTools)
		tools := &assistant.Tools{Config: cfg.AssistantTools, JournalConfig: source, WebReader: reader}
		engine.Assistant = &assistant.Agent{Client: lc, Tools: tools, MaxRounds: cfg.AssistantTools.MaxRounds, MaxCalls: cfg.AssistantTools.MaxCalls}
	}
	engine.Silencer = proxy
	var playing nowplaying.Tracker
	if err := engine.SetRouting(withoutModelFallback(cfg.Routing, llmDisabled)); err != nil {
		slog.Error("routing rejected", "error", err)
		os.Exit(1)
	}
	if *query != "" || *check || *route != "" {
		runOneShot(lc, hf, engine, llmDisabled, *query, *route, *check)
		return
	}
	if err := logs.Configure(cfg.ConversationLog); err != nil {
		slog.Error("conversation log rejected", "error", err)
		os.Exit(1)
	}
	defer logs.Close()
	proxy.Journal = logs
	var cues *bridge.VoiceCues
	if cfg.VoiceCues.Enabled {
		cues, err = bridge.OpenVoiceCues(cfg.VoiceCues.CacheDir)
		if err != nil {
			slog.Error("native cue cache rejected")
			os.Exit(1)
		}
	}
	if cfg.Mode == "hybrid" {
		proxy.NewSession = func(ctx context.Context, up, down bridge.Sender) bridge.Session {
			n := bridge.NewNative(ctx, up, down, engine)
			n.VoiceCues = cues

			n.GateText = false
			n.ShouldListen = cfg.ListenAfterReply
			if cfg.LocalProgress.Enabled {
				n.ProgressAfter = time.Duration(cfg.LocalProgress.AfterMS) * time.Millisecond
				n.ProgressInterval = time.Duration(cfg.LocalProgress.IntervalMS) * time.Millisecond
			}

			return n
		}
	}
	var airPlay *airPlayControl
	var musicAssistant *sendspin.Player
	playerSources := func() []nowplaying.Source {
		var sources []nowplaying.Source
		if musicAssistant != nil {
			sources = append(sources, sendspinNow(musicAssistant))
		}
		if airPlay != nil {
			sources = append(sources, airPlay.now)
		}
		return sources
	}
	// One look at the players a second serves the state report, the ring and /metrics.
	var trackMu sync.Mutex
	var trackAt time.Time
	var track *nowplaying.Info
	currentTrack := func(ctx context.Context) *nowplaying.Info {
		trackMu.Lock()
		defer trackMu.Unlock()
		if time.Since(trackAt) > 900*time.Millisecond {
			track, trackAt = playing.Pick(ctx, playerSources()...), time.Now()
		}
		return track
	}
	if ring != nil {
		ring.AddMusic(func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			info := currentTrack(ctx)
			return info != nil && info.Status == nowplaying.Playing
		})
	}
	mux := http.NewServeMux()
	mcpConfigured := 0
	if cfg.Mode == "hybrid" && cfg.AssistantTools.Enabled {
		for _, server := range cfg.AssistantTools.MCPServers {
			if !server.Disabled {
				mcpConfigured++
			}
		}
	}
	// The speaker's own volume and Alice's state, read on the speaker without the cloud.
	var volume atomic.Pointer[localstate.Volume]
	var voice atomic.Value
	voice.Store("idle")
	stateChanged := make(chan struct{}, 1)
	changed := func() {
		select {
		case stateChanged <- struct{}{}:
		default:
		}
	}
	healthState := func(ctx context.Context) map[string]any {
		state := engine.RoutingStatus()
		return map[string]any{"status": "ok", "mode": cfg.Mode, "client_frames": clientCount.Load(), "server_frames": serverCount.Load(), "routing_revision": state.Revision, "routing_rules": state.ActiveRules, "conversation_log_enabled": logs.Enabled(), "conversation_log_failures": logs.Failures(), "assistant_tools_enabled": cfg.AssistantTools.Enabled, "dialogue_context_enabled": cfg.DialogueContext.Enabled && cfg.Mode == "hybrid", "home_assistant_enabled": engine.Home != nil && cfg.Mode == "hybrid", "mcp_servers_configured": mcpConfigured, "speaker_id": os.Getenv("QUASAR_HOME_SPEAKER_ID"), "speaker_area": os.Getenv("QUASAR_HOME_AREA"), "activity_dropped": func() uint64 {
			if hf != nil {
				return hf.ActivityDropped()
			}
			return 0
		}(), "voice_phrase_revision": func() string {
			if hf != nil {
				return hf.VoicePhraseRevision()
			}
			return ""
		}(), "voice_phrase_count": func() int {
			if hf != nil {
				return hf.VoicePhraseCount()
			}
			return 0
		}(), "volume_push": true, "music_assistant": func() map[string]any {
			if musicAssistant == nil {
				return nil
			}
			return map[string]any{"linked": musicAssistant.Linked(), "streaming": musicAssistant.Streaming()}
		}(), "airplay": func() map[string]any {
			if airPlay != nil {
				return airPlay.health()
			}
			return nil
		}(), "now_playing": currentTrack(ctx), "volume": volume.Load(), "alice_state": voice.Load()}
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		data, _ := json.Marshal(healthState(r.Context()))
		_, _ = w.Write(data)
	})
	mux.HandleFunc("GET /volume", proxy.VolumeHandler)
	// The ring's last changes of look with the reason, to find what makes it blink.
	mux.HandleFunc("GET /ring", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var items []indicator.Transition
		if ring != nil {
			items = ring.Transitions()
		}
		data, _ := json.Marshal(items)
		_, _ = w.Write(data)
	})
	// Where the last requests spent their time; no text, only the route and milliseconds.
	mux.HandleFunc("GET /timeline", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		data, _ := json.Marshal(bridge.RecentTimelines())
		_, _ = w.Write(data)
	})
	mux.HandleFunc("GET /player", func(w http.ResponseWriter, r *http.Request) {
		if err := engine.PlayerControl(r.Context(), r.URL.Query().Get("action")); err != nil {
			http.Error(w, "player unavailable", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /music-state", func(w http.ResponseWriter, r *http.Request) {
		if ring != nil {
			ring.SetMusic(r.URL.Query().Get("playing") == "1")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/uni.ws", proxy)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(cfg.AllowedIPs) > 0 {
			ip, _, _ := net.SplitHostPort(r.RemoteAddr)
			allowed := false
			for _, candidate := range cfg.AllowedIPs {
				if ip == candidate {
					allowed = true
					break
				}
			}
			if !allowed {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
	server := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// One volume for the phone's AirPlay slider and the speaker (part 10.10).
	var airPlayRef atomic.Pointer[airPlayControl]
	speakerVolume := &unifiedVolume{
		set: proxy.SetVolume,
		level: func() (float64, bool) {
			if v := volume.Load(); v != nil {
				return v.Level, true
			}
			return 0, false
		},
		report: func(ctx context.Context, db float64) error {
			if c := airPlayRef.Load(); c != nil {
				return c.ReportVolume(ctx, db)
			}
			return nil
		},
		busy: func() bool {
			state, _ := voice.Load().(string)
			return state != "" && state != "idle"
		},
	}
	go localstate.WatchVolume(ctx, func(v localstate.Volume) {
		first := volume.Swap(&v) == nil
		if ring != nil && !first {
			ring.VolumeChanged()
		}
		if !first {
			speakerVolume.changed(v.Level)
		}
		changed()
	})
	// Built before the watchers start so that they only ever read a settled pointer.
	if hf != nil && hf.URL != "" {
		// QUASAR_SENDSPIN_URL points a development bridge at a separately run service.
		musicAssistant = &sendspin.Player{URL: cmp.Or(os.Getenv("QUASAR_SENDSPIN_URL"), hf.URL), Token: hf.Token, Speaker: hf.SpeakerID}
		musicAssistant.OnChange = func() {
			if ring != nil {
				ring.SetMusic(musicAssistant.Streaming())
			}
			changed()
		}
	}
	go localstate.WatchAlice(ctx, localstate.AliceSocket, func(state string) {
		voice.Store(state)
		if musicAssistant != nil {
			musicAssistant.SetAliceState(state)
		}
		if airPlay != nil {
			airPlay.SetAliceState(state)
		}
		if ring != nil {
			ring.SetVoice(state)
		}
		if state == "idle" {
			speakerVolume.aliceIdle()
		}
		changed()
	})
	if control, err := newAirPlayControl(ctx, os.Getenv("QUASAR_DATA_DIR"), os.Getenv("QUASAR_HOME_AREA"), airplay.VolumeHooks{Sender: speakerVolume.fromSender, Speaker: speakerVolume.speaker}); err != nil {
		slog.Warn("airplay_unavailable", "error", err)
	} else {
		airPlay = control
		airPlayRef.Store(control)
		engine.AirPlay = control
		mux.Handle("GET /airplay", control)
		defer control.shutdown()
		go control.resume()
		go control.followAddress(30*time.Second, routeIP)
	}
	// Music Assistant reaches the speaker through the app, so it needs the app link and the audio helper.
	if musicAssistant != nil {
		if helper, err := newPCMHelper(airPlay, os.Getenv("QUASAR_DATA_DIR")); err != nil {
			slog.Warn("sendspin_unavailable", "error", err)
		} else {
			musicAssistant.Open = helper.Open
			engine.MusicAssistant = musicAssistant
			if airPlay != nil {
				go func() {
					var one oneSound
					ticker := time.NewTicker(time.Second)
					defer ticker.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
							one.check(ctx, airPlay, musicAssistant)
						}
					}
				}()
			}
			go musicAssistant.Run(ctx)
		}
	}
	started := time.Now()
	snapshot := func(ctx context.Context) metrics.Snapshot {
		players := map[string]nowplaying.Source{}
		if musicAssistant != nil {
			players["music_assistant"] = sendspinNow(musicAssistant)
		}
		if airPlay != nil {
			players["airplay"] = airPlay.now
		}
		return metrics.Snapshot{Status: "ok", SpeakerID: os.Getenv("QUASAR_HOME_SPEAKER_ID"), BridgeUptimeS: int64(time.Since(started).Seconds()),
			System: metrics.Device.System(), NowPlaying: currentTrack(ctx), Players: metrics.Players(ctx, players),
			Volume: volume.Load(), AliceState: voice.Load().(string)}
	}
	mux.Handle("GET /metrics", metrics.Handler("", snapshot))
	if token := os.Getenv("QUASAR_HOME_FUNCTIONS_TOKEN"); token != "" {
		go serveLAN(ctx, token, mux)
	}
	if hf != nil {
		// The speaker reports itself on every change, so the app does not poll it.
		go watchState(ctx, healthState, hf.ReportState, stateChanged)
		hf.StartVoiceCache(ctx)
		hf.StartActivity(ctx)
		engine.Activity = hf.RecordActivity
	}
	reloads := make(chan os.Signal, 1)
	signal.Notify(reloads, syscall.SIGHUP)
	defer signal.Stop(reloads)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-reloads:
				if err := reloadRouting(*path, cfg, engine, logs); err != nil {
					slog.Warn("routing_reload_rejected", "error", err)
					continue
				}
				state := engine.RoutingStatus()
				slog.Info("routing_reloaded", "revision", state.Revision, "active_rules", state.ActiveRules)
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	slog.Info("listening", "address", cfg.Listen, "mode", cfg.Mode, "routing_revision", engine.RoutingStatus().Revision)
	if cfg.TLSCert != "" && cfg.TLSKey != "" {
		err = server.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
	} else {
		err = server.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("listener failed")
		os.Exit(1)
	}
}

type noLLMPlanner struct{}

func (noLLMPlanner) Complete(context.Context, []llm.Message, bool) (string, error) {
	return "", llm.ErrDisabled
}
