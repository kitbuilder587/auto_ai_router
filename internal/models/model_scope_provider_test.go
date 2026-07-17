package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInferLiteLLMShortModelProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		modelID  string
		provider string
		ok       bool
	}{
		{name: "openai", modelID: "gpt-4o-mini", provider: "openai", ok: true},
		{name: "anthropic", modelID: "claude-sonnet-4-5", provider: "anthropic", ok: true},
		{name: "anthropic future date", modelID: "claude-opus-5-1-20270101", provider: "anthropic", ok: true},
		{name: "vertex gemini", modelID: "gemini-2.5-flash", provider: "vertex_ai", ok: true},
		{name: "bedrock anthropic", modelID: "anthropic.claude-3-5-sonnet-20240620-v1:0", provider: "bedrock", ok: true},
		{name: "bedrock amazon", modelID: "amazon.titan-text-lite-v1", provider: "bedrock", ok: true},
		{name: "unknown custom", modelID: "custom-short"},
		{name: "synthetic openai suffix", modelID: "gpt-4o-mini-retry"},
		{name: "non-pinned claude punctuation", modelID: "claude-sonnet-4.5"},
		{name: "unknown bedrock-like", modelID: "anthropic.custom-model"},
		{name: "already provider-qualified", modelID: "openai/gpt-4o-mini"},
		{name: "empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider, ok := inferLiteLLMShortModelProvider(tt.modelID)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.provider, provider)
		})
	}
}
