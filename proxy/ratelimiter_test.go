package proxy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// helper to quickly compile a RateLimit for tests.
func mustCompile(t *testing.T, rl RateLimit) *compiledRateLimit {
	t.Helper()
	require.NoError(t, rl.compile())
	return rl.compiled
}

func TestRateLimiter_BurstAndSteadyRate(t *testing.T) {
	t.Parallel()
	// 1 request every 100ms, burst 2.
	rl := mustCompile(t, RateLimit{
		PathRegexp: ".*",
		Requests:   1,
		Per:        100 * time.Millisecond,
		Burst:      2,
	})

	L402Key := "userA"

	// Burst allows two requests.
	require.True(t, rl.allowFor(L402Key))
	require.True(t, rl.allowFor(L402Key))

	// Third should be immediately denied.
	require.False(t, rl.allowFor(L402Key))

	// Suggested retry delay should come from the steady rate ~100ms.
	delay, ok := rl.reserveDelay(L402Key)
	require.True(t, ok)
	require.GreaterOrEqual(t, delay, 95*time.Millisecond)
	require.Less(t, delay, 200*time.Millisecond)

	// Wait for the delay to pass, then check again.
	time.Sleep(delay)
	require.True(t, rl.allowFor(L402Key))
	require.False(t, rl.allowFor(L402Key))
}

func TestRateLimiter_ReserveDelayDoesNotConsume(t *testing.T) {
	t.Parallel()
	// 1 request every 200ms, burst 1. Use global limiter (empty L402 key).
	rl := mustCompile(t, RateLimit{
		PathRegexp: ".*",
		Requests:   1,
		Per:        200 * time.Millisecond,
		Burst:      1,
	})

	// Consume the single burst token.
	require.True(t, rl.allowFor(""))

	// The next one is denied.
	require.False(t, rl.allowFor(""))

	// Compute delay twice; should not consume tokens or increase delay
	// beyond a single interval. The first delay should be close to 200ms.
	d1, ok := rl.reserveDelay("")
	require.True(t, ok)
	require.GreaterOrEqual(t, d1, 180*time.Millisecond)
	require.Less(t, d1, 300*time.Millisecond)

	// Immediately compute the delay again. The delay should be roughly the
	// same (since we canceled the reservation and did not consume a token).
	d2, ok := rl.reserveDelay("")
	require.True(t, ok)

	// Allow for some scheduler jitter; ensure the second delay isn't
	// inflated by another interval.
	require.Less(t, d2, d1+50*time.Millisecond)

	// After the original delay has passed, we should be allowed again.
	time.Sleep(d1)
	require.True(t, rl.allowFor(""))
}

func TestRateLimiter_MultipleRules_StrictestGoverns(t *testing.T) {
	t.Parallel()
	// Two rules match the same path:
	// lenient allows burst 2, strict allows burst 1.
	strictRateLimit := RateLimit{
		PathRegexp: ".*",
		Requests:   1,
		Per:        200 * time.Millisecond,
		Burst:      1}
	strict := mustCompile(t, strictRateLimit)

	lenientRateLimit := RateLimit{
		PathRegexp: ".*",
		Requests:   10,
		Per:        200 * time.Millisecond,
		Burst:      2,
	}
	lenient := mustCompile(t, lenientRateLimit)

	key := "userA"

	allowsAll := func() bool {
		return strict.allowFor(key) && lenient.allowFor(key)
	}

	// The first request passes both.
	require.True(t, allowsAll())

	// The second request should be denied by strict rule even though
	// lenient would allow it. The overall decision is to deny.
	require.False(t, allowsAll())

	// Suggested retry delay should come from the strict rule ~200ms.
	delay, ok := strict.reserveDelay(key)
	require.True(t, ok)
	require.GreaterOrEqual(t, delay, 180*time.Millisecond)
	require.Less(t, delay, 300*time.Millisecond)
}

func TestRateLimiter_PerIdentityIsolationVsGlobal(t *testing.T) {
	t.Parallel()
	// 1 rps, burst 1. Separate identities shouldn't affect each other.
	rl := mustCompile(
		t, RateLimit{
			PathRegexp: ".*",
			Requests:   1,
			Per:        time.Second,
			Burst:      1},
	)

	// Two distinct identities.
	L402KeyA := "A"
	L402KeyB := "B"

	// Both have their own burst token available.
	require.True(t, rl.allowFor(L402KeyA))
	require.True(t, rl.allowFor(L402KeyB))

	// The second immediate call for A should be denied while B is still
	// unaffected.
	require.False(t, rl.allowFor(L402KeyA))
	require.False(t, rl.allowFor(L402KeyB)) // still denied

	// Global limiter: separate instance with its own bucket.
	rl2 := mustCompile(
		t, RateLimit{
			PathRegexp: ".*",
			Requests:   1,
			Per:        time.Second,
			Burst:      1},
	)
	require.True(t, rl2.allowFor(""))  // consume global burst
	require.False(t, rl2.allowFor("")) // no more global tokens
}
