// Package telemetry records bounded command metadata without blocking voice work.
package telemetry

import (
 "context"
 "crypto/rand"
 "encoding/hex"
 "errors"
 "strings"
 "sync"
 "sync/atomic"
 "time"
)

type Call struct { Name string `json:"name"`; Status string `json:"status"`; Target string `json:"target"` }
type Event struct {
 At string `json:"at"`; Calls []Call `json:"calls"`; DurationMS int64 `json:"duration_ms"`
 Error string `json:"error,omitempty"`; ID string `json:"id"`; Route string `json:"route"`
 LLMMS int64 `json:"llm_ms,omitempty"`; Model string `json:"model,omitempty"`; Source string `json:"source"`; SpeakerID string `json:"speaker_id"`; Status string `json:"status"`; Text string `json:"text"`
}
type contextKey struct{}
type Trace struct { mu sync.Mutex; began time.Time; event Event; explicit bool }
func HasTrace(ctx context.Context)bool {_,ok:=ctx.Value(contextKey{}).(*Trace);return ok}
func clip(s string,n int) string { r:=[]rune(s);if len(r)>n {r=r[:n]};return string(r) }
func Begin(ctx context.Context,text,route,source string)(context.Context,*Trace) {
 var id [16]byte;_,_=rand.Read(id[:])
 trace:=&Trace{began:time.Now(),event:Event{ID:hex.EncodeToString(id[:]),At:time.Now().UTC().Format(time.RFC3339Nano),
  Text:clip(strings.TrimSpace(text),1000),Route:clip(route,16),Source:clip(source,32),Calls:[]Call{}}}
 return context.WithValue(ctx,contextKey{},trace),trace
}
func Status(ctx context.Context,status string) {
 if trace,ok:=ctx.Value(contextKey{}).(*Trace);ok {trace.mu.Lock();trace.event.Status=status;trace.explicit=true;trace.mu.Unlock()}
}
// AddLLM shows how much of a slow command the model took.
func AddLLM(ctx context.Context,elapsed time.Duration) {
 if trace,ok:=ctx.Value(contextKey{}).(*Trace);ok {trace.mu.Lock();trace.event.LLMMS=min(trace.event.LLMMS+elapsed.Milliseconds(),600000);trace.mu.Unlock()}
}
// SetModel keeps the model that answered last; a gateway may hide it behind an alias.
func SetModel(ctx context.Context,model string) {
 if len(model)>64 {model=model[:64]}
 if trace,ok:=ctx.Value(contextKey{}).(*Trace);ok&&model!="" {trace.mu.Lock();trace.event.Model=model;trace.mu.Unlock()}
}
func AddCall(ctx context.Context,name,status,target string) {
 if trace,ok:=ctx.Value(contextKey{}).(*Trace);ok {
  trace.mu.Lock();defer trace.mu.Unlock()
  if len(trace.event.Calls)<4 {trace.event.Calls=append(trace.event.Calls,Call{clip(name,100),clip(status,20),clip(target,100)})}
  rank:=func(s string)int {switch s {case "error":return 6;case "unavailable":return 5;case "accepted":return 4;case "confirmed":return 3;case "already":return 2;default:return 0}}
  if rank(status)>rank(trace.event.Status) {trace.event.Status=status;trace.explicit=true}
 }
}
// Raw errors may carry private text, so the journal keeps only a fixed class.
func errorClass(err error) string {
 text:=err.Error()
 for _,class:=range [][2]string{{"LLM returned HTTP","Model refused the request"},{"LLM transport","Model unreachable"},{"LLM answer exceeded","Model answer too long"},{"LLM returned no answer","Model returned nothing"},{"home plan","Model returned a bad plan"},{"home function plan","Model returned a bad plan"},{"home-functions","Home functions unavailable"}} {
  if strings.Contains(text,class[0]) {return class[1]}
 }
 return "Command processing failed"
}
func (t *Trace) Finish(route string,err error,answered bool) Event {
 t.mu.Lock();defer t.mu.Unlock();event:=t.event
 if route!="" {event.Route=clip(route,16)}
 event.DurationMS=time.Since(t.began).Milliseconds();if event.DurationMS>600000 {event.DurationMS=600000}
 if err!=nil {event.Status="error";event.Error=errorClass(err);if errors.Is(err,context.Canceled)||errors.Is(err,context.DeadlineExceeded) {event.Status="cancelled";event.Error="";if errors.Is(err,context.DeadlineExceeded) {event.Status="error";event.Error="Timed out"}}
 }else if !t.explicit {switch {case event.Route=="yandex":event.Status="forwarded";case answered:event.Status="answered";default:event.Status="handled"}}
 event.Calls=append([]Call{},event.Calls...);return event
}

type Writer struct { queue chan Event; dropped atomic.Uint64 }
func NewWriter(ctx context.Context,send func(context.Context,Event)error)*Writer {
 writer:=&Writer{queue:make(chan Event,32)}
 go func(){for {select {case <-ctx.Done():return;case event:=<-writer.queue:
  callCtx,cancel:=context.WithTimeout(ctx,2*time.Second);err:=send(callCtx,event);cancel();if err!=nil {writer.dropped.Add(1)}
 }}}()
 return writer
}
func (w *Writer) Enqueue(event Event) {if w==nil||event.Text=="" {return};select {case w.queue<-event:default:w.dropped.Add(1)}}
func (w *Writer) Dropped()uint64 {if w==nil {return 0};return w.dropped.Load()}
