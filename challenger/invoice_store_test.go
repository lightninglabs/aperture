package challenger

import (
	"sync"
	"testing"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// TestInvoiceStateStoreSetGetState verifies that SetState correctly stores
// invoice states and GetState retrieves them accurately. It tests the basic
// round-trip of setting and getting a state, overwriting with a new state, and
// querying a hash that has never been set.
func TestInvoiceStateStoreSetGetState(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)

	hash1 := lntypes.Hash{1}
	hash2 := lntypes.Hash{2}
	hashMissing := lntypes.Hash{99}

	// Setting and getting a state should return the correct value.
	store.SetState(hash1, lnrpc.Invoice_OPEN)
	state, exists := store.GetState(hash1)
	require.True(t, exists)
	require.Equal(t, lnrpc.Invoice_OPEN, state)

	// Setting a different hash should be independent.
	store.SetState(hash2, lnrpc.Invoice_SETTLED)
	state, exists = store.GetState(hash2)
	require.True(t, exists)
	require.Equal(t, lnrpc.Invoice_SETTLED, state)

	// The first hash should still be accessible.
	state, exists = store.GetState(hash1)
	require.True(t, exists)
	require.Equal(t, lnrpc.Invoice_OPEN, state)

	// Overwriting a state should return the new value.
	store.SetState(hash1, lnrpc.Invoice_SETTLED)
	state, exists = store.GetState(hash1)
	require.True(t, exists)
	require.Equal(t, lnrpc.Invoice_SETTLED, state)

	// Getting a state for an unknown hash should return false.
	_, exists = store.GetState(hashMissing)
	require.False(t, exists)
}

// TestInvoiceStateStoreDeleteState verifies that DeleteState removes a
// previously set invoice state and that deleting a non-existent hash is a
// no-op.
func TestInvoiceStateStoreDeleteState(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)

	hash := lntypes.Hash{1}

	// Set then delete.
	store.SetState(hash, lnrpc.Invoice_OPEN)
	_, exists := store.GetState(hash)
	require.True(t, exists)

	store.DeleteState(hash)
	_, exists = store.GetState(hash)
	require.False(t, exists)

	// Deleting a non-existent hash should not panic.
	store.DeleteState(lntypes.Hash{99})
}

// TestInvoiceStateStoreSetStateSameValueNoBroadcast verifies that calling
// SetState with the same value as the current state does not trigger a
// broadcast (the condition variable is only signaled on actual changes). We
// verify this indirectly by checking that a waiter for a different state does
// not wake up spuriously.
func TestInvoiceStateStoreSetStateSameValueNoBroadcast(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)
	store.MarkInitialLoadComplete()

	hash := lntypes.Hash{1}
	store.SetState(hash, lnrpc.Invoice_OPEN)

	// Start a waiter for SETTLED. It should not return early.
	done := make(chan error, 1)
	go func() {
		done <- store.WaitForState(
			hash, lnrpc.Invoice_SETTLED,
			100*time.Millisecond, 200*time.Millisecond,
		)
	}()

	// Re-set the same state multiple times; this should not wake the
	// waiter because the state has not actually changed.
	for i := 0; i < 10; i++ {
		store.SetState(hash, lnrpc.Invoice_OPEN)
	}

	// The waiter should time out.
	select {
	case err := <-done:
		require.Error(t, err)
		require.Contains(t, err.Error(), "invoice status not correct")
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not return within expected time")
	}
}

// TestInvoiceStateStoreInitialLoadComplete verifies the behavior of
// MarkInitialLoadComplete and IsInitialLoadComplete.
func TestInvoiceStateStoreInitialLoadComplete(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)

	// Initially, the load should not be complete.
	require.False(t, store.IsInitialLoadComplete())

	// Marking it complete should flip the flag.
	store.MarkInitialLoadComplete()
	require.True(t, store.IsInitialLoadComplete())

	// Calling it again should be idempotent and not panic.
	store.MarkInitialLoadComplete()
	require.True(t, store.IsInitialLoadComplete())
}

// TestInvoiceStateStoreWaitForInitialLoadAlreadyDone verifies that
// WaitForInitialLoad returns immediately if the load is already marked as
// complete.
func TestInvoiceStateStoreWaitForInitialLoadAlreadyDone(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)
	store.MarkInitialLoadComplete()

	err := store.WaitForInitialLoad(100 * time.Millisecond)
	require.NoError(t, err)
}

// TestInvoiceStateStoreWaitForInitialLoadSuccess verifies that
// WaitForInitialLoad blocks until the load is marked as complete by another
// goroutine.
func TestInvoiceStateStoreWaitForInitialLoadSuccess(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)

	// Mark load complete after a short delay.
	go func() {
		time.Sleep(50 * time.Millisecond)
		store.MarkInitialLoadComplete()
	}()

	err := store.WaitForInitialLoad(2 * time.Second)
	require.NoError(t, err)
}

// TestInvoiceStateStoreWaitForInitialLoadTimeout verifies that
// WaitForInitialLoad returns an error if the load does not complete within
// the timeout.
func TestInvoiceStateStoreWaitForInitialLoadTimeout(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)

	err := store.WaitForInitialLoad(50 * time.Millisecond)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timed out waiting for initial")
}

// TestInvoiceStateStoreWaitForInitialLoadShutdown verifies that
// WaitForInitialLoad returns an error when the quit channel is closed before
// the load completes.
func TestInvoiceStateStoreWaitForInitialLoadShutdown(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)

	// Close quit after a short delay.
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(quit)
	}()

	err := store.WaitForInitialLoad(2 * time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "shutting down")
}

// TestInvoiceStateStoreWaitForStateAlreadySettled verifies that WaitForState
// returns immediately if the desired state has already been reached.
func TestInvoiceStateStoreWaitForStateAlreadySettled(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)
	store.MarkInitialLoadComplete()

	hash := lntypes.Hash{1}
	store.SetState(hash, lnrpc.Invoice_SETTLED)

	err := store.WaitForState(
		hash, lnrpc.Invoice_SETTLED,
		100*time.Millisecond, 100*time.Millisecond,
	)
	require.NoError(t, err)
}

// TestInvoiceStateStoreWaitForStateTransition verifies that WaitForState
// blocks until the state changes to the desired value.
func TestInvoiceStateStoreWaitForStateTransition(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)
	store.MarkInitialLoadComplete()

	hash := lntypes.Hash{1}
	store.SetState(hash, lnrpc.Invoice_OPEN)

	// Transition to SETTLED after a short delay.
	go func() {
		time.Sleep(50 * time.Millisecond)
		store.SetState(hash, lnrpc.Invoice_SETTLED)
	}()

	err := store.WaitForState(
		hash, lnrpc.Invoice_SETTLED,
		100*time.Millisecond, 2*time.Second,
	)
	require.NoError(t, err)
}

// TestInvoiceStateStoreWaitForStateTimeout verifies that WaitForState returns
// an error when the desired state is not reached within the timeout.
func TestInvoiceStateStoreWaitForStateTimeout(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)
	store.MarkInitialLoadComplete()

	hash := lntypes.Hash{1}
	store.SetState(hash, lnrpc.Invoice_OPEN)

	err := store.WaitForState(
		hash, lnrpc.Invoice_SETTLED,
		100*time.Millisecond, 50*time.Millisecond,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invoice status not correct")
}

// TestInvoiceStateStoreWaitForStateTimeoutNoInvoice verifies that WaitForState
// returns a specific "no active or settled invoice found" error when the hash
// does not exist in the store after the timeout.
func TestInvoiceStateStoreWaitForStateTimeoutNoInvoice(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)
	store.MarkInitialLoadComplete()

	// Wait for a hash that was never set.
	hash := lntypes.Hash{42}
	err := store.WaitForState(
		hash, lnrpc.Invoice_SETTLED,
		100*time.Millisecond, 50*time.Millisecond,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no active or settled invoice found")
}

// TestInvoiceStateStoreWaitForStateShutdown verifies that WaitForState returns
// a shutdown error when the quit channel is closed while waiting for a state
// transition.
func TestInvoiceStateStoreWaitForStateShutdown(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)
	store.MarkInitialLoadComplete()

	hash := lntypes.Hash{1}
	store.SetState(hash, lnrpc.Invoice_OPEN)

	// Close quit after a short delay.
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(quit)
	}()

	err := store.WaitForState(
		hash, lnrpc.Invoice_SETTLED,
		100*time.Millisecond, 2*time.Second,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "shutting down")
}

// TestInvoiceStateStoreWaitForStateInitialLoadFirst verifies that WaitForState
// waits for the initial load to complete before checking the invoice state.
func TestInvoiceStateStoreWaitForStateInitialLoadFirst(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)

	hash := lntypes.Hash{1}

	// Mark load complete and set state after a short delay.
	go func() {
		time.Sleep(50 * time.Millisecond)
		store.SetState(hash, lnrpc.Invoice_SETTLED)
		store.MarkInitialLoadComplete()
	}()

	err := store.WaitForState(
		hash, lnrpc.Invoice_SETTLED,
		2*time.Second, 2*time.Second,
	)
	require.NoError(t, err)
}

// TestInvoiceStateStoreWaitForStateInitialLoadTimeout verifies that
// WaitForState returns a timeout error if the initial load does not complete
// in time, even though the request timeout itself is generous.
func TestInvoiceStateStoreWaitForStateInitialLoadTimeout(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)

	hash := lntypes.Hash{1}
	err := store.WaitForState(
		hash, lnrpc.Invoice_SETTLED,
		50*time.Millisecond, 2*time.Second,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timed out waiting for initial")
}

// TestWaitForConditionSuccess verifies that waitForCondition returns nil when
// the condition is met before the timeout.
func TestWaitForConditionSuccess(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)

	conditionMet := false

	// Signal the condition after a short delay.
	go func() {
		time.Sleep(50 * time.Millisecond)
		store.mtx.Lock()
		conditionMet = true
		store.cond.Broadcast()
		store.mtx.Unlock()
	}()

	err := store.waitForCondition(
		func() bool { return conditionMet },
		2*time.Second, "should not time out",
	)
	require.NoError(t, err)
}

// TestWaitForConditionAlreadyTrue verifies that waitForCondition returns
// immediately when the condition is already true.
func TestWaitForConditionAlreadyTrue(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)

	err := store.waitForCondition(
		func() bool { return true },
		100*time.Millisecond, "should not time out",
	)
	require.NoError(t, err)
}

// TestWaitForConditionTimeout verifies that waitForCondition returns the
// correct error message when the condition is not met within the timeout.
func TestWaitForConditionTimeout(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)

	err := store.waitForCondition(
		func() bool { return false },
		50*time.Millisecond, "custom timeout message",
	)
	require.Error(t, err)
	require.Equal(t, "custom timeout message", err.Error())
}

// TestWaitForConditionQuit verifies that waitForCondition returns a shutdown
// error when the quit channel is closed before the condition is met.
func TestWaitForConditionQuit(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)

	// Close quit after a short delay.
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(quit)
	}()

	err := store.waitForCondition(
		func() bool { return false },
		2*time.Second, "should not see this",
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "shutting down")
}

// TestInvoiceStateStoreConcurrentSetAndWait exercises concurrent SetState calls
// racing with WaitForState calls to verify thread safety. Multiple goroutines
// set states while multiple goroutines wait for those states.
func TestInvoiceStateStoreConcurrentSetAndWait(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)
	store.MarkInitialLoadComplete()

	const numWaiters = 20
	var wg sync.WaitGroup
	errs := make([]error, numWaiters)

	// Launch waiters before any state is set.
	for i := 0; i < numWaiters; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			hash := lntypes.Hash{byte(idx)}
			errs[idx] = store.WaitForState(
				hash, lnrpc.Invoice_SETTLED,
				100*time.Millisecond, 2*time.Second,
			)
		}(i)
	}

	// Give waiters time to start blocking.
	time.Sleep(20 * time.Millisecond)

	// Set all hashes to SETTLED from concurrent goroutines.
	for i := 0; i < numWaiters; i++ {
		go func(idx int) {
			hash := lntypes.Hash{byte(idx)}
			store.SetState(hash, lnrpc.Invoice_SETTLED)
		}(i)
	}

	wg.Wait()

	for i := 0; i < numWaiters; i++ {
		require.NoError(t, errs[i], "waiter %d should succeed", i)
	}
}

// TestInvoiceStateStoreConcurrentMarkInitialLoad verifies that calling
// MarkInitialLoadComplete from multiple goroutines concurrently is safe and
// idempotent.
func TestInvoiceStateStoreConcurrentMarkInitialLoad(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.MarkInitialLoadComplete()
		}()
	}
	wg.Wait()

	require.True(t, store.IsInitialLoadComplete())
}

// TestInvoiceStateStoreDeleteWhileWaiting verifies that deleting an invoice
// state does not cause a waiter for that hash to spuriously succeed. The
// waiter should still time out since the desired state is never reached.
func TestInvoiceStateStoreDeleteWhileWaiting(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)
	store.MarkInitialLoadComplete()

	hash := lntypes.Hash{1}
	store.SetState(hash, lnrpc.Invoice_OPEN)

	// Start waiting for SETTLED.
	done := make(chan error, 1)
	go func() {
		done <- store.WaitForState(
			hash, lnrpc.Invoice_SETTLED,
			100*time.Millisecond, 200*time.Millisecond,
		)
	}()

	// Delete the state while the waiter is blocking.
	time.Sleep(30 * time.Millisecond)
	store.DeleteState(hash)

	select {
	case err := <-done:
		// The waiter should fail because the state was deleted, not
		// transitioned to SETTLED.
		require.Error(t, err)
		require.Contains(t, err.Error(), "no active or settled invoice found")
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not return within expected time")
	}
}

// TestInvoiceStateStorePropertySetGetRoundTrip uses property-based testing to
// verify that any hash/state pair written with SetState can be read back with
// GetState.
func TestInvoiceStateStorePropertySetGetRoundTrip(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)

	rapid.Check(t, func(rt *rapid.T) {
		// Generate a random hash.
		var hash lntypes.Hash
		hashBytes := rapid.SliceOfN(
			rapid.Byte(), 32, 32,
		).Draw(rt, "hash_bytes")
		copy(hash[:], hashBytes)

		// Generate a random valid invoice state.
		stateVal := rapid.IntRange(0, 3).Draw(rt, "state")
		state := lnrpc.Invoice_InvoiceState(stateVal)

		store.SetState(hash, state)

		got, exists := store.GetState(hash)
		require.True(rt, exists, "state should exist after SetState")
		require.Equal(rt, state, got, "state should match what was set")
	})
}

// TestInvoiceStateStorePropertyDeleteRemoves uses property-based testing to
// verify that after SetState followed by DeleteState, GetState returns false.
func TestInvoiceStateStorePropertyDeleteRemoves(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)

	rapid.Check(t, func(rt *rapid.T) {
		var hash lntypes.Hash
		hashBytes := rapid.SliceOfN(
			rapid.Byte(), 32, 32,
		).Draw(rt, "hash_bytes")
		copy(hash[:], hashBytes)

		stateVal := rapid.IntRange(0, 3).Draw(rt, "state")
		state := lnrpc.Invoice_InvoiceState(stateVal)

		store.SetState(hash, state)
		store.DeleteState(hash)

		_, exists := store.GetState(hash)
		require.False(rt, exists,
			"state should not exist after DeleteState")
	})
}

// TestInvoiceStateStoreMultipleWaitersOnSameHash verifies that multiple
// goroutines waiting on the same hash all wake up when the state changes.
func TestInvoiceStateStoreMultipleWaitersOnSameHash(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)
	store.MarkInitialLoadComplete()

	hash := lntypes.Hash{1}
	store.SetState(hash, lnrpc.Invoice_OPEN)

	const numWaiters = 10
	var wg sync.WaitGroup
	errs := make([]error, numWaiters)

	for i := 0; i < numWaiters; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = store.WaitForState(
				hash, lnrpc.Invoice_SETTLED,
				100*time.Millisecond, 2*time.Second,
			)
		}(i)
	}

	// Allow waiters to start blocking.
	time.Sleep(20 * time.Millisecond)

	// Transition to SETTLED should wake all waiters.
	store.SetState(hash, lnrpc.Invoice_SETTLED)

	wg.Wait()
	for i := 0; i < numWaiters; i++ {
		require.NoError(t, errs[i], "waiter %d should succeed", i)
	}
}

// TestWaitForConditionMultipleBroadcasts verifies that waitForCondition
// correctly re-checks the condition after each broadcast and only returns
// when the condition is actually true.
func TestWaitForConditionMultipleBroadcasts(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)

	counter := 0
	targetCount := 5

	// Spawn a goroutine that increments the counter and broadcasts
	// multiple times, each time not yet meeting the condition until the
	// final increment.
	go func() {
		for i := 0; i < targetCount; i++ {
			time.Sleep(10 * time.Millisecond)
			store.mtx.Lock()
			counter++
			store.cond.Broadcast()
			store.mtx.Unlock()
		}
	}()

	err := store.waitForCondition(
		func() bool { return counter >= targetCount },
		2*time.Second, "should not time out",
	)
	require.NoError(t, err)
}

// TestInvoiceStateStoreWaitForStateIntermediateStates verifies that
// WaitForState correctly ignores intermediate state transitions and only
// returns when the exact desired state is reached.
func TestInvoiceStateStoreWaitForStateIntermediateStates(t *testing.T) {
	t.Parallel()

	quit := make(chan struct{})
	store := NewInvoiceStateStore(quit)
	store.MarkInitialLoadComplete()

	hash := lntypes.Hash{1}
	store.SetState(hash, lnrpc.Invoice_OPEN)

	// Transition through intermediate states before reaching SETTLED.
	go func() {
		time.Sleep(30 * time.Millisecond)
		store.SetState(hash, lnrpc.Invoice_ACCEPTED)
		time.Sleep(30 * time.Millisecond)
		store.SetState(hash, lnrpc.Invoice_SETTLED)
	}()

	err := store.WaitForState(
		hash, lnrpc.Invoice_SETTLED,
		100*time.Millisecond, 2*time.Second,
	)
	require.NoError(t, err)
}
