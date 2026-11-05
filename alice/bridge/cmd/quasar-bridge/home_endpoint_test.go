package main

import (
	"testing"

	"quasarbridge/internal/assistant"
)

func TestHomeToolsFollowRuntimeAddress(t *testing.T) {
	t.Setenv("QUASAR_HOME_FUNCTIONS_URL", "http://192.168.1.104:18081/")
	cfg := config{}
	cfg.AssistantTools.MCPServers = []assistant.MCPServerConfig{{Name: "home", URL: "http://192.168.1.96:18080/mcp"}, {Name: "search", URL: "http://192.168.1.96:20129/mcp"}}
	useHomeEndpoint(&cfg)
	home, search := cfg.AssistantTools.MCPServers[0], cfg.AssistantTools.MCPServers[1]
	if home.URL != "http://192.168.1.104:18081/mcp" || home.BearerTokenEnv != "QUASAR_HOME_FUNCTIONS_TOKEN" || home.HeadersEnv["X-Alice-Speaker"] != "QUASAR_HOME_SPEAKER_ID" {
		t.Fatalf("%+v", home)
	}
	if search.URL != "http://192.168.1.96:20129/mcp" {
		t.Fatalf("%+v", search)
	}
}
