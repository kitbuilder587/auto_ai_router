package vertexresponses

import "github.com/mixaill76/auto_ai_router/internal/converter/responses"

func generateResponseID() string          { return responses.GenerateResponseID() }
func generateItemID(prefix string) string { return responses.GenerateItemID(prefix) }
