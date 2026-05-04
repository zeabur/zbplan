package zbplan

import (
	"context"
	"fmt"
	"os"

	claude "github.com/cloudwego/eino-ext/components/model/claude"
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
		}
	}
	if cfg.BaseURL != "" {
		c.BaseURL = &cfg.BaseURL
	}
	return claude.NewChatModel(ctx, c)
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
