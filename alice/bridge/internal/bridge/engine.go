// Modified for Alice Gemini Bridge: native acknowledgement, persona and microphone control fixes.
package bridge

import (
	"context"
	"encoding/json/v2"
	"errors"
	"quasarbridge/internal/dialogue"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"quasarbridge/internal/llm"
	"quasarbridge/internal/playback"
	"quasarbridge/internal/webaddress"
 "quasarbridge/internal/telemetry"
)

type Completer interface {
	Complete(context.Context, []llm.Message, bool) (string, error)
}
type HomeHandler interface {
	Handle(context.Context, string) (string, error)
}
type PlaybackController interface {
	Active(context.Context) bool
	Control(context.Context, string) (string, error)
}
type MusicResumer interface {
	StartPlayback(context.Context) (string, error)
}
type PlaylistPlayer interface {
	PlayURI(context.Context, string) (string, error)
}
type YouTubePlayer interface {
	Available() bool
	Active(context.Context) bool
	Play(context.Context, string) error
	Control(context.Context, string) (string, error)
}
type SpeakerRelay interface {
	RelayRoom(string) bool
	Relay(context.Context, string, string) error
}
type SpotifyFinder interface {
	FindSpotify(context.Context, string, string) (string, []string, error)
}
type TrackQueue interface {
	Queue(context.Context, []string) error
}
type PlaylistSource interface {
	Playlists() []playback.Playlist
}
type MusicStarter interface {
	CanStartMusic(string) bool
	StartMusic(context.Context, string) (string, error)
}
type Engine struct {
 Activity func(telemetry.Event)
	SystemPrompt string
	LLM          Completer
	Assistant    Completer
	Home         HomeHandler
	Playback     PlaybackController
	Music        MusicStarter
	YouTube      YouTubePlayer
	Silencer     interface{ Silence(context.Context) error }
	Dialogue     *dialogue.Store
	dialogueOnce sync.Once
	routing      atomic.Pointer[RoutingPolicy]
}

func (e *Engine) dialogue() *dialogue.Store {
	e.dialogueOnce.Do(func() {
		if e.Dialogue == nil {
			e.Dialogue = dialogue.New(dialogue.DefaultConfig())
		}
	})
	return e.Dialogue
}

// Prepare reserves this speaker's request before ASR/classification can finish late.
func (e *Engine) Prepare(ctx context.Context) context.Context {
	if ctx.Err() != nil {
		return ctx
	}
	ctx, _, _ = e.dialogue().Start(ctx)
	return ctx
}

// ListenAfterRoute identifies replies that cannot start silent media playback.
func (e *Engine) ListenAfterRoute(decision RouteDecision) bool {
	return decision.Route == "home" || (decision.Route == "llm" && e.Assistant == nil)
}

func (e *Engine) Resolve(ctx context.Context, text string) (Resolution, error) {
	return e.ResolveDecision(ctx, text, e.Decide(ctx, text))
}
func (e *Engine) ResolveDecision(ctx context.Context, text string, decision RouteDecision) (resolution Resolution, resolveErr error) {
 ctx,activity:=telemetry.Begin(ctx,text,decision.Route,decision.Source)
 defer func(){if e.Activity!=nil {err:=resolveErr;if err==nil {err=ctx.Err()};e.Activity(activity.Finish(resolution.Route,err,resolution.Answer!=""))}}()
	route := decision.Route
	if decision.prepared {
		ctx = e.dialogue().Use(ctx, decision.ticket)
	} else {
		ctx = e.Prepare(ctx)
	}
	_, _, ticket := e.dialogue().Start(ctx)
	if ctx.Err() != nil {
		return Resolution{Route: route}, ctx.Err()
	}

	if !e.dialogue().Current(ctx, ticket) {
		return Resolution{Route: route}, context.Canceled
	}
	if decision.Source == "dialogue_retry" {
		return e.retryQuestion(ctx, ticket)
	}
	if decision.Source == "dialogue_reset" {
		e.dialogue().ClearReplay(ctx, ticket)
		if !e.dialogue().Clear(ctx, ticket) {
			return Resolution{Route: route}, context.Canceled
		}
		return Resolution{Route: "llm", Answer: "Начинаем новый разговор."}, nil
	}
	// A new topic ends the conversation, but "тише" in the middle of one does not.
	aside := route == "playback" || decision.Rule == "playback_and_volume" || decision.Rule == "speaker_playback"
	if route != "llm" && !aside && !e.dialogue().Clear(ctx, ticket) {
		return Resolution{Route: route}, context.Canceled
	}
	result := Resolution{Route: route}
	switch route {
	case "music":
		if e.Music == nil {
			return result, errors.New("music playback is unavailable")
		}
		answer, err := e.Music.StartMusic(ctx, text)
		if !e.dialogue().Current(ctx, ticket) {
			return result, context.Canceled
		}
		result.Answer = answer
		return result, err
	case "playback":
		if decision.Source == "relay" {
			room, message, _ := playback.Relay(text)
			result.Answer = "Передала."
			if err := e.Home.(SpeakerRelay).Relay(ctx, room, message); err != nil {
				result.Answer = "Не получилось передать: колонка не отвечает."
			}
			return result, nil
		}
		if decision.Source == "youtube_control" {
			answer, err := e.YouTube.Control(ctx, playback.Command(text))
			result.Answer = answer
			return result, err
		}
		if decision.Source == "playback_youtube" {
			if e.YouTube == nil || !e.YouTube.Available() {
				result.Answer = "YouTube Music на этой колонке не установлен."
				return result, nil
			}
			if e.Playback != nil && e.Playback.Active(ctx) {
				_, _ = e.Playback.Control(ctx, "pause")
			}
			kind, query, _, _ := playback.Search(text)
			if kind == "liked" {
				query = "мои лайки"
			}
			if err := e.YouTube.Play(ctx, query); err != nil {
				result.Answer = "Не нашла это в YouTube Music."
			}
			return result, nil
		}
		if e.Playback == nil {
			return Resolution{Route: "yandex"}, nil
		}
		if e.YouTube != nil && e.YouTube.Active(ctx) {
			// Two players must not sound at once.
			_, _ = e.YouTube.Control(ctx, "stop")
		}
		if decision.Source == "playback_missing" {
			result.Answer = "Не нашла такой плейлист в Spotify."
			return result, nil
		}
		if player, ok := e.Playback.(PlaylistPlayer); ok && decision.Source == "playback_search" {
			kind, query, _, _ := playback.Search(text)
			uri, next, err := e.Home.(SpotifyFinder).FindSpotify(ctx, kind, query)
			if err != nil {
				result.Answer = "Spotify сейчас не отвечает."
				return result, nil
			}
			if uri == "" {
				result.Answer = "Не нашла это в Spotify."
				return result, nil
			}
			answer, err := player.PlayURI(ctx, uri)
			result.Answer = answer
			if queue, ok := e.Playback.(TrackQueue); ok && err == nil && len(next) > 0 {
				// The track already plays, so a lost queue only ends the music sooner.
				_ = queue.Queue(ctx, next)
			}
			return result, err
		}
		if player, ok := e.Playback.(PlaylistPlayer); ok && decision.Source == "playback_playlist" {
			answer, err := player.PlayURI(ctx, e.playlistURI(text))
			result.Answer = answer
			return result, err
		}
		if resumer, ok := e.Playback.(MusicResumer); ok && decision.Source == "playback_start" {
			answer, err := resumer.StartPlayback(ctx)
			result.Answer = answer
			return result, err
		}
		action := playback.Command(text)
		if action == "" {
			return result, errors.New("unknown playback control")
		}
		answer, err := e.Playback.Control(ctx, action)
		if !e.dialogue().Current(ctx, ticket) {
			return result, context.Canceled
		}
		result.Answer = answer
		return result, err
	case "home":
		if e.Home == nil {
			return Resolution{Route: "yandex"}, nil
		}
		var answer string
		var err error
		if decision.Source == "voice_phrase" {
			handler, ok := e.Home.(interface {
				HandleVoice(context.Context, string) (string, error)
			})
			if !ok {
				return result, errors.New("voice handler unavailable")
			}
			answer, err = handler.HandleVoice(ctx, text)
		} else {
			answer, err = e.Home.Handle(ctx, text)
		}
		if errors.Is(err, llm.ErrDisabled) {
			return Resolution{Route: "yandex"}, nil
		}
		result.Answer = answer
		return result, err
	case "llm":
		answer, err := e.Fallback(ctx, text)
		if errors.Is(err, llm.ErrDisabled) {
			return Resolution{Route: "yandex"}, nil
		}
		return answer, err
	default:
		return Resolution{Route: "yandex"}, nil
	}
}
func (e *Engine) Fallback(ctx context.Context, text string) (fallbackResult Resolution, fallbackErr error) {
 if e.Activity!=nil && !telemetry.HasTrace(ctx) {
  var trace *telemetry.Trace
  ctx,trace=telemetry.Begin(ctx,text,"llm","native_refusal")
  defer func(){err:=fallbackErr;if err==nil {err=ctx.Err()};e.Activity(trace.Finish(fallbackResult.Route,err,fallbackResult.Answer!=""))}()
 }
	if strings.Contains(strings.ToLower(text), "сайт") || webaddress.MusicRequest(text) {
		text = webaddress.Normalize(text)
	}
	r := Resolution{Route: "llm"}
	if e.LLM == nil && e.Assistant == nil {
		return r, errors.New("LLM is not configured")
	}
	answerer := e.LLM
	if e.Assistant != nil {
		answerer = e.Assistant
	}
	ctx, snapshot, ticket := e.dialogue().Start(ctx)
	if !e.dialogue().Current(ctx, ticket) {
		return r, context.Canceled
	}
	messages := []llm.Message{{Role: "system", Content: "Ты голосовой помощник пользователя. Отвечай по-русски, кратко и по существу, обычно до трёх предложений. Ответ будет прочитан вслух: без Markdown и служебных рассуждений. Выполняй внешние действия только доступными инструментами по явному запросу пользователя. Не заявляй о выполнении действий без успешного результата инструмента; если инструмента нет, прямо скажи об этом. Для Home Assistant есть отдельный обработчик умного дома."}}
	messages = append(messages, snapshot.Messages...)
	messages = append(messages, llm.Message{Role: "user", Content: text})
	private := snapshot.PrivateHistory
	if e.SystemPrompt != "" {
		messages[0].Content += "\n" + e.SystemPrompt
	}
	var answer string
	var err error
	if contextual, ok := answerer.(interface {
		CompleteDialogue(context.Context, []llm.Message, bool) (string, bool, error)
	}); ok {
		answer, private, err = contextual.CompleteDialogue(ctx, messages, private)
	} else {
		answer, err = answerer.Complete(ctx, messages, false)
	}
	if !e.dialogue().Current(ctx, ticket) {
		return r, context.Canceled
	}
	if err == nil {
		if !e.dialogue().Commit(ctx, ticket, text, answer, private) {
			return r, context.Canceled
		}
	}
	r.Answer = answer
	return r, err
}
func containsOne(text string, needles []string) bool {
	for _, s := range needles {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}
func (e *Engine) SetRouting(cfg RoutingConfig) error {
	p, err := CompileRouting(cfg)
	if err != nil {
		return err
	}
	e.routing.Store(p)
	return nil
}
func (e *Engine) musicSource() string {
	if source, ok := e.Home.(interface{ MusicSource() string }); ok {
		return source.MusicSource()
	}
	return ""
}
// wantsYouTube follows the service named in the phrase, then the panel's default.
func (e *Engine) wantsYouTube(text string) bool {
	_, _, provider, found := playback.Search(text)
	if found && provider != "" {
		return provider == "youtube"
	}
	if !found && !playback.Start(text) {
		return false
	}
	source, ok := e.Home.(interface{ MusicProvider() string })
	return ok && source.MusicProvider() == "youtube"
}
// PlayerControl serves the panel: the player that owns the speaker takes the action.
func (e *Engine) PlayerControl(ctx context.Context, action string) error {
	switch action {
	case "pause", "resume", "next", "previous":
	case "silence":
		// One button stops whatever sounds: every player and Alice herself.
		if e.YouTube != nil && e.YouTube.Active(ctx) {
			_, _ = e.YouTube.Control(ctx, "stop")
		}
		if e.Playback != nil && e.Playback.Active(ctx) {
			_, _ = e.Playback.Control(ctx, "pause")
		}
		if e.Silencer == nil {
			return nil
		}
		return e.Silencer.Silence(ctx)
	default:
		return errors.New("unknown player action")
	}
	if e.YouTube != nil && e.YouTube.Active(ctx) {
		_, err := e.YouTube.Control(ctx, action)
		return err
	}
	if e.Playback != nil && e.Playback.Active(ctx) {
		_, err := e.Playback.Control(ctx, action)
		return err
	}
	return errors.New("nothing is playing")
}
func (e *Engine) canSearch() bool {
	_, ok := e.Home.(SpotifyFinder)
	return ok
}
func (e *Engine) playlistURI(text string) string {
	source, ok := e.Home.(PlaylistSource)
	if !ok {
		return ""
	}
	return playback.PickPlaylist(text, source.Playlists())
}
func (e *Engine) policy() *RoutingPolicy {
	if p := e.routing.Load(); p != nil {
		return p
	}
	return builtInRouting
}

type RoutingStatus struct {
	Revision    string
	ActiveRules int
}

func (e *Engine) RoutingStatus() RoutingStatus {
	p := e.policy()
	return RoutingStatus{Revision: p.revision, ActiveRules: len(p.rules)}
}
func (e *Engine) IsRefusal(text string) bool { return e.policy().isRefusal(text) }
func IsRefusal(text string) bool             { return builtInRouting.isRefusal(text) }
func (e *Engine) Decide(ctx context.Context, text string) (decision RouteDecision) {
	ctx, snapshot, ticket := e.dialogue().Start(ctx)
	defer func() {
		// Choose the cloud path before Native can start local progress/TTS.
		if decision.Route == "home" && e.Home == nil {
			decision.Route = "yandex"
			decision.Source = "home_disabled"
		}
		decision.ticket = ticket
		decision.prepared = true
	}()
	if matcher, ok := e.Home.(interface {
		MatchVoice(context.Context, string) bool
	}); ok && matcher.MatchVoice(ctx, text) {
		return RouteDecision{Route: "home", Source: "voice_phrase"}
	}
	p := e.policy()
	if isRetryCommand(text) {
		return RouteDecision{Route: "llm", Source: "dialogue_retry"}
	}
	if dialogue.IsReset(text) {
		if h, ok := e.Home.(interface{ ResetPending(context.Context) }); ok {
			h.ResetPending(ctx)
		}
		return RouteDecision{Route: "llm", Source: "dialogue_reset"}
	}
	if relay, ok := e.Home.(SpeakerRelay); ok {
		// Only a known room counts: "скажи в двух словах" stays with Alice.
		if room, _, found := playback.Relay(text); found && relay.RelayRoom(room) {
			return RouteDecision{Route: "playback", Source: "relay"}
		}
	}
	if e.YouTube != nil && playback.Command(text) != "" && e.YouTube.Active(ctx) {
		return RouteDecision{Route: "playback", Source: "youtube_control"}
	}
	if e.musicSource() == "yandex" {
		if e.YouTube != nil && playback.MusicAsk(text) && e.YouTube.Active(ctx) {
			_, _ = e.YouTube.Control(ctx, "stop")
		}
	} else if e.wantsYouTube(text) {
		return RouteDecision{Route: "playback", Source: "playback_youtube"}
	}
	if e.Playback != nil && playback.Command(text) != "" && e.Playback.Active(ctx) {
		return RouteDecision{Route: "playback", Source: "playback"}
	}
	if _, ok := e.Playback.(PlaylistPlayer); ok {
		if e.musicSource() == "yandex" {
			// Stock music takes over, so Spotify must not keep playing under it.
			if playback.MusicAsk(text) && e.Playback.Active(ctx) {
				_, _ = e.Playback.Control(ctx, "pause")
			}
		} else if e.playlistURI(text) != "" {
			return RouteDecision{Route: "playback", Source: "playback_playlist"}
		} else if playback.Start(text) {
			return RouteDecision{Route: "playback", Source: "playback_start"}
		} else if _, _, _, found := playback.Search(text); found && e.canSearch() {
			return RouteDecision{Route: "playback", Source: "playback_search"}
		} else if playback.MusicAsk(text) {
			return RouteDecision{Route: "playback", Source: "playback_missing"}
		}
	}
	if matcher, ok := e.Home.(interface {
		MatchDevice(context.Context, string) bool
	}); ok && matcher.MatchDevice(ctx, text) {
		return RouteDecision{Route: "home", Source: "device_name"}
	}
	if d, ok := p.match(text); ok {
		if d.Route == "llm" && e.Music != nil && e.Music.CanStartMusic(text) {
			d.Route = "music"
		}
		return d
	}
	if h, ok := e.Home.(interface{ HasPending(context.Context) bool }); ok && h.HasPending(ctx) {
		return RouteDecision{Route: "home", Source: "home_clarification"}
	}
	if len(snapshot.Messages) > 0 && dialogue.IsFollowup(text) {
		return RouteDecision{Route: "llm", Source: "dialogue"}
	}
	if p.defaultRoute != "classify" {
		return RouteDecision{Route: p.defaultRoute, Source: "default"}
	}
	failed := RouteDecision{Route: p.classifier.OnError, Source: "classifier_error"}
	if e.LLM == nil {
		return failed
	}
	classCtx, cancel := context.WithTimeout(ctx, time.Duration(p.classifier.TimeoutMS)*time.Millisecond)
	defer cancel()
	messages := []llm.Message{{Role: "system", Content: p.classifier.Prompt}}
	messages = append(messages, snapshot.Messages...)
	messages = append(messages, llm.Message{Role: "user", Content: text})
	answer, err := e.LLM.Complete(classCtx, messages, true)
	if err != nil {
		return failed
	}
	var choice struct {
		Route string `json:"route"`
	}
	if json.Unmarshal([]byte(answer), &choice) != nil || !directRoute(choice.Route) {
		return failed
	}
	return RouteDecision{Route: choice.Route, Source: "classifier"}
}
