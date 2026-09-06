package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestObserveSSECapturesFirstContentAndUsage(t *testing.T) {
	t.Parallel()

	stream := strings.NewReader(strings.Join([]string{
		": keepalive",
		`data: {"choices":[{"delta":{"content":"hello"}}]}`,
		`data: {"choices":[{"delta":{"content":" world"}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`,
		"data: [DONE]",
		"",
	}, "\n"))

	usage, firstSample, bytesRead, firstEventMS, err := ObserveSSE(stream, time.Now().Add(-150*time.Millisecond))
	if err != nil {
		t.Fatalf("observe stream: %v", err)
	}
	if firstSample != "hello" {
		t.Fatalf("unexpected first sample: %q", firstSample)
	}
	if firstEventMS < 100 {
		t.Fatalf("first event too small: %d", firstEventMS)
	}
	if bytesRead == 0 {
		t.Fatal("expected bytes to be counted")
	}
	if usage == nil {
		t.Fatal("expected streamed usage")
	}
	if usage.PromptTokens != 12 || usage.CompletionTokens != 3 || usage.TotalTokens != 15 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestLoadCasesValidatesInput(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cases.json")
	if err := os.WriteFile(path, []byte(`[{"id":"small","prompt":"write a test"}]`), 0o644); err != nil {
		t.Fatalf("write cases: %v", err)
	}

	cases, err := LoadCases(path)
	if err != nil {
		t.Fatalf("load cases: %v", err)
	}
	if len(cases) != 1 || cases[0].ID != "small" {
		t.Fatalf("unexpected cases: %+v", cases)
	}
}

func TestRunCaseStreamsAgainstOpenAICompatibleBackend(t *testing.T) {
	t.Parallel()

	var requestPayload struct {
		Model         string    `json:"model"`
		Messages      []Message `json:"messages"`
		Stream        bool      `json:"stream"`
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
		MaxTokens int `json:"max_tokens"`
	}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestPayload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Devrail-Request-ID", "req-123")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(backend.Close)

	result := RunCase(context.Background(), Options{
		BaseURL:    backend.URL + "/v1",
		Model:      "local-coder",
		HTTPClient: backend.Client(),
		MaxTokens:  64,
		Timeout:    time.Second,
	}, Case{ID: "smoke", Prompt: "reply ok"})

	if result.Error != "" {
		t.Fatalf("unexpected result error: %s", result.Error)
	}
	if result.Status != http.StatusOK {
		t.Fatalf("unexpected status: %d", result.Status)
	}
	if result.RequestID != "req-123" {
		t.Fatalf("unexpected request id: %q", result.RequestID)
	}
	if result.PromptTokens != 4 || result.CompletionTokens != 2 || result.TotalTokens != 6 {
		t.Fatalf("unexpected usage: %+v", result)
	}
	if result.FirstContentSample != "ok" {
		t.Fatalf("unexpected first sample: %q", result.FirstContentSample)
	}
	if requestPayload.Model != "local-coder" || !requestPayload.Stream {
		t.Fatalf("unexpected request payload: %+v", requestPayload)
	}
	if !requestPayload.StreamOptions.IncludeUsage {
		t.Fatal("expected streamed usage option")
	}
	if requestPayload.MaxTokens != 64 {
		t.Fatalf("unexpected max tokens: %d", requestPayload.MaxTokens)
	}
}

func TestRunCaseReportsNonStreamResponse(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad model"}}`))
	}))
	t.Cleanup(backend.Close)

	result := RunCase(context.Background(), Options{
		BaseURL:    backend.URL + "/v1",
		Model:      "local-coder",
		HTTPClient: backend.Client(),
		Timeout:    time.Second,
	}, Case{ID: "bad", Prompt: "hello"})

	if result.Status != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d", result.Status)
	}
	if result.ResponseBytes == 0 {
		t.Fatal("expected response bytes")
	}
	if !strings.Contains(result.Error, "expected text/event-stream response") {
		t.Fatalf("unexpected error: %q", result.Error)
	}
}

func TestRunWritesJSONL(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cases.json")
	if err := os.WriteFile(path, []byte(`[{"id":"bad-url","prompt":"hello"}]`), 0o644); err != nil {
		t.Fatalf("write cases: %v", err)
	}

	var out bytes.Buffer
	err := Run(context.Background(), Options{
		BaseURL:   "not-a-url",
		Model:     "local-coder",
		CasesPath: path,
		Output:    &out,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	var result Result
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &result); err != nil {
		t.Fatalf("decode jsonl: %v", err)
	}
	if result.CaseID != "bad-url" || result.Error == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
}
