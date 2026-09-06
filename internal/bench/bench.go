package bench

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Case struct {
	ID       string    `json:"id"`
	Prompt   string    `json:"prompt"`
	Messages []Message `json:"messages,omitempty"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Result struct {
	CaseID             string `json:"case_id"`
	Model              string `json:"model"`
	RequestID          string `json:"request_id,omitempty"`
	Status             int    `json:"status"`
	Streaming          bool   `json:"streaming"`
	FirstEventMS       int64  `json:"first_event_ms"`
	DurationMS         int64  `json:"duration_ms"`
	ResponseBytes      int64  `json:"response_bytes"`
	PromptTokens       int    `json:"prompt_tokens,omitempty"`
	CompletionTokens   int    `json:"completion_tokens,omitempty"`
	TotalTokens        int    `json:"total_tokens,omitempty"`
	Error              string `json:"error,omitempty"`
	FirstContentSample string `json:"first_content_sample,omitempty"`
}

type Options struct {
	BaseURL    string
	Model      string
	APIKey     string
	CasesPath  string
	Output     io.Writer
	HTTPClient *http.Client
	MaxTokens  int
	Timeout    time.Duration
}

type streamUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *streamUsage `json:"usage"`
}

func Run(ctx context.Context, opts Options) error {
	if strings.TrimSpace(opts.BaseURL) == "" {
		return errors.New("base URL is required")
	}
	if strings.TrimSpace(opts.Model) == "" {
		return errors.New("model is required")
	}
	if opts.Output == nil {
		opts.Output = os.Stdout
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Minute
	}

	cases, err := LoadCases(opts.CasesPath)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(opts.Output)
	for _, benchCase := range cases {
		result := RunCase(ctx, opts, benchCase)
		if err := encoder.Encode(result); err != nil {
			return fmt.Errorf("write result: %w", err)
		}
	}
	return nil
}

func LoadCases(path string) ([]Case, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("cases path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cases: %w", err)
	}

	var cases []Case
	if err := json.Unmarshal(data, &cases); err != nil {
		return nil, fmt.Errorf("decode cases: %w", err)
	}
	for i, benchCase := range cases {
		if strings.TrimSpace(benchCase.ID) == "" {
			return nil, fmt.Errorf("case %d missing id", i)
		}
		if strings.TrimSpace(benchCase.Prompt) == "" && len(benchCase.Messages) == 0 {
			return nil, fmt.Errorf("case %q needs prompt or messages", benchCase.ID)
		}
	}
	return cases, nil
}

func RunCase(ctx context.Context, opts Options, benchCase Case) Result {
	result := Result{CaseID: benchCase.ID, Model: opts.Model, Streaming: true, FirstEventMS: -1}
	timeoutCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	body, err := requestBody(opts.Model, opts.MaxTokens, benchCase)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	endpoint, err := chatCompletionsURL(opts.BaseURL)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	req, err := http.NewRequestWithContext(timeoutCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if opts.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+opts.APIKey)
	}

	started := time.Now()
	resp, err := opts.HTTPClient.Do(req)
	if err != nil {
		result.DurationMS = time.Since(started).Milliseconds()
		result.Error = err.Error()
		return result
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	result.Status = resp.StatusCode
	result.RequestID = resp.Header.Get("X-Devrail-Request-ID")
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		body, readErr := io.ReadAll(resp.Body)
		result.DurationMS = time.Since(started).Milliseconds()
		result.ResponseBytes = int64(len(body))
		if readErr != nil {
			result.Error = readErr.Error()
			return result
		}
		result.Error = fmt.Sprintf("expected text/event-stream response, got status %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(body)), 240))
		return result
	}

	usage, firstSample, bytesRead, firstEvent, err := ObserveSSE(resp.Body, started)
	result.DurationMS = time.Since(started).Milliseconds()
	result.ResponseBytes = bytesRead
	result.FirstEventMS = firstEvent
	result.FirstContentSample = firstSample
	if usage != nil {
		result.PromptTokens = usage.PromptTokens
		result.CompletionTokens = usage.CompletionTokens
		result.TotalTokens = usage.TotalTokens
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func ObserveSSE(r io.Reader, started time.Time) (*streamUsage, string, int64, int64, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var usage *streamUsage
	var firstSample string
	firstEventMS := int64(-1)
	var bytesRead int64

	for scanner.Scan() {
		line := scanner.Text()
		bytesRead += int64(len(line) + 1)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if firstEventMS < 0 {
			firstEventMS = time.Since(started).Milliseconds()
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return usage, firstSample, bytesRead, firstEventMS, fmt.Errorf("decode stream chunk: %w", err)
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		if firstSample == "" {
			for _, choice := range chunk.Choices {
				if choice.Delta.Content != "" {
					firstSample = truncate(choice.Delta.Content, 120)
					break
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, firstSample, bytesRead, firstEventMS, err
	}
	return usage, firstSample, bytesRead, firstEventMS, nil
}

func requestBody(model string, maxTokens int, benchCase Case) ([]byte, error) {
	messages := benchCase.Messages
	if len(messages) == 0 {
		messages = []Message{{Role: "user", Content: benchCase.Prompt}}
	}
	body := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   true,
		"stream_options": map[string]bool{
			"include_usage": true,
		},
	}
	if maxTokens > 0 {
		body["max_tokens"] = maxTokens
	}
	return json.Marshal(body)
}

func chatCompletionsURL(base string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse base URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("base URL must include scheme and host: %q", base)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/chat/completions"
	return parsed.String(), nil
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
