package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"github.com/devrail-dev/devrail-router/internal/config"
)

type Server struct {
	cfg config.Config
}

func New(cfg config.Config) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &Server{cfg: cfg}, nil
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
