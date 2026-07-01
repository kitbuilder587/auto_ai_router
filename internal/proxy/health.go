package proxy

import (
	"net/http"

	"github.com/mixaill76/auto_ai_router/internal/balancer"
	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/httputil"
	modelpkg "github.com/mixaill76/auto_ai_router/internal/models"
	"github.com/mixaill76/auto_ai_router/internal/proxy/webui"
	"github.com/mixaill76/auto_ai_router/internal/scopes"
)

func (p *Proxy) HealthCheck() (bool, *httputil.ProxyHealthResponse) {
	return p.healthCheckForScopes(scopes.All(), true)
}

func (p *Proxy) HealthCheckForScopes(requestScopes scopes.Set) (bool, *httputil.ProxyHealthResponse) {
	return p.healthCheckForScopes(requestScopes, false)
}

func (p *Proxy) healthCheckForScopes(requestScopes scopes.Set, useGlobalBannedCount bool) (bool, *httputil.ProxyHealthResponse) {
	creds := p.balancer.GetCredentialsSnapshotWithScopes(requestScopes)
	totalCreds := len(creds)
	availableCreds := p.balancer.GetAvailableCountWithScopes(requestScopes)
	bannedCreds := p.balancer.GetBannedCountWithScopes(requestScopes)
	if useGlobalBannedCount {
		bannedCreds = p.balancer.GetBannedCount()
	}

	healthy := availableCreds > 0

	// Collect credentials info
	credentialsInfo := make(map[string]httputil.CredentialHealthStats)
	if creds == nil {
		creds = []config.CredentialConfig{}
	}
	visibleCreds := make(map[string]bool, len(creds))
	for _, cred := range creds {
		visibleCreds[cred.Name] = true

		// For proxy credentials, get limits from rateLimiter (updated by UpdateStatsFromRemoteProxy)
		// For other credentials, use config values
		limitRPM := cred.RPM
		limitTPM := cred.TPM
		if cred.Type == config.ProviderTypeProxy {
			rateLimiterRPM := p.rateLimiter.GetLimitRPM(cred.Name)
			rateLimiterTPM := p.rateLimiter.GetLimitTPM(cred.Name)
			if rateLimiterRPM != -1 {
				limitRPM = rateLimiterRPM
			}
			if rateLimiterTPM != -1 {
				limitTPM = rateLimiterTPM
			}
		}

		// Check if credential has any banned models
		isBanned := p.balancer.HasAnyBan(cred.Name)

		credentialsInfo[cred.Name] = httputil.CredentialHealthStats{
			Type:       string(cred.Type),
			IsFallback: cred.IsFallback,
			IsBanned:   isBanned,
			Weight:     balancer.EffectiveWeight(0, cred.Weight),
			Scopes:     cred.Scopes,
			CurrentRPM: p.rateLimiter.GetCurrentRPM(cred.Name),
			CurrentTPM: p.rateLimiter.GetCurrentTPM(cred.Name),
			LimitRPM:   limitRPM,
			LimitTPM:   limitTPM,
		}
	}

	// Collect models info from rateLimiter (which tracks all credential:model pairs)
	modelsInfo := make(map[string]httputil.ModelHealthStats)

	// Get all tracked credential:model pairs from rateLimiter (pre-parsed)
	// This includes duplicates when same model is available from different credentials
	allTrackedModels := p.rateLimiter.GetAllModelPairs()
	for _, pair := range allTrackedModels {
		if !visibleCreds[pair.Credential] {
			continue
		}
		p.addModelHealthStats(modelsInfo, creds, pair.Credential, pair.Model)
	}

	if p.modelManager != nil {
		for _, cred := range creds {
			for _, model := range p.modelManager.GetModelsForCredential(cred.Name) {
				modelKey := cred.Name + ":" + model.ID
				if _, ok := modelsInfo[modelKey]; ok {
					continue
				}
				p.addModelHealthStats(modelsInfo, creds, cred.Name, model.ID)
			}
		}
	}

	// Enrich models and credentials with error code counts from banned pairs
	bannedPairs := p.balancer.GetBannedPairs()
	// credentialErrorCounts accumulates error counts per credential across all its banned models
	credentialErrorCounts := make(map[string]map[int]int)
	for _, bp := range bannedPairs {
		if !visibleCreds[bp.Credential] {
			continue
		}
		modelKey := bp.Credential + ":" + bp.Model
		if ms, ok := modelsInfo[modelKey]; ok {
			if len(bp.ErrorCodeCounts) > 0 {
				counts := make(map[int]int, len(bp.ErrorCodeCounts))
				for code, cnt := range bp.ErrorCodeCounts {
					counts[code] = cnt
				}
				ms.ErrorCodeCounts = counts
				modelsInfo[modelKey] = ms
			}
		}
		// Aggregate into per-credential counts
		if len(bp.ErrorCodeCounts) > 0 {
			if credentialErrorCounts[bp.Credential] == nil {
				credentialErrorCounts[bp.Credential] = make(map[int]int)
			}
			for code, cnt := range bp.ErrorCodeCounts {
				credentialErrorCounts[bp.Credential][code] += cnt
			}
		}
	}
	// Apply aggregated error counts to credential info
	for credName, counts := range credentialErrorCounts {
		if cs, ok := credentialsInfo[credName]; ok {
			cs.BannedErrorCounts = counts
			credentialsInfo[credName] = cs
		}
	}

	status := &httputil.ProxyHealthResponse{
		Status:               "healthy",
		CredentialsAvailable: availableCreds,
		CredentialsBanned:    bannedCreds,
		TotalCredentials:     totalCreds,
		Credentials:          credentialsInfo,
		Models:               modelsInfo,
	}

	if !healthy {
		status.Status = "unhealthy"
	}

	return healthy, status
}

func (p *Proxy) ModelsForScopes(requestScopes scopes.Set, includeAccessGroups bool) modelpkg.ModelsResponse {
	if p.modelManager == nil {
		return modelpkg.ModelsResponse{Object: "list", Data: []modelpkg.Model{}}
	}
	if requestScopes.IsAll() {
		if includeAccessGroups {
			return p.modelManager.GetAllModelsWithAccessGroups()
		}
		return p.modelManager.GetAllModels()
	}
	return p.modelManager.GetModelsForCredentials(
		p.balancer.GetCredentialsSnapshotWithScopes(requestScopes),
		includeAccessGroups,
	)
}

func (p *Proxy) addModelHealthStats(
	modelsInfo map[string]httputil.ModelHealthStats,
	creds []config.CredentialConfig,
	credentialName string,
	modelID string,
) {
	modelKey := credentialName + ":" + modelID
	credWeight := credentialWeight(creds, credentialName)
	modelWeight := 0
	if p.modelManager != nil {
		modelWeight = p.modelManager.GetModelWeightForCredential(modelID, credentialName)
	}
	modelsInfo[modelKey] = httputil.ModelHealthStats{
		Credential: credentialName,
		Model:      modelID,
		IsBanned:   p.balancer.IsBanned(credentialName, modelID),
		Weight:     balancer.EffectiveWeight(modelWeight, credWeight),
		CurrentRPM: p.rateLimiter.GetCurrentModelRPM(credentialName, modelID),
		CurrentTPM: p.rateLimiter.GetCurrentModelTPM(credentialName, modelID),
		LimitRPM:   p.rateLimiter.GetModelLimitRPM(credentialName, modelID),
		LimitTPM:   p.rateLimiter.GetModelLimitTPM(credentialName, modelID),
	}
}

func credentialWeight(creds []config.CredentialConfig, credentialName string) int {
	for _, cred := range creds {
		if cred.Name == credentialName {
			return cred.Weight
		}
	}
	return 0
}

// VisualHealthCheck serves the static health dashboard HTML.
func (p *Proxy) VisualHealthCheck(w http.ResponseWriter, r *http.Request) {
	webui.ServeHealth(w, r)
}
