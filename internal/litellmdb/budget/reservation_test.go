package budget

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/valkey-io/valkey-go"
)

func integrationReserver(t *testing.T, prefix string) *Reserver {
	t.Helper()
	address := os.Getenv("VALKEY_ADDR")
	if address == "" {
		t.Skip("VALKEY_ADDR not set")
	}
	client, err := valkey.NewClient(valkey.ClientOption{
		InitAddress:       []string{address},
		ForceSingleClient: true,
	})
	require.NoError(t, err)
	t.Cleanup(client.Close)
	return New(client, prefix, time.Minute)
}

func TestNilReserverIsNoOp(t *testing.T) {
	var reserver *Reserver
	allowed, err := reserver.TryReserve(context.Background(), "token:test", 99, 5, 100)
	require.NoError(t, err)
	require.True(t, allowed)
	require.NoError(t, reserver.Reconcile(context.Background(), "token:test", -5))
}

func TestReservationRejectsAndRollsBack(t *testing.T) {
	reserver := integrationReserver(t, "test:budget:rollback:")
	ctx := context.Background()

	allowed, err := reserver.TryReserve(ctx, "token:test", 99, 5, 100)
	require.NoError(t, err)
	require.False(t, allowed)

	allowed, err = reserver.TryReserve(ctx, "token:test", 99, 0.5, 100)
	require.NoError(t, err)
	require.True(t, allowed)
}

func TestReservationReconcilesToActualCost(t *testing.T) {
	reserver := integrationReserver(t, "test:budget:reconcile:")
	ctx := context.Background()

	allowed, err := reserver.TryReserve(ctx, "token:test", 50, 40, 100)
	require.NoError(t, err)
	require.True(t, allowed)
	require.NoError(t, reserver.Reconcile(ctx, "token:test", -30))

	allowed, err = reserver.TryReserve(ctx, "token:test", 50, 39, 100)
	require.NoError(t, err)
	require.True(t, allowed)
}
