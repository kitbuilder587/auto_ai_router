package models

import (
	"github.com/mixaill76/auto_ai_router/internal/httputil"
	"github.com/mixaill76/auto_ai_router/internal/scope"
)

func AggregateProviderScopesFromHealth(health *httputil.ProxyHealthResponse, includeFallback bool) ScopeMetadata {
	expressions := make([]*scope.Expression, 0, len(health.Credentials))
	for _, stats := range health.Credentials {
		if !includeFallback && stats.IsFallback {
			continue
		}
		expressions = append(expressions, credentialScopeExpression(stats))
	}
	return scopeMetadataFromExpression(scope.Or(expressions...))
}

func AggregateModelScopesFromHealth(health *httputil.ProxyHealthResponse, includeFallback bool) map[string]ScopeMetadata {
	expressions := make(map[string][]*scope.Expression)
	for _, modelStats := range health.Models {
		if modelStats.Model == "" {
			continue
		}
		credStats, ok := health.Credentials[modelStats.Credential]
		if !ok {
			continue
		}
		if !includeFallback && credStats.IsFallback {
			continue
		}
		expressions[modelStats.Model] = append(expressions[modelStats.Model], modelScopeExpression(modelStats, credStats))
	}

	result := make(map[string]ScopeMetadata, len(expressions))
	for modelID, modelExpressions := range expressions {
		result[modelID] = scopeMetadataFromExpression(scope.Or(modelExpressions...))
	}
	return result
}

func credentialScopeExpression(stats httputil.CredentialHealthStats) *scope.Expression {
	if stats.ScopeExpression != nil {
		return scope.NormalizeExpression(stats.ScopeExpression)
	}
	return scope.FromScopes(stats.Scopes, stats.DeniedScopes)
}

func modelScopeExpression(modelStats httputil.ModelHealthStats, credStats httputil.CredentialHealthStats) *scope.Expression {
	if modelStats.ScopeExpression != nil {
		return scope.NormalizeExpression(modelStats.ScopeExpression)
	}
	if len(modelStats.Scopes) > 0 || len(modelStats.DeniedScopes) > 0 {
		return scope.FromScopes(modelStats.Scopes, modelStats.DeniedScopes)
	}
	return credentialScopeExpression(credStats)
}

func scopeMetadataFromExpression(expression *scope.Expression) ScopeMetadata {
	scopes, deniedScopes := expression.LegacyProjection()
	return ScopeMetadata{
		Scopes:          scopes,
		DeniedScopes:    deniedScopes,
		ScopeExpression: scope.NormalizeExpression(expression),
	}
}
