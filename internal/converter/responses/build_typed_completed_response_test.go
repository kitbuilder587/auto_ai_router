package responses

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTypedCompletedResponse_BasicText(t *testing.T) {
	acc := &streamAccumulator{
		responseID:     "resp_test123",
		model:          "gpt-4o",
		createdAt:      1700000000,
		fullText:       "Hello, world!",
		messageStarted: true,
		messageItemID:  "msg_test123",
		usage: &chatCompletionsUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
		finishReason: nil,
	}

	resp := buildTypedCompletedResponse(acc)

	assert.Equal(t, "resp_test123", resp.ID)
	assert.Equal(t, "gpt-4o", resp.Model)
	assert.Equal(t, "completed", resp.Status)
	assert.Len(t, resp.Output, 1)

	// Check message output item
	msgItem := resp.Output[0]
	assert.Equal(t, "message", msgItem.Type)
	assert.Equal(t, "msg_test123", msgItem.ID)
	assert.Equal(t, "completed", msgItem.Status)
	assert.Equal(t, "assistant", msgItem.Role)
	assert.Len(t, msgItem.Content, 1)
	assert.Equal(t, "output_text", msgItem.Content[0].Type)
	assert.Equal(t, "Hello, world!", msgItem.Content[0].Text)

	// Check usage
	assert.NotNil(t, resp.Usage)
	assert.Equal(t, 10, resp.Usage.InputTokens)
	assert.Equal(t, 5, resp.Usage.OutputTokens)
	assert.Equal(t, 15, resp.Usage.TotalTokens)
}

func TestBuildTypedCompletedResponse_ToolCalls(t *testing.T) {
	acc := &streamAccumulator{
		responseID:     "resp_test456",
		model:          "gpt-4o",
		createdAt:      1700000000,
		messageStarted: true,
		messageItemID:  "msg_test456",
		toolCalls: []accumulatedToolCall{
			{
				id:        "call_123",
				name:      "get_weather",
				arguments: `{"city":"Paris"}`,
				itemID:    "fc_test123",
			},
		},
		usage: &chatCompletionsUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}

	resp := buildTypedCompletedResponse(acc)

	assert.Equal(t, "completed", resp.Status)
	assert.Len(t, resp.Output, 1)

	// Check function call output item
	fcItem := resp.Output[0]
	assert.Equal(t, "function_call", fcItem.Type)
	assert.Equal(t, "fc_test123", fcItem.ID)
	assert.Equal(t, "call_123", fcItem.CallID)
	assert.Equal(t, "get_weather", fcItem.Name)
	assert.Equal(t, `{"city":"Paris"}`, fcItem.Arguments)
	assert.Equal(t, "completed", fcItem.Status)
}

func TestBuildTypedCompletedResponse_TextAndToolCalls(t *testing.T) {
	acc := &streamAccumulator{
		responseID:     "resp_test789",
		model:          "gpt-4o",
		createdAt:      1700000000,
		fullText:       "The weather is sunny.",
		messageStarted: true,
		messageItemID:  "msg_test789",
		toolCalls: []accumulatedToolCall{
			{
				id:        "call_456",
				name:      "get_weather",
				arguments: `{"city":"Paris"}`,
				itemID:    "fc_test456",
			},
		},
	}

	resp := buildTypedCompletedResponse(acc)

	assert.Len(t, resp.Output, 2)

	// First is message
	msgItem := resp.Output[0]
	assert.Equal(t, "message", msgItem.Type)
	assert.Equal(t, "The weather is sunny.", msgItem.Content[0].Text)

	// Second is function call
	fcItem := resp.Output[1]
	assert.Equal(t, "function_call", fcItem.Type)
	assert.Equal(t, "get_weather", fcItem.Name)
}

func TestBuildTypedCompletedResponse_IncompleteLength(t *testing.T) {
	lengthReason := "length"
	acc := &streamAccumulator{
		responseID:     "resp_test999",
		model:          "gpt-4o",
		createdAt:      1700000000,
		fullText:       "Partial",
		messageStarted: true,
		messageItemID:  "msg_test999",
		usage: &chatCompletionsUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
		finishReason: &lengthReason,
	}

	resp := buildTypedCompletedResponse(acc)

	assert.Equal(t, "incomplete", resp.Status)
	require.NotNil(t, resp.IncompleteDetails)
	assert.Equal(t, "max_output_tokens", resp.IncompleteDetails.Reason)
}

func TestBuildTypedCompletedResponse_IncompleteContentFilter(t *testing.T) {
	filterReason := "content_filter"
	acc := &streamAccumulator{
		responseID:     "resp_test888",
		model:          "gpt-4o",
		createdAt:      1700000000,
		fullText:       "Some text",
		messageStarted: true,
		messageItemID:  "msg_test888",
		finishReason:   &filterReason,
	}

	resp := buildTypedCompletedResponse(acc)

	assert.Equal(t, "incomplete", resp.Status)
	require.NotNil(t, resp.IncompleteDetails)
	assert.Equal(t, "content_filter", resp.IncompleteDetails.Reason)
}

func TestBuildTypedCompletedResponse_WithMetadata(t *testing.T) {
	acc := &streamAccumulator{
		responseID:         "resp_test777",
		model:              "gpt-4o",
		createdAt:          1700000000,
		fullText:           "Hello",
		messageStarted:     true,
		messageItemID:      "msg_test777",
		storeFlag:          true,
		previousResponseID: "resp_previous123",
		requestMetadata:    map[string]string{"custom_field": "custom_value"},
	}

	resp := buildTypedCompletedResponse(acc)

	assert.True(t, resp.Store)
	assert.Equal(t, "resp_previous123", resp.PreviousResponseID)
	assert.Equal(t, "custom_value", resp.Metadata["custom_field"])
}

func TestBuildTypedCompletedResponse_WithCachedTokens(t *testing.T) {
	acc := &streamAccumulator{
		responseID:     "resp_test666",
		model:          "gpt-4o",
		createdAt:      1700000000,
		fullText:       "Hello",
		messageStarted: true,
		messageItemID:  "msg_test666",
		usage: &chatCompletionsUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			CachedTokens:     80,
			ReasoningTokens:  20,
		},
	}

	resp := buildTypedCompletedResponse(acc)

	assert.NotNil(t, resp.Usage)
	assert.NotNil(t, resp.Usage.InputTokensDetails)
	assert.Equal(t, 80, resp.Usage.InputTokensDetails.CachedTokens)
	assert.NotNil(t, resp.Usage.OutputTokensDetails)
	assert.Equal(t, 20, resp.Usage.OutputTokensDetails.ReasoningTokens)
}

func TestBuildTypedCompletedResponse_Empty(t *testing.T) {
	acc := &streamAccumulator{
		responseID: "resp_empty",
		model:      "gpt-4o",
		createdAt:  1700000000,
		// No message started, no text, no tool calls
	}

	resp := buildTypedCompletedResponse(acc)

	assert.Equal(t, "completed", resp.Status)
	assert.Empty(t, resp.Output)
	assert.Nil(t, resp.Usage)
}

func TestBuildTypedCompletedResponse_EmptyWithUsage(t *testing.T) {
	acc := &streamAccumulator{
		responseID: "resp_empty_usage",
		model:      "gpt-4o",
		createdAt:  1700000000,
		usage: &chatCompletionsUsage{
			PromptTokens:     10,
			CompletionTokens: 0,
			TotalTokens:      10,
		},
	}

	resp := buildTypedCompletedResponse(acc)

	assert.Equal(t, "completed", resp.Status)
	assert.Empty(t, resp.Output)
	assert.NotNil(t, resp.Usage)
	assert.Equal(t, 10, resp.Usage.InputTokens)
	assert.Equal(t, 0, resp.Usage.OutputTokens)
}
