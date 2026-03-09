package ratelimit

// counterBackend is the pluggable storage layer for RPM/TPM sliding-window counters.
// All implementations must be safe for concurrent use.
//
// Key naming is the caller's responsibility; the backend treats keys as opaque strings.
// limit == -1 always means "unlimited" for RPM/TPM checks.
type counterBackend interface {
	// tryAllowRPM atomically removes expired entries, checks the RPM limit, and
	// records the request if allowed. Returns true when the request is permitted.
	tryAllowRPM(key string, limit int) bool

	// canAllowRPM checks the RPM limit without recording the request.
	canAllowRPM(key string, limit int) bool

	// canAllowTPM checks whether the sum of tokens in the sliding window is below limit.
	canAllowTPM(key string, limit int) bool

	// consumeTokens records tokenCount tokens consumed at the current time.
	consumeTokens(key string, tokenCount int)

	// currentRPM returns the number of requests recorded in the last 60 seconds.
	currentRPM(key string) int

	// currentTPM returns the sum of tokens recorded in the last 60 seconds.
	currentTPM(key string) int

	// tryAllowAll atomically checks credential RPM+TPM and, if modelKey != "",
	// model RPM+TPM. Records credential and model RPM only when all checks pass.
	// limit == -1 means unlimited for any individual limit.
	tryAllowAll(credKey string, credRPM, credTPM int, modelKey string, modelRPM, modelTPM int) bool

	// setCurrentUsage overwrites the sliding-window counters to reflect the given
	// current RPM and TPM values. Used to sync state from remote proxies.
	// Redis backends may implement this as a no-op.
	setCurrentUsage(key string, currentRPM, currentTPM int)
}
