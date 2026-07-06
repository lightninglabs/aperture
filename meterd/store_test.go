package meterd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestStoreBookDebitExhaust walks a bundle through its life cycle: booking,
// authorization, draw-down and exhaustion.
func TestStoreBookDebitExhaust(t *testing.T) {
	t.Parallel()

	s, err := newStore("")
	require.NoError(t, err)

	// An unknown token is neither authorized nor debitable.
	_, found, err := s.authorize("unknown", 0)
	require.NoError(t, err)
	require.False(t, found)

	_, found, err = s.debit("unknown", 10, 0)
	require.NoError(t, err)
	require.False(t, found)

	// Book a fresh bundle and verify its balance.
	booked, err := s.book("token1", "gpt-test", 1000, 1500)
	require.NoError(t, err)
	require.True(t, booked)

	remaining, found, err := s.authorize("token1", 0)
	require.NoError(t, err)
	require.True(t, found)
	require.EqualValues(t, 1000, remaining)

	// Draw the bundle down.
	after, found, err := s.debit("token1", 600, 0)
	require.NoError(t, err)
	require.True(t, found)
	require.EqualValues(t, 400, after.RemainingTokens)
	require.Equal(t, "gpt-test", after.Model)
	require.EqualValues(t, 1500, after.PriceSats)

	// A zero debit must not change the balance.
	after, found, err = s.debit("token1", 0, 0)
	require.NoError(t, err)
	require.True(t, found)
	require.EqualValues(t, 400, after.RemainingTokens)

	// Over-debiting clamps the balance at zero rather than going
	// negative.
	after, found, err = s.debit("token1", 600, 0)
	require.NoError(t, err)
	require.True(t, found)
	require.EqualValues(t, 0, after.RemainingTokens)

	// The exhausted bundle is still known, but has no balance left.
	remaining, found, err = s.authorize("token1", 0)
	require.NoError(t, err)
	require.True(t, found)
	require.EqualValues(t, 0, remaining)
}

// TestStoreIdempotentBooking verifies that booking the same token twice does
// not reset its balance.
func TestStoreIdempotentBooking(t *testing.T) {
	t.Parallel()

	s, err := newStore("")
	require.NoError(t, err)

	booked, err := s.book("token1", "gpt-test", 1000, 1500)
	require.NoError(t, err)
	require.True(t, booked)

	_, _, err = s.debit("token1", 999, 0)
	require.NoError(t, err)

	// The second booking must be a no-op that leaves the drawn-down
	// balance untouched.
	booked, err = s.book("token1", "other-model", 1000, 9999)
	require.NoError(t, err)
	require.False(t, booked)

	b, ok := s.get("token1")
	require.True(t, ok)
	require.EqualValues(t, 1, b.RemainingTokens)
	require.Equal(t, "gpt-test", b.Model)
	require.EqualValues(t, 1500, b.PriceSats)
}

// TestStorePersistence verifies that balances written to the state file
// survive a store reload intact.
func TestStorePersistence(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "meterd-state.json")

	s, err := newStore(statePath)
	require.NoError(t, err)

	// Book two bundles, authorize one and draw it down partially.
	_, err = s.book("token1", "gpt-test", 1000, 1500)
	require.NoError(t, err)
	_, err = s.book("token2", "claude-test", 2000, 3000)
	require.NoError(t, err)

	_, _, err = s.authorize("token1", 0)
	require.NoError(t, err)
	_, _, err = s.debit("token1", 250, 0)
	require.NoError(t, err)

	// The state file must exist after the mutations above.
	_, err = os.Stat(statePath)
	require.NoError(t, err)

	// Reload the store from disk and verify all balances are intact.
	reloaded, err := newStore(statePath)
	require.NoError(t, err)

	b1, ok := reloaded.get("token1")
	require.True(t, ok)
	require.EqualValues(t, 750, b1.RemainingTokens)
	require.Equal(t, "gpt-test", b1.Model)
	require.EqualValues(t, 1500, b1.PriceSats)
	require.True(t, b1.Authorized)

	b2, ok := reloaded.get("token2")
	require.True(t, ok)
	require.EqualValues(t, 2000, b2.RemainingTokens)
	require.Equal(t, "claude-test", b2.Model)
	require.EqualValues(t, 3000, b2.PriceSats)
	require.False(t, b2.Authorized)
}

// TestStoreCorruptStateFile verifies that a corrupt state file is reported
// as an error instead of silently starting fresh.
func TestStoreCorruptStateFile(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "meterd-state.json")
	require.NoError(t, os.WriteFile(statePath, []byte("not json"), 0644))

	_, err := newStore(statePath)
	require.ErrorContains(t, err, "unable to parse state file")
}

// TestStoreUnauthorizedBundleCap verifies that never-authorized bundles are
// evicted oldest-first once their count exceeds the configured cap, bounding
// challenge-mint spam.
func TestStoreUnauthorizedBundleCap(t *testing.T) {
	t.Parallel()

	s, err := newStoreWithConfig(storeConfig{maxUnauthorized: 3})
	require.NoError(t, err)

	// Pre-populate the map directly with un-authorized bundles carrying
	// strictly increasing creation times, so the eviction order is
	// deterministic. The final booking triggers the cap enforcement.
	base := time.Now().UTC()
	for i := 0; i < 4; i++ {
		s.bundles[fmt.Sprintf("token%d", i)] = &bundle{
			Model:           "gpt-test",
			RemainingTokens: 1000,
			CreatedAt:       base.Add(time.Duration(i) * time.Second),
		}
	}

	// token4 is the newest and booking it pushes the count to five, over
	// the cap of three, evicting the two oldest un-paid bundles.
	s.bundles["token4"] = &bundle{
		Model:           "gpt-test",
		RemainingTokens: 1000,
		CreatedAt:       base.Add(4 * time.Second),
	}
	require.Equal(t, 2, s.evictOldestUnauthorizedLocked())

	require.Len(t, s.bundles, 3)
	_, ok := s.get("token0")
	require.False(t, ok)
	_, ok = s.get("token1")
	require.False(t, ok)
	_, ok = s.get("token2")
	require.True(t, ok)
	_, ok = s.get("token4")
	require.True(t, ok)

	// An authorized bundle is never a candidate for eviction, even when it
	// is the oldest. Mark the oldest survivor authorized and age it, then
	// fill up with fresh un-paid bundles.
	s.bundles["token2"].Authorized = true
	s.bundles["token2"].CreatedAt = base.Add(-time.Hour)

	for i := 5; i < 9; i++ {
		s.bundles[fmt.Sprintf("token%d", i)] = &bundle{
			Model:           "gpt-test",
			RemainingTokens: 1000,
			CreatedAt:       base.Add(time.Duration(i) * time.Second),
		}
	}
	s.evictOldestUnauthorizedLocked()

	// token2 is authorized, so it survives despite being the oldest.
	_, ok = s.get("token2")
	require.True(t, ok)

	// Booking through the public path also enforces the cap.
	for i := 9; i < 12; i++ {
		_, err := s.book(
			fmt.Sprintf("live%d", i), "gpt-test", 1000, 1500,
		)
		require.NoError(t, err)
	}

	// The authorized token2 plus at most maxUnauthorized un-paid bundles
	// remain.
	var unauthorized int
	for _, b := range s.bundles {
		if !b.Authorized {
			unauthorized++
		}
	}
	require.LessOrEqual(t, unauthorized, 3)
}

// TestStoreReservation verifies that the per-request reservation bounds how far
// concurrent authorizations can overdraw a near-empty bundle.
func TestStoreReservation(t *testing.T) {
	t.Parallel()

	s, err := newStore("")
	require.NoError(t, err)

	_, err = s.book("token", "gpt-test", 100, 1500)
	require.NoError(t, err)

	// First authorization sees the full balance and reserves 100.
	available, found, err := s.authorize("token", 100)
	require.NoError(t, err)
	require.True(t, found)
	require.EqualValues(t, 100, available)

	// The bundle now has 100 tokens all reserved, so a second concurrent
	// authorization sees zero available and is denied.
	available, found, err = s.authorize("token", 100)
	require.NoError(t, err)
	require.True(t, found)
	require.EqualValues(t, 0, available)

	// Reporting the first request releases its reservation and debits the
	// actual usage, freeing the balance again.
	after, found, err := s.debit("token", 40, 100)
	require.NoError(t, err)
	require.True(t, found)
	require.EqualValues(t, 60, after.RemainingTokens)
	require.EqualValues(t, 0, after.Reserved)

	// With the reservation released, a fresh authorization sees the
	// remaining balance.
	available, _, err = s.authorize("token", 100)
	require.NoError(t, err)
	require.EqualValues(t, 60, available)
}

// TestStoreCoalescedPersistence verifies that bookings do not rewrite the state
// file synchronously, that a debit flushes immediately, and that an explicit
// flush drains pending state.
func TestStoreCoalescedPersistence(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "state.json")
	s, err := newStore(statePath)
	require.NoError(t, err)

	// A booking only marks the store dirty; it does not write the file.
	_, err = s.book("token", "gpt-test", 1000, 1500)
	require.NoError(t, err)
	require.True(t, s.dirty)
	_, statErr := os.Stat(statePath)
	require.True(t, os.IsNotExist(statErr))

	// An explicit flush drains the pending state to disk and clears the
	// dirty flag.
	require.NoError(t, s.flush())
	require.False(t, s.dirty)
	_, err = os.Stat(statePath)
	require.NoError(t, err)

	// A real debit flushes immediately, without waiting for a flush.
	_, _, err = s.authorize("token", 0)
	require.NoError(t, err)
	_, _, err = s.debit("token", 100, 0)
	require.NoError(t, err)
	require.False(t, s.dirty)

	reloaded, err := newStore(statePath)
	require.NoError(t, err)
	b, ok := reloaded.get("token")
	require.True(t, ok)
	require.EqualValues(t, 900, b.RemainingTokens)
}

// TestStoreExpireStale verifies that only bundles that were never authorized
// and are older than the TTL are expired.
func TestStoreExpireStale(t *testing.T) {
	t.Parallel()

	s, err := newStore("")
	require.NoError(t, err)

	_, err = s.book("stale", "gpt-test", 1000, 1500)
	require.NoError(t, err)
	_, err = s.book("fresh", "gpt-test", 1000, 1500)
	require.NoError(t, err)
	_, err = s.book("used", "gpt-test", 1000, 1500)
	require.NoError(t, err)

	// Age two of the bundles beyond the TTL and mark one of them as
	// authorized.
	old := time.Now().Add(-25 * time.Hour)
	s.bundles["stale"].CreatedAt = old
	s.bundles["used"].CreatedAt = old

	_, _, err = s.authorize("used", 0)
	require.NoError(t, err)

	// Only the stale, never-authorized bundle must be removed.
	require.Equal(t, 1, s.expireStale(24*time.Hour))

	_, ok := s.get("stale")
	require.False(t, ok)
	_, ok = s.get("fresh")
	require.True(t, ok)
	_, ok = s.get("used")
	require.True(t, ok)
}
