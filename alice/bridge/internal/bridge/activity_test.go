package bridge

import (
 "context"
 "testing"
 "quasarbridge/internal/telemetry"
)
type activityHome struct{}
func(activityHome)Handle(ctx context.Context,_ string)(string,error){telemetry.AddCall(ctx,"run_kitchen","confirmed","Кухня");return "",nil}
func TestActivityObserverRecordsRealHomeCallsAndNativeForwarding(t *testing.T) {
 var events []telemetry.Event
 engine:=&Engine{Home:activityHome{},Activity:func(event telemetry.Event){events=append(events,event)}}
 if _,err:=engine.ResolveDecision(context.Background(),"выключи свет",RouteDecision{Route:"home",Source:"rule"});err!=nil {t.Fatal(err)}
 if len(events)!=1||events[0].Status!="confirmed"||len(events[0].Calls)!=1 {t.Fatalf("missing command result: %+v",events)}
 if _,err:=engine.ResolveDecision(context.Background(),"таймер",RouteDecision{Route:"yandex",Source:"rule"});err!=nil {t.Fatal(err)}
 if len(events)!=2||events[1].Status!="forwarded"||len(events[1].Calls)!=0 {t.Fatal("native command was not recorded as forwarding only")}
}
