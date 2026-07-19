package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractOpenAIChatToolNames(t *testing.T) {
	body := []byte(`{
        "model": "fixture-model",
        "messages": [{"role": "user", "content": "hello"}],
        "tools": [
            {"type": "function", "function": {"name": "weather"}},
            {"type": "function", "function": {"name": "local_time"}},
            {"type": "function", "function": {"name": "weather"}},
            {"type": "function", "function": {"name": "Weather"}}
        ]
    }`)

	assert.Equal(t, []string{"weather", "local_time", "Weather"}, extractOpenAIChatToolNames("/v1/chat/completions", body))
	assert.Equal(t, []string{"weather", "local_time", "Weather"}, extractOpenAIChatToolNames("/chat/completions/", body))
}

func TestExtractOpenAIChatToolNamesIgnoresMalformedOptionalEntries(t *testing.T) {
	body := []byte(`{
        "model": "fixture-model",
        "messages": [{"role": "user", "content": "hello"}],
        "tools": [
            null,
            "not-an-object",
            {"function": null},
            {"function": {}},
            {"function": {"name": 42}},
            {"function": {"name": ""}},
            {"function": {"name": "weather"}}
        ]
    }`)

	assert.Equal(t, []string{"weather"}, extractOpenAIChatToolNames("/v1/chat/completions", body))
	assert.Nil(t, extractOpenAIChatToolNames("/v1/chat/completions", []byte(`{"tools": {"function": {"name": "weather"}}}`)))
	assert.Nil(t, extractOpenAIChatToolNames("/v1/chat/completions", []byte(`not-json`)))
}

func TestExtractOpenAIChatToolNamesExcludesOtherAPIs(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","function":{"name":"weather"}}]}`)

	assert.Nil(t, extractOpenAIChatToolNames("/v1/responses", body))
	assert.Nil(t, extractOpenAIChatToolNames("/v1/messages", body))
	assert.Nil(t, extractOpenAIChatToolNames("/v1/completions", body))
}
