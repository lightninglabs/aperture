package lnc

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrSessionNotFound is returned when a session is not found in the
	// database.
	ErrSessionNotFound = errors.New("session not found")
)

// Store represents access to a persistent session store.
type Store interface {
	// AddSession adds a record for a new session in the database.
	AddSession(ctx context.Context, session *Session) error

	// GetSession retrieves the session record matching the passphrase
	// entropy.
	GetSession(ctx context.Context,
		passphraseEntropy []byte) (*Session, error)

	// SetRemotePubKey sets the remote public key for a session.
	SetRemotePubKey(ctx context.Context, passphraseEntropy,
		remotePubKey []byte) error

	// SetExpiry sets the expiry time for a session.
	SetExpiry(ctx context.Context, passphraseEntroy []byte,
		expiry time.Time) error
}
