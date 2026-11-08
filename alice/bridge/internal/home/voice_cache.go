package home

import (
 "context"
 "crypto/sha256"
 "encoding/json"
 "errors"
 "log/slog"
 "net/http"
 "strings"
 "sync"
 "sync/atomic"
 "time"
 "unicode"

 "quasarbridge/internal/playback"
)

type voicePhraseSnapshot struct {
 Phrases map[string]bool
 Revision string
 Active int
 Names []string
 Playlists []playback.Playlist
 MusicSource string
 RelayRooms []string
 MusicProvider string
}
type voicePhraseCache struct {
 snapshot atomic.Pointer[voicePhraseSnapshot]
 once sync.Once
 routing atomic.Pointer[[sha256.Size]byte]
 tag atomic.Pointer[string]
}

func normalizeVoicePhrase(text string) string {
 text = strings.Map(func(r rune) rune {
  if unicode.IsPunct(r) { return ' ' }
  r=unicode.ToLower(r)
  if r=='ё' {return 'е'}
  return r
 },text)
 words:=strings.Fields(text)
 if len(words)>0 && words[0]=="алиса" {words=words[1:]}
 // Politeness never changes a command, and the server drops it the same way.
 kept:=words[:0]
 for _,word:=range words {if word!="пожалуйста" {kept=append(kept,word)}}
 return strings.Join(kept," ")
}

var deviceVerbs=map[string]bool{"включи":true,"выключи":true,"отключи":true,"погаси":true,"зажги":true,"открой":true,"закрой":true,"запусти":true,"останови":true,"сделай":true,"прибавь":true,"убавь":true,"установи":true,"поставь":true,"переключи":true,"какая":true,"какой":true,"какое":true,"сколько":true,"горит":true,"работает":true}
var mediaWords=[]string{"музык","песн","радио","трек","плейлист","альбом","подкаст","будильник","таймер","напоминан","мультик","фильм"}

// MatchDevice sends a command that names a known device home before any rule can
// hand it to Yandex. A name unknown to HA stays with Yandex, which may own it.
func (f *Functions) MatchDevice(_ context.Context,text string) bool {
 snapshot:=f.voice.snapshot.Load()
 if snapshot==nil {return false}
 phrase:=normalizeVoicePhrase(text)
 hasVerb:=false
 for _,word:=range strings.Fields(phrase) {hasVerb=hasVerb||deviceVerbs[word]}
 if !hasVerb {return false}
 for _,word:=range mediaWords {if strings.Contains(phrase,word) {return false}}
 padded:=" "+phrase+" "
 for _,name:=range snapshot.Names {if strings.Contains(padded," "+name+" ") {return true}}
 return false
}

// MatchVoice performs no I/O, locks or waits on the request path.
func (f *Functions) MatchVoice(_ context.Context,text string) bool {
 snapshot:=f.voice.snapshot.Load()
 return snapshot!=nil && snapshot.Phrases[normalizeVoicePhrase(text)]
}
func (f *Functions) VoicePhraseCount() int {
 if snapshot:=f.voice.snapshot.Load();snapshot!=nil {return snapshot.Active}
 return 0
}

// Playlists returns the Spotify playlists named in the panel.
func (f *Functions) Playlists() []playback.Playlist {
 if snapshot:=f.voice.snapshot.Load();snapshot!=nil {return snapshot.Playlists}
 return nil
}

// RelayRoom reports whether a spoken room has a speaker that can talk.
func (f *Functions) RelayRoom(room string) bool {
 snapshot:=f.voice.snapshot.Load()
 if snapshot==nil {return false}
 stem:=playback.RoomStem(room)
 for _,known:=range snapshot.RelayRooms {if known==stem {return true}}
 return false
}

// Relay has the server speak a message on the speaker of another room.
func (f *Functions) Relay(ctx context.Context,room,message string) error {
 ctx,cancel:=context.WithTimeout(ctx,5*time.Second);defer cancel()
 var done struct {OK bool `json:"ok"`}
 return f.request(ctx,http.MethodPost,"/relay",map[string]string{"room":room,"message":message},&done)
}

// ReportState tells the server what this speaker plays and how it is doing.
func (f *Functions) ReportState(ctx context.Context,state map[string]any) error {
 ctx,cancel:=context.WithTimeout(ctx,2*time.Second);defer cancel()
 var done struct {OK bool `json:"ok"`}
 return f.request(ctx,http.MethodPost,"/now-playing",state,&done)
}

// FindSpotify asks the server, which holds the account, for something to play.
func (f *Functions) FindSpotify(ctx context.Context,kind,query string) (string,[]string,error) {
 ctx,cancel:=context.WithTimeout(ctx,5*time.Second);defer cancel()
 var found struct {URI string `json:"uri"`; Next []string `json:"next"`}
 err:=f.request(ctx,http.MethodPost,"/spotify/search",map[string]string{"kind":kind,"query":query},&found)
 return found.URI,found.Next,err
}

// MusicProvider returns the alternative service used when the phrase names none.
func (f *Functions) MusicProvider() string {
 if snapshot:=f.voice.snapshot.Load();snapshot!=nil {return snapshot.MusicProvider}
 return ""
}

// MusicSource returns the source picked in the panel: "alternative" or "yandex".
func (f *Functions) MusicSource() string {
 if snapshot:=f.voice.snapshot.Load();snapshot!=nil {return snapshot.MusicSource}
 return ""
}

func (f *Functions) RefreshVoiceCache(ctx context.Context) error {
 ctx,cancel:=context.WithTimeout(ctx,2*time.Second);defer cancel()
 var response struct {
  Revision string `json:"revision"`
  Commands []struct {Enabled *bool `json:"enabled"`; Phrases []string `json:"phrases"`; Target string `json:"target"`} `json:"commands"`
  DeviceNames []string `json:"device_names"`
  Routing json.RawMessage `json:"routing"`
  Phrases struct {
   Spotify []string `json:"spotify"`
   YouTube []string `json:"youtube"`
   Relay []string `json:"relay"`
   Commands map[string][]string `json:"commands"`
   Replies map[string]string `json:"replies"`
  } `json:"phrases"`
  Playlists []playback.Playlist `json:"playlists"`
  MusicSource string `json:"music_source"`
  MusicProvider string `json:"music_provider"`
  RelayRooms []string `json:"relay_rooms"`
 }
 // The table is only parsed when it changed; a weak speaker skips thousands of phrases.
 tag:=""
 if held:=f.voice.tag.Load();held!=nil {tag=*held}
 if err:=f.requestTagged(ctx,http.MethodGet,"/voice-phrases",nil,&response,&tag);err!=nil {
  if errors.Is(err,errNotModified) {return nil}
  return err
 }
 if response.Revision=="" {return errors.New("incomplete voice table")}
 if len(response.Commands)>1000 {return errors.New("voice table exceeds limit")}
 phrases:=map[string]bool{}
 owners:=map[string]string{}
 enabled:=map[string]bool{}
 for _,command:=range response.Commands {
  if !strings.HasPrefix(command.Target,"automation.") && !strings.HasPrefix(command.Target,"script.") {continue}
  for _,text:=range command.Phrases {
   phrase:=normalizeVoicePhrase(text)
   if phrase=="" || len(phrase)>4000 {continue}
   if old,ok:=owners[phrase];ok && old!=command.Target {phrases[phrase]=false;continue}
   owners[phrase]=command.Target
   enabled[phrase]=command.Enabled==nil || *command.Enabled
   if _,ok:=phrases[phrase];!ok {phrases[phrase]=true}
  }
 }
 for phrase,valid:=range phrases {if !valid {delete(phrases,phrase)}}
 if len(phrases)>5000 {return errors.New("voice table exceeds limit")}
 active:=0
 for phrase:=range phrases {if enabled[phrase] {active++}}
 if len(response.DeviceNames)>2000 {return errors.New("voice table exceeds limit")}
 names:=make([]string,0,len(response.DeviceNames))
 for _,name:=range response.DeviceNames {if name=normalizeVoicePhrase(name);name!="" {names=append(names,name)}}
 previous:=f.voice.snapshot.Load()
 f.voice.snapshot.Store(&voicePhraseSnapshot{Phrases:phrases,Revision:response.Revision,Active:active,Names:names,Playlists:response.Playlists,MusicSource:response.MusicSource,RelayRooms:response.RelayRooms,MusicProvider:response.MusicProvider})
 f.applyRouting(response.Routing)
 // The owner's words and replies arrive with the table; an old server sends none.
 playback.SetCustom(playback.Custom{Spotify:response.Phrases.Spotify,YouTube:response.Phrases.YouTube,Relay:response.Phrases.Relay,Commands:response.Phrases.Commands})
 if f.OnReplies!=nil {f.OnReplies(response.Phrases.Replies)}
 if previous==nil || previous.Revision!=response.Revision {
  slog.Info("voice_phrase_cache_updated","phrases",len(phrases),"revision",response.Revision)
 }
 f.voice.tag.Store(&tag)
 return nil
}

// applyRouting tries each published table once. A rejected one leaves the working
// routing in place, so a bad release cannot take the speaker's routes away.
func (f *Functions) applyRouting(raw json.RawMessage) {
 if len(raw)==0 || f.OnRouting==nil {return}
 sum:=sha256.Sum256(raw)
 if last:=f.voice.routing.Load();last!=nil && *last==sum {return}
 f.voice.routing.Store(&sum)
 if err:=f.OnRouting(raw);err!=nil {slog.Warn("routing_update_rejected","error",err);return}
 slog.Info("routing_updated")
}

func (f *Functions) StartVoiceCache(ctx context.Context) {
 f.voice.once.Do(func(){
  if err:=f.RefreshVoiceCache(ctx);err!=nil {slog.Warn("voice_phrase_cache_unavailable")}
  go func(){
   ticker:=time.NewTicker(5*time.Second);defer ticker.Stop()
   for {select {
   case <-ctx.Done():return
   case <-ticker.C:
    // Keep the last complete table on errors. Execution revalidates at server.
    if err:=f.RefreshVoiceCache(ctx);err!=nil {slog.Debug("voice_phrase_cache_refresh_failed")}
   }}
  }()
 })
}

func (f *Functions) VoicePhraseRevision() string {
 if snapshot:=f.voice.snapshot.Load();snapshot!=nil {return snapshot.Revision};return ""
}
