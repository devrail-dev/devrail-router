package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"
)

func main() {
	address := flag.String("address", "127.0.0.1:9000", "listen address")
	expectedAPIKey := flag.String("expected-api-key", "", "expected bearer token")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r, *expectedAPIKey) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data": []map[string]any{{
				"id":       "mock-model",
				"object":   "model",
				"owned_by": "mock-openai-backend",
			}},
		})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r, *expectedAPIKey) {
			return
		}
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "chatcmpl-mock",
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   payload.Model,
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]string{
					"role":    "assistant",
					"content": "ok from " + payload.Model,
				},
				"finish_reason": "stop",
			}},
		})
	})

	slog.Info("starting mock OpenAI-compatible backend", "address", *address)
	if err := http.ListenAndServe(*address, mux); err != nil {
		slog.Error("mock backend stopped", "error", err)
		os.Exit(1)
	}
}

func authorized(w http.ResponseWriter, r *http.Request, expectedAPIKey string) bool {
	if expectedAPIKey == "" {
		return true
	}
	if r.Header.Get("Authorization") == "Bearer "+expectedAPIKey {
		return true
	}
	http.Error(w, "missing or invalid Authorization header", http.StatusUnauthorized)
	return false
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("write response", "error", err)
	}
}
