package challenger

import (
	"fmt"
	"sync"
	"sync/atomic" // Import atomic package
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
)

// InvoiceStateStore manages the state of invoices in a thread-safe manner and
// allows goroutines to wait for specific states or initial load completion.
type InvoiceStateStore struct {
	// states holds the last known state for invoices.
	states map[lntypes.Hash]lnrpc.Invoice_InvoiceState

	// mtx guards access to states and initialLoadComplete.
	mtx sync.Mutex

	// cond is used to signal waiters when states is updated or when the
	// initial load completes.
	cond *sync.Cond

	// initialLoadComplete is true once the initial fetching of all
	// historical invoices is done. Use atomic for lock-free reads/writes.
	initialLoadComplete atomic.Bool

	// quit channel signals the store that the challenger is shutting down.
	// Waiters should abort if this channel is closed.
	quit <-chan struct{}
}

// NewInvoiceStateStore creates a new instance of InvoiceStateStore. The quit
// channel should be the challenger's main quit channel.
func NewInvoiceStateStore(quit <-chan struct{}) *InvoiceStateStore {
	s := &InvoiceStateStore{
		states: make(map[lntypes.Hash]lnrpc.Invoice_InvoiceState),
		quit:   quit,
	}

	// Initialize cond with the store's mutex.
	s.cond = sync.NewCond(&s.mtx)

	return s
}

// SetState adds or updates the state for a given invoice hash. It notifies any
// waiting goroutines about the change.
func (s *InvoiceStateStore) SetState(hash lntypes.Hash,
	state lnrpc.Invoice_InvoiceState) {

	s.mtx.Lock()
	defer s.mtx.Unlock()

	// Only broadcast if the state actually changes or is new.
	currentState, exists := s.states[hash]
	if !exists || currentState != state {
		s.states[hash] = state

		// Signal potential waiters.
		s.cond.Broadcast()
	}
}

// DeleteState removes an invoice state from the store, typically used for
// irrelevant (canceled/expired) invoices. It notifies any waiting goroutines
// about the change.
func (s *InvoiceStateStore) DeleteState(hash lntypes.Hash) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	// Only broadcast if the state actually existed.
	if _, exists := s.states[hash]; exists {
		delete(s.states, hash)

		// Signal potential waiters.
		s.cond.Broadcast()
	}
}

// GetState retrieves the current state for a given invoice hash.
func (s *InvoiceStateStore) GetState(hash lntypes.Hash,
) (lnrpc.Invoice_InvoiceState, bool) {

	s.mtx.Lock()
	defer s.mtx.Unlock()

	state, exists := s.states[hash]
	return state, exists
}

// MarkInitialLoadComplete sets the initialLoadComplete flag to true atomically
// and broadcasts on the condition variable to wake up any waiting goroutines.
func (s *InvoiceStateStore) MarkInitialLoadComplete() {
	// Check atomically first to potentially avoid locking and broadcasting.
	if s.initialLoadComplete.Load() {
		// Already marked so we can return early.
		return
	}

	// Grab the lock now to ensure we can use the condition variable safely.
	s.mtx.Lock()
	defer s.mtx.Unlock()

	// Double-check under lock in case another goroutine just did it.
	if !s.initialLoadComplete.Load() {
		s.initialLoadComplete.Store(true)

		// Wake up everyone waiting.
		s.cond.Broadcast()
		log.Infof("Invoice store marked initial load as complete.")
	}
}

// IsInitialLoadComplete checks atomically if the initial historical invoice
// load has finished.
func (s *InvoiceStateStore) IsInitialLoadComplete() bool {
	return s.initialLoadComplete.Load()
}

// waitForCondition blocks until the provided condition function returns true, a
// timeout occurs, or the quit signal is received. The mutex `s.mtx` MUST be
// held by the caller when calling this function. The mutex will be unlocked
// while waiting and re-locked before returning. It returns an error if the
// timeout is reached or the quit signal is received.
func (s *InvoiceStateStore) waitForCondition(condition func() bool,
	timeout time.Duration, timeoutMsg string) error {

	// Check condition immediately before waiting.
	if condition() {
		return nil
	}

	// Start the timeout timer.
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	// Channel to signal when the condition is met or quit signal is
	// received.
	waitDone := make(chan struct{})

	// Goroutine to wait on the condition variable.
	go func() {
		// Re-acquire lock for cond.Wait
		s.mtx.Lock()
		for !condition() {
			// Check quit signal before waiting indefinitely.
			select {
			case <-s.quit:
				s.mtx.Unlock()
				close(waitDone)
				return
			default:
			}

			// Wait for the condition to be signaled.
			s.cond.Wait()
		}
		s.mtx.Unlock()
		close(waitDone)
	}()

	// Unlock to allow the waiting goroutine to acquire it. We expect the
	// caller to already have held the lock.
	s.mtx.Unlock()

	// Wait for either the condition to be met, timeout, or quit signal.
	var errResult error
	select {
	case <-waitDone:
		// Condition met or quit signal received by waiter.
		if !timer.Stop() {
			// Timer already fired and channel might contain value,
			// drain it. Use a select to prevent blocking if the
			// channel is empty.
			select {
			case <-timer.C:
			default:
			}
		}

		// Re-check quit signal after waitDone is closed.
		select {
		case <-s.quit:
			log.Warnf("waitForCondition: Shutdown signal received " +
				"while condition was being met.")

			errResult = fmt.Errorf("challenger shutting down")

		default:
			// Condition was met successfully.
			errResult = nil
		}

	case <-timer.C:
		// Timeout expired.
		log.Warnf("waitForCondition: %s (timeout: %v)", timeoutMsg,
			timeout)
		errResult = fmt.Errorf("%s", timeoutMsg)

		// We need to signal the waiting goroutine to stop, best way is via
		// quit channel, but we don't control that. The waiting goroutine will
		// eventually see the condition is true (if it changes later) or hit the
		// quit signal.

	case <-s.quit:
		// Shutdown signal received while waiting for timer/condition.
		log.Warnf("waitForCondition: Shutdown signal received.")

		timer.Stop()
		errResult = fmt.Errorf("challenger shutting down")
	}

	// Re-acquire lock before returning, as expected by the caller.
	s.mtx.Lock()
	return errResult
}

// WaitForState blocks until the specified invoice hash reaches the desiredState
// or a timeout occurs. It first waits for the initial historical invoice load
// to complete if necessary. initialLoadTimeout applies only if waiting for the
// initial load. requestTimeout applies when waiting for the specific invoice
// state change.
func (s *InvoiceStateStore) WaitForState(hash lntypes.Hash,
	desiredState lnrpc.Invoice_InvoiceState, initialLoadTimeout time.Duration,
	requestTimeout time.Duration) error {

	// Check to see if we need to wait for the initial load to complete.
	if !s.initialLoadComplete.Load() {
		log.Debugf("WaitForState: Initial load not complete, waiting "+
			"up to %v for hash %v...",
			initialLoadTimeout, hash)

		initialLoadCondition := func() bool {
			return s.initialLoadComplete.Load()
		}

		timeoutMsg := fmt.Sprintf("timed out waiting for initial "+
			"invoice load after %v", initialLoadTimeout)

		err := s.waitForCondition(
			initialLoadCondition, initialLoadTimeout, timeoutMsg,
		)
		if err != nil {
			log.Warnf("WaitForState: Error waiting for initial "+
				"load for hash %v: %v", hash, err)
			return err
		}

		log.Debugf("WaitForState: Initial load completed for hash %v",
			hash)
	}

	// We'll first check to see if the state is already where we need it to
	// be.
	currentState, hasInvoice := s.states[hash]
	if hasInvoice && currentState == desiredState {
		log.Debugf("WaitForState: Hash %v already in desired state %v.",
			hash, desiredState)
		return nil
	}

	// If not, then we'll wait in the background for the condition to be
	// met.
	log.Debugf("WaitForState: Waiting up to %v for hash %v to reach "+
		"state %v...", requestTimeout, hash, desiredState)

	specificStateCondition := func() bool {
		// Re-check state within the condition function under lock.
		st, exists := s.states[hash]
		return exists && st == desiredState
	}

	timeoutMsg := fmt.Sprintf("timed out waiting for state %v after %v",
		desiredState, requestTimeout)

	// We'll wait for the invoice to reach the desired state.
	err := s.waitForCondition(
		specificStateCondition, requestTimeout, timeoutMsg,
	)
	if err != nil {
		// If we timed out, provide a more specific error message based
		// on the final state.
		finalState, finalExists := s.states[hash]
		if err.Error() == timeoutMsg {
			log.Warnf("WaitForState: Timed out after %v waiting "+
				"for hash %v state %v. Final state: %v, "+
				"exists: %v", requestTimeout, hash,
				desiredState, finalState, finalExists)

			if !finalExists {
				return fmt.Errorf("no active or settled "+
					"invoice found for hash=%v after "+
					"timeout", hash)
			}

			return fmt.Errorf("invoice status %v not %v before "+
				"timeout for hash=%v", finalState,
				desiredState, hash)
		}

		// Otherwise, it was likely a shutdown error.
		log.Warnf("WaitForState: Error waiting for specific "+
			"state for hash %v: %v", hash, err)
		return err
	}

	// Condition was met successfully.
	log.Debugf("WaitForState: Hash %v reached desired state %v.",
		hash, desiredState)
	return nil
}

// WaitForInitialLoad blocks until the initial historical invoice load has
// completed, or a timeout occurs.
func (s *InvoiceStateStore) WaitForInitialLoad(timeout time.Duration) error {
	// Check if already complete.
	if s.initialLoadComplete.Load() {
		return nil
	}

	log.Debugf("WaitForInitialLoad: Initial load not complete, waiting up to %v...",
		timeout)

	initialLoadCondition := func() bool {
		// Atomic read, no lock needed for this condition check.
		return s.initialLoadComplete.Load()
	}
	timeoutMsg := fmt.Sprintf("timed out waiting for initial invoice load after %v", timeout)

	s.mtx.Lock()

	// Wait for the condition using the helper.
	err := s.waitForCondition(initialLoadCondition, timeout, timeoutMsg)
	if err != nil {
		log.Warnf("WaitForInitialLoad: Error waiting: %v", err)
		return err // Return error (timeout or shutdown)
	}

	log.Debugf("WaitForInitialLoad: Initial load completed.")
	return nil
}
