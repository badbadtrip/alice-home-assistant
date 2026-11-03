package telemetry

import (
 "context"
 "errors"
 "strings"
 "testing"
 "time"
)

func TestTraceRecordsActualCallsAndCancellationWithoutErrorSecrets(t *testing.T) {
 ctx,trace:=Begin(context.Background(),"выключи свет","home","voice_phrase")
 AddCall(ctx,"run_kitchen","confirmed","Кухня")
 event:=trace.Finish("home",nil,false)
 if event.Status!="confirmed"||len(event.Calls)!=1||event.Calls[0].Target!="Кухня" {t.Fatalf("wrong activity: %+v",event)}
 cancelled:=trace.Finish("home",context.Canceled,false)
 if cancelled.Status!="cancelled"||cancelled.Error!="" {t.Fatal("cancellation misreported")}
 failed:=trace.Finish("home",errors.New("secret-token upstream body"),false)
 if strings.Contains(failed.Error,"secret")||failed.Status!="error" {t.Fatal("raw error leaked")}
}
func TestTraceBoundsFieldsAndDoesNotClaimNativeCompletion(t *testing.T) {
 ctx,trace:=Begin(context.Background(),strings.Repeat("🦊",2500),"yandex","rule")
 for i:=0;i<10;i++ {AddCall(ctx,"x","accepted","y")}
 event:=trace.Finish("home",nil,false)
 if len(event.Calls)!=4||len([]rune(event.Text))!=1000 {t.Fatal("event exceeds contract bounds")}
 _,native:=Begin(context.Background(),"поставь таймер","yandex","rule")
 if native.Finish("yandex",nil,false).Status!="forwarded" {t.Fatal("native routing claimed execution")}
}
func TestQueueNeverBlocksVoiceWhenHTTPWorkerIsStalled(t *testing.T) {
 ctx,cancel:=context.WithCancel(context.Background());defer cancel()
 entered,release:=make(chan struct{}),make(chan struct{})
 writer:=NewWriter(ctx,func(context.Context,Event)error {select {case <-entered:default:close(entered)};<-release;return nil})
 writer.Enqueue(Event{Text:"first"})
 select {case <-entered:case <-time.After(time.Second):t.Fatal("worker did not start")}
 done:=make(chan struct{})
 go func(){for i:=0;i<100;i++ {writer.Enqueue(Event{Text:"queued"})};close(done)}()
 select {case <-done:case <-time.After(time.Second):close(release);t.Fatal("voice blocked on telemetry")}
 if writer.Dropped()==0 {t.Fatal("full queue did not record drops")}
 close(release)
}
