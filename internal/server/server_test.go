package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	assertOpenAIErrorCode(t, rec.Body.Bytes(), "unknown_model_alias")
}

func TestMalformedRequestReturnsOpenAIError(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"messages":[]}`),
	)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if rec.Header().Get("X-Devrail-Request-ID") == "" {
		t.Fatal("expected request id response header")
	}
	assertOpenAIErrorCode(t, rec.Body.Bytes(), "invalid_request")
}

func TestRequestIDHeaderIsPropagated(t *testing.T) {
	t.Parallel()

	var backendRequestID string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendRequestID = r.Header.Get("X-Request-ID")
		writeJSON(w, http.StatusOK, map[string]string{"model": "target-model"})
	}))
	t.Cleanup(backend.Close)

	srv := testServerWithBackend(t, backend.URL, config.ModelConfig{
		ID:          "local-coder",
		Backend:     "lmstudio",
		TargetModel: "target-model",
	})
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"local-coder","messages":[]}`),
	)
	req.Header.Set("X-Request-ID", "test-request-id")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if got := rec.Header().Get("X-Devrail-Request-ID"); got != "test-request-id" {
		t.Fatalf("unexpected response request id: %q", got)
	}
	if backendRequestID != "test-request-id" {
		t.Fatalf("unexpected backend request id: %q", backendRequestID)
	}
}

func TestRoutingRuleSelectsTargetByPromptSize(t *testing.T) {
	t.Parallel()

	var backendModel string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode backend request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		backendModel = payload.Model
		writeJSON(w, http.StatusOK, map[string]string{"model": payload.Model})
	}))
	t.Cleanup(backend.Close)

	srv := testServerWithBackend(t, backend.URL, config.ModelConfig{
		ID:          "local-coder-auto",
		Backend:     "lmstudio",
		TargetModel: "fast-model",
		Routing: config.RoutingConfig{
			Rules: []config.RoutingRuleConfig{{
				ID:             "large-prompt",
				TargetModel:    "deep-model",
				MinPromptChars: 20,
			}},
		},
	})
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"local-coder-auto","messages":[{"role":"user","content":"please analyze this larger prompt"}]}`),
	)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if backendModel != "deep-model" {
		t.Fatalf("unexpected backend model: %q", backendModel)
	}
}

func TestRoutingRuleFallsBackToDefaultTarget(t *testing.T) {
	t.Parallel()

	var backendModel string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode backend request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		backendModel = payload.Model
		writeJSON(w, http.StatusOK, map[string]string{"model": payload.Model})
	}))
	t.Cleanup(backend.Close)

	srv := testServerWithBackend(t, backend.URL, config.ModelConfig{
		ID:          "local-coder-auto",
		Backend:     "lmstudio",
		TargetModel: "fast-model",
		Routing: config.RoutingConfig{
			Rules: []config.RoutingRuleConfig{{
				ID:          "hard-work",
				TargetModel: "deep-model",
				AnyKeywords: []string{"architecture"},
			}},
		},
	})
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"local-coder-auto","messages":[{"role":"user","content":"say ok"}]}`),
	)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if backendModel != "fast-model" {
		t.Fatalf("unexpected backend model: %q", backendModel)
	}
}

func TestRewriteModelClampsOversizedTargetAliasOutput(t *testing.T) {
	t.Parallel()

	body, err := rewriteModel([]byte(`{"model":"local-coder-auto","messages":[],"max_tokens":32768}`), "local-coder-fast", 8192)
	if err != nil {
		t.Fatalf("rewrite model: %v", err)
	}

	var payload struct {
		Model    string `json:"model"`
		MaxToken int    `json:"max_tokens"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode rewritten payload: %v", err)
	}
	if payload.Model != "local-coder-fast" {
		t.Fatalf("unexpected model: %q", payload.Model)
	}
	if payload.MaxToken != 8192 {
		t.Fatalf("unexpected max_tokens: %d", payload.MaxToken)
	}
}

func TestJoinOpenAIPathAvoidsDuplicateVersionPrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		basePath    string
		requestPath string
		want        string
	}{
		{
			name:        "versioned base",
			basePath:    "/v1",
			requestPath: "/v1/chat/completions",
			want:        "/v1/chat/completions",
		},
		{
			name:        "root base",
			basePath:    "",
			requestPath: "/v1/chat/completions",
			want:        "/v1/chat/completions",
		},
		{
			name:        "nested base",
			basePath:    "/openai",
			requestPath: "/v1/chat/completions",
			want:        "/openai/v1/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := joinOpenAIPath(tt.basePath, tt.requestPath); got != tt.want {
				t.Fatalf("joinOpenAIPath(%q, %q) = %q, want %q", tt.basePath, tt.requestPath, got, tt.want)
			}
		})
	}
}

func TestModelLimiterQueuesRequests(t *testing.T) {
	t.Parallel()

	backendStarted := make(chan struct{})
	releaseBackend := make(chan struct{})
	var backendStartedCount atomic.Int32
	var active atomic.Int32
	var maxActive atomic.Int32

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		for {
			previous := maxActive.Load()
			if current <= previous || maxActive.CompareAndSwap(previous, current) {
				break
			}
		}
		defer active.Add(-1)

		if backendStartedCount.Add(1) == 1 {
			close(backendStarted)
			<-releaseBackend
		}

		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode backend request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"model": payload.Model})
	}))
	t.Cleanup(backend.Close)

	srv := testServerWithBackend(t, backend.URL+"/v1", config.ModelConfig{
		ID:                    "local-coder",
		Backend:               "lmstudio",
		TargetModel:           "target-model",
		MaxConcurrentRequests: 1,
		MaxQueueSize:          1,
		QueueTimeout:          "1s",
	})

	firstDone := make(chan int, 1)
	go func() {
		firstDone <- serveChat(t, srv, "local-coder")
	}()

	select {
	case <-backendStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first backend request")
	}

	secondDone := make(chan int, 1)
	go func() {
		secondDone <- serveChat(t, srv, "local-coder")
	}()

	select {
	case status := <-secondDone:
		t.Fatalf("second request finished before slot was released with status %d", status)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseBackend)

	if status := <-firstDone; status != http.StatusOK {
		t.Fatalf("unexpected first status: %d", status)
	}
	if status := <-secondDone; status != http.StatusOK {
		t.Fatalf("unexpected second status: %d", status)
	}
	if maxActive.Load() != 1 {
		t.Fatalf("backend saw %d concurrent requests, want 1", maxActive.Load())
	}
}

func TestModelLimiterRejectsFullQueue(t *testing.T) {
	t.Parallel()

	backendStarted := make(chan struct{})
	releaseBackend := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(backendStarted)
		<-releaseBackend
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))
	t.Cleanup(backend.Close)

	srv := testServerWithBackend(t, backend.URL, config.ModelConfig{
		ID:                    "local-coder",
		Backend:               "lmstudio",
		TargetModel:           "target-model",
		MaxConcurrentRequests: 1,
		MaxQueueSize:          0,
		QueueTimeout:          "1s",
	})

	firstDone := make(chan int, 1)
	go func() {
		firstDone <- serveChat(t, srv, "local-coder")
	}()

	select {
	case <-backendStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first backend request")
	}

	if status := serveChat(t, srv, "local-coder"); status != http.StatusTooManyRequests {
		t.Fatalf("unexpected status: %d", status)
	}

	close(releaseBackend)
	if status := <-firstDone; status != http.StatusOK {
		t.Fatalf("unexpected first status: %d", status)
	}
}

func TestModelLimiterTimesOutQueuedRequest(t *testing.T) {
	t.Parallel()

	backendStarted := make(chan struct{})
	releaseBackend := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(backendStarted)
		<-releaseBackend
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))
	t.Cleanup(backend.Close)

	srv := testServerWithBackend(t, backend.URL, config.ModelConfig{
		ID:                    "local-coder",
		Backend:               "lmstudio",
		TargetModel:           "target-model",
		MaxConcurrentRequests: 1,
		MaxQueueSize:          1,
		QueueTimeout:          "25ms",
	})

	firstDone := make(chan int, 1)
	go func() {
		firstDone <- serveChat(t, srv, "local-coder")
	}()

	select {
	case <-backendStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first backend request")
	}

	if status := serveChat(t, srv, "local-coder"); status != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d", status)
	}

	close(releaseBackend)
	if status := <-firstDone; status != http.StatusOK {
		t.Fatalf("unexpected first status: %d", status)
	}
}

func TestEnsureCommandRunsBeforeProxy(t *testing.T) {
	t.Parallel()

	ensureFile := filepath.Join(t.TempDir(), "ensured")
	ensureCommand := []string{"/bin/sh", "-c", "printf ready > " + ensureFile}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := os.Stat(ensureFile); err != nil {
			t.Errorf("ensure command did not run before backend request: %v", err)
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))
	t.Cleanup(backend.Close)

	srv := testServerWithBackend(t, backend.URL, config.ModelConfig{
		ID:          "local-coder",
		Backend:     "lmstudio",
		TargetModel: "target-model",
		Ensure: config.EnsureConfig{
			Mode:    "command",
			Command: ensureCommand,
			Timeout: "1s",
		},
	})

	if status := serveChat(t, srv, "local-coder"); status != http.StatusOK {
		t.Fatalf("unexpected status: %d", status)
	}
}

func TestEnsureCommandFailureReturnsServiceUnavailable(t *testing.T) {
	t.Parallel()

	var backendCalls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))
	t.Cleanup(backend.Close)

	srv := testServerWithBackend(t, backend.URL, config.ModelConfig{
		ID:          "local-coder",
		Backend:     "lmstudio",
		TargetModel: "target-model",
		Ensure: config.EnsureConfig{
			Mode:    "command",
			Command: []string{"/bin/sh", "-c", "echo nope >&2; exit 7"},
			Timeout: "1s",
		},
	})

	if status := serveChat(t, srv, "local-coder"); status != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d", status)
	}
	if backendCalls.Load() != 0 {
		t.Fatalf("backend received %d calls, want 0", backendCalls.Load())
	}
}

func TestBackendResponseTelemetryLogsUsage(t *testing.T) {
	var logs bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() {
		slog.SetDefault(originalLogger)
	})

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode backend request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   payload.Model,
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]string{
					"role":    "assistant",
					"content": "ok",
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{
				"prompt_tokens":     51,
				"completion_tokens": 4,
				"total_tokens":      55,
			},
		})
	}))
	t.Cleanup(backend.Close)

	srv := testServerWithBackend(t, backend.URL, config.ModelConfig{
		ID:          "local-coder",
		Backend:     "lmstudio",
		TargetModel: "target-model",
	})

	if status := serveChat(t, srv, "local-coder"); status != http.StatusOK {
		t.Fatalf("unexpected status: %d", status)
	}

	logText := logs.String()
	for _, want := range []string{
		`"msg":"backend response completed"`,
		`"request_id":`,
		`"alias":"local-coder"`,
		`"target_model":"target-model"`,
		`"upstream_model":"target-model"`,
		`"status":200`,
		`"prompt_tokens":51`,
		`"completion_tokens":4`,
		`"total_tokens":55`,
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("expected log to contain %s, got logs:\n%s", want, logText)
		}
	}
}

func TestStreamingBackendTelemetryLogsUsage(t *testing.T) {
	var logs bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() {
		slog.SetDefault(originalLogger)
	})

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"model\":\"target-model\",\"choices\":[{\"delta\":{\"content\":\"o\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"model\":\"target-model\",\"choices\":[{\"delta\":{\"content\":\"k\"}}],\"usage\":{\"prompt_tokens\":27,\"completion_tokens\":2,\"total_tokens\":29}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(backend.Close)

	srv := testServerWithBackend(t, backend.URL, config.ModelConfig{
		ID:          "local-coder",
		Backend:     "lmstudio",
		TargetModel: "target-model",
	})
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"local-coder","messages":[],"stream":true}`),
	)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Fatalf("stream body was not proxied: %s", rec.Body.String())
	}

	logText := logs.String()
	for _, want := range []string{
		`"msg":"backend response completed"`,
		`"streaming":true`,
		`"first_event_ms":`,
		`"upstream_model":"target-model"`,
		`"prompt_tokens":27`,
		`"completion_tokens":2`,
		`"total_tokens":29`,
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("expected log to contain %s, got logs:\n%s", want, logText)
		}
	}
}

func TestMetricsEndpointExposesRequestTelemetry(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode backend request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"model": payload.Model,
			"usage": map[string]int{
				"prompt_tokens":     9,
				"completion_tokens": 3,
				"total_tokens":      12,
			},
		})
	}))
	t.Cleanup(backend.Close)

	srv := testServerWithBackend(t, backend.URL, config.ModelConfig{
		ID:          "local-coder",
		Backend:     "lmstudio",
		TargetModel: "target-model",
	})

	if status := serveChat(t, srv, "local-coder"); status != http.StatusOK {
		t.Fatalf("unexpected status: %d", status)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"# TYPE devrail_router_requests_total counter",
		`devrail_router_requests_total{alias="local-coder",backend="lmstudio",target_model="target-model",status="200",streaming="false"} 1`,
		`devrail_router_request_duration_seconds_bucket{alias="local-coder",backend="lmstudio",target_model="target-model",status="200",streaming="false",le="+Inf"} 1`,
		"devrail_router_prompt_tokens_total 9",
		"devrail_router_completion_tokens_total 3",
		"devrail_router_total_tokens_total 12",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected metrics to contain %s, got:\n%s", want, body)
		}
	}
}

func TestMetricsEndpointExposesStreamingFirstEventLatency(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"model\":\"target-model\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
	}))
	t.Cleanup(backend.Close)

	srv := testServerWithBackend(t, backend.URL, config.ModelConfig{
		ID:          "local-coder",
		Backend:     "lmstudio",
		TargetModel: "target-model",
	})
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"local-coder","messages":[],"stream":true}`),
	)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRec := httptest.NewRecorder()
	srv.ServeHTTP(metricsRec, metricsReq)
	body := metricsRec.Body.String()

	for _, want := range []string{
		`devrail_router_requests_total{alias="local-coder",backend="lmstudio",target_model="target-model",status="200",streaming="true"} 1`,
		`devrail_router_first_event_latency_seconds_bucket{alias="local-coder",backend="lmstudio",target_model="target-model",status="200",streaming="true",le="+Inf"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected metrics to contain %s, got:\n%s", want, body)
		}
	}
}

func TestBackendProxyErrorReturnsOpenAIError(t *testing.T) {
	t.Parallel()

	srv := testServerWithBackend(t, "http://127.0.0.1:1/v1", config.ModelConfig{
		ID:          "local-coder",
		Backend:     "lmstudio",
		TargetModel: "target-model",
	})
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"local-coder","messages":[]}`),
	)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	assertOpenAIErrorCode(t, rec.Body.Bytes(), "backend_request_failed")
}

func testServer(t *testing.T) *Server {
	t.Helper()

	return testServerWithBackend(t, "http://127.0.0.1:1234/v1", config.ModelConfig{
		ID:          "local-coder",
		Backend:     "lmstudio",
		TargetModel: "qwen/qwen3.6-35b-a3b",
	})
}

func testServerWithBackend(t *testing.T, backendURL string, model config.ModelConfig) *Server {
	t.Helper()

	srv, err := New(config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:0"},
		Models: []config.ModelConfig{model},
		Backends: []config.BackendConfig{{
			ID:      "lmstudio",
			BaseURL: backendURL,
		}},
	})
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	return srv
}

func serveChat(t *testing.T, srv *Server, model string) int {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"`+model+`","messages":[]}`),
	)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)
	_, _ = io.Copy(io.Discard, rec.Result().Body)
	return rec.Code
}

func assertOpenAIErrorCode(t *testing.T, raw []byte, want string) {
	t.Helper()

	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode error response: %v\nbody: %s", err, string(raw))
	}
	if payload.Error.Code != want {
		t.Fatalf("unexpected error code: %q, want %q", payload.Error.Code, want)
	}
}
