package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/devrail-dev/devrail-router/internal/config"
)

type Server struct {
	cfg         config.Config
	limiters    map[string]*modelLimiter
	ensureLocks map[string]*sync.Mutex
	metrics     *metricsRegistry
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

	return &Server{cfg: cfg, limiters: limiters, ensureLocks: ensureLocks, metrics: newMetricsRegistry()}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		s.handleHealth(w, r)
	case r.URL.Path == "/metrics":
		s.handleMetrics(w, r)
	case r.URL.Path == "/v1/models":
		s.handleModels(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/"):
		s.proxyOpenAI(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = io.WriteString(w, s.metrics.render())
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
		MaxOutput     int    `json:"max_output_tokens,omitempty"`
		ToolCall      bool   `json:"tool_call,omitempty"`
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
			MaxOutput:     model.MaxOutputTokens,
			ToolCall:      model.ToolCalls,
			TargetModel:   model.TargetModel,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   models,
	})
}

func (s *Server) proxyOpenAI(w http.ResponseWriter, r *http.Request) {
	requestID := devrailRequestID(r)
	w.Header().Set("X-Devrail-Request-ID", requestID)

	modelID, body, err := requestModel(r)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_request")
		s.metrics.record(requestMetrics{Alias: modelID, Status: http.StatusBadRequest, Started: time.Now()})
		return
	}

	model, ok := s.cfg.Model(modelID)
	if !ok {
		writeOpenAIError(w, http.StatusBadRequest, fmt.Sprintf("unknown model alias %q", modelID), "invalid_request_error", "unknown_model_alias")
		s.metrics.record(requestMetrics{Alias: modelID, Status: http.StatusBadRequest, Started: time.Now()})
		return
	}

	backend, ok := s.cfg.Backend(model.Backend)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, fmt.Sprintf("unknown backend %q", model.Backend), "devrail_config_error", "unknown_backend")
		s.metrics.record(requestMetricsFromModel(model, config.BackendConfig{}, http.StatusInternalServerError, time.Now()))
		return
	}

	waited, release, ok := s.acquireModelSlot(w, r, model, backend, requestID)
	if !ok {
		return
	}
	if release != nil {
		defer release()
	}

	if !s.ensureModelReady(w, r, model, requestID) {
		s.metrics.record(requestMetricsFromModel(model, backend, http.StatusServiceUnavailable, time.Now()))
		return
	}

	targetModel, routeRule := s.selectTargetModel(r.Context(), body, model, requestID)
	targetMaxOutputTokens := 0
	if targetAlias, ok := s.cfg.Model(targetModel); ok {
		targetMaxOutputTokens = targetAlias.MaxOutputTokens
	}
	body, err = rewriteModel(body, targetModel, targetMaxOutputTokens)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_request")
		s.metrics.record(requestMetricsFromModel(model, backend, http.StatusBadRequest, time.Now()))
		return
	}
	routedModel := model
	routedModel.TargetModel = targetModel

	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))

	target, err := url.Parse(backend.BaseURL)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "backend base_url is invalid", "devrail_config_error", "invalid_backend_base_url")
		s.metrics.record(requestMetricsFromModel(model, backend, http.StatusInternalServerError, time.Now()))
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	started := time.Now()
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = joinOpenAIPath(target.Path, r.URL.Path)
		req.Host = target.Host
		setBackendAuth(req, backend)
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		instrumentBackendResponse(resp, started, waited, routedModel, backend, requestID, s.metrics)
		return nil
	}
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, proxyErr error) {
		slog.Error(
			"backend request failed",
			"method", req.Method,
			"path", req.URL.Path,
			"request_id", requestID,
			"alias", model.ID,
			"target_model", targetModel,
			"route_rule", routeRule,
			"backend", backend.ID,
			"duration_ms", time.Since(started).Milliseconds(),
			"error", proxyErr,
		)
		s.metrics.record(requestMetricsFromModel(routedModel, backend, http.StatusBadGateway, started))
		writeOpenAIError(rw, http.StatusBadGateway, "backend request failed", "devrail_backend_error", "backend_request_failed")
	}

	slog.Info("routing request", "request_id", requestID, "alias", model.ID, "target_model", targetModel, "route_rule", routeRule, "backend", backend.ID)
	proxy.ServeHTTP(w, r)
}

func (s *Server) acquireModelSlot(w http.ResponseWriter, r *http.Request, model config.ModelConfig, backend config.BackendConfig, requestID string) (time.Duration, func(), bool) {
	limiter, ok := s.limiters[model.ID]
	if !ok {
		return 0, nil, true
	}

	waited, snapshot, release, err := limiter.acquire(r.Context())
	if err == nil {
		w.Header().Set("X-Devrail-Queue-Wait-Ms", fmt.Sprintf("%d", waited.Milliseconds()))
		slog.Info(
			"acquired model slot",
			"request_id", requestID,
			"alias", model.ID,
			"active", snapshot.active,
			"queued", snapshot.queued,
			"wait_ms", waited.Milliseconds(),
		)
		return waited, release, true
	}

	status := http.StatusRequestTimeout
	switch {
	case errors.Is(err, errQueueFull):
		status = http.StatusTooManyRequests
		writeOpenAIError(w, http.StatusTooManyRequests, "model queue is full", "devrail_queue_full", "queue_full")
	case errors.Is(err, errQueueTimeout):
		status = http.StatusServiceUnavailable
		writeOpenAIError(w, http.StatusServiceUnavailable, "timed out waiting for model queue", "devrail_queue_timeout", "queue_timeout")
	default:
		writeOpenAIError(w, http.StatusRequestTimeout, "request canceled while waiting for model queue", "devrail_queue_canceled", "queue_canceled")
	}

	slog.Warn(
		"rejected queued request",
		"request_id", requestID,
		"alias", model.ID,
		"active", snapshot.active,
		"queued", snapshot.queued,
		"wait_ms", waited.Milliseconds(),
		"error", err,
	)
	metrics := requestMetricsFromModel(model, backend, status, time.Now())
	metrics.QueueWait = waited
	s.metrics.record(metrics)
	return waited, nil, false
}

func (s *Server) ensureModelReady(w http.ResponseWriter, r *http.Request, model config.ModelConfig, requestID string) bool {
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
		return s.ensureModelReadyWithCommand(w, r, model, requestID)
	default:
		writeOpenAIError(w, http.StatusInternalServerError, "model ensure mode is unsupported", "devrail_ensure_unsupported", "ensure_unsupported")
		return false
	}
}

func (s *Server) ensureModelReadyWithCommand(w http.ResponseWriter, r *http.Request, model config.ModelConfig, requestID string) bool {
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
			"request_id", requestID,
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
		"request_id", requestID,
		"alias", model.ID,
		"target_model", model.TargetModel,
		"output", outputText,
	)
	return true
}

type responseTelemetry struct {
	RequestID        string
	Alias            string
	TargetModel      string
	Backend          string
	UpstreamModel    string
	Status           int
	Streaming        bool
	FirstEvent       time.Time
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Bytes            int64
	QueueWait        time.Duration
	Started          time.Time
	metrics          *metricsRegistry
}

type telemetryReadCloser struct {
	body      io.ReadCloser
	telemetry *responseTelemetry
	once      sync.Once
}

func (body *telemetryReadCloser) Read(p []byte) (int, error) {
	n, err := body.body.Read(p)
	body.telemetry.Bytes += int64(n)
	if errors.Is(err, io.EOF) {
		body.log()
	}
	return n, err
}

func (body *telemetryReadCloser) Close() error {
	err := body.body.Close()
	body.log()
	return err
}

func (body *telemetryReadCloser) log() {
	body.once.Do(func() {
		logTelemetry(body.telemetry)
	})
}

func (telemetry responseTelemetry) firstEventMilliseconds() int64 {
	if telemetry.FirstEvent.IsZero() {
		return 0
	}

	return telemetry.FirstEvent.Sub(telemetry.Started).Milliseconds()
}

type streamTelemetryReadCloser struct {
	body       io.ReadCloser
	telemetry  *responseTelemetry
	lineBuffer string
	once       sync.Once
}

func (body *streamTelemetryReadCloser) Read(p []byte) (int, error) {
	n, err := body.body.Read(p)
	body.telemetry.Bytes += int64(n)
	body.observe(p[:n])
	if errors.Is(err, io.EOF) {
		body.log()
	}
	return n, err
}

func (body *streamTelemetryReadCloser) Close() error {
	err := body.body.Close()
	body.log()
	return err
}

func (body *streamTelemetryReadCloser) observe(chunk []byte) {
	if len(chunk) == 0 {
		return
	}

	body.lineBuffer += string(chunk)
	for {
		index := strings.IndexByte(body.lineBuffer, '\n')
		if index < 0 {
			break
		}
		line := strings.TrimRight(body.lineBuffer[:index], "\r")
		body.lineBuffer = body.lineBuffer[index+1:]
		body.observeLine(line)
	}

	if len(body.lineBuffer) > 64*1024 {
		body.lineBuffer = ""
	}
}

func (body *streamTelemetryReadCloser) observeLine(line string) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return
	}

	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "" || data == "[DONE]" {
		return
	}

	if body.telemetry.FirstEvent.IsZero() {
		body.telemetry.FirstEvent = time.Now()
	}
	applyOpenAIUsageTelemetry([]byte(data), body.telemetry)
}

func (body *streamTelemetryReadCloser) log() {
	body.once.Do(func() {
		logTelemetry(body.telemetry)
	})
}

func logTelemetry(telemetry *responseTelemetry) {
	telemetry.metrics.record(requestMetrics{
		Alias:            telemetry.Alias,
		TargetModel:      telemetry.TargetModel,
		Backend:          telemetry.Backend,
		Status:           telemetry.Status,
		Streaming:        telemetry.Streaming,
		FirstEvent:       telemetry.FirstEvent,
		PromptTokens:     telemetry.PromptTokens,
		CompletionTokens: telemetry.CompletionTokens,
		TotalTokens:      telemetry.TotalTokens,
		Bytes:            telemetry.Bytes,
		QueueWait:        telemetry.QueueWait,
		Started:          telemetry.Started,
	})
	slog.Info(
		"backend response completed",
		"request_id", telemetry.RequestID,
		"alias", telemetry.Alias,
		"target_model", telemetry.TargetModel,
		"upstream_model", telemetry.UpstreamModel,
		"backend", telemetry.Backend,
		"status", telemetry.Status,
		"streaming", telemetry.Streaming,
		"first_event_ms", telemetry.firstEventMilliseconds(),
		"duration_ms", time.Since(telemetry.Started).Milliseconds(),
		"bytes", telemetry.Bytes,
		"prompt_tokens", telemetry.PromptTokens,
		"completion_tokens", telemetry.CompletionTokens,
		"total_tokens", telemetry.TotalTokens,
	)
}

func instrumentBackendResponse(
	resp *http.Response,
	started time.Time,
	queueWait time.Duration,
	model config.ModelConfig,
	backend config.BackendConfig,
	requestID string,
	metrics *metricsRegistry,
) {
	if resp.Body == nil {
		metrics.record(requestMetricsFromModel(model, backend, resp.StatusCode, started))
		return
	}

	telemetry := &responseTelemetry{
		RequestID:   requestID,
		Alias:       model.ID,
		TargetModel: model.TargetModel,
		Backend:     backend.ID,
		Status:      resp.StatusCode,
		Started:     started,
		QueueWait:   queueWait,
		metrics:     metrics,
	}

	if isEventStreamResponse(resp) {
		telemetry.Streaming = true
		resp.Body = &streamTelemetryReadCloser{body: resp.Body, telemetry: telemetry}
		return
	}

	if isJSONResponse(resp) {
		raw, err := io.ReadAll(resp.Body)
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Warn("close backend response body", "alias", model.ID, "backend", backend.ID, "error", closeErr)
		}
		if err == nil {
			applyOpenAIUsageTelemetry(raw, telemetry)
			resp.Body = io.NopCloser(bytes.NewReader(raw))
			resp.ContentLength = int64(len(raw))
			resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(raw)))
		} else {
			slog.Warn("read backend response body for telemetry", "alias", model.ID, "backend", backend.ID, "error", err)
			resp.Body = io.NopCloser(bytes.NewReader(nil))
			resp.ContentLength = 0
			resp.Header.Set("Content-Length", "0")
		}
	}

	resp.Body = &telemetryReadCloser{body: resp.Body, telemetry: telemetry}
}

func isEventStreamResponse(resp *http.Response) bool {
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	return strings.Contains(contentType, "text/event-stream")
}

func isJSONResponse(resp *http.Response) bool {
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	return strings.Contains(contentType, "application/json")
}

func applyOpenAIUsageTelemetry(raw []byte, telemetry *responseTelemetry) {
	var payload struct {
		Model string `json:"model"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}

	telemetry.UpstreamModel = payload.Model
	telemetry.PromptTokens = payload.Usage.PromptTokens
	telemetry.CompletionTokens = payload.Usage.CompletionTokens
	telemetry.TotalTokens = payload.Usage.TotalTokens
}

type requestMetrics struct {
	Alias            string
	TargetModel      string
	Backend          string
	Status           int
	Streaming        bool
	FirstEvent       time.Time
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Bytes            int64
	QueueWait        time.Duration
	Started          time.Time
}

type metricsRegistry struct {
	mu                sync.Mutex
	requests          map[string]*metricSeries
	durationSeconds   histogram
	queueWaitSeconds  histogram
	firstEventSeconds histogram
	responseBytes     float64
	promptTokens      float64
	completionTokens  float64
	totalTokens       float64
}

type metricSeries struct {
	Labels metricLabels
	Value  float64
}

type metricLabels struct {
	Alias       string
	Backend     string
	TargetModel string
	Status      string
	Streaming   string
	Le          string
}

type histogram struct {
	Buckets []float64
	Series  map[string]*histogramSeries
}

type histogramSeries struct {
	Labels  metricLabels
	Counts  []uint64
	Count   uint64
	Sum     float64
	HasData bool
}

func newMetricsRegistry() *metricsRegistry {
	latencyBuckets := []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300}
	return &metricsRegistry{
		requests: make(map[string]*metricSeries),
		durationSeconds: histogram{
			Buckets: latencyBuckets,
			Series:  make(map[string]*histogramSeries),
		},
		queueWaitSeconds: histogram{
			Buckets: latencyBuckets,
			Series:  make(map[string]*histogramSeries),
		},
		firstEventSeconds: histogram{
			Buckets: latencyBuckets,
			Series:  make(map[string]*histogramSeries),
		},
	}
}

func requestMetricsFromModel(model config.ModelConfig, backend config.BackendConfig, status int, started time.Time) requestMetrics {
	return requestMetrics{
		Alias:       model.ID,
		TargetModel: model.TargetModel,
		Backend:     backend.ID,
		Status:      status,
		Started:     started,
	}
}

func (registry *metricsRegistry) record(metrics requestMetrics) {
	if registry == nil {
		return
	}

	labels := metricLabels{
		Alias:       metrics.Alias,
		Backend:     metrics.Backend,
		TargetModel: metrics.TargetModel,
		Status:      strconv.Itoa(metrics.Status),
		Streaming:   strconv.FormatBool(metrics.Streaming),
	}
	duration := time.Since(metrics.Started).Seconds()
	if metrics.Started.IsZero() {
		duration = 0
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()

	key := labels.key()
	if registry.requests[key] == nil {
		registry.requests[key] = &metricSeries{Labels: labels}
	}
	registry.requests[key].Value++
	registry.durationSeconds.observe(labels, duration)
	registry.queueWaitSeconds.observe(labels, metrics.QueueWait.Seconds())
	if !metrics.FirstEvent.IsZero() {
		registry.firstEventSeconds.observe(labels, metrics.FirstEvent.Sub(metrics.Started).Seconds())
	}
	registry.responseBytes += float64(metrics.Bytes)
	registry.promptTokens += float64(metrics.PromptTokens)
	registry.completionTokens += float64(metrics.CompletionTokens)
	registry.totalTokens += float64(metrics.TotalTokens)
}

func (hist *histogram) observe(labels metricLabels, value float64) {
	key := labels.key()
	series := hist.Series[key]
	if series == nil {
		series = &histogramSeries{
			Labels: labels,
			Counts: make([]uint64, len(hist.Buckets)),
		}
		hist.Series[key] = series
	}
	for index, bucket := range hist.Buckets {
		if value <= bucket {
			series.Counts[index]++
		}
	}
	series.Count++
	series.Sum += value
	series.HasData = true
}

func (registry *metricsRegistry) render() string {
	registry.mu.Lock()
	defer registry.mu.Unlock()

	var builder strings.Builder
	writeMetricHelp(&builder, "devrail_router_requests_total", "Total proxied or router-rejected OpenAI-compatible requests.")
	writeMetricType(&builder, "devrail_router_requests_total", "counter")
	for _, series := range sortedMetricSeries(registry.requests) {
		writeMetricLine(&builder, "devrail_router_requests_total", series.Labels, series.Value)
	}

	writeHistogram(&builder, "devrail_router_request_duration_seconds", "End-to-end router request duration in seconds.", registry.durationSeconds)
	writeHistogram(&builder, "devrail_router_queue_wait_seconds", "Time spent waiting for a model concurrency slot in seconds.", registry.queueWaitSeconds)
	writeHistogram(&builder, "devrail_router_first_event_latency_seconds", "Time to first Server-Sent Event for streamed upstream responses in seconds.", registry.firstEventSeconds)

	writeMetricHelp(&builder, "devrail_router_response_bytes_total", "Total response body bytes proxied from upstream backends.")
	writeMetricType(&builder, "devrail_router_response_bytes_total", "counter")
	writeMetricLine(&builder, "devrail_router_response_bytes_total", metricLabels{}, registry.responseBytes)

	writeMetricHelp(&builder, "devrail_router_prompt_tokens_total", "Total OpenAI-compatible prompt tokens reported by upstream backends.")
	writeMetricType(&builder, "devrail_router_prompt_tokens_total", "counter")
	writeMetricLine(&builder, "devrail_router_prompt_tokens_total", metricLabels{}, registry.promptTokens)

	writeMetricHelp(&builder, "devrail_router_completion_tokens_total", "Total OpenAI-compatible completion tokens reported by upstream backends.")
	writeMetricType(&builder, "devrail_router_completion_tokens_total", "counter")
	writeMetricLine(&builder, "devrail_router_completion_tokens_total", metricLabels{}, registry.completionTokens)

	writeMetricHelp(&builder, "devrail_router_total_tokens_total", "Total OpenAI-compatible total tokens reported by upstream backends.")
	writeMetricType(&builder, "devrail_router_total_tokens_total", "counter")
	writeMetricLine(&builder, "devrail_router_total_tokens_total", metricLabels{}, registry.totalTokens)

	return builder.String()
}

func writeHistogram(builder *strings.Builder, name, help string, hist histogram) {
	writeMetricHelp(builder, name, help)
	writeMetricType(builder, name, "histogram")
	for _, series := range sortedHistogramSeries(hist.Series) {
		if !series.HasData {
			continue
		}
		for index, bucket := range hist.Buckets {
			labels := series.Labels.with("le", strconv.FormatFloat(bucket, 'g', -1, 64))
			writeMetricLine(builder, name+"_bucket", labels, float64(series.Counts[index]))
		}
		writeMetricLine(builder, name+"_bucket", series.Labels.with("le", "+Inf"), float64(series.Count))
		writeMetricLine(builder, name+"_sum", series.Labels, series.Sum)
		writeMetricLine(builder, name+"_count", series.Labels, float64(series.Count))
	}
}

func writeMetricHelp(builder *strings.Builder, name, help string) {
	fmt.Fprintf(builder, "# HELP %s %s\n", name, help)
}

func writeMetricType(builder *strings.Builder, name, metricType string) {
	fmt.Fprintf(builder, "# TYPE %s %s\n", name, metricType)
}

func writeMetricLine(builder *strings.Builder, name string, labels metricLabels, value float64) {
	fmt.Fprintf(builder, "%s%s %s\n", name, labels.prometheus(), strconv.FormatFloat(value, 'f', -1, 64))
}

func sortedMetricSeries(series map[string]*metricSeries) []*metricSeries {
	keys := make([]string, 0, len(series))
	for key := range series {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	values := make([]*metricSeries, 0, len(keys))
	for _, key := range keys {
		values = append(values, series[key])
	}
	return values
}

func sortedHistogramSeries(series map[string]*histogramSeries) []*histogramSeries {
	keys := make([]string, 0, len(series))
	for key := range series {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	values := make([]*histogramSeries, 0, len(keys))
	for _, key := range keys {
		values = append(values, series[key])
	}
	return values
}

func (labels metricLabels) key() string {
	return labels.Alias + "\xff" + labels.Backend + "\xff" + labels.TargetModel + "\xff" + labels.Status + "\xff" + labels.Streaming
}

func (labels metricLabels) with(name, value string) metricLabels {
	switch name {
	case "le":
		labels.Le = value
	}
	return labels
}

func (labels metricLabels) prometheus() string {
	parts := []string{}
	if labels.Alias != "" {
		parts = append(parts, `alias="`+escapePrometheusLabel(labels.Alias)+`"`)
	}
	if labels.Backend != "" {
		parts = append(parts, `backend="`+escapePrometheusLabel(labels.Backend)+`"`)
	}
	if labels.TargetModel != "" {
		parts = append(parts, `target_model="`+escapePrometheusLabel(labels.TargetModel)+`"`)
	}
	if labels.Status != "" {
		parts = append(parts, `status="`+escapePrometheusLabel(labels.Status)+`"`)
	}
	if labels.Streaming != "" {
		parts = append(parts, `streaming="`+escapePrometheusLabel(labels.Streaming)+`"`)
	}
	if labels.Le != "" {
		parts = append(parts, `le="`+escapePrometheusLabel(labels.Le)+`"`)
	}
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escapePrometheusLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

func devrailRequestID(r *http.Request) string {
	for _, header := range []string{"X-Devrail-Request-ID", "X-Request-ID"} {
		value := strings.TrimSpace(r.Header.Get(header))
		if value != "" {
			return value
		}
	}

	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}

	return fmt.Sprintf("%d", time.Now().UnixNano())
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

type routingFeatures struct {
	PromptChars       int
	Text              string
	RequestedMaxToken int
}

func (s *Server) selectTargetModel(ctx context.Context, body []byte, model config.ModelConfig, requestID string) (string, string) {
	features := extractRoutingFeatures(body)
	for _, rule := range model.Routing.Rules {
		if routingRuleMatches(rule, features) {
			if rule.ID != "" {
				return rule.TargetModel, rule.ID
			}
			return rule.TargetModel, rule.TargetModel
		}
	}

	if model.Routing.Classifier.Enabled() {
		target, ok := s.classifyTargetModel(ctx, model, features, requestID)
		if ok {
			return target, "classifier"
		}
	}

	return model.TargetModel, "default"
}

func (s *Server) classifyTargetModel(ctx context.Context, model config.ModelConfig, features routingFeatures, requestID string) (string, bool) {
	classifier := model.Routing.Classifier
	backend, ok := s.cfg.Backend(classifier.Backend)
	if !ok {
		slog.Warn("routing classifier backend is missing", "request_id", requestID, "alias", model.ID, "backend", classifier.Backend)
		return "", false
	}

	timeout, err := classifier.TimeoutDuration()
	if err != nil {
		slog.Warn("routing classifier timeout is invalid", "request_id", requestID, "alias", model.ID, "error", err)
		return "", false
	}
	classifierCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	requestPayload := map[string]any{
		"model":       classifier.Model,
		"messages":    classifierMessages(classifier, features),
		"temperature": 0,
		"stream":      false,
		"max_tokens":  classifierMaxTokens(classifier),
	}
	requestBody, err := json.Marshal(requestPayload)
	if err != nil {
		slog.Warn("routing classifier request could not be encoded", "request_id", requestID, "alias", model.ID, "error", err)
		return "", false
	}

	endpoint, err := backendChatCompletionsURL(backend)
	if err != nil {
		slog.Warn("routing classifier backend URL is invalid", "request_id", requestID, "alias", model.ID, "backend", backend.ID, "error", err)
		return "", false
	}

	req, err := http.NewRequestWithContext(classifierCtx, http.MethodPost, endpoint, bytes.NewReader(requestBody))
	if err != nil {
		slog.Warn("routing classifier request could not be created", "request_id", requestID, "alias", model.ID, "error", err)
		return "", false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", requestID)
	setBackendAuth(req, backend)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("routing classifier request failed", "request_id", requestID, "alias", model.ID, "error", err)
		return "", false
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		slog.Warn("routing classifier response could not be read", "request_id", requestID, "alias", model.ID, "status", resp.StatusCode, "error", err)
		return "", false
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("routing classifier returned non-success status", "request_id", requestID, "alias", model.ID, "status", resp.StatusCode, "body", trimForLog(string(responseBody), 2048))
		return "", false
	}

	target, ok := classifierTargetFromResponse(responseBody, classifier.TargetModels)
	if !ok {
		slog.Warn("routing classifier returned no allowed target", "request_id", requestID, "alias", model.ID, "allowed_targets", strings.Join(classifier.TargetModels, ","), "body", trimForLog(string(responseBody), 2048))
		return "", false
	}

	slog.Info("routing classifier selected target", "request_id", requestID, "alias", model.ID, "target_model", target)
	return target, true
}

func extractRoutingFeatures(body []byte) routingFeatures {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return routingFeatures{}
	}

	text := requestText(payload)
	return routingFeatures{
		PromptChars:       len(text),
		Text:              strings.ToLower(text),
		RequestedMaxToken: requestMaxTokens(payload),
	}
}

func routingRuleMatches(rule config.RoutingRuleConfig, features routingFeatures) bool {
	if rule.MinPromptChars > 0 && features.PromptChars < rule.MinPromptChars {
		return false
	}
	if rule.MaxPromptChars > 0 && features.PromptChars > rule.MaxPromptChars {
		return false
	}
	if rule.MinOutputTokens > 0 && features.RequestedMaxToken < rule.MinOutputTokens {
		return false
	}
	if rule.MaxOutputTokens > 0 && features.RequestedMaxToken > 0 && features.RequestedMaxToken > rule.MaxOutputTokens {
		return false
	}
	if len(rule.AnyKeywords) > 0 {
		for _, keyword := range rule.AnyKeywords {
			if strings.Contains(features.Text, strings.ToLower(strings.TrimSpace(keyword))) {
				return true
			}
		}
		return false
	}

	return true
}

func requestText(payload map[string]any) string {
	var builder strings.Builder
	appendTextValue(&builder, payload["messages"])
	appendTextValue(&builder, payload["input"])
	appendTextValue(&builder, payload["prompt"])
	return builder.String()
}

func appendTextValue(builder *strings.Builder, value any) {
	switch typed := value.(type) {
	case string:
		builder.WriteString(typed)
		builder.WriteByte('\n')
	case []any:
		for _, item := range typed {
			appendTextValue(builder, item)
		}
	case map[string]any:
		for _, key := range []string{"role", "content", "text", "input_text", "prompt"} {
			appendTextValue(builder, typed[key])
		}
	}
}

func requestMaxTokens(payload map[string]any) int {
	for _, key := range []string{"max_completion_tokens", "max_tokens"} {
		if value, ok := numericJSONValue(payload[key]); ok {
			return value
		}
	}
	return 0
}

func numericJSONValue(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	default:
		return 0, false
	}
}

func classifierMessages(classifier config.RoutingClassifierConfig, features routingFeatures) []map[string]string {
	systemPrompt := strings.TrimSpace(classifier.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = defaultClassifierSystemPrompt(classifier.TargetModels)
	}

	return []map[string]string{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": classifierUserPrompt(features, classifier.TargetModels)},
	}
}

func defaultClassifierSystemPrompt(targets []string) string {
	return "You route coding-agent requests to the cheapest adequate model. Return only JSON with target_model set to one of: " + strings.Join(targets, ", ") + ". Choose the stronger model for architecture, debugging, security, migrations, production risk, broad refactors, ambiguous planning, long-context synthesis, or high requested output. Choose the faster model for routine edits, short questions, status checks, formatting, simple commands, and low-risk local changes."
}

func classifierUserPrompt(features routingFeatures, targets []string) string {
	text := features.Text
	if len(text) > 6000 {
		head := text[:3000]
		tail := text[len(text)-3000:]
		text = head + "\n...[middle omitted]...\n" + tail
	}

	return fmt.Sprintf(
		"/no_think\nAllowed targets: %s\nPrompt chars: %d\nRequested max output tokens: %d\nRequest text:\n%s\n\nReturn only: {\"target_model\":\"<one allowed target>\"}",
		strings.Join(targets, ", "),
		features.PromptChars,
		features.RequestedMaxToken,
		text,
	)
}

func classifierMaxTokens(classifier config.RoutingClassifierConfig) int {
	if classifier.MaxTokens > 0 {
		return classifier.MaxTokens
	}
	return 64
}

func backendChatCompletionsURL(backend config.BackendConfig) (string, error) {
	parsed, err := url.Parse(backend.BaseURL)
	if err != nil {
		return "", err
	}
	parsed.Path = joinOpenAIPath(parsed.Path, "/v1/chat/completions")
	return parsed.String(), nil
}

func classifierTargetFromResponse(body []byte, allowed []string) (string, bool) {
	text := classifierResponseText(body)
	if target, ok := classifierTargetFromText(text, allowed); ok {
		return target, true
	}
	return classifierTargetFromText(string(body), allowed)
}

func classifierResponseText(body []byte) string {
	var payload struct {
		Choices []struct {
			Message map[string]any `json:"message"`
			Text    string         `json:"text"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Choices) == 0 {
		return ""
	}

	var builder strings.Builder
	for _, choice := range payload.Choices {
		builder.WriteString(choice.Text)
		builder.WriteByte('\n')
		for _, key := range []string{"content", "reasoning_content"} {
			if value, ok := choice.Message[key].(string); ok {
				builder.WriteString(value)
				builder.WriteByte('\n')
			}
		}
	}
	return builder.String()
}

func classifierTargetFromText(text string, allowed []string) (string, bool) {
	var payload struct {
		TargetModel string `json:"target_model"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &payload); err == nil {
		for _, target := range allowed {
			if payload.TargetModel == target {
				return target, true
			}
		}
	}

	for _, target := range allowed {
		if strings.Contains(text, target) {
			return target, true
		}
	}
	return "", false
}

func trimForLog(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "...[truncated]"
}

func rewriteModel(body []byte, targetModel string, maxOutputTokens int) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse request body: %w", err)
	}

	payload["model"] = targetModel
	clampRequestMaxTokens(payload, maxOutputTokens)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode request body: %w", err)
	}

	return body, nil
}

func clampRequestMaxTokens(payload map[string]any, maxOutputTokens int) {
	if maxOutputTokens <= 0 {
		return
	}
	for _, key := range []string{"max_completion_tokens", "max_tokens"} {
		value, ok := numericJSONValue(payload[key])
		if !ok || value <= maxOutputTokens {
			continue
		}
		payload[key] = maxOutputTokens
	}
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
