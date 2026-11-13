package main

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"os"
	"time"

	"quasarbridge/internal/bridge"
	"quasarbridge/internal/home"
	"quasarbridge/internal/llm"
)

// runOneShot serves -query, -route and -check-backends: one diagnostic run, then exit.
func runOneShot(lc *llm.Client, hf *home.Functions, engine *bridge.Engine, llmDisabled bool, query, route string, check bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	if hf != nil && (route != "" || query != "") {
		_ = hf.RefreshVoiceCache(ctx)
	}
	if route != "" {
		data, _ := json.Marshal(engine.Decide(ctx, route))
		fmt.Println(string(data))
		return
	}
	if check {
		llmStatus := "disabled"
		if !llmDisabled {
			if _, err := lc.Complete(ctx, []llm.Message{{Role: "user", Content: "Ответь одним словом: связь"}}, false); err != nil {
				slog.Error("LLM check failed")
				os.Exit(1)
			}
			llmStatus = "ok"
		}
		if hf != nil {
			catalog, err := hf.Catalog(ctx)
			if err != nil {
				slog.Error("Home functions check failed", "error", err)
				os.Exit(1)
			}
			fmt.Printf("LLM: %s; Home functions: %d functions\n", llmStatus, len(catalog))
			return
		}
		fmt.Printf("LLM: %s; Home Assistant: disabled\n", llmStatus)
		return
	}
	r, err := engine.Resolve(ctx, query)
	if err != nil {
		slog.Error("query failed", "route", r.Route, "error", err)
		os.Exit(1)
	}
	data, _ := json.Marshal(r)
	fmt.Println(string(data))
	return
}
