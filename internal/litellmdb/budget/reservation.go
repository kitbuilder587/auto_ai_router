// Package budget provides atomic Redis-backed budget pre-reservation.
//
// A request seeds an entity counter from the authenticated PostgreSQL spend
// snapshot, reserves its estimated maximum cost atomically, and reconciles the
// reservation once the provider's real cost is known. This closes the
// check-then-spend race between concurrent AIR replicas.
package budget

import (
	"context"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"
)

const commandTimeout = 3 * time.Second

const luaTryReserve = `
local key = KEYS[1]
local db_spend = tonumber(ARGV[1])
local estimated_cost = tonumber(ARGV[2])
local max_budget = tonumber(ARGV[3])
local ttl = tonumber(ARGV[4])
if redis.call('EXISTS', key) == 0 then
  redis.call('SET', key, db_spend)
end
local new_value = redis.call('INCRBYFLOAT', key, estimated_cost)
redis.call('EXPIRE', key, ttl)
if max_budget >= 0 and tonumber(new_value) > max_budget then
  redis.call('INCRBYFLOAT', key, -estimated_cost)
  return 0
end
return 1
`

// Reserver performs atomic budget reservations against Redis/Valkey. A nil
// Reserver or nil client is deliberately a no-op so deployments can fail open
// to the existing PostgreSQL snapshot validation.
type Reserver struct {
	client    valkey.Client
	keyPrefix string
	ttl       time.Duration
}

func New(client valkey.Client, keyPrefix string, ttl time.Duration) *Reserver {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &Reserver{client: client, keyPrefix: keyPrefix, ttl: ttl}
}

func (r *Reserver) enabled() bool {
	return r != nil && r.client != nil
}

func (r *Reserver) commandContext(parent context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= commandTimeout {
		return parent, func() {}
	}
	return context.WithTimeout(parent, commandTimeout)
}

// TryReserve adds estimatedCost to entity's shared counter and rolls the
// increment back if it would exceed maxBudget. maxBudget < 0 is unlimited.
func (r *Reserver) TryReserve(
	ctx context.Context,
	entity string,
	dbSpend float64,
	estimatedCost float64,
	maxBudget float64,
) (bool, error) {
	if !r.enabled() {
		return true, nil
	}
	commandCtx, cancel := r.commandContext(ctx)
	defer cancel()

	ttlSeconds := max(int64(r.ttl/time.Second), 1)
	result, err := r.client.Do(commandCtx, r.client.B().Eval().
		Script(luaTryReserve).
		Numkeys(1).
		Key(r.keyPrefix+entity).
		Arg(strconv.FormatFloat(dbSpend, 'f', -1, 64)).
		Arg(strconv.FormatFloat(estimatedCost, 'f', -1, 64)).
		Arg(strconv.FormatFloat(maxBudget, 'f', -1, 64)).
		Arg(strconv.FormatInt(ttlSeconds, 10)).
		Build()).AsInt64()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

// Reconcile changes a successful reservation by actualCost-estimatedCost.
func (r *Reserver) Reconcile(ctx context.Context, entity string, delta float64) error {
	if !r.enabled() || delta == 0 {
		return nil
	}
	commandCtx, cancel := r.commandContext(ctx)
	defer cancel()

	key := r.keyPrefix + entity
	if err := r.client.Do(commandCtx, r.client.B().Incrbyfloat().
		Key(key).
		Increment(delta).
		Build()).Error(); err != nil {
		return err
	}
	ttlSeconds := max(int64(r.ttl/time.Second), 1)
	_ = r.client.Do(commandCtx, r.client.B().Expire().
		Key(key).
		Seconds(ttlSeconds).
		Build()).Error()
	return nil
}
