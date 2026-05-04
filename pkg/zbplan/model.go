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
	// MaxTokens defaults to 8192 when zero.
	MaxTokens int
	// ThinkingBudget is the extended-thinking token budget. Defaults to 4096
	// when zero. Set to -1 to disable extended thinking entirely.
	ThinkingBudget int
}

// NewClaudeModel returns a Claude tool-calling model from the given config.
func NewClaudeModel(ctx context.Context, cfg ClaudeConfig) (model.ToolCallingChatModel, error) {
	if cfg.Model == "" {
		cfg.Model = "claude-sonnet-4-6"
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 8192
	}
	if cfg.ThinkingBudget == 0 {
		cfg.ThinkingBudget = 4096
	}

	c := &claude.Config{
		APIKey:    cfg.APIKey,
		Model:     cfg.Model,
		MaxTokens: cfg.MaxTokens,
	}
	if cfg.ThinkingBudget > 0 {
		c.Thinking = &claude.Thinking{
			Enable:       true,
			BudgetTokens: cfg.ThinkingBudget,
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
