package home

import (
 "context"
 "net/http"
 "net/http/httptest"
 "testing"
)

func TestVoiceCacheRefreshAndOfflineMatch(t *testing.T) {
 body:=`{"revision":"one","commands":[{"target":"automation.kitchen","phrases":["Не включай свет на кухне"]}]}`
 server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){w.Write([]byte(body))}))
 f:=Functions{URL:server.URL}
 if err:=f.RefreshVoiceCache(t.Context());err!=nil {t.Fatal(err)}
 if !f.MatchVoice(t.Context(),"Алиса, НЕ включай свет на кухне!") {t.Fatal("normalization mismatch")}
 body=`{"revision":"two","commands":[{"target":"script.night","phrases":["Я ушёл спать"]}]}`
 if err:=f.RefreshVoiceCache(t.Context());err!=nil {t.Fatal(err)}
 if f.MatchVoice(t.Context(),"не включай свет на кухне") || !f.MatchVoice(t.Context(),"я ушел спать") {t.Fatal("cache did not replace table")}
 server.Close()
 if err:=f.RefreshVoiceCache(t.Context());err==nil {t.Fatal("expected offline error")}
 if !f.MatchVoice(t.Context(),"Я ушёл спать") {t.Fatal("offline refresh lost last good table")}
}

func TestRevokedVoiceCannotFallBackToPlannerOrStandardLight(t *testing.T) {
 server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  if r.URL.Path=="/functions" && r.Method==http.MethodGet {w.Write([]byte(`{}`));return}
  if r.URL.Path=="/resolve" {w.Write([]byte(`{"matched":false}`));return}
  t.Error("revoked mapping fetched state or performed an action");w.WriteHeader(500)
 }));defer server.Close()
 f:=Functions{URL:server.URL}
 answer,err:=f.HandleVoice(t.Context(),"выключи свет на кухне")
 if err!=nil || answer=="" {t.Fatalf("answer=%q err=%v",answer,err)}
}

func BenchmarkVoiceCacheMatch(b *testing.B) {
 f:=Functions{}
 f.voice.snapshot.Store(&voicePhraseSnapshot{Phrases:map[string]bool{"не включай свет на кухне":true}})
 b.ReportAllocs()
 for b.Loop(){f.MatchVoice(context.Background(),"Алиса, не включай свет на кухне!")}
}

func TestOldServerCannotExecuteUnprovenVoiceMatch(t *testing.T) {
 calls:=0
 server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  if r.URL.Path=="/functions" && r.Method==http.MethodGet {w.Write([]byte(`{"set_light":{"arguments":{}}}`));return}
  if r.URL.Path=="/resolve" {w.Write([]byte(`{"matched":true,"calls":[{"name":"set_light","arguments":{}}]}`));return}
  calls++;w.Write([]byte(`{}`))
 }));defer server.Close()
 f:=Functions{URL:server.URL}
 answer,err:=f.HandleVoice(t.Context(),"выключи свет")
 if err!=nil || answer=="" || calls!=0 {t.Fatalf("old server action: answer=%q calls=%d error=%v",answer,calls,err)}
}

func TestDisabledSpeakerStillInterceptsPhraseWithoutFallback(t *testing.T) {
 server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  if r.Header.Get("X-Alice-Speaker")!="lite2" {t.Error("missing speaker identity")}
  w.Write([]byte(`{"revision":"disabled","commands":[{"enabled":false,"target":"automation.kitchen","phrases":["Не включай свет на кухне"]}]}`))
 }));defer server.Close()
 f:=Functions{URL:server.URL,SpeakerID:"lite2"}
 if err:=f.RefreshVoiceCache(t.Context());err!=nil {t.Fatal(err)}
 if !f.MatchVoice(t.Context(),"не включай свет на кухне") || f.VoicePhraseCount()!=0 {t.Fatal("disabled phrase must intercept, but not count as enabled")}
}

func TestLocalDictionaryEntryIsAcceptedByCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"revision":"r","commands":[{"enabled":true,"target":"script.alice_local_dictionary","phrases":["выключи компьютер","какая температура на улице"]}]}`))
	}))
	defer server.Close()
	f := &Functions{URL: server.URL, Token: "t"}
	if err := f.RefreshVoiceCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.MatchVoice(context.Background(), "Алиса, выключи компьютер!") || !f.MatchVoice(context.Background(), "какая температура на улице?") {
		t.Fatal("dictionary phrase not matched")
	}
	if f.MatchVoice(context.Background(), "не выключай компьютер") {
		t.Fatal("unknown phrase matched")
	}
}
