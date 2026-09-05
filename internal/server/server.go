package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/devrail-dev/devrail-router/internal/config"
)

type Server struct {
	cfg         config.Config
	limiters    map[string]*modelLimiter
	ensureLocks map[string]*sync.Mutex
}

func New(cfg config.Config) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	limiters := make(map[string]*modelLimiter, len(cfg.Models))
	for _, model := range cfg.Models {
		limiter, err := newModelLimiter(model)
		if err != nil {
			return nil, err
		}
		if limiter != nil {
			limiters[model.ID] = limiter
		}
	}

	ensureLocks := make(map[string]*sync.Mutex, len(cfg.Models))
	for _, model := range cfg.Models {
		if model.Ensure.Mode != "" && model.Ensure.Mode != "disabled" {
			ensureLocks[model.ID] = &sync.Mutex{}
		}
	}

	return &Server{cfg: cfg, limiters: limiters, ensureLocks: ensureLocks}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		s.handleHealth(w, r)
	case r.URL.Path == "/v1/models":
		s.handleModels(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/"):
		s.proxyOpenAI(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	type modelResponse struct {
		ID            string `json:"id"`
		Object        string `json:"object"`
		OwnedBy       string `json:"owned_by"`
		Name          string `json:"name,omitempty"`
		ContextWindow int    `json:"context_window,omitempty"`
		TargetModel   string `json:"target_model,omitempty"`
	}

	models := make([]modelResponse, 0, len(s.cfg.Models))
	for _, model := range s.cfg.Models {
		models = append(models, modelResponse{
			ID:            model.ID,
			Object:        "model",
			OwnedBy:       "devrail-router",
			Name:          model.Name,
			ContextWindow: model.ContextWindow,
			TargetModel:   model.TargetModel,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   models,
	})
}

func (s *Server) proxyOpenAI(w http.ResponseWriter, r *http.Request) {
	modelID, body, err := requestModel(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	model, ok := s.cfg.Model(modelID)
	if !ok {
		http.Error(w, fmt.Sprintf("unknown model alias %q", modelID), http.StatusBadRequest)
		return
	}

	backend, ok := s.cfg.Backend(model.Backend)
	if !ok {
		http.Error(w, fmt.Sprintf("unknown backend %q", model.Backend), http.StatusInternalServerError)
		return
	}

	release, ok := s.acquireModelSlot(w, r, model)
	if !ok {
		return
	}
	if release != nil {
		defer release()
	}

	if !s.ensureModelReady(w, r, model) {
		return
	}

	body, err = rewriteModel(body, model.TargetModel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))

	target, err := url.Parse(backend.BaseURL)
	if err != nil {
		http.Error(w, "backend base_url is invalid", http.StatusInternalServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = joinOpenAIPath(target.Path, r.URL.Path)
		req.Host = target.Host
		setBackendAuth(req, backend)
	}
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, proxyErr error) {
		slog.Error("backend request failed", "method", req.Method, "path", req.URL.Path, "backend", backend.ID, "error", proxyErr)
		http.Error(rw, "backend request failed", http.StatusBadGateway)
	}

	slog.Info("routing request", "alias", model.ID, "target_model", model.TargetModel, "backend", backend.ID)
	proxy.ServeHTTP(w, r)
}

func (s *Server) acquireModelSlot(w http.ResponseWriter, r *http.Request, model config.ModelConfig) (func(), bool) {
	limiter, ok := s.limiters[model.ID]
	if !ok {
		return nil, true
	}

	waited, snapshot, release, err := limiter.acquire(r.Context())
	if err == nil {
		w.Header().Set("X-Devrail-Queue-Wait-Ms", fmt.Sprintf("%d", waited.Milliseconds()))
		slog.Info(
			"acquired model slot",
			"alias", model.ID,
			"active", snapshot.active,
			"queued", snapshot.queued,
			"wait_ms", waited.Milliseconds(),
		)
		return release, true
	}

	switch {
	case errors.Is(err, errQueueFull):
		writeOpenAIError(w, http.StatusTooManyRequests, "model queue is full", "devrail_queue_full", "queue_full")
	case errors.Is(err, errQueueTimeout):
		writeOpenAIError(w, http.StatusServiceUnavailable, "timed out waiting for model queue", "devrail_queue_timeout", "queue_timeout")
	default:
		writeOpenAIError(w, http.StatusRequestTimeout, "request canceled while waiting for model queue", "devrail_queue_canceled", "queue_canceled")
	}

	slog.Warn(
		"rejected queued request",
		"alias", model.ID,
		"active", snapshot.active,
		"queued", snapshot.queued,
		"wait_ms", waited.Milliseconds(),
		"error", err,
	)
	return nil, false
}

func (s *Server) ensureModelReady(w http.ResponseWriter, r *http.Request, model config.ModelConfig) bool {
	if model.Ensure.Mode == "" || model.Ensure.Mode == "disabled" {
		return true
	}

	lock := s.ensureLocks[model.ID]
	if lock == nil {
		lock = &sync.Mutex{}
	}

	lock.Lock()
	defer lock.Unlock()

	switch model.Ensure.Mode {
	case "command":
		return s.ensureModelReadyWithCommand(w, r, model)
	default:
		writeOpenAIError(w, http.StatusInternalServerError, "model ensure mode is unsupported", "devrail_ensure_unsupported", "ensure_unsupported")
		return false
	}
}

func (s *Server) ensureModelReadyWithCommand(w http.ResponseWriter, r *http.Request, model config.ModelConfig) bool {
	timeout, err := model.Ensure.TimeoutDuration()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "model ensure timeout is invalid", "devrail_ensure_config_error", "ensure_config_error")
		return false
	}

	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	command := []string(model.Ensure.Command)
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	output, err := cmd.CombinedOutput()
	outputText := strings.TrimSpace(string(output))
	if len(outputText) > 2048 {
		outputText = outputText[:2048] + "...[truncated]"
	}
	if err != nil {
		status := http.StatusServiceUnavailable
		message := "model profile is not ready"
		code := "ensure_failed"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			message = "timed out ensuring model profile"
			code = "ensure_timeout"
		}

		slog.Warn(
			"model ensure command failed",
			"alias", model.ID,
			"target_model", model.TargetModel,
			"error", err,
			"output", outputText,
		)
		writeOpenAIError(w, status, message, "devrail_"+code, code)
		return false
	}

	slog.Info(
		"model ensure command completed",
		"alias", model.ID,
		"target_model", model.TargetModel,
		"output", outputText,
	)
	return true
}

func requestModel(r *http.Request) (string, []byte, error) {
	if r.Body == nil {
		return "", nil, fmt.Errorf("request body is required")
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", nil, fmt.Errorf("read request body: %w", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", nil, fmt.Errorf("parse request body: %w", err)
	}

	model, ok := payload["model"].(string)
	if !ok || model == "" {
		return "", nil, fmt.Errorf("request body must include a model string")
	}

	return model, body, nil
}

func rewriteModel(body []byte, targetModel string) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse request body: %w", err)
	}

	payload["model"] = targetModel
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode request body: %w", err)
	}

	return body, nil
}

func setBackendAuth(req *http.Request, backend config.BackendConfig) {
	if backend.APIKeyEnv == "" {
		return
	}

	apiKey := os.Getenv(backend.APIKeyEnv)
	if apiKey == "" {
		req.Header.Del("Authorization")
		return
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)
}

func joinPath(basePath, requestPath string) string {
	return strings.TrimRight(basePath, "/") + "/" + strings.TrimLeft(requestPath, "/")
}

func joinOpenAIPath(basePath, requestPath string) string {
	basePath = strings.TrimRight(basePath, "/")
	if basePath != "" && basePath != "/" {
		if requestPath == basePath || strings.HasPrefix(requestPath, basePath+"/") {
			return requestPath
		}
	}

	return joinPath(basePath, requestPath)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("write response", "error", err)
	}
}

func writeOpenAIError(w http.ResponseWriter, status int, message, errorType, code string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"message": message,
			"type":    errorType,
			"code":    code,
		},
	})
}
