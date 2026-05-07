package responses

import (
	"encoding/json"
	"testing"
)

func TestBuildInProgressResponse_RequiredSchemaFields(t *testing.T) {
	resp := BuildInProgressResponse("resp_test", "model_test", 123)

	requiredKeys := []string{
		"completed_at",
		"incomplete_details",
		"usage",
		"error",
		"tool_choice",
		"tools",
		"parallel_tool_calls",
		"instructions",
		"previous_response_id",
		"max_output_tokens",
		"reasoning",
		"safety_identifier",
		"prompt_cache_key",
		"store",
		"background",
		"presence_penalty",
		"frequency_penalty",
		"top_logprobs",
		"temperature",
		"top_p",
		"truncation",
		"service_tier",
		"text",
		"max_tool_calls",
	}

	for _, key := range requiredKeys {
		if _, ok := resp[key]; !ok {
			t.Fatalf("expected key %q to be present", key)
		}
	}
}

func TestBuildCompletedResponse_JSONIncludesRequiredSchemaFields(t *testing.T) {
	resp := BuildCompletedResponse(CompletedResponseParams{
		ID:        "resp_test",
		Model:     "model_test",
		CreatedAt: 123,
		Status:    "completed",
		Output:    []OutputItem{},
		Metadata:  map[string]string{},
	})

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	requiredKeys := []string{
		"completed_at",
		"incomplete_details",
		"usage",
		"error",
		"tool_choice",
		"tools",
		"parallel_tool_calls",
		"instructions",
		"previous_response_id",
		"max_output_tokens",
		"reasoning",
		"safety_identifier",
		"prompt_cache_key",
		"store",
		"background",
		"presence_penalty",
		"frequency_penalty",
		"top_logprobs",
		"temperature",
		"top_p",
		"truncation",
		"service_tier",
		"text",
		"max_tool_calls",
	}

	for _, key := range requiredKeys {
		if _, ok := parsed[key]; !ok {
			t.Fatalf("expected key %q to be present in JSON", key)
		}
	}
}
