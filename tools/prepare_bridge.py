"""Reconstruct the reviewed bridge from pinned upstream + the project's patches."""
from pathlib import Path
import json
import shutil
import subprocess

ROOT=Path(__file__).resolve().parents[1]
WORK=ROOT.parent
SOURCE=ROOT/'.build/bridge'
COMMIT='8c1438269232171464d80b8b8fa50795cbf8c177'
PATCH=WORK/'alice-gemini-bridge/patches/quasar-integration.patch'
INTEGRATION=WORK/'home-functions/integration'


def prepare():
    if not SOURCE.exists():
        subprocess.run(['git','clone','--quiet','https://github.com/skrashevich/quasar-bridge.git',str(SOURCE)],check=True)
    head=subprocess.check_output(['git','-C',str(SOURCE),'rev-parse','HEAD'],text=True).strip()
    if head!=COMMIT:
        if subprocess.check_output(['git','-C',str(SOURCE),'status','--porcelain']):raise ValueError('Inspect existing bridge changes before switching source')
        subprocess.run(['git','-C',str(SOURCE),'checkout','--quiet',COMMIT],check=True)
    marker=SOURCE/'.alice-app-prepared'
    if marker.exists():
        optional_llm()
        print('Reviewed bridge source already prepared');return
    subprocess.run(['git','-C',str(SOURCE),'apply','--check',str(PATCH)],check=True)
    subprocess.run(['git','-C',str(SOURCE),'apply',str(PATCH)],check=True)
    files={
        'functions.go':'internal/home/functions.go','functions_test.go':'internal/home/functions_test.go',
        'control_functions_test.go':'internal/home/control_functions_test.go','voice_cache.go':'internal/home/voice_cache.go',
        'voice_cache_test.go':'internal/home/voice_cache_test.go','panel-home-activity.go':'internal/home/activity.go',
        'panel-home-activity_test.go':'internal/home/activity_test.go','panel-telemetry.go':'internal/telemetry/activity.go',
        'panel-telemetry_test.go':'internal/telemetry/activity_test.go','panel-internal-bridge-engine.go':'internal/bridge/engine.go',
        'panel-activity-engine_test.go':'internal/bridge/activity_test.go','panel-cmd-quasar-bridge-main.go':'cmd/quasar-bridge/main.go',
        'control_bridge_test.go':'internal/bridge/control_bridge_test.go','quiet_home_test.go':'internal/bridge/quiet_home_test.go',
        'voice_routing_test.go':'internal/bridge/voice_routing_test.go'}
    for source,target in files.items():
        out=SOURCE/target;out.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(INTEGRATION/source,out)
    progress=(INTEGRATION/'live-progress.go').read_text(encoding='utf-8')
    progress=progress.replace('func (n *Native) progress(p *nativeRequest) func() {','func (n *Native) progress(p *nativeRequest) func() { return n.progressAfter(p, 0) }\nfunc (n *Native) progressAfter(p *nativeRequest, minimum time.Duration) func() {',1)
    progress=progress.replace('if n.VoiceCues != nil {\n\t\t\tdelay = 0\n\t\t}','if n.VoiceCues != nil { delay = minimum }; if delay < minimum { delay = minimum }',1)
    progress=progress.replace('if n.VoiceCues != nil {\n\t\t<-ready','if n.VoiceCues != nil && minimum == 0 {\n\t\t<-ready',1)
    (SOURCE/'internal/bridge/progress.go').write_text(progress,encoding='utf-8')
    native=(INTEGRATION/'live-native.go').read_text(encoding='utf-8')
    native=native.replace('stop = n.progress(p)','if decision.Route == "home" { stop = n.progressAfter(p, 900*time.Millisecond) } else { stop = n.progress(p) }',1)
    native=native.replace('decision.Route != "yandex" && (n.ProgressAfter > 0 || n.VoiceCues != nil)','decision.Route != "yandex" && decision.Route != "home" && (n.ProgressAfter > 0 || n.VoiceCues != nil)',1)
    native=native.replace('silentPlayback := n.hasSilentPlayback(p)','silentPlayback := n.hasSilentPlayback(p) || (text == "" && n.hasQueuedSuccess(p))',1)
    (SOURCE/'internal/bridge/native.go').write_text(native,encoding='utf-8')
    sound=SOURCE/'internal/bridge/sound.go'
    with sound.open('a',encoding='utf-8') as f:f.write('\nfunc (n *Native) hasQueuedSuccess(p *nativeRequest) bool {\n n.mu.Lock(); defer n.mu.Unlock(); return p.soundDirective != nil && p.soundRequest != nil && p.soundRequest.Sound == "success"\n}\n')
    marker.write_text(COMMIT)
    optional_llm()
    (ROOT/'BRIDGE-SOURCE.json').write_text(json.dumps({'repository':'https://github.com/skrashevich/quasar-bridge','commit':COMMIT,'changes':'integration patch plus reviewed Home Functions, quiet control and activity deltas; see source manifest'},indent=2)+'\n')
    print('Prepared pinned bridge and Home Functions integration')


def optional_llm():
    if not any('func ValidateAnswer(' in p.read_text(encoding='utf-8') for p in (SOURCE/'internal/llm').glob('*.go')):
        shutil.copyfile(ROOT/'tools/speech_validation.go.txt',SOURCE/'internal/llm/speech_validation.go')
    client=SOURCE/'internal/llm/client.go'
    content=client.read_text(encoding='utf-8')
    if 'if !structured { if err := ValidateAnswer(m.Content)' not in content:
        content=content.replace('return strings.TrimSpace(m.Content), nil','if !structured { if err := ValidateAnswer(m.Content); err != nil { return "", err } }\n\treturn strings.TrimSpace(m.Content), nil',1)
        client.write_text(content,encoding='utf-8')
    path=SOURCE/'cmd/quasar-bridge/main.go'
    source=path.read_text(encoding='utf-8')
    if 'type noLLMPlanner struct{}' in source:return
    source=source.replace('engine := &bridge.Engine{LLM: lc,', 'var planner home.Planner = lc\n\tllmDisabled := os.Getenv("QUASAR_LLM_DISABLED") == "1"\n\tif llmDisabled { planner = noLLMPlanner{} }\n\tengine := &bridge.Engine{LLM: planner,')
    source=source.replace('Planner: lc}', 'Planner: planner}')
    old='''\t\t\t_, err := lc.Complete(ctx, []llm.Message{{Role: "user", Content: "Ответь одним словом: связь"}}, false)
\t\t\tif err != nil {
\t\t\t\tslog.Error("LLM check failed")
\t\t\t\tos.Exit(1)
\t\t\t}'''
    new='''\t\t\tllmStatus := "disabled"
\t\t\tif !llmDisabled {
\t\t\t\tif _, err := lc.Complete(ctx, []llm.Message{{Role: "user", Content: "Ответь одним словом: связь"}}, false); err != nil {
\t\t\t\t\tslog.Error("LLM check failed"); os.Exit(1)
\t\t\t\t}
\t\t\t\tllmStatus = "ok"
\t\t\t}'''
    if old not in source:raise ValueError('Backend check changed; inspect source')
    source=source.replace(old,new)
    source=source.replace('fmt.Printf("LLM: ok; Home functions: %d functions\\n", len(catalog))','fmt.Printf("LLM: %s; Home functions: %d functions\\n", llmStatus, len(catalog))')
    source=source.replace('fmt.Println("LLM: ok; Home Assistant: disabled")','fmt.Printf("LLM: %s; Home Assistant: disabled\\n", llmStatus)')
    source=source.replace('fmt.Printf("LLM: ok; Home Assistant: %d entities\\n", len(entities))','fmt.Printf("LLM: %s; Home Assistant: %d entities\\n", llmStatus, len(entities))')
    source+='\ntype noLLMPlanner struct{}\nfunc (noLLMPlanner) Complete(context.Context, []llm.Message, bool) (string, error) { return "", llm.ErrDisabled }\n'
    path.write_text(source,encoding='utf-8')


if __name__=='__main__':prepare()
