package scopes

import (
	"sort"
	"strings"
)

type Set struct {
	all    bool
	values map[string]struct{}
}

func All() Set {
	return Set{all: true}
}

func Empty() Set {
	return Set{}
}

func From(values []string) Set {
	normalized := NormalizeList(values)
	for _, value := range normalized {
		if value == "*" {
			return All()
		}
	}
	if len(normalized) == 0 {
		return Empty()
	}
	result := Set{values: make(map[string]struct{}, len(normalized))}
	for _, value := range normalized {
		result.values[value] = struct{}{}
	}
	return result
}

func (s Set) IsAll() bool {
	return s.all
}

func (s Set) IsEmpty() bool {
	return !s.all && len(s.values) == 0
}

func (s Set) Has(value string) bool {
	if s.all {
		return true
	}
	_, ok := s.values[normalize(value)]
	return ok
}

func (s Set) Values() []string {
	if s.all {
		return []string{"*"}
	}
	values := make([]string, 0, len(s.values))
	for value := range s.values {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func Allows(credentialScopes []string, requestScopes Set) bool {
	allowed := NormalizeList(credentialScopes)
	if len(allowed) == 0 || requestScopes.IsAll() {
		return true
	}
	if requestScopes.IsEmpty() {
		return false
	}
	for _, value := range allowed {
		if value == "*" || requestScopes.Has(value) {
			return true
		}
	}
	return false
}

func NormalizeList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			value := normalize(part)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
