package zbplan

import (
	"fmt"
	"time"
)

// Limits bounds model, tool, build, memory, and wall-clock work for one Run.
// Zero-valued fields use DefaultLimits; negative values are invalid.
type Limits struct {
	MaxBuildAttempts           int
	MaxAgentSteps              int
	MaxModelRequests           int
	MaxToolCalls               int
	MaxParallelToolCalls       int
	MaxRetainedToolOutputBytes int
	MaxBuildLogBytes           int
	RunTimeout                 time.Duration
	ToolTimeout                time.Duration
	BuildTimeout               time.Duration
}

// DefaultLimits returns secure operational limits for one Run.
func DefaultLimits() Limits {
	return Limits{
		MaxBuildAttempts:           3,
		MaxAgentSteps:              16,
		MaxModelRequests:           24,
		MaxToolCalls:               24,
		MaxParallelToolCalls:       4,
		MaxRetainedToolOutputBytes: 1 << 20,
		MaxBuildLogBytes:           128 << 10,
		RunTimeout:                 15 * time.Minute,
		ToolTimeout:                30 * time.Second,
		BuildTimeout:               10 * time.Minute,
	}
}

func (l Limits) normalized() (Limits, error) {
	defaults := DefaultLimits()
	if l.MaxBuildAttempts == 0 {
		l.MaxBuildAttempts = defaults.MaxBuildAttempts
	}
	if l.MaxAgentSteps == 0 {
		l.MaxAgentSteps = defaults.MaxAgentSteps
	}
	if l.MaxModelRequests == 0 {
		l.MaxModelRequests = defaults.MaxModelRequests
	}
	if l.MaxToolCalls == 0 {
		l.MaxToolCalls = defaults.MaxToolCalls
	}
	if l.MaxParallelToolCalls == 0 {
		l.MaxParallelToolCalls = defaults.MaxParallelToolCalls
	}
	if l.MaxRetainedToolOutputBytes == 0 {
		l.MaxRetainedToolOutputBytes = defaults.MaxRetainedToolOutputBytes
	}
	if l.MaxBuildLogBytes == 0 {
		l.MaxBuildLogBytes = defaults.MaxBuildLogBytes
	}
	if l.RunTimeout == 0 {
		l.RunTimeout = defaults.RunTimeout
	}
	if l.ToolTimeout == 0 {
		l.ToolTimeout = defaults.ToolTimeout
	}
	if l.BuildTimeout == 0 {
		l.BuildTimeout = defaults.BuildTimeout
	}

	if l.MaxBuildAttempts < 1 || l.MaxAgentSteps < 1 || l.MaxModelRequests < 1 || l.MaxToolCalls < 1 || l.MaxParallelToolCalls < 1 || l.MaxRetainedToolOutputBytes < 1 || l.MaxBuildLogBytes < 1 || l.RunTimeout < 0 || l.ToolTimeout < 0 || l.BuildTimeout < 0 {
		return Limits{}, fmt.Errorf("zbplan: limits must be positive")
	}
	return l, nil
}

// RunStats reports host-observed work for a completed Run.
type RunStats struct {
	ModelRequests           int
	ToolCalls               int
	BuildSolves             int
	RetainedToolOutputBytes int
	Duration                time.Duration
}
