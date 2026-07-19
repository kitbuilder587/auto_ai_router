package budget

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"
)

// reserverForTest builds a Reserver backed by a real Redis/Valkey from
// VALKEY_ADDR, mirroring internal/ratelimit's integration-test convention.
// Skips the test when the variable is unset.
func reserverForTest(t *testing.T, prefix string) *Reserver {
	t.Helper()
	addr := os.Getenv("VALKEY_ADDR")
	if addr == "" {
		t.Skip("VALKEY_ADDR not set, skipping Redis integration test")
	}
	client, err := valkey.NewClient(valkey.ClientOption{
		InitAddress:       []string{addr},
		ForceSingleClient: true,
	})
	if err != nil {
		t.Fatalf("failed to create valkey client: %v", err)
	}
	t.Cleanup(client.Close)
	return New(client, prefix, time.Minute, nil)
}

func TestTryReserve_SeedsFromDBSpendAndAllows(t *testing.T) {
	r := reserverForTest(t, "test:budget:seed:")
	ctx := context.Background()
	entity := "token:seed"

	allowed, err := r.TryReserve(ctx, entity, 90, 5, 100, true)
	if err != nil {
		t.Fatalf("TryReserve error: %v", err)
	}
	if !allowed {
		t.Fatal("expected first reservation under budget to be allowed")
	}
}

func TestTryReserve_RejectsAndRollsBack(t *testing.T) {
	r := reserverForTest(t, "test:budget:reject:")
	ctx := context.Background()
	entity := "token:reject"

	allowed, err := r.TryReserve(ctx, entity, 99, 5, 100, true)
	if err != nil {
		t.Fatalf("TryReserve error: %v", err)
	}
	if allowed {
		t.Fatal("expected reservation over budget to be rejected")
	}
	allowed, err = r.TryReserve(ctx, entity, 99, 0.5, 100, true)
	if err != nil {
		t.Fatalf("TryReserve error: %v", err)
	}
	if !allowed {
		t.Fatal("expected cheap reservation after rollback to be allowed (rollback failed)")
	}
}

func TestTryReserve_ExactBoundaryMatchesLiteLLM190(t *testing.T) {
	r := reserverForTest(t, "test:budget:boundary:")
	ctx := context.Background()

	keyAllowed, err := r.TryReserve(ctx, "token:key", 99, 1, 100, true)
	if err != nil {
		t.Fatalf("key TryReserve error: %v", err)
	}
	if keyAllowed {
		t.Fatal("key reservation at max_budget must be rejected (>= boundary)")
	}

	teamAllowed, err := r.TryReserve(ctx, "team:team", 99, 1, 100, false)
	if err != nil {
		t.Fatalf("team TryReserve error: %v", err)
	}
	if !teamAllowed {
		t.Fatal("team reservation at max_budget must remain allowed (> boundary)")
	}
}

func TestTryReserve_UnlimitedAlwaysAllows(t *testing.T) {
	r := reserverForTest(t, "test:budget:unlimited:")
	allowed, err := r.TryReserve(context.Background(), "token:unlimited", 1e9, 1e9, -1, true)
	if err != nil {
		t.Fatalf("TryReserve error: %v", err)
	}
	if !allowed {
		t.Fatal("expected unlimited budget (max < 0) to always allow")
	}
}

func TestReconcile_AdjustsCounter(t *testing.T) {
	r := reserverForTest(t, "test:budget:reconcile:")
	ctx := context.Background()
	entity := "token:reconcile"
	allowed, err := r.TryReserve(ctx, entity, 50, 40, 100, true)
	if err != nil || !allowed {
		t.Fatalf("setup reservation failed: allowed=%v err=%v", allowed, err)
	}
	if err := r.Reconcile(ctx, entity, -30); err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	allowed, err = r.TryReserve(ctx, entity, 50, 39, 100, true)
	if err != nil {
		t.Fatalf("TryReserve error: %v", err)
	}
	if !allowed {
		t.Fatal("expected room after reconcile reduced the counter")
	}
	if err := r.Reconcile(ctx, entity, 50); err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	allowed, err = r.TryReserve(ctx, entity, 50, 1, 100, true)
	if err != nil {
		t.Fatalf("TryReserve error: %v", err)
	}
	if allowed {
		t.Fatal("expected rejection after reconcile pushed counter over budget")
	}
}

func TestNilReserver_NoOp(t *testing.T) {
	ctx := context.Background()
	var nilReserver *Reserver
	allowed, err := nilReserver.TryReserve(ctx, "e", 0, 1, 1, true)
	if err != nil || !allowed {
		t.Fatalf("nil Reserver TryReserve should be no-op allow: allowed=%v err=%v", allowed, err)
	}
	if err := nilReserver.Reconcile(ctx, "e", 1); err != nil {
		t.Fatalf("nil Reserver Reconcile should be no-op: %v", err)
	}
	rc := New(nil, "test:budget:nilclient:", time.Minute, nil)
	allowed, err = rc.TryReserve(ctx, "e", 100, 100, 1, true)
	if err != nil || !allowed {
		t.Fatalf("nil-client Reserver TryReserve should be no-op allow: allowed=%v err=%v", allowed, err)
	}
	if err := rc.Reconcile(ctx, "e", 5); err != nil {
		t.Fatalf("nil-client Reserver Reconcile should be no-op: %v", err)
	}
}
