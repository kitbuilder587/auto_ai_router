package anthropicresponses

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/converter/anthropic"
	"github.com/mixaill76/auto_ai_router/internal/converter/responses"
)

// AnthropicToResponsesResponse converts an Anthropic Messages API response body to a
// responses.Response, using displayModelID as the echoed model name.
func AnthropicToResponsesResponse(body []byte, displayModelID, responseID string, createdAt int64) (*responses.Response, error) {
	var ar anthropic.AnthropicResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return nil, fmt.Errorf("AnthropicToResponsesResponse: parse: %w", err)
	}
	if responseID == "" {
		responseID = generateResponseID()
	}
	if createdAt == 0 {
		createdAt = time.Now().Unix()
	}
	if displayModelID == "" {
		displayModelID = ar.Model
	}
	return buildAnthropicResponse(&ar, displayModelID, responseID, createdAt), nil
}

func buildAnthropicResponse(
	ar *anthropic.AnthropicResponse,
	model, responseID string,
	createdAt int64,
) *responses.Response {
	status, incompleteDetails := anthropicStopReasonToStatus(ar.StopReason)
	output := anthropicContentToOutputItems(ar.Content)
	usage := anthropicUsageToUsage(ar.Usage)
	completedAt := createdAt

	return &responses.Response{
		ID:                 responseID,
		Object:             "response",
		CreatedAt:          createdAt,
		CompletedAt:        &completedAt,
		Model:              model,
		Status:             status,
		IncompleteDetails:  incompleteDetails,
		Output:             output,
		Usage:              usage,
		Error:              nil,
		Metadata:           map[string]string{},
		Tools:              []responses.Tool{},
		ParallelToolCalls:  true,
		PreviousResponseID: nil,
		Instructions:       nil,
	}
}

// anthropicContentToOutputItems converts Anthropic content blocks to Responses API output items.
func anthropicContentToOutputItems(blocks []anthropic.ContentBlock) []responses.OutputItem {
	var output []responses.OutputItem
	var msgContent []responses.OutputContent

	flushMessage := func() {
		if len(msgContent) == 0 {
			return
		}
		output = append(output, responses.OutputItem{
			Type:    "message",
			ID:      generateItemID("msg_"),
			Status:  "completed",
			Role:    "assistant",
			Content: msgContent,
		})
		msgContent = nil
	}

	for _, block := range blocks {
		switch block.Type {
		case "text":
			msgContent = append(msgContent, responses.OutputContent{
				Type:        "output_text",
				Text:        block.Text,
				Annotations: []responses.Annotation{},
			})

		case "thinking":
			// Flush any accumulated text first.
			flushMessage()
			reasoningItem := responses.OutputItem{
				Type:   "reasoning",
				ID:     generateItemID("rs_"),
				Status: "completed",
			}
			if block.Thinking != "" {
				reasoningItem.Summary = []responses.OutputContent{
					{Type: "summary_text", Text: block.Thinking},
				}
			}
			if block.Signature != "" {
				reasoningItem.EncryptedContent = block.Signature
			}
			output = append(output, reasoningItem)

		case "tool_use":
			// Flush accumulated text before a tool call.
			flushMessage()
			// Detect computer_use tool call: the input always contains an "action" key.
			// Regular function calls use arbitrary input schemas.
			if inputMap, ok := block.Input.(map[string]interface{}); ok {
				if _, hasAction := inputMap["action"]; hasAction {
					output = append(output, responses.OutputItem{
						Type:   "computer_call",
						ID:     generateItemID("cc_"),
						Status: "completed",
						CallID: block.ID,
						Name:   block.Name,
						Action: block.Input,
					})
					continue
				}
			}
			argsJSON := "{}"
			if block.Input != nil {
				if b, err := json.Marshal(block.Input); err == nil {
					argsJSON = string(b)
				}
			}
			output = append(output, responses.OutputItem{
				Type:      "function_call",
				ID:        generateItemID("fc_"),
				Status:    "completed",
				CallID:    block.ID,
				Name:      block.Name,
				Arguments: argsJSON,
			})
		}
	}

	flushMessage()

	if len(output) == 0 {
		output = []responses.OutputItem{{
			Type:    "message",
			ID:      generateItemID("msg_"),
			Status:  "completed",
			Role:    "assistant",
			Content: []responses.OutputContent{{Type: "output_text", Text: "", Annotations: []responses.Annotation{}}},
		}}
	}

	return output
}

// anthropicStopReasonToStatus maps Anthropic stop_reason to Responses API status.
func anthropicStopReasonToStatus(stopReason string) (string, *responses.IncompleteDetails) {
	switch stopReason {
	case "end_turn", "tool_use", "":
		return "completed", nil
	case "max_tokens":
		return "incomplete", &responses.IncompleteDetails{Reason: "max_output_tokens"}
	case "stop_sequence":
		return "completed", nil
	default:
		return "completed", nil
	}
}

// anthropicUsageToUsage converts Anthropic usage to responses.Usage.
func anthropicUsageToUsage(au anthropic.AnthropicUsage) *responses.Usage {
	return &responses.Usage{
		InputTokens:  au.InputTokens,
		OutputTokens: au.OutputTokens,
		TotalTokens:  au.InputTokens + au.OutputTokens,
		InputTokensDetails: responses.InputDetails{
			CachedTokens: au.CacheReadInputTokens,
		},
		OutputTokensDetails: responses.OutputDetails{},
	}
}

// generateResponseID generates a "resp_" prefixed unique ID.
func generateResponseID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "resp_" + hex.EncodeToString(b)
}

// generateItemID generates a short unique ID with the given prefix.
func generateItemID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}
