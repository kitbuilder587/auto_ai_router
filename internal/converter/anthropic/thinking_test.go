package anthropic

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMapReasoningEffortToBudget(t *testing.T) {
	tests := []struct {
		effort string
		want   int
	}{
		{"minimal", 1024},
		{"low", 5000},
		{"medium", 15000},
		{"high", 30000},
		{"disable", 0},
		{"none", 0},
		{"unknown", 0},
		{"", 0},
	}
	for _, tt := range tests {
		t.Run(tt.effort, func(t *testing.T) {
			assert.Equal(t, tt.want, mapReasoningEffortToBudget(tt.effort))
		})
	}
}

// Claude 3.x: expects type="enabled" + budget_tokens, no output_config.
func TestMapThinkingConfig_Classic(t *testing.T) {
	const model = "claude-3-7-sonnet-20250219"

	t.Run("nil_thinking_no_effort", func(t *testing.T) {
		tc, oc := mapThinkingConfig(nil, "", model)
		assert.Nil(t, tc)
		assert.Nil(t, oc)
	})

	t.Run("enabled_with_budget", func(t *testing.T) {
		thinking := map[string]interface{}{
			"type":          "enabled",
			"budget_tokens": float64(10000),
		}
		tc, oc := mapThinkingConfig(thinking, "", model)
		assert.NotNil(t, tc)
		assert.Equal(t, "enabled", tc.Type)
		assert.Equal(t, 10000, tc.BudgetTokens)
		assert.Nil(t, oc)
	})

	t.Run("disabled", func(t *testing.T) {
		thinking := map[string]interface{}{"type": "disabled"}
		tc, oc := mapThinkingConfig(thinking, "", model)
		assert.Nil(t, tc)
		assert.Nil(t, oc)
	})

	t.Run("enabled_no_budget_falls_to_reasoning_effort", func(t *testing.T) {
		thinking := map[string]interface{}{
			"type":          "enabled",
			"budget_tokens": float64(0),
		}
		tc, oc := mapThinkingConfig(thinking, "medium", model)
		assert.NotNil(t, tc)
		assert.Equal(t, "enabled", tc.Type)
		assert.Equal(t, 15000, tc.BudgetTokens)
		assert.Nil(t, oc)
	})

	t.Run("reasoning_effort_only", func(t *testing.T) {
		tc, oc := mapThinkingConfig(nil, "high", model)
		assert.NotNil(t, tc)
		assert.Equal(t, "enabled", tc.Type)
		assert.Equal(t, 30000, tc.BudgetTokens)
		assert.Nil(t, oc)
	})

	t.Run("reasoning_effort_disable", func(t *testing.T) {
		tc, oc := mapThinkingConfig(nil, "disable", model)
		assert.Nil(t, tc)
		assert.Nil(t, oc)
	})
}

// Claude Opus 4+ and mythos-preview: expects type="adaptive" + output_config.effort, no budget_tokens.
func TestMapThinkingConfig_Adaptive(t *testing.T) {
	models := []string{
		"claude-opus-4-7",
		"claude-opus-4-6",
		"claude-opus-4-5-20251101",
		"claude-mythos-preview",
	}

	for _, model := range models {
		model := model
		t.Run(model+"/reasoning_effort_high", func(t *testing.T) {
			tc, oc := mapThinkingConfig(nil, "high", model)
			assert.NotNil(t, tc)
			assert.Equal(t, "adaptive", tc.Type)
			assert.Equal(t, 0, tc.BudgetTokens)
			assert.NotNil(t, oc)
			assert.Equal(t, "high", oc.Effort)
		})

		t.Run(model+"/reasoning_effort_medium", func(t *testing.T) {
			tc, oc := mapThinkingConfig(nil, "medium", model)
			assert.NotNil(t, tc)
			assert.Equal(t, "adaptive", tc.Type)
			assert.NotNil(t, oc)
			assert.Equal(t, "medium", oc.Effort)
		})

		t.Run(model+"/reasoning_effort_minimal_maps_to_low", func(t *testing.T) {
			tc, oc := mapThinkingConfig(nil, "minimal", model)
			assert.NotNil(t, tc)
			assert.Equal(t, "adaptive", tc.Type)
			assert.NotNil(t, oc)
			assert.Equal(t, "low", oc.Effort)
		})

		t.Run(model+"/enabled_budget_converts_to_effort", func(t *testing.T) {
			thinking := map[string]interface{}{
				"type":          "enabled",
				"budget_tokens": float64(20000),
			}
			tc, oc := mapThinkingConfig(thinking, "", model)
			assert.NotNil(t, tc)
			assert.Equal(t, "adaptive", tc.Type)
			assert.NotNil(t, oc)
			assert.Equal(t, "high", oc.Effort)
		})

		t.Run(model+"/adaptive_type_passthrough", func(t *testing.T) {
			thinking := map[string]interface{}{
				"type":   "adaptive",
				"effort": "xhigh",
			}
			tc, oc := mapThinkingConfig(thinking, "", model)
			assert.NotNil(t, tc)
			assert.Equal(t, "adaptive", tc.Type)
			assert.NotNil(t, oc)
			assert.Equal(t, "xhigh", oc.Effort)
		})

		t.Run(model+"/disabled", func(t *testing.T) {
			thinking := map[string]interface{}{"type": "disabled"}
			tc, oc := mapThinkingConfig(thinking, "", model)
			assert.Nil(t, tc)
			assert.Nil(t, oc)
		})

		t.Run(model+"/no_effort_nil", func(t *testing.T) {
			tc, oc := mapThinkingConfig(nil, "none", model)
			assert.Nil(t, tc)
			assert.Nil(t, oc)
		})
	}
}

func TestIsAdaptiveThinkingModel(t *testing.T) {
	tests := []struct {
		model    string
		adaptive bool
	}{
		// Opus 4 family → adaptive
		{"claude-opus-4-7", true},
		{"claude-opus-4-6", true},
		{"claude-opus-4-5-20251101", true},
		{"claude-mythos-preview", true},
		// Claude 4 Sonnet/Haiku → no thinking support, not adaptive
		{"claude-sonnet-4-6", false},
		{"claude-haiku-4-5-20251001", false},
		// Claude 3.x → legacy enabled+budget_tokens
		{"claude-3-7-sonnet-20250219", false},
		{"claude-3-5-sonnet-20241022", false},
		{"claude-3-opus-20240229", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.adaptive, isAdaptiveThinkingModel(tt.model))
		})
	}
}
