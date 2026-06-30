package anthropic

import "strings"

const minTextTokensWithThinking = 1024

// mapThinkingConfig maps OpenAI thinking / reasoning_effort parameters to an Anthropic
// ThinkingConfig (and optional OutputConfig for Claude 4+ adaptive thinking).
//
// Priority:
//  1. Anthropic-style thinking param: {"type": "enabled"/"adaptive", ...}
//  2. OpenAI reasoning_effort string → mapped accordingly
//
// Claude 3.x: returns (*AnthropicThinking{Type:"enabled", BudgetTokens:N}, nil)
// Claude 4+:  returns (*AnthropicThinking{Type:"adaptive"}, *AnthropicOutputConfig{Effort:E})
func mapThinkingConfig(thinking interface{}, reasoningEffort string, modelName string) (*AnthropicThinking, *AnthropicOutputConfig) {
	adaptive := isAdaptiveThinkingModel(modelName)

	if thinking != nil {
		if thinkingMap, ok := thinking.(map[string]interface{}); ok {
			thinkingType, _ := thinkingMap["type"].(string)
			switch thinkingType {
			case "enabled":
				budgetTokens, _ := thinkingMap["budget_tokens"].(float64)
				if budgetTokens > 0 {
					if adaptive {
						return &AnthropicThinking{Type: "adaptive"},
							&AnthropicOutputConfig{Effort: budgetTokensToEffort(int(budgetTokens))}
					}
					return &AnthropicThinking{Type: "enabled", BudgetTokens: int(budgetTokens)}, nil
				}
				// "enabled" without positive budget — fall through to reasoning_effort
			case "adaptive":
				effort, _ := thinkingMap["effort"].(string)
				if effort == "" {
					effort = "medium"
				}
				if adaptive {
					return &AnthropicThinking{Type: "adaptive"}, &AnthropicOutputConfig{Effort: effort}
				}
				// Caller passed adaptive but model uses legacy format — convert effort→budget
				budget := effortToBudgetTokens(effort)
				if budget > 0 {
					return &AnthropicThinking{Type: "enabled", BudgetTokens: budget}, nil
				}
			case "disabled":
				return nil, nil
			}
		}
	}

	if reasoningEffort != "" {
		if adaptive {
			effort := mapReasoningEffortToEffort(reasoningEffort)
			if effort != "" {
				return &AnthropicThinking{Type: "adaptive"}, &AnthropicOutputConfig{Effort: effort}
			}
		} else {
			budget := mapReasoningEffortToBudget(reasoningEffort)
			if budget > 0 {
				return &AnthropicThinking{Type: "enabled", BudgetTokens: budget}, nil
			}
		}
	}

	return nil, nil
}

// appendBetaUnique appends s to slice only if it is not already present.
func appendBetaUnique(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}

// MapThinkingConfigFromEffort is the public entry point for packages (e.g. responses converter)
// that only have an effort string and model name, without a raw thinking param.
func MapThinkingConfigFromEffort(effort, modelName string) (*AnthropicThinking, *AnthropicOutputConfig) {
	return mapThinkingConfig(nil, effort, modelName)
}

// EnsureMaxTokensForThinking raises max_tokens when legacy thinking uses a
// budget_tokens value. Anthropic requires max_tokens to be greater than the
// thinking budget; otherwise the provider rejects the request before generation.
func EnsureMaxTokensForThinking(maxTokens int, thinking *AnthropicThinking) int {
	if thinking == nil || thinking.Type != "enabled" || thinking.BudgetTokens <= 0 {
		return maxTokens
	}
	if maxTokens > thinking.BudgetTokens {
		return maxTokens
	}
	return thinking.BudgetTokens + minTextTokensWithThinking
}

// isAdaptiveThinkingModel reports whether the model uses the Claude 4+ adaptive thinking API
// (thinking.type="adaptive" + output_config.effort) instead of the legacy enabled+budget_tokens.
//
// Only Opus-4 generation models and claude-mythos-preview use adaptive thinking.
// Claude 3.x models use budget_tokens-based enabled thinking.
// Claude 4 Sonnet/Haiku do not support extended thinking at all.
func isAdaptiveThinkingModel(model string) bool {
	if strings.Contains(model, "opus") {
		return true
	}
	if strings.Contains(model, "mythos") {
		return true
	}
	return false
}

// mapReasoningEffortToBudget maps an OpenAI reasoning_effort string to an Anthropic
// budget_tokens value for Claude 3.x models. Returns 0 for unknown or disabled values.
func mapReasoningEffortToBudget(effort string) int {
	switch effort {
	case "minimal":
		return 1024
	case "low":
		return 5000
	case "medium":
		return 15000
	case "high":
		return 30000
	case "disable", "none":
		return 0
	default:
		return 0
	}
}

// mapReasoningEffortToEffort maps an OpenAI reasoning_effort string to an Anthropic
// output_config.effort value for Claude 4+ models. Returns "" for disabled values.
func mapReasoningEffortToEffort(effort string) string {
	switch effort {
	case "minimal", "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh":
		return "xhigh"
	case "max":
		return "max"
	case "disable", "none":
		return ""
	default:
		return ""
	}
}

// budgetTokensToEffort converts a legacy budget_tokens value to the nearest
// output_config.effort level for Claude 4+ adaptive thinking.
func budgetTokensToEffort(budget int) string {
	switch {
	case budget <= 5000:
		return "low"
	case budget <= 15000:
		return "medium"
	case budget <= 30000:
		return "high"
	default:
		return "high"
	}
}

// effortToBudgetTokens converts an output_config.effort string to a legacy
// budget_tokens value for Claude 3.x models.
func effortToBudgetTokens(effort string) int {
	switch effort {
	case "low":
		return 5000
	case "medium":
		return 15000
	case "high", "xhigh", "max":
		return 30000
	default:
		return 0
	}
}
