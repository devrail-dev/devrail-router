package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

const DefaultPath = "/etc/devrail/router.yaml"

type Config struct {
	Server   ServerConfig    `yaml:"server"`
	Models   []ModelConfig   `yaml:"models"`
	Backends []BackendConfig `yaml:"backends"`
}

type ServerConfig struct {
	Address string `yaml:"address"`
}

type ModelConfig struct {
	ID                    string `yaml:"id"`
	Name                  string `yaml:"name"`
	Backend               string `yaml:"backend"`
	TargetModel           string `yaml:"target_model"`
	ContextWindow         int    `yaml:"context_window"`
	MaxOutputTokens       int    `yaml:"max_output_tokens"`
	ToolCalls             bool   `yaml:"tool_calls"`
	MaxConcurrentRequests int    `yaml:"max_concurrent_requests"`
	MaxQueueSize          int    `yaml:"max_queue_size"`
	QueueTimeout          string `yaml:"queue_timeout"`
}

type BackendConfig struct {
	ID        string `yaml:"id"`
	Type      string `yaml:"type"`
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"`
}

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, err
	}

	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (cfg *Config) ApplyDefaults() {
	if cfg.Server.Address == "" {
		cfg.Server.Address = "127.0.0.1:8080"
	}
}

func (cfg Config) Validate() error {
	if len(cfg.Backends) == 0 {
		return errors.New("at least one backend is required")
	}
	if len(cfg.Models) == 0 {
		return errors.New("at least one model alias is required")
	}

	backends := make(map[string]BackendConfig, len(cfg.Backends))
	for _, backend := range cfg.Backends {
		if backend.ID == "" {
			return errors.New("backend id is required")
		}
		if backend.BaseURL == "" {
			return fmt.Errorf("backend %q base_url is required", backend.ID)
		}
		if _, err := url.ParseRequestURI(backend.BaseURL); err != nil {
			return fmt.Errorf("backend %q base_url is invalid: %w", backend.ID, err)
		}
		if _, ok := backends[backend.ID]; ok {
			return fmt.Errorf("backend %q is duplicated", backend.ID)
		}
		backends[backend.ID] = backend
	}

	models := make(map[string]struct{}, len(cfg.Models))
	for _, model := range cfg.Models {
		if model.ID == "" {
			return errors.New("model id is required")
		}
		if model.Backend == "" {
			return fmt.Errorf("model %q backend is required", model.ID)
		}
		if _, ok := backends[model.Backend]; !ok {
			return fmt.Errorf("model %q references unknown backend %q", model.ID, model.Backend)
		}
		if model.TargetModel == "" {
			return fmt.Errorf("model %q target_model is required", model.ID)
		}
		if model.MaxConcurrentRequests < 0 {
			return fmt.Errorf("model %q max_concurrent_requests must be non-negative", model.ID)
		}
		if model.MaxQueueSize < 0 {
			return fmt.Errorf("model %q max_queue_size must be non-negative", model.ID)
		}
		if _, err := model.QueueTimeoutDuration(); err != nil {
			return fmt.Errorf("model %q queue_timeout is invalid: %w", model.ID, err)
		}
		if _, ok := models[model.ID]; ok {
			return fmt.Errorf("model %q is duplicated", model.ID)
		}
		models[model.ID] = struct{}{}
	}

	return nil
}

func (cfg Config) Model(id string) (ModelConfig, bool) {
	for _, model := range cfg.Models {
		if model.ID == id {
			return model, true
		}
	}

	return ModelConfig{}, false
}

func (cfg Config) Backend(id string) (BackendConfig, bool) {
	for _, backend := range cfg.Backends {
		if backend.ID == id {
			return backend, true
		}
	}

	return BackendConfig{}, false
}

func (model ModelConfig) QueueTimeoutDuration() (time.Duration, error) {
	if model.QueueTimeout == "" {
		return 0, nil
	}

	duration, err := time.ParseDuration(model.QueueTimeout)
	if err != nil {
		return 0, err
	}
	if duration < 0 {
		return 0, errors.New("duration must be non-negative")
	}

	return duration, nil
}
