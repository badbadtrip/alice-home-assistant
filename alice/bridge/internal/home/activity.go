package home

import (
 "context"
 "net/http"
 "quasarbridge/internal/telemetry"
)

func (f *Functions) StartActivity(ctx context.Context) {
 f.activity=telemetry.NewWriter(ctx,func(ctx context.Context,event telemetry.Event)error {
  var result map[string]any
  return f.request(ctx,http.MethodPost,"/events",event,&result)
 })
}
func (f *Functions) RecordActivity(event telemetry.Event) {
 event.SpeakerID=f.SpeakerID
 f.activity.Enqueue(event)
}
func (f *Functions) ActivityDropped()uint64 {return f.activity.Dropped()}
