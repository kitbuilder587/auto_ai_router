package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVirtualKeyRouteAccessMatchesPinnedLiteLLMForAIRSurface(t *testing.T) {
	airLLMRoutes := []string{
		"/v1/chat/completions",
		"/v1/completions",
		"/v1/embeddings",
		"/v1/images/generations",
		"/v1/images/edits",
		"/v1/models",
		"/v1/responses",
		"/v1/responses/resp_123",
		"/v1/responses/resp_123/input_items",
		"/v1/responses/resp_123/cancel",
		// The pinned response-id route pattern also matches AIR's compact path.
		"/v1/responses/compact",
	}

	for _, route := range airLLMRoutes {
		t.Run(route, func(t *testing.T) {
			assert.True(t, isVirtualKeyAllowedToCallRoute(nil, route), "empty allowed_routes is unrestricted")
			assert.True(t, isVirtualKeyAllowedToCallRoute([]string{liteLLMLLMAPIRoutesGroup}, route))
			assert.False(t, isVirtualKeyAllowedToCallRoute([]string{"management_routes"}, route))
		})
	}
}

func TestVirtualKeyRouteAccessPreservesPinnedExplicitAndWildcardRules(t *testing.T) {
	tests := []struct {
		name          string
		allowedRoutes []string
		route         string
		want          bool
	}{
		{name: "exact", allowedRoutes: []string{"/v1/chat/completions"}, route: "/v1/chat/completions", want: true},
		{name: "explicit subpath", allowedRoutes: []string{"/v1/responses"}, route: "/v1/responses/resp_123", want: true},
		{name: "prefix boundary", allowedRoutes: []string{"/v1/responses"}, route: "/v1/responses-evil", want: false},
		{name: "wildcard", allowedRoutes: []string{"/v1/images/*"}, route: "/v1/images/edits", want: true},
		{name: "unknown named group fails closed", allowedRoutes: []string{"management_routes"}, route: "/v1/chat/completions", want: false},
		{name: "openai group", allowedRoutes: []string{liteLLMOpenAIRoutesGroup}, route: "/v1/embeddings", want: true},
		{name: "info group only models", allowedRoutes: []string{liteLLMInfoRoutesGroup}, route: "/v1/models", want: true},
		{name: "info group rejects inference", allowedRoutes: []string{liteLLMInfoRoutesGroup}, route: "/v1/chat/completions", want: false},
		{name: "response id cannot contain slash", allowedRoutes: []string{liteLLMLLMAPIRoutesGroup}, route: "/v1/responses/resp_123/unknown", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isVirtualKeyAllowedToCallRoute(tt.allowedRoutes, tt.route))
		})
	}
}

func TestFormatLiteLLMAllowedRoutes(t *testing.T) {
	assert.Equal(t, "['management_routes']", formatLiteLLMAllowedRoutes([]string{"management_routes"}))
	assert.Equal(t, "['llm_api_routes', '/v1/images/*']", formatLiteLLMAllowedRoutes([]string{"llm_api_routes", "/v1/images/*"}))
}
