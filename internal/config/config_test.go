package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadValidConfig(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "router.yaml")
	raw := []byte(`
models:
  - id: local-coder
    backend: lmstudio
    target_model: qwen/qwen3.6-35b-a3b
backends:
  - id: lmstudio
    type: openai-compatible
    base_url: http://127.0.0.1:1234/v1
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Server.Address != "127.0.0.1:8080" {
		t.Fatalf("unexpected default address: %q", cfg.Server.Address)
	}
	if _, ok := cfg.Model("local-coder"); !ok {
		t.Fatal("expected local-coder model")
	}
}

func TestValidateQueueSettings(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Models: []ModelConfig{{
			ID:                    "local-coder",
			Backend:               "lmstudio",
			TargetModel:           "qwen/qwen3.6-35b-a3b",
			MaxConcurrentRequests: 1,
			MaxQueueSize:          2,
			QueueTimeout:          "250ms",
		}},
		Backends: []BackendConfig{{
			ID:      "lmstudio",
			BaseURL: "http://127.0.0.1:1234/v1",
		}},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate config: %v", err)
	}

	duration, err := cfg.Models[0].QueueTimeoutDuration()
	if err != nil {
		t.Fatalf("parse queue timeout: %v", err)
	}
	if duration != 250*time.Millisecond {
		t.Fatalf("unexpected queue timeout: %s", duration)
	}
}

func TestValidateInvalidQueueTimeout(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Models: []ModelConfig{{
			ID:           "local-coder",
			Backend:      "lmstudio",
			TargetModel:  "qwen/qwen3.6-35b-a3b",
			QueueTimeout: "eventually",
		}},
		Backends: []BackendConfig{{
			ID:      "lmstudio",
			BaseURL: "http://127.0.0.1:1234/v1",
		}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "queue_timeout") {
		t.Fatalf("expected queue_timeout error, got: %v", err)
	}
}

func TestValidateUnknownBackend(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Models: []ModelConfig{{
			ID:          "local-coder",
			Backend:     "missing",
			TargetModel: "qwen/qwen3.6-35b-a3b",
		}},
		Backends: []BackendConfig{{
			ID:      "lmstudio",
			BaseURL: "http://127.0.0.1:1234/v1",
		}},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}
