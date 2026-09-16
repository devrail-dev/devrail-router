package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
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
	Ensure                EnsureConfig
	Routing               RoutingConfig `yaml:"routing"`
}

type BackendConfig struct {
	ID        string `yaml:"id"`
	Type      string `yaml:"type"`
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"`
}

type EnsureConfig struct {
	Mode    string      `yaml:"mode"`
	Command CommandArgs `yaml:"command"`
	Timeout string      `yaml:"timeout"`
}

type RoutingConfig struct {
	Rules      []RoutingRuleConfig     `yaml:"rules"`
	Classifier RoutingClassifierConfig `yaml:"classifier"`
}

type RoutingRuleConfig struct {
	ID              string   `yaml:"id"`
	TargetModel     string   `yaml:"target_model"`
	MinPromptChars  int      `yaml:"min_prompt_chars"`
	MaxPromptChars  int      `yaml:"max_prompt_chars"`
	MinOutputTokens int      `yaml:"min_output_tokens"`
	MaxOutputTokens int      `yaml:"max_output_tokens"`
	AnyKeywords     []string `yaml:"any_keywords"`
}

type RoutingClassifierConfig struct {
	Backend      string   `yaml:"backend"`
	Model        string   `yaml:"model"`
	TargetModels []string `yaml:"target_models"`
	SystemPrompt string   `yaml:"system_prompt"`
	Timeout      string   `yaml:"timeout"`
	MaxTokens    int      `yaml:"max_tokens"`
}

type CommandArgs []string

func (args *CommandArgs) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var command string
		if err := value.Decode(&command); err != nil {
			return err
		}
		if command == "" {
			*args = nil
			return nil
		}
		*args = []string{"/bin/sh", "-c", command}
		return nil
	case yaml.SequenceNode:
		var command []string
		if err := value.Decode(&command); err != nil {
			return err
		}
		*args = command
		return nil
	default:
		return fmt.Errorf("command must be a string or list of strings")
	}
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
		if err := model.Ensure.Validate(); err != nil {
			return fmt.Errorf("model %q ensure is invalid: %w", model.ID, err)
		}
		if err := model.Routing.Validate(); err != nil {
			return fmt.Errorf("model %q routing is invalid: %w", model.ID, err)
		}
		if model.Routing.Classifier.Enabled() {
			if _, ok := backends[model.Routing.Classifier.Backend]; !ok {
				return fmt.Errorf("model %q routing classifier references unknown backend %q", model.ID, model.Routing.Classifier.Backend)
			}
		}
		if _, ok := models[model.ID]; ok {
			return fmt.Errorf("model %q is duplicated", model.ID)
		}
		models[model.ID] = struct{}{}
	}

	return nil
}

func (routing RoutingConfig) Validate() error {
	for index, rule := range routing.Rules {
		if rule.TargetModel == "" {
			return fmt.Errorf("rule %d target_model is required", index)
		}
		if rule.MinPromptChars < 0 {
			return fmt.Errorf("rule %d min_prompt_chars must be non-negative", index)
		}
		if rule.MaxPromptChars < 0 {
			return fmt.Errorf("rule %d max_prompt_chars must be non-negative", index)
		}
		if rule.MinPromptChars > 0 && rule.MaxPromptChars > 0 && rule.MinPromptChars > rule.MaxPromptChars {
			return fmt.Errorf("rule %d min_prompt_chars must be less than or equal to max_prompt_chars", index)
		}
		if rule.MinOutputTokens < 0 {
			return fmt.Errorf("rule %d min_output_tokens must be non-negative", index)
		}
		if rule.MaxOutputTokens < 0 {
			return fmt.Errorf("rule %d max_output_tokens must be non-negative", index)
		}
		if rule.MinOutputTokens > 0 && rule.MaxOutputTokens > 0 && rule.MinOutputTokens > rule.MaxOutputTokens {
			return fmt.Errorf("rule %d min_output_tokens must be less than or equal to max_output_tokens", index)
		}
		hasCondition := rule.MinPromptChars > 0 ||
			rule.MaxPromptChars > 0 ||
			rule.MinOutputTokens > 0 ||
			rule.MaxOutputTokens > 0 ||
			len(rule.AnyKeywords) > 0
		if !hasCondition {
			return fmt.Errorf("rule %d must define at least one condition", index)
		}
		for keywordIndex, keyword := range rule.AnyKeywords {
			if strings.TrimSpace(keyword) == "" {
				return fmt.Errorf("rule %d any_keywords[%d] must not be empty", index, keywordIndex)
			}
		}
	}

	if err := routing.Classifier.Validate(); err != nil {
		return fmt.Errorf("classifier is invalid: %w", err)
	}

	return nil
}

func (classifier RoutingClassifierConfig) Validate() error {
	if classifier.Backend == "" && classifier.Model == "" && len(classifier.TargetModels) == 0 && classifier.SystemPrompt == "" && classifier.Timeout == "" && classifier.MaxTokens == 0 {
		return nil
	}
	if classifier.Backend == "" {
		return errors.New("backend is required")
	}
	if classifier.Model == "" {
		return errors.New("model is required")
	}
	if len(classifier.TargetModels) == 0 {
		return errors.New("target_models is required")
	}
	for index, target := range classifier.TargetModels {
		if strings.TrimSpace(target) == "" {
			return fmt.Errorf("target_models[%d] must not be empty", index)
		}
	}
	if classifier.MaxTokens < 0 {
		return errors.New("max_tokens must be non-negative")
	}
	_, err := classifier.TimeoutDuration()
	return err
}

func (classifier RoutingClassifierConfig) Enabled() bool {
	return classifier.Backend != "" || classifier.Model != "" || len(classifier.TargetModels) > 0 || classifier.SystemPrompt != "" || classifier.Timeout != "" || classifier.MaxTokens != 0
}

func (classifier RoutingClassifierConfig) TimeoutDuration() (time.Duration, error) {
	if classifier.Timeout == "" {
		return 15 * time.Second, nil
	}

	duration, err := time.ParseDuration(classifier.Timeout)
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, errors.New("duration must be positive")
	}

	return duration, nil
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

func (ensure EnsureConfig) Validate() error {
	switch ensure.Mode {
	case "", "disabled":
		return nil
	case "command":
		if len(ensure.Command) == 0 {
			return errors.New("command is required when mode is command")
		}
		for _, arg := range ensure.Command {
			if arg == "" {
				return errors.New("command arguments must not be empty")
			}
		}
		_, err := ensure.TimeoutDuration()
		return err
	default:
		return fmt.Errorf("unknown mode %q", ensure.Mode)
	}
}

func (ensure EnsureConfig) TimeoutDuration() (time.Duration, error) {
	if ensure.Timeout == "" {
		return 30 * time.Second, nil
	}

	duration, err := time.ParseDuration(ensure.Timeout)
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, errors.New("duration must be positive")
	}

	return duration, nil
}
