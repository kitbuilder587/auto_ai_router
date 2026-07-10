package balancer

import "github.com/mixaill76/auto_ai_router/internal/config"

// swrrNode holds the smooth weighted round-robin state for a single credential.
type swrrNode struct {
	current int
}

// schedKey identifies an independent SWRR cycle. Using a comparable struct (rather than a
// formatted string) keeps it allocation-free on the selection hot path.
type schedKey struct {
	model        string
	fallbackOnly bool
	proxyOnly    bool
	reqType      config.ProviderType
	excluding    bool
	priority     int
	scopeKey     string
}

// swrrState is the SWRR scheduler for one schedKey. Nodes are keyed by credential name so
// the live set can be reconciled cheaply on every request.
type swrrState struct {
	nodes map[string]*swrrNode
}

func newSWRRState() *swrrState {
	return &swrrState{nodes: make(map[string]*swrrNode)}
}

// advance reconciles the live node set, accumulates effective weight into each live node's
// running counter (nginx smooth weighted round-robin), and returns the total live weight.
// Banned/excluded credentials are absent from liveWeights, so they neither accumulate
// (which would cause a burst when they recover) nor get selected.
func (s *swrrState) advance(liveWeights map[string]int) int {
	for name := range s.nodes {
		if _, ok := liveWeights[name]; !ok {
			delete(s.nodes, name)
		}
	}
	total := 0
	for name, w := range liveWeights {
		n, ok := s.nodes[name]
		if !ok {
			n = &swrrNode{}
			s.nodes[name] = n
		}
		n.current += w
		total += w
	}
	return total
}

// commit charges the selected credential the total live weight, the SWRR step that keeps
// long-run selection proportional to configured weights.
func (s *swrrState) commit(name string, total int) {
	if n, ok := s.nodes[name]; ok {
		n.current -= total
	}
}

func (s *swrrState) currentOf(name string) int {
	if n, ok := s.nodes[name]; ok {
		return n.current
	}
	return 0
}

// schedKeyFor builds the SWRR cycle key. The model is only part of the key when model
// filtering is active; otherwise every model shares one candidate set, so they must share
// one cycle too — keeping the key out avoids unbounded map growth from arbitrary model
// names. Must be called with r.mu held.
func (r *RoundRobin) schedKeyFor(modelID string, allowOnlyFallback, allowOnlyProxy bool, requiredType config.ProviderType, excluding bool, scopeKey string) schedKey {
	model := modelID
	if model == "" || r.modelChecker == nil || !r.modelChecker.IsEnabled() {
		model = ""
	}
	return schedKey{model: model, fallbackOnly: allowOnlyFallback, proxyOnly: allowOnlyProxy, reqType: requiredType, excluding: excluding, scopeKey: scopeKey}
}

// swrrStateFor returns (creating if needed) the SWRR scheduler for a selection cycle.
// Must be called with r.mu held.
func (r *RoundRobin) swrrStateFor(key schedKey) *swrrState {
	st, ok := r.swrr[key]
	if !ok {
		st = newSWRRState()
		r.swrr[key] = st
	}
	return st
}

// EffectiveWeight resolves the weighted round-robin fallback chain: model-level override,
// then credential default, then 1.
func EffectiveWeight(modelWeight, credWeight int) int {
	if modelWeight > 0 {
		return modelWeight
	}
	if credWeight > 0 {
		return credWeight
	}
	return 1
}

// effectiveWeight resolves the weight for a (credential, model) pair, mirroring how RPM is
// resolved: model-level override first, then the credential default, then 1.
func (r *RoundRobin) effectiveWeight(cred *config.CredentialConfig, modelID string) int {
	modelWeight := 0
	if modelID != "" && r.modelChecker != nil && r.modelChecker.IsEnabled() {
		modelWeight = r.modelChecker.GetModelWeightForCredential(modelID, cred.Name)
	}
	return EffectiveWeight(modelWeight, cred.Weight)
}
