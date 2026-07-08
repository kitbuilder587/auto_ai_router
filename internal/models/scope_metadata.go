package models

import (
	"slices"

	"github.com/mixaill76/auto_ai_router/internal/httputil"
	"github.com/mixaill76/auto_ai_router/internal/scope"
)

type scopeAggregation struct {
	unrestricted       bool
	unrestrictedDenied []map[string]struct{}
	wildcardScoped     bool
	wildcardDenied     []map[string]struct{}
	scoped             map[string]struct{}
}

func AggregateProviderScopesFromHealth(health *httputil.ProxyHealthResponse, includeFallback bool) ScopeMetadata {
	var agg scopeAggregation
	for _, stats := range health.Credentials {
		if !includeFallback && stats.IsFallback {
			continue
		}
		agg.add(stats.Scopes, stats.DeniedScopes)
	}
	return agg.metadata()
}

func AggregateModelScopesFromHealth(health *httputil.ProxyHealthResponse, includeFallback bool) map[string]ScopeMetadata {
	aggregations := make(map[string]*scopeAggregation)
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
		if aggregations[modelStats.Model] == nil {
			aggregations[modelStats.Model] = &scopeAggregation{}
		}
		aggregations[modelStats.Model].add(credStats.Scopes, credStats.DeniedScopes)
	}

	result := make(map[string]ScopeMetadata, len(aggregations))
	for modelID, agg := range aggregations {
		result[modelID] = agg.metadata()
	}
	return result
}

func (a *scopeAggregation) add(scopes, deniedScopes []string) {
	scopes = scope.NormalizeList(scopes)
	deniedScopes = scope.NormalizeList(deniedScopes)
	if slices.Contains(deniedScopes, "*") {
		return
	}

	denied := toScopeSet(deniedScopes)
	if len(scopes) == 0 {
		a.unrestricted = true
		a.unrestrictedDenied = append(a.unrestrictedDenied, denied)
		return
	}

	if slices.Contains(scopes, "*") {
		a.wildcardScoped = true
		a.wildcardDenied = append(a.wildcardDenied, denied)
		return
	}

	if a.scoped == nil {
		a.scoped = make(map[string]struct{}, len(scopes))
	}
	for _, value := range scopes {
		if _, blocked := denied[value]; blocked {
			continue
		}
		a.scoped[value] = struct{}{}
	}
}

func (a *scopeAggregation) metadata() ScopeMetadata {
	if a.unrestricted {
		denied := intersectScopeSets(a.unrestrictedDenied)
		removeScopes(denied, a.scoped)
		return ScopeMetadata{DeniedScopes: setToSortedList(denied)}
	}
	if a.wildcardScoped {
		denied := intersectScopeSets(a.wildcardDenied)
		removeScopes(denied, a.scoped)
		return ScopeMetadata{
			Scopes:       []string{"*"},
			DeniedScopes: setToSortedList(denied),
		}
	}
	return ScopeMetadata{Scopes: setToSortedList(a.scoped)}
}

func toScopeSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func intersectScopeSets(sets []map[string]struct{}) map[string]struct{} {
	if len(sets) == 0 {
		return nil
	}
	result := make(map[string]struct{}, len(sets[0]))
	for value := range sets[0] {
		result[value] = struct{}{}
	}
	for _, set := range sets[1:] {
		for value := range result {
			if _, ok := set[value]; !ok {
				delete(result, value)
			}
		}
	}
	return result
}

func removeScopes(target, values map[string]struct{}) {
	for value := range values {
		delete(target, value)
	}
}

func setToSortedList(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}
