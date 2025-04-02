package challenger

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
)

// InvoiceStateStore manages the state of invoices in a thread-safe manner and
// allows goroutines to wait for specific states or initial load completion.
type InvoiceStateStore struct {
	// states holds the last known state for invoices.
	states map[lntypes.Hash]lnrpc.Invoice_InvoiceState

	// mtx guards access to states.
	mtx sync.Mutex

	// cond is used to signal waiters when states is updated or when the
	// initial load completes.
	cond *sync.Cond

	// initialLoadComplete is true once the initial fetching of all
	// historical invoices is done.
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
func (s *InvoiceStateStore) GetState(
	hash lntypes.Hash) (lnrpc.Invoice_InvoiceState, bool) {

	s.mtx.Lock()
	defer s.mtx.Unlock()

	state, exists := s.states[hash]
	return state, exists
}

// MarkInitialLoadComplete sets the initialLoadComplete flag to true atomically
// and broadcasts on the condition variable to wake up any waiting goroutines.
func (s *InvoiceStateStore) MarkInitialLoadComplete() {
	// Check atomically first to potentially avoid locking and
	// broadcasting.
	if s.initialLoadComplete.Load() {
		return
	}

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

// waitForCondition blocks until the provided condition function returns true,
// a timeout occurs, or the quit signal is received. The condition function is
// called under the store's mutex. This method manages all locking internally
// and the caller should NOT hold the mutex.
func (s *InvoiceStateStore) waitForCondition(condition func() bool,
	timeout time.Duration, timeoutMsg string) error {

	var (
		wg             sync.WaitGroup
		doneChan       = make(chan struct{})
		timeoutReached bool
		conditionMet   bool
	)

	// Spawn a goroutine that will signal timeout. This ensures the
	// waiter goroutine doesn't block forever if the condition is never
	// met.
	wg.Add(1)
	go func() {
		defer wg.Done()

		select {
		case <-doneChan:
			// Condition was met before timeout.
			return

		case <-time.After(timeout):
			// Timeout reached.

		case <-s.quit:
			// Shutdown signal.
		}

		s.mtx.Lock()
		timeoutReached = true
		s.cond.Broadcast()
		s.mtx.Unlock()
	}()

	// Spawn the main waiter goroutine that blocks on the condition
	// variable.
	wg.Add(1)
	go func() {
		defer wg.Done()

		s.mtx.Lock()
		for !condition() && !timeoutReached {
			s.cond.Wait()
		}
		conditionMet = condition()
		s.mtx.Unlock()

		close(doneChan)
	}()

	// Wait for both goroutines to complete.
	wg.Wait()

	if !conditionMet {
		select {
		case <-s.quit:
			return fmt.Errorf("challenger shutting down")
		default:
			return fmt.Errorf("%s", timeoutMsg)
		}
	}

	return nil
}

// WaitForState blocks until the specified invoice hash reaches the
// desiredState or a timeout occurs. It first waits for the initial historical
// invoice load to complete if necessary. initialLoadTimeout applies only if
// waiting for the initial load. requestTimeout applies when waiting for the
// specific invoice state change.
func (s *InvoiceStateStore) WaitForState(hash lntypes.Hash,
	desiredState lnrpc.Invoice_InvoiceState,
	initialLoadTimeout time.Duration,
	requestTimeout time.Duration) error {

	// Check to see if we need to wait for the initial load to complete.
	if !s.initialLoadComplete.Load() {
		log.Debugf("WaitForState: Initial load not complete, "+
			"waiting up to %v for hash %v...",
			initialLoadTimeout, hash)

		timeoutMsg := fmt.Sprintf("timed out waiting for initial "+
			"invoice load after %v", initialLoadTimeout)

		err := s.waitForCondition(
			func() bool {
				return s.initialLoadComplete.Load()
			},
			initialLoadTimeout, timeoutMsg,
		)
		if err != nil {
			log.Warnf("WaitForState: Error waiting for initial "+
				"load for hash %v: %v", hash, err)
			return err
		}

		log.Debugf("WaitForState: Initial load completed for "+
			"hash %v", hash)
	}

	// Check if the state is already where we need it to be.
	currentState, hasInvoice := s.GetState(hash)
	if hasInvoice && currentState == desiredState {
		log.Debugf("WaitForState: Hash %v already in desired "+
			"state %v.", hash, desiredState)
		return nil
	}

	// Wait for the invoice to reach the desired state.
	log.Debugf("WaitForState: Waiting up to %v for hash %v to reach "+
		"state %v...", requestTimeout, hash, desiredState)

	timeoutMsg := fmt.Sprintf("timed out waiting for state %v after %v",
		desiredState, requestTimeout)

	err := s.waitForCondition(
		func() bool {
			st, exists := s.states[hash]
			return exists && st == desiredState
		},
		requestTimeout, timeoutMsg,
	)
	if err != nil {
		// If we timed out, provide a more specific error message
		// based on the final state.
		finalState, finalExists := s.GetState(hash)
		if err.Error() == timeoutMsg {
			if !finalExists {
				return fmt.Errorf("no active or settled "+
					"invoice found for hash=%v after "+
					"timeout", hash)
			}

			return fmt.Errorf("invoice status not correct "+
				"before timeout, hash=%v, status=%v",
				hash, finalState)
		}

		return err
	}

	log.Debugf("WaitForState: Hash %v reached desired state %v.",
		hash, desiredState)

	return nil
}

// WaitForInitialLoad blocks until the initial historical invoice load has
// completed, or a timeout occurs.
func (s *InvoiceStateStore) WaitForInitialLoad(
	timeout time.Duration) error {

	if s.initialLoadComplete.Load() {
		return nil
	}

	log.Debugf("WaitForInitialLoad: Initial load not complete, "+
		"waiting up to %v...", timeout)

	timeoutMsg := fmt.Sprintf(
		"timed out waiting for initial invoice load after %v",
		timeout,
	)

	err := s.waitForCondition(
		func() bool {
			return s.initialLoadComplete.Load()
		},
		timeout, timeoutMsg,
	)
	if err != nil {
		log.Warnf("WaitForInitialLoad: Error waiting: %v", err)
		return err
	}

	log.Debugf("WaitForInitialLoad: Initial load completed.")

	return nil
}
