package zbplan

import (
	"context"
	"fmt"
	"os"
	"strings"

	claude "github.com/cloudwego/eino-ext/components/model/claude"
	openai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

// ClaudeConfig holds settings for NewClaudeModel.
// Zero values use the documented defaults.
type ClaudeConfig struct {
	// APIKey is the Anthropic API key. Required.
	APIKey string
	// Model defaults to "claude-sonnet-4-6" when empty.
	Model string
	// BaseURL overrides the Anthropic API endpoint when non-empty.
	BaseURL string
	// MaxTokens defaults to 16000 when zero. Note: with adaptive thinking
	// enabled, max_tokens covers both thinking tokens and response text.
	MaxTokens int
	// DisableThinking disables adaptive thinking when true.
	// By default, adaptive thinking is enabled.
	DisableThinking bool
}

// OpenAIConfig holds settings for NewOpenAIModel.
type OpenAIConfig struct {
	// APIKey is the OpenAI API key. Required.
	APIKey string
	// Model defaults to "gpt-5.5" when empty.
	Model string
	// BaseURL overrides the OpenAI API endpoint when non-empty.
	BaseURL string
}

// NewClaudeModel returns a Claude tool-calling model from the given config.
func NewClaudeModel(ctx context.Context, cfg ClaudeConfig) (model.ToolCallingChatModel, error) {
	if cfg.Model == "" {
		cfg.Model = "claude-sonnet-4-6"
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 16000
	}

	c := &claude.Config{
		APIKey:    cfg.APIKey,
		Model:     cfg.Model,
		MaxTokens: cfg.MaxTokens,
	}
	if !cfg.DisableThinking {
		// eino-ext only exposes budget_tokens thinking; inject adaptive thinking
		// via AdditionalRequestFields so it is set at the JSON level instead.
		c.AdditionalRequestFields = map[string]any{
			"thinking": map[string]any{"type": "adaptive"},
			"effort":   "high",
		}
	}
	if cfg.BaseURL != "" {
		c.BaseURL = &cfg.BaseURL
	}
	return claude.NewChatModel(ctx, c)
}

// NewOpenAIModel returns an OpenAI tool-calling model from the given config.
func NewOpenAIModel(ctx context.Context, cfg OpenAIConfig) (model.ToolCallingChatModel, error) {
	if cfg.Model == "" {
		cfg.Model = "gpt-5.5"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	c := &openai.ChatModelConfig{
		APIKey:  cfg.APIKey,
		Model:   cfg.Model,
		BaseURL: cfg.BaseURL,
		User:    new("zbplan"),
	}
	return openai.NewChatModel(ctx, c)
}

// NewClaudeModelFromEnv reads ZBPLAN_ANTHROPIC_API_KEY, ZBPLAN_ANTHROPIC_MODEL,
// and ZBPLAN_ANTHROPIC_BASE_URL from the environment, matching the default
// cmd/zbplan behavior.
func NewClaudeModelFromEnv(ctx context.Context) (model.ToolCallingChatModel, error) {
	apiKey := os.Getenv("ZBPLAN_ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("ZBPLAN_ANTHROPIC_API_KEY is not set")
	}
	return NewClaudeModel(ctx, ClaudeConfig{
		APIKey:  apiKey,
		Model:   os.Getenv("ZBPLAN_ANTHROPIC_MODEL"),
		BaseURL: os.Getenv("ZBPLAN_ANTHROPIC_BASE_URL"),
	})
}

// NewOpenAIModelFromEnv reads ZBPLAN_OPENAI_API_KEY, ZBPLAN_OPENAI_MODEL,
// and ZBPLAN_OPENAI_BASE_URL from the environment, matching the default
// cmd/zbplan behavior.
func NewOpenAIModelFromEnv(ctx context.Context) (model.ToolCallingChatModel, error) {
	apiKey := os.Getenv("ZBPLAN_OPENAI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("ZBPLAN_OPENAI_API_KEY is not set")
	}
	return NewOpenAIModel(ctx, OpenAIConfig{
		APIKey:  apiKey,
		Model:   os.Getenv("ZBPLAN_OPENAI_MODEL"),
		BaseURL: os.Getenv("ZBPLAN_OPENAI_BASE_URL"),
	})
}

// NewModelFromEnv reads the appropriate model configuration from the environment.
// ZBPLAN_MODEL defaults to "claude" when unset.
func NewModelFromEnv(ctx context.Context) (model.ToolCallingChatModel, error) {
	modelType := strings.ToLower(os.Getenv("ZBPLAN_MODEL"))
	switch modelType {
	case "", "claude":
		return NewClaudeModelFromEnv(ctx)
	case "openai":
		return NewOpenAIModelFromEnv(ctx)
	default:
		return nil, fmt.Errorf("invalid ZBPLAN_MODEL %q (expected: claude|openai)", modelType)
	}
}
