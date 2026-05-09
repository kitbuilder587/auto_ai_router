package anthropicresponses

import (
	"github.com/mixaill76/auto_ai_router/internal/converter/anthropic"
	"github.com/mixaill76/auto_ai_router/internal/converter/responses"
)

// responsesToolsToAnthropic converts Responses API tools to Anthropic tool definitions.
// Unsupported tool types (web_search, file_search, mcp, image_generation) are dropped.
func responsesToolsToAnthropic(tools []responses.Tool) ([]anthropic.AnthropicTool, []string) {
	var anthropicTools []anthropic.AnthropicTool
	var betas []string

	for _, t := range tools {
		switch t.Type {
		case "function":
			anthropicTools = append(anthropicTools, anthropic.AnthropicTool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.Parameters,
			})

		case "computer_use_preview":
			w := 0
			h := 0
			if t.DisplayWidth != nil {
				w = *t.DisplayWidth
			}
			if t.DisplayHeight != nil {
				h = *t.DisplayHeight
			}
			anthropicTools = append(anthropicTools, anthropic.AnthropicTool{
				Type:            "computer_20241022",
				Name:            "computer",
				DisplayWidthPx:  w,
				DisplayHeightPx: h,
			})
			// Computer use requires the beta header.
			betas = appendUnique(betas, "computer-use-2024-10-22")

			// web_search, file_search, code_interpreter, mcp, image_generation are not
			// supported by Anthropic — skip them.
		}
	}

	return anthropicTools, betas
}

// responsesToolChoiceToAnthropic maps Responses API tool_choice to Anthropic tool_choice.
func responsesToolChoiceToAnthropic(toolChoice interface{}) interface{} {
	if toolChoice == nil {
		return nil
	}
	switch tc := toolChoice.(type) {
	case string:
		switch tc {
		case "none":
			return map[string]interface{}{"type": "none"}
		case "auto":
			return map[string]interface{}{"type": "auto"}
		case "required":
			return map[string]interface{}{"type": "any"}
		default:
			return nil
		}
	case map[string]interface{}:
		tcType, _ := tc["type"].(string)
		if tcType == "function" {
			name, _ := tc["name"].(string)
			return map[string]interface{}{
				"type": "tool",
				"name": name,
			}
		}
	}
	return nil
}

func appendUnique(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}
