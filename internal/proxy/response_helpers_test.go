package proxy

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInjectStreamOptions_AddsIncludeUsage(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	modified := injectStreamOptions(body)

	var raw map[string]interface{}
	if err := json.Unmarshal(modified, &raw); err != nil {
		t.Fatalf("failed to unmarshal modified body: %v", err)
	}

	streamOptions, ok := raw["stream_options"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected stream_options map, got %T", raw["stream_options"])
	}
	if includeUsage, ok := streamOptions["include_usage"].(bool); !ok || !includeUsage {
		t.Fatalf("expected include_usage=true, got %v", streamOptions["include_usage"])
	}
}

func TestInjectStreamOptions_UpdatesExisting(t *testing.T) {
	body := []byte(`{"stream_options":{"include_usage":false,"foo":1}}`)
	modified := injectStreamOptions(body)

	var raw map[string]interface{}
	if err := json.Unmarshal(modified, &raw); err != nil {
		t.Fatalf("failed to unmarshal modified body: %v", err)
	}

	streamOptions, ok := raw["stream_options"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected stream_options map, got %T", raw["stream_options"])
	}
	if includeUsage, ok := streamOptions["include_usage"].(bool); !ok || !includeUsage {
		t.Fatalf("expected include_usage=true, got %v", streamOptions["include_usage"])
	}
	if streamOptions["foo"] != float64(1) {
		t.Fatalf("expected foo to be preserved, got %v", streamOptions["foo"])
	}
}

func TestInjectStreamOptions_ReplacesNonMap(t *testing.T) {
	body := []byte(`{"stream_options":"bad"}`)
	modified := injectStreamOptions(body)

	var raw map[string]interface{}
	if err := json.Unmarshal(modified, &raw); err != nil {
		t.Fatalf("failed to unmarshal modified body: %v", err)
	}

	streamOptions, ok := raw["stream_options"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected stream_options map, got %T", raw["stream_options"])
	}
	if includeUsage, ok := streamOptions["include_usage"].(bool); !ok || !includeUsage {
		t.Fatalf("expected include_usage=true, got %v", streamOptions["include_usage"])
	}
}

func TestInjectStreamOptions_InvalidJSON(t *testing.T) {
	body := []byte(`{"stream_options":`)
	modified := injectStreamOptions(body)
	if !bytes.Equal(modified, body) {
		t.Fatalf("expected invalid json to be returned as-is")
	}
}

func TestExtractSpendRequestFieldsPreservesJSONValuesAndTags(t *testing.T) {
	body := []byte(`{
		"model":"openai/gpt-4o-mini",
		"metadata":{
			"null_value":null,
			"false_value":false,
			"zero_value":0,
			"empty_string":"",
			"empty_array":[],
			"empty_object":{},
			"shape":{"nested":[true,0,"",null,false]}
		},
		"tags":["identity","", "identity","request"]
	}`)

	metadata, tags := extractSpendRequestFields(body, "application/json; charset=utf-8")
	require.NotNil(t, metadata)
	assert.Nil(t, metadata["null_value"])
	assert.Equal(t, false, metadata["false_value"])
	zero, ok := metadata["zero_value"].(json.Number)
	require.True(t, ok, "zero must remain a JSON number")
	assert.Equal(t, "0", zero.String())
	assert.Equal(t, "", metadata["empty_string"])
	assert.Empty(t, metadata["empty_array"])
	assert.Empty(t, metadata["empty_object"])
	encoded, err := json.Marshal(metadata)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"null_value":null,
		"false_value":false,
		"zero_value":0,
		"empty_string":"",
		"empty_array":[],
		"empty_object":{},
		"shape":{"nested":[true,0,"",null,false]}
	}`, string(encoded))
	assert.Equal(t, []string{"identity", "", "identity", "request"}, tags)
}

func TestStripProviderRequestTagsOnlyMutatesValidJSONWithRootTags(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		want        string
		exact       bool
	}{
		{
			name:        "array tags are consumed and other fields remain",
			contentType: "application/json; charset=utf-8",
			body:        `{"model":"gpt-4","tags":["one","two"],"metadata":{"keep":true},"temperature":0.25}`,
			want:        `{"model":"gpt-4","metadata":{"keep":true},"temperature":0.25}`,
		},
		{
			name:        "malformed extension value still cannot leak",
			contentType: "application/json",
			body:        `{"model":"gpt-4","tags":{"unexpected":true},"messages":[]}`,
			want:        `{"model":"gpt-4","messages":[]}`,
		},
		{
			name:        "body without tags stays byte exact",
			contentType: "application/json",
			body:        "{ \"model\" : \"gpt-4\" }",
			want:        "{ \"model\" : \"gpt-4\" }",
			exact:       true,
		},
		{
			name:        "invalid json stays byte exact",
			contentType: "application/json",
			body:        `{"tags":`,
			want:        `{"tags":`,
			exact:       true,
		},
		{
			name:        "multipart decision-required surface stays byte exact",
			contentType: "multipart/form-data; boundary=test",
			body:        "--test\r\ncontent-disposition: form-data; name=\"tags\"\r\n\r\none\r\n--test--\r\n",
			want:        "--test\r\ncontent-disposition: form-data; name=\"tags\"\r\n\r\none\r\n--test--\r\n",
			exact:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripProviderRequestTags([]byte(tt.body), tt.contentType)
			if !tt.exact {
				assert.JSONEq(t, tt.want, string(got))
				return
			}
			assert.Equal(t, tt.want, string(got))
		})
	}
}
