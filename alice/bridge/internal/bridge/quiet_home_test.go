package bridge

import (
 "context"
 "testing"
 "time"
 "github.com/coder/websocket"
 fileplayer "github.com/freequasar/quasar-go-genproto/proto/alice/protos/endpoint/capabilities/audio_file_player"
 "google.golang.org/protobuf/proto"
 "quasarbridge/internal/sound"
)

type quietHomeResolver struct{}
func (quietHomeResolver) Decide(context.Context,string) RouteDecision { return RouteDecision{Route:"home"} }
func (r quietHomeResolver) Resolve(ctx context.Context,s string)(Resolution,error) {return r.ResolveDecision(ctx,s,r.Decide(ctx,s))}
func (quietHomeResolver) ResolveDecision(ctx context.Context,_ string,_ RouteDecision)(Resolution,error) {
 // Slow enough to exercise the old progress-cue path.
 time.Sleep(30*time.Millisecond)
 return Resolution{Route:"home"},sound.Play(ctx,sound.Request{Sound:"success"})
}

func TestHomeSuccessUsesOnlyNativeDoneSound(t *testing.T) {
 n,up,down := soundSession(t,quietHomeResolver{})
 n.VoiceCues = &VoiceCues{}
 n.ProgressAfter=time.Millisecond
 if err:=n.Client(t.Context(),websocket.MessageBinary,soundInput("quiet-home")); err!=nil {t.Fatal(err)}
 var count int
 for {
  f:=nextSoundFrame(t,down)
  if f.Server.GetEvent().GetDeferredAliceResponse()!=nil {t.Fatal("home started progress stream")}
  if r:=f.Server.GetEvent().GetAliceResponse(); r!=nil {
   if r.GetResponse().GetIsStreaming() || r.GetVoiceResponse().GetHasVoiceResponse() || r.GetVoiceResponse().GetOutputSpeech().GetText()!="" {t.Fatal("home success generated speech or progress")}
   ds:=r.GetResponse().GetDirectives()
   if len(ds)!=1 || ds[0].GetName()!="local_audio_file_play" {t.Fatal("missing native done sound")}
   var play fileplayer.TAudioFilePlayerCapability_TLocalAudioFilePlayDirective
   if err:=proto.Unmarshal(ds[0].GetPayloadRaw(),&play);err!=nil {t.Fatal(err)}
   if play.GetSoundToPlay()!=fileplayer.TAudioFilePlayerCapability_AliceIoTSoundsDone || play.GetPlayTimes()!=1 {t.Fatal("wrong completion sound")}
   count++;break
  }
 }
 n.Close()
 for len(up)>0 {if f:=<-up; f.Kind()=="TtsGenerate" {t.Fatal("home success requested TTS")}}
 if count!=1 {t.Fatal("completion sound must play once")}
}
