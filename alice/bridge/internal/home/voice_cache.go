package home

import (
 "context"
 "errors"
 "log/slog"
 "net/http"
 "strings"
 "sync"
 "sync/atomic"
 "time"
 "unicode"
)

type voicePhraseSnapshot struct {
 Phrases map[string]bool
 Revision string
 Active int
}
type voicePhraseCache struct {
 snapshot atomic.Pointer[voicePhraseSnapshot]
 once sync.Once
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
 return strings.Join(words," ")
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

func (f *Functions) RefreshVoiceCache(ctx context.Context) error {
 ctx,cancel:=context.WithTimeout(ctx,2*time.Second);defer cancel()
 var response struct {
  Revision string `json:"revision"`
  Commands []struct {Enabled *bool `json:"enabled"`; Phrases []string `json:"phrases"`; Target string `json:"target"`} `json:"commands"`
 }
 if err:=f.request(ctx,http.MethodGet,"/voice-phrases",nil,&response);err!=nil {return err}
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
 previous:=f.voice.snapshot.Load()
 f.voice.snapshot.Store(&voicePhraseSnapshot{Phrases:phrases,Revision:response.Revision,Active:active})
 if previous==nil || previous.Revision!=response.Revision {
  slog.Info("voice_phrase_cache_updated","phrases",len(phrases),"revision",response.Revision)
 }
 return nil
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
