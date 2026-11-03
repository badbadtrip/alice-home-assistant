package bridge

import("context";"testing")
type voiceHomeFixture struct{normal,exact int}
func(h *voiceHomeFixture)MatchVoice(context.Context,string)bool{return true}
func(h *voiceHomeFixture)Handle(context.Context,string)(string,error){h.normal++;return "normal",nil}
func(h *voiceHomeFixture)HandleVoice(context.Context,string)(string,error){h.exact++;return "exact",nil}
func TestRegisteredVoicePrecedesNativeRouting(t *testing.T){
 h:=&voiceHomeFixture{};e:=&Engine{Home:h}
 d:=e.Decide(t.Context(),"поставь таймер на минуту")
 if d.Route!="home" || d.Source!="voice_phrase"{t.Fatalf("wrong route: %+v",d)}
 r,err:=e.ResolveDecision(t.Context(),"поставь таймер на минуту",d)
 if err!=nil || r.Answer!="exact" || h.exact!=1 || h.normal!=0 {t.Fatalf("wrong handler: %+v %v",r,err)}
}
