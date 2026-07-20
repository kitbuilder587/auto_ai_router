package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/connection"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/queries"
	"github.com/mixaill76/auto_ai_router/internal/security"
	"github.com/mixaill76/auto_ai_router/internal/utils"
)

// Authenticator provides token authentication via LiteLLM database
// Synchronous (blocking) - token validation must complete before request processing
type Authenticator struct {
	pool   *connection.ConnectionPool
	cache  *Cache
	logger *slog.Logger
}

// NewAuthenticator creates a new authenticator
func NewAuthenticator(pool *connection.ConnectionPool, cache *Cache, logger *slog.Logger) *Authenticator {
	return &Authenticator{
		pool:   pool,
		cache:  cache,
		logger: logger,
	}
}

// FetchMasterKey seeds the auth cache with the proxy master key. The config
// value is the source of truth: the copy litellm stores in
// LiteLLM_Config.general_settings never overrides it and is read only to
// detect drift — a differing DB value means litellm and AIR are configured
// with different keys, so the trusted litellm->AIR hop would break. The DB
// copy is used only when the config value is empty, and a DB failure is
// non-fatal so the config key keeps working without the DB.
func (a *Authenticator) FetchMasterKey(ctx context.Context, defaultKey string) error {
	dbKey := a.fetchMasterKeyFromDB(ctx)

	masterKey := defaultKey
	source := "config"
	switch {
	case masterKey == "":
		masterKey = dbKey
		source = "DB"
	case dbKey != "" && dbKey != masterKey:
		a.logger.Warn("Master key in LiteLLM DB differs from config; config value takes precedence")
	}
	if masterKey == "" {
		return models.ErrTokenNotFound
	}

	info := models.TokenInfo{
		Token:   HashToken(masterKey),
		KeyName: "litellm-master-key",
		UserID:  "litellm-master-key",
	}
	a.cache.Set(info.Token, &info)

	a.logger.Debug("Master key loaded", "source", source)

	return nil
}

// fetchMasterKeyFromDB returns the master key stored in LiteLLM_Config, or ""
// when the database is unavailable or stores none.
func (a *Authenticator) fetchMasterKeyFromDB(ctx context.Context) string {
	if a.pool == nil || !a.pool.IsHealthy() {
		return ""
	}

	conn, err := a.pool.Acquire(ctx)
	if err != nil {
		a.logger.Warn("Failed to acquire connection for master key lookup",
			"error", err,
		)
		return ""
	}
	defer conn.Release()

	var masterKey *string
	if err := conn.QueryRow(ctx, queries.QueryMasterKey).Scan(&masterKey); err != nil {
		a.logger.Warn("Failed to fetch master key from DB",
			"error", err,
		)
		return ""
	}
	if masterKey == nil {
		return ""
	}
	return *masterKey
}

// ValidateToken validates a token and returns its information
//
// Algorithm:
// 1. Hash token (sha256) if it starts with "sk-"
// 2. Check cache
// 3. If not in cache - query database
// 4. Validate (blocked, expires, budget)
// 5. Cache result
//
// Returns error if token is invalid or database is unavailable
func (a *Authenticator) ValidateToken(ctx context.Context, rawToken string) (*models.TokenInfo, error) {
	if rawToken == "" {
		return nil, models.ErrTokenNotFound
	}

	// 1. Hash token
	hashedToken := HashToken(rawToken)

	// 2. Check cache
	if info, ok := a.cache.Get(hashedToken); ok {
		a.logger.Debug("Token found in cache",
			"token_prefix", security.MaskToken(hashedToken),
		)
		// Validate even from cache (expires, budget could have changed externally)
		if err := info.Validate(""); err != nil {
			return nil, err
		}
		return info, nil
	}

	// 3. Query database. A miss may be a rotated key inside its grace period:
	// LiteLLM keeps the old hash working until revoke_at via
	// LiteLLM_DeprecatedVerificationToken (utils.py _lookup_deprecated_key),
	// so mirror that with a single non-chaining fallback to the active token.
	info, err := a.fetchTokenFromDB(ctx, hashedToken)
	if err == models.ErrTokenNotFound {
		if activeToken := a.lookupDeprecatedToken(ctx, hashedToken); activeToken != "" {
			a.logger.Debug("Deprecated key used during grace period",
				"token_prefix", security.MaskToken(hashedToken),
			)
			info, err = a.fetchTokenFromDB(ctx, activeToken)
		}
	}
	if err != nil {
		return nil, err
	}

	// 4. Validate
	if err := info.Validate(""); err != nil {
		// Don't cache invalid tokens
		return nil, err
	}

	// 5. Cache
	a.cache.Set(hashedToken, info)

	a.logger.Debug("Token validated from DB",
		"token_prefix", security.MaskToken(hashedToken),
		"user_id", info.UserID,
		"team_id", info.TeamID,
	)

	return info, nil
}

// ValidateTokenForModel validates a token with model access check
func (a *Authenticator) ValidateTokenForModel(ctx context.Context, rawToken, model string) (*models.TokenInfo, error) {
	info, err := a.ValidateToken(ctx, rawToken)
	if err != nil {
		return nil, err
	}

	// Check model access
	if !info.IsModelAllowed(model) {
		return nil, models.ErrModelNotAllowed
	}

	return info, nil
}

// fetchTokenFromDB loads token from database with full budget hierarchy
// Single query loads: Token → User → Team → Organization → Memberships
// All with their budget data (embedded or external via BudgetTable)
func (a *Authenticator) fetchTokenFromDB(ctx context.Context, hashedToken string) (*models.TokenInfo, error) {
	if !a.pool.IsHealthy() {
		return nil, models.ErrConnectionFailed
	}

	conn, err := a.pool.Acquire(ctx)
	if err != nil {
		a.logger.Error("Failed to acquire connection for auth",
			"error", err,
		)
		return nil, models.ErrConnectionFailed
	}
	defer conn.Release()
	var info models.TokenInfo

	// ============ Token fields ============
	var keyName, keyAlias, userID, teamID, orgID, projectID, agentID *string
	var tokenMaxBudget *float64
	var tokenTPMLimit, tokenRPMLimit *int64
	var expires *time.Time
	var blocked *bool
	var tokenModels []string
	var tokenAllowedRoutes []string
	var tokenMetadata []byte

	// ============ User fields ============
	var userIDCheck, userAlias, userEmail *string
	var userMaxBudget, userSpend *float64
	var userTPMLimit, userRPMLimit *int64
	var userModels []string

	// ============ Team fields ============
	var teamIDCheck, teamAlias *string
	var teamOrganizationID *string
	var teamMaxBudget, teamSpend *float64
	var teamBlocked *bool
	var teamTPMLimit, teamRPMLimit *int64
	var teamModels []string

	// ============ Project fields ============
	var projectIDCheck *string
	var projectModels []string
	var projectBlocked *bool

	// ============ Organization fields (with external budget) ============
	var orgIDCheck *string
	var orgSpend *float64
	var orgMaxBudget *float64
	var orgTPMLimit, orgRPMLimit *int64

	// ============ TeamMembership fields (with external budget) ============
	var teamMemberSpend *float64
	var teamMemberMaxBudget *float64
	var teamMemberTPMLimit, teamMemberRPMLimit *int64
	var teamMemberModels []string

	// ============ OrganizationMembership fields (with external budget) ============
	var orgMemberSpend *float64
	var orgMemberMaxBudget *float64
	var orgMemberTPMLimit, orgMemberRPMLimit *int64

	// ============ Access group IDs ============
	var tokenAccessGroupIDs, teamAccessGroupIDs []string

	err = conn.QueryRow(ctx, queries.QueryValidateTokenWithHierarchy, hashedToken).Scan(
		// Token
		&info.Token,
		&keyName,
		&keyAlias,
		&userID,
		&teamID,
		&orgID,
		&info.Spend,
		&tokenMaxBudget,
		&tokenTPMLimit,
		&tokenRPMLimit,
		&expires,
		&blocked,
		&tokenModels,
		&tokenAllowedRoutes,
		&projectID,
		&agentID,
		&tokenMetadata,

		// User
		&userIDCheck,
		&userAlias,
		&userEmail,
		&userMaxBudget,
		&userSpend,
		&userTPMLimit,
		&userRPMLimit,
		&userModels,

		// Team
		&teamIDCheck,
		&teamAlias,
		&teamOrganizationID, // team_organization_id (nullable, positional)
		&teamMaxBudget,
		&teamSpend,
		&teamBlocked,
		&teamTPMLimit,
		&teamRPMLimit,
		&teamModels,

		// Project
		&projectIDCheck,
		&projectModels,
		&projectBlocked,

		// Organization
		&orgIDCheck,
		&orgSpend,
		&orgMaxBudget,
		&orgTPMLimit,
		&orgRPMLimit,

		// TeamMembership
		&teamMemberSpend,
		&teamMemberMaxBudget,
		&teamMemberTPMLimit,
		&teamMemberRPMLimit,
		&teamMemberModels,

		// OrganizationMembership
		&orgMemberSpend,
		&orgMemberMaxBudget,
		&orgMemberTPMLimit,
		&orgMemberRPMLimit,

		// Access groups
		&tokenAccessGroupIDs,
		&teamAccessGroupIDs,
	)

	if err != nil {
		if err == pgx.ErrNoRows {
			a.logger.Debug("Token not found in DB",
				"token_prefix", security.MaskToken(hashedToken),
			)
			return nil, models.ErrTokenNotFound
		}
		a.logger.Error("Failed to query token",
			"error", err,
			"token_prefix", security.MaskToken(hashedToken),
		)
		return nil, models.ErrConnectionFailed
	}

	// Convert nullable Token fields
	if keyName != nil {
		info.KeyName = *keyName
	}
	if keyAlias != nil {
		info.KeyAlias = *keyAlias
	}
	if userID != nil {
		info.UserID = *userID
	}
	if teamID != nil {
		info.TeamID = *teamID
	}
	if orgID != nil {
		info.OrganizationID = *orgID
	}
	if projectID != nil {
		info.ProjectID = *projectID
	}
	if agentID != nil {
		info.AgentID = *agentID
	}
	if blocked != nil {
		info.Blocked = *blocked
	}
	info.Models = tokenModels
	info.AllowedRoutes = tokenAllowedRoutes
	info.Metadata, info.Tags = decodeTokenMetadata(tokenMetadata)

	info.MaxBudget = tokenMaxBudget
	info.Expires = expires
	info.TPMLimit = tokenTPMLimit
	info.RPMLimit = tokenRPMLimit

	// Set User fields (if user exists)
	if userAlias != nil {
		info.UserAlias = *userAlias
	}
	if userEmail != nil {
		info.UserEmail = *userEmail
	}
	info.UserMaxBudget = userMaxBudget
	info.UserSpend = userSpend
	info.UserTPMLimit = userTPMLimit
	info.UserRPMLimit = userRPMLimit
	info.UserModels = userModels

	// Set Team fields (if team exists)
	if teamAlias != nil {
		info.TeamAlias = *teamAlias
	}
	info.TeamMaxBudget = teamMaxBudget
	info.TeamSpend = teamSpend
	info.TeamBlocked = teamBlocked
	info.TeamTPMLimit = teamTPMLimit
	info.TeamRPMLimit = teamRPMLimit
	info.TeamModels = teamModels

	// team_id_check / project_id_check are join sentinels: a NULL there while
	// the token carries the reference means the parent row is gone. Such a
	// dangling parent must fail closed (see TokenInfo.TeamDangling) instead of
	// leaving an unrestricted empty scope. An orphan user_id intentionally
	// keeps its optional user policy unrestricted — production holds valid
	// tokens whose owner row no longer exists (see auth_test.go).
	info.TeamDangling = teamID != nil && teamIDCheck == nil
	info.ProjectDangling = projectID != nil && projectIDCheck == nil

	// Project model scope is enforced whenever the token has a project_id.
	info.ProjectModels = projectModels
	info.ProjectBlocked = projectBlocked

	// Set Organization fields (external budget from BudgetTable)
	info.OrgSpend = orgSpend
	info.OrgMaxBudget = orgMaxBudget
	info.OrgTPMLimit = orgTPMLimit
	info.OrgRPMLimit = orgRPMLimit

	// Set TeamMembership fields (external budget from BudgetTable)
	info.TeamMemberSpend = teamMemberSpend
	info.TeamMemberMaxBudget = teamMemberMaxBudget
	info.TeamMemberTPMLimit = teamMemberTPMLimit
	info.TeamMemberRPMLimit = teamMemberRPMLimit
	info.TeamMemberModels = teamMemberModels

	// Set OrganizationMembership fields (external budget from BudgetTable)
	info.OrgMemberSpend = orgMemberSpend
	info.OrgMemberMaxBudget = orgMemberMaxBudget
	info.OrgMemberTPMLimit = orgMemberTPMLimit
	info.OrgMemberRPMLimit = orgMemberRPMLimit

	// Expand model scopes with unified access groups. A failed group fetch
	// degrades to no expansion (denial stays denial), matching LiteLLM's
	// swallow-and-continue in _get_resources_from_access_groups: it can never
	// grant more access than the DB row authorizes.
	a.applyAccessGroups(ctx, conn, &info, tokenAccessGroupIDs, teamAccessGroupIDs)

	a.logger.Debug("Token loaded with full hierarchy",
		"token_prefix", security.MaskToken(hashedToken),
		"user_id", info.UserID,
		"team_id", info.TeamID,
		"org_id", info.OrganizationID,
	)

	return &info, nil
}

// applyAccessGroups resolves the key's and team's access_group_ids into
// model grants on info. Resolution errors are logged and swallowed: the
// expansion only ever widens access, so skipping it fails closed.
func (a *Authenticator) applyAccessGroups(
	ctx context.Context,
	conn *pgxpool.Conn,
	info *models.TokenInfo,
	tokenGroupIDs []string,
	teamGroupIDs []string,
) {
	if len(tokenGroupIDs) == 0 && len(teamGroupIDs) == 0 {
		return
	}

	allIDs := make([]string, 0, len(tokenGroupIDs)+len(teamGroupIDs))
	allIDs = append(allIDs, tokenGroupIDs...)
	allIDs = append(allIDs, teamGroupIDs...)

	rows, err := conn.Query(ctx, queries.QuerySelectAccessGroups, allIDs)
	if err != nil {
		a.logger.Warn("Failed to load access groups; model access falls back to native allowlists",
			"error", err,
		)
		return
	}
	defer rows.Close()

	var groups []models.AccessGroup
	for rows.Next() {
		var group models.AccessGroup
		if err := rows.Scan(&group.ID, &group.Models, &group.AssignedTeamIDs, &group.AssignedKeyIDs); err != nil {
			a.logger.Warn("Failed to scan access group row; model access falls back to native allowlists",
				"error", err,
			)
			return
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		a.logger.Warn("Failed to iterate access groups; model access falls back to native allowlists",
			"error", err,
		)
		return
	}

	info.KeyAccessGroupModels, info.TeamAccessGroupModels = models.ResolveAccessGroupModels(
		info.Token, info.TeamID, tokenGroupIDs, teamGroupIDs, groups,
	)
}

// lookupDeprecatedToken returns the active token hash for a rotated key that
// is still inside its grace period, or "" when there is none. Mirrors
// LiteLLM's _lookup_deprecated_key: lookup failures are swallowed so a
// missing table or DB hiccup degrades to the plain token-not-found outcome.
func (a *Authenticator) lookupDeprecatedToken(ctx context.Context, hashedToken string) string {
	conn, err := a.pool.Acquire(ctx)
	if err != nil {
		a.logger.Warn("Failed to acquire connection for deprecated key lookup",
			"error", err,
		)
		return ""
	}
	defer conn.Release()

	var activeTokenID *string
	err = conn.QueryRow(ctx, queries.QueryLookupDeprecatedToken, hashedToken, utils.NowUTC()).Scan(&activeTokenID)
	if err != nil {
		if err != pgx.ErrNoRows {
			a.logger.Warn("Deprecated key lookup failed",
				"error", err,
				"token_prefix", security.MaskToken(hashedToken),
			)
		}
		return ""
	}
	if activeTokenID == nil {
		return ""
	}
	return *activeTokenID
}

func decodeTokenMetadata(raw []byte) (map[string]interface{}, []string) {
	if len(raw) == 0 {
		return nil, nil
	}

	var metadata map[string]interface{}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, nil
	}
	tagValue, exists := metadata["tags"]
	if !exists || tagValue == nil {
		return metadata, nil
	}
	encodedTags, err := json.Marshal(tagValue)
	if err != nil {
		return metadata, nil
	}
	var tags []string
	if err := json.Unmarshal(encodedTags, &tags); err != nil {
		return metadata, nil
	}
	return metadata, append([]string(nil), tags...)
}

// InvalidateToken removes a token from cache
func (a *Authenticator) InvalidateToken(hashedToken string) {
	a.cache.Invalidate(hashedToken)
}

// CacheStats returns cache statistics
func (a *Authenticator) CacheStats() models.AuthCacheStats {
	return a.cache.Stats()
}

// ==================== Helper functions ====================

// HashToken hashes a token using LiteLLM algorithm
// Tokens starting with "sk-" are hashed with SHA256
// Others are returned as-is (already hashed)
func HashToken(token string) string {
	if strings.HasPrefix(token, "sk-") {
		hash := sha256.Sum256([]byte(token))
		return hex.EncodeToString(hash[:])
	}
	return token
}
