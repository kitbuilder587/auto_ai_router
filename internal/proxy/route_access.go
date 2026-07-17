package proxy

import "strings"

const (
	liteLLMLLMAPIRoutesGroup = "llm_api_routes"
	liteLLMOpenAIRoutesGroup = "openai_routes"
	liteLLMInfoRoutesGroup   = "info_routes"
)

// isVirtualKeyAllowedToCallRoute mirrors the pinned LiteLLM RouteChecks order
// for the route groups that can reach AIR's public surface. Empty route lists
// are intentionally unrestricted; a non-empty unrecognized list fails closed.
func isVirtualKeyAllowedToCallRoute(allowedRoutes []string, route string) bool {
	if len(allowedRoutes) == 0 {
		return true
	}

	// LiteLLM checks explicit routes before expanding named route groups. An
	// explicit route also grants subpaths on a segment boundary.
	for _, allowedRoute := range allowedRoutes {
		if route == allowedRoute || strings.HasPrefix(route, allowedRoute+"/") {
			return true
		}
	}

	for _, allowedRoute := range allowedRoutes {
		switch allowedRoute {
		case liteLLMLLMAPIRoutesGroup, liteLLMOpenAIRoutesGroup:
			if isPinnedLiteLLMOpenAIRoute(route) {
				return true
			}
		case liteLLMInfoRoutesGroup:
			if route == "/v1/models" {
				return true
			}
		}
	}

	// LiteLLM applies trailing-star wildcard rules after named groups.
	for _, allowedRoute := range allowedRoutes {
		if strings.HasSuffix(allowedRoute, "*") && strings.HasPrefix(route, strings.TrimSuffix(allowedRoute, "*")) {
			return true
		}
	}

	return false
}

// isPinnedLiteLLMOpenAIRoute is the intersection of the pinned image's
// openai_routes group and AIR's currently supported public API. Responses
// resource patterns use one path segment for response_id, as LiteLLM does.
func isPinnedLiteLLMOpenAIRoute(route string) bool {
	switch route {
	case "/v1/chat/completions",
		"/v1/completions",
		"/v1/embeddings",
		"/v1/images/generations",
		"/v1/images/edits",
		"/v1/models",
		"/v1/responses":
		return true
	}

	const responsesPrefix = "/v1/responses/"
	if !strings.HasPrefix(route, responsesPrefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(route, responsesPrefix), "/")
	if len(parts) == 1 {
		return parts[0] != ""
	}
	return len(parts) == 2 && parts[0] != "" && (parts[1] == "input_items" || parts[1] == "cancel")
}

func formatLiteLLMAllowedRoutes(allowedRoutes []string) string {
	var formatted strings.Builder
	formatted.WriteByte('[')
	for index, route := range allowedRoutes {
		if index > 0 {
			formatted.WriteString(", ")
		}
		formatted.WriteByte('\'')
		formatted.WriteString(strings.ReplaceAll(strings.ReplaceAll(route, "\\", "\\\\"), "'", "\\'"))
		formatted.WriteByte('\'')
	}
	formatted.WriteByte(']')
	return formatted.String()
}
