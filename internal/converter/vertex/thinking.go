package vertex

import (
	"strings"

	"google.golang.org/genai"
)

// mapReasoningToThinkingConfig maps OpenAI reasoning params to Vertex ThinkingConfig.
// Checks Anthropic-style thinking first, then falls back to reasoning_effort.
func mapReasoningToThinkingConfig(thinking interface{}, reasoningEffort string, model string) *genai.ThinkingConfig {
	if thinking != nil {
		if thinkingMap, ok := thinking.(map[string]interface{}); ok {
			return mapAnthropicThinking(thinkingMap, model)
		}
	}
	if reasoningEffort != "" {
		return mapReasoningEffort(reasoningEffort, model)
	}
	return nil
}

// isGemini3Model returns true for Gemini 3+ models (use ThinkingLevel API)
func isGemini3Model(model string) bool {
	return strings.Contains(strings.ToLower(model), "gemini-3")
}

// isFlashModel returns true for flash variants
func isFlashModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "flash")
}

// isThinkingCapableModel returns true for models that support dynamic thinking
// (Gemini 2.5+ and Gemini 3+). These models think autonomously when ThinkingConfig
// is not set, causing unpredictable latency.
func isThinkingCapableModel(model string) bool {
	lower := strings.ToLower(model)
	return strings.Contains(lower, "gemini-2.5") || strings.Contains(lower, "gemini-3")
}

// isGemini25ProModel returns true for Gemini 2.5 Pro variants.
// These models require thinking to always be enabled (ThinkingBudget=0 is invalid).
func isGemini25ProModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "gemini-2.5-pro")
}

// disableThinkingConfig returns a ThinkingConfig that minimizes thinking computation,
// or nil if the model does not support disabling thinking.
//
// Gemini 2.5 flash: ThinkingBudget=0 (full disable supported).
// Gemini 2.5 pro: returns nil — thinking cannot be disabled for this model.
// Gemini 3 flash: ThinkingLevel=Minimal (lowest level supported).
// Gemini 3 non-flash: ThinkingLevel=Low (Minimal is not supported on pro variants).
func disableThinkingConfig(model string) *genai.ThinkingConfig {
	if isGemini3Model(model) {
		if isFlashModel(model) {
			return &genai.ThinkingConfig{
				IncludeThoughts: false,
				ThinkingLevel:   genai.ThinkingLevelMinimal,
			}
		}
		// Gemini 3 pro: Minimal is not supported, Low is the minimum.
		return &genai.ThinkingConfig{
			IncludeThoughts: false,
			ThinkingLevel:   genai.ThinkingLevelLow,
		}
	}
	// Gemini 2.5 pro: thinking cannot be disabled.
	if isGemini25ProModel(model) {
		return nil
	}
	// Gemini 2.5 flash: budget=0 fully disables thinking.
	zero := int32(0)
	return &genai.ThinkingConfig{
		IncludeThoughts: false,
		ThinkingBudget:  &zero,
	}
}

// mapReasoningEffort maps OpenAI reasoning_effort to Vertex ThinkingConfig.
// Gemini 2.5 uses ThinkingBudget (tokens), Gemini 3+ uses ThinkingLevel (enum).
func mapReasoningEffort(effort string, model string) *genai.ThinkingConfig {
	config := &genai.ThinkingConfig{IncludeThoughts: true}

	if isGemini3Model(model) {
		// Gemini 3+: ThinkingLevel enum.
		// "minimal" uses Minimal only on flash; pro minimum is Low.
		// "low" maps to Low for all variants (not Minimal).
		// "medium" maps to Medium for all variants.
		// "high" maps to High for all variants.
		switch effort {
		case "minimal":
			if isFlashModel(model) {
				config.ThinkingLevel = genai.ThinkingLevelMinimal
			} else {
				config.ThinkingLevel = genai.ThinkingLevelLow
			}
		case "low":
			config.ThinkingLevel = genai.ThinkingLevelLow
		case "medium":
			config.ThinkingLevel = genai.ThinkingLevelMedium
		case "high":
			config.ThinkingLevel = genai.ThinkingLevelHigh
		case "disable", "none":
			return disableThinkingConfig(model)
		}
	} else {
		// Gemini 2.5: ThinkingBudget (tokens).
		// Official budget values per reasoning_effort level:
		//   minimal/low → 1,024  |  medium → 8,192  |  high → 24,576
		var budget int32
		switch effort {
		case "minimal", "low":
			budget = 1024
		case "medium":
			budget = 8192
		case "high":
			budget = 24576
		case "disable", "none":
			return disableThinkingConfig(model)
		}
		config.ThinkingBudget = &budget
	}

	return config
}

// mapNativeThinkingConfig maps Gemini-native thinking_config from extra_body to ThinkingConfig.
// Format: {"thinking_budget": 1024, "thinking_level": "medium", "include_thoughts": true}
// thinking_budget is used for Gemini 2.5; thinking_level is used for Gemini 3+.
// If both are present, thinking_budget takes precedence for Gemini 2.5 and
// thinking_level takes precedence for Gemini 3+.
func mapNativeThinkingConfig(tcMap map[string]interface{}, model string) *genai.ThinkingConfig {
	config := &genai.ThinkingConfig{IncludeThoughts: true}

	if include, ok := tcMap["include_thoughts"].(bool); ok {
		config.IncludeThoughts = include
	}

	if isGemini3Model(model) {
		if levelStr, ok := tcMap["thinking_level"].(string); ok {
			switch levelStr {
			case "minimal":
				if isFlashModel(model) {
					config.ThinkingLevel = genai.ThinkingLevelMinimal
				} else {
					config.ThinkingLevel = genai.ThinkingLevelLow
				}
			case "low":
				config.ThinkingLevel = genai.ThinkingLevelLow
			case "medium":
				config.ThinkingLevel = genai.ThinkingLevelMedium
			case "high":
				config.ThinkingLevel = genai.ThinkingLevelHigh
			default:
				config.ThinkingLevel = genai.ThinkingLevelLow
			}
		} else {
			// No thinking_level specified: use default for model type.
			if isFlashModel(model) {
				config.ThinkingLevel = genai.ThinkingLevelMinimal
			} else {
				config.ThinkingLevel = genai.ThinkingLevelLow
			}
		}
	} else {
		// Gemini 2.5: use thinking_budget if provided.
		if budget, ok := tcMap["thinking_budget"].(float64); ok {
			if budget == 0 && isGemini25ProModel(model) {
				// 2.5-pro cannot disable thinking; ignore budget=0.
				return nil
			}
			v := int32(budget)
			config.ThinkingBudget = &v
		}
	}

	return config
}

// mapAnthropicThinking maps Anthropic-style thinking param to Vertex ThinkingConfig.
// Format: {"type": "enabled", "budget_tokens": 15000}
func mapAnthropicThinking(thinking map[string]interface{}, model string) *genai.ThinkingConfig {
	thinkingType, _ := thinking["type"].(string)
	budgetTokens, _ := thinking["budget_tokens"].(float64)

	if thinkingType != "enabled" || budgetTokens <= 0 {
		return disableThinkingConfig(model)
	}

	config := &genai.ThinkingConfig{}

	config.IncludeThoughts = true

	if isGemini3Model(model) {
		// Map Anthropic budget_tokens to Gemini 3 ThinkingLevel.
		// Thresholds align with official budget values: high≥24576, medium≥8192, low≥1024.
		switch {
		case budgetTokens >= 15000:
			config.ThinkingLevel = genai.ThinkingLevelHigh
		case budgetTokens >= 5000:
			config.ThinkingLevel = genai.ThinkingLevelMedium
		default:
			// Flash supports Minimal; pro minimum is Low.
			if isFlashModel(model) {
				config.ThinkingLevel = genai.ThinkingLevelMinimal
			} else {
				config.ThinkingLevel = genai.ThinkingLevelLow
			}
		}
	} else {
		budget := int32(budgetTokens)
		config.ThinkingBudget = &budget
	}

	return config
}
