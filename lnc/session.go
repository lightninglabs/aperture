package lnc

import (
	"fmt"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightninglabs/lightning-node-connect/mailbox"
)

// Session contains all the information needed for an LNC connection.
type Session struct {
	// PassphraseWords is the list of words the PassphraseEntropy is derived
	// from.
	PassphraseWords string

	// PassphraseEntropy is the entropy. This field identifies the session.
	PassphraseEntropy []byte

	// RemoteStaticPubKey is the public key of the remote peer.
	RemoteStaticPubKey *btcec.PublicKey

	// LocalStaticPrivKey is the private key of the local peer.
	LocalStaticPrivKey *btcec.PrivateKey

	// MailboxAddr is the address of the mailbox server.
	MailboxAddr string

	// CreatedAt is the time the session was added to the database.
	CreatedAt time.Time

	// Expiry is the time the session will expire.
	Expiry *time.Time

	// DevServer signals if we need to skip the verification of the server's
	// tls certificate.
	DevServer bool
}

// NewSession creates a new non-initialized session.
func NewSession(passphrase, mailboxAddr string, devServer bool) (*Session,
	error) {

	switch {
	case passphrase == "":
		return nil, fmt.Errorf("passphrase cannot be empty")

	case mailboxAddr == "":
		return nil, fmt.Errorf("mailbox address cannot be empty")
	}

	words := strings.Split(passphrase, " ")
	if len(words) != mailbox.NumPassphraseWords {
		return nil, fmt.Errorf("invalid passphrase. Expected %d "+
			"words, got %d", mailbox.NumPassphraseWords,
			len(words))
	}

	var mnemonicWords [mailbox.NumPassphraseWords]string
	copy(mnemonicWords[:], words)
	entropy := mailbox.PassphraseMnemonicToEntropy(mnemonicWords)

	return &Session{
		PassphraseWords:   passphrase,
		PassphraseEntropy: entropy[:],
		MailboxAddr:       mailboxAddr,
		DevServer:         devServer,
	}, nil
}
