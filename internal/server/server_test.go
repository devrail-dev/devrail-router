package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devrail-dev/devrail-router/internal/config"
)

func TestModelsEndpointReturnsAliases(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			TargetModel string `json:"target_model"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) != 1 {
		t.Fatalf("unexpected model count: %d", len(payload.Data))
	}
	if payload.Data[0].ID != "local-coder" {
		t.Fatalf("unexpected model id: %q", payload.Data[0].ID)
	}
}

func TestUnknownAliasReturnsBadRequest(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"missing","messages":[]}`),
	)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
}

func testServer(t *testing.T) *Server {
	t.Helper()

	srv, err := New(config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:0"},
		Models: []config.ModelConfig{{
			ID:          "local-coder",
			Backend:     "lmstudio",
			TargetModel: "qwen/qwen3.6-35b-a3b",
		}},
		Backends: []config.BackendConfig{{
			ID:      "lmstudio",
			BaseURL: "http://127.0.0.1:1234/v1",
		}},
	})
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	return srv
}
