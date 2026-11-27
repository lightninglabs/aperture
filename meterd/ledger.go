package meterd

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"

	"github.com/lightningnetwork/lnd/lntypes"
)

// UsageSource says how the tokens of a debit were determined.
type UsageSource uint8

const (
	// UsageSplit means the response's usage object carried the prompt
	// and completion split.
	UsageSplit UsageSource = iota + 1

	// UsageTotal means the usage object carried only a total count.
	UsageTotal

	// UsageEstimate means no usage object was found and the response
	// settled at the authorization's estimate.
	UsageEstimate
)

// String returns the source's name as stored by ledgers.
func (s UsageSource) String() string {
	switch s {
	case UsageSplit:
		return "split"

	case UsageTotal:
		return "total"

	case UsageEstimate:
		return "estimate"

	default:
		return "unknown"
	}
}

// UsageRecord describes one debit: the bundle tokens drawn and the usage that
// determined them. It is what a UsageLedger stores for each metered response.
type UsageRecord struct {
	// Tokens is the number of bundle tokens debited.
	Tokens int64

	// Source says which of the fields below determined Tokens.
	Source UsageSource

	// PromptTokens, CachedTokens and CompletionTokens are the usage split
	// when Source is UsageSplit. CachedTokens is the part of PromptTokens
	// served from the upstream's prompt cache.
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64

	// TotalTokens is the reported total when Source is UsageTotal.
	TotalTokens int64

	// Weighted reports whether Tokens is the split weighted by the rates
	// below, rather than the plain token count. A split is weighted
	// whenever the booked model's rates resolve.
	Weighted bool

	// InputMsatPerThousandTokens, OutputMsatPerThousandTokens and
	// CachedInputPermille are the rates a weighted debit was computed
	// with. With them, anyone holding the record can recompute Tokens
	// exactly.
	InputMsatPerThousandTokens  int64
	OutputMsatPerThousandTokens int64
	CachedInputPermille         int64
}

// UsageLedger is an optional interface a Store implements to keep a
// per-request ledger, for example to issue usage receipts to buyers. When the
// store implements it, the server binds every token to the payment hash of
// the credential that authorized it, and debits through DebitUsage so the
// ledger learns the usage behind each debit.
type UsageLedger interface {
	// RecordPayment binds a token to the payment hash of the credential
	// an admitted request presented. It is called for every admitted
	// request, so it must be idempotent and cheap for a known binding.
	RecordPayment(tokenID string, paymentHash lntypes.Hash) error

	// DebitUsage behaves exactly like Store.Debit for usage.Tokens and
	// additionally records the usage.
	DebitUsage(tokenID string, releaseEstimate int64,
		usage UsageRecord) (Bundle, bool, error)
}

// l402AuthRegex matches an L402 (or legacy LSAT) Authorization value: a
// base64 macaroon and a hex preimage, as l402.FromHeader accepts them.
var l402AuthRegex = regexp.MustCompile(
	`^(?i:LSAT|L402) ([a-zA-Z0-9+/=]+):([a-fA-F0-9]{64})$`,
)

// paymentHashFromRequestText returns the payment hash of the credential the
// serialized request authenticated with. For an L402 credential it is the
// hash in the macaroon identifier, accepted only when the identifier names
// the given token and the presented preimage matches it. For an MPP charge
// credential the metered token ID is itself the payment hash. Aperture has
// verified the credential before the request reaches the price server, so
// the checks here only guard against a request text that does not belong to
// the token.
//
// The identifier is decoded here rather than through the l402 package, which
// would pull lnd's RPC client into every price server embedding meterd.
func paymentHashFromRequestText(text, tokenID string) (lntypes.Hash, bool) {
	req, err := http.ReadRequest(
		bufio.NewReader(strings.NewReader(text)),
	)
	if err != nil {
		return lntypes.Hash{}, false
	}

	for _, value := range req.Header.Values("Authorization") {
		if m := l402AuthRegex.FindStringSubmatch(value); m != nil {
			return l402PaymentHash(m[1], m[2], tokenID)
		}

		scheme, _, _ := strings.Cut(value, " ")
		if !strings.EqualFold(scheme, "Payment") {
			continue
		}

		hash, err := lntypes.MakeHashFromStr(tokenID)
		if err != nil {
			return lntypes.Hash{}, false
		}

		return hash, true
	}

	return lntypes.Hash{}, false
}

// l402PaymentHash returns the payment hash of an L402 credential whose
// macaroon identifier names the given token and whose preimage pays it.
func l402PaymentHash(macBase64, preimageHex,
	tokenID string) (lntypes.Hash, bool) {

	mac, err := base64.StdEncoding.DecodeString(macBase64)
	if err != nil {
		return lntypes.Hash{}, false
	}

	id, ok := macaroonV2Identifier(mac)
	if !ok {
		return lntypes.Hash{}, false
	}

	// An L402 identifier of version 0 is the version, the payment hash
	// and the token ID.
	const idLen = 2 + lntypes.HashSize + 32
	if len(id) != idLen || binary.BigEndian.Uint16(id[:2]) != 0 {
		return lntypes.Hash{}, false
	}

	var hash lntypes.Hash
	copy(hash[:], id[2:2+lntypes.HashSize])
	if hex.EncodeToString(id[2+lntypes.HashSize:]) != tokenID {
		return lntypes.Hash{}, false
	}

	preimage, err := lntypes.MakePreimageFromStr(strings.ToLower(preimageHex))
	if err != nil || !preimage.Matches(hash) {
		return lntypes.Hash{}, false
	}

	return hash, true
}

// macaroonV2Identifier returns the identifier of a macaroon in the v2 binary
// format, the one aperture mints: a version byte, then type-length-value
// fields, the identifier being field 2 of the first section.
func macaroonV2Identifier(mac []byte) ([]byte, bool) {
	const (
		fieldEOS        = 0
		fieldIdentifier = 2
	)

	if len(mac) == 0 || mac[0] != 2 {
		return nil, false
	}

	rest := mac[1:]
	for len(rest) > 0 {
		fieldType, n := binary.Uvarint(rest)
		if n <= 0 {
			return nil, false
		}
		rest = rest[n:]
		if fieldType == fieldEOS {
			return nil, false
		}

		size, n := binary.Uvarint(rest)
		if n <= 0 || size > uint64(len(rest)-n) {
			return nil, false
		}
		rest = rest[n:]
		if fieldType == fieldIdentifier {
			return rest[:size], true
		}
		rest = rest[size:]
	}

	return nil, false
}

// recordPayment binds an admitted request's token to its payment hash when
// the store keeps a usage ledger. A failure is logged and does not deny the
// request: the binding only serves receipts, never metering.
func (s *Server) recordPayment(tokenID, requestText string) {
	ledger, ok := s.store.(UsageLedger)
	if !ok {
		return
	}

	hash, ok := paymentHashFromRequestText(requestText, tokenID)
	if !ok {
		log.Debugf("No payment hash found in the request for token %s",
			tokenID)

		return
	}

	if err := ledger.RecordPayment(tokenID, hash); err != nil {
		log.Errorf("Unable to record the payment hash of token %s: %v",
			tokenID, err)
	}
}

// usageRecord describes a debit of the given tokens for the usage ledger.
// counts and found are what the response's usage extraction returned, and
// rates are the booked model's rates, nil when they did not resolve. It
// mirrors the decisions ReportUsage takes, so the record explains the debit
// exactly.
func usageRecord(tokens int64, counts *usageCounts, found bool,
	rates *ModelConfig) UsageRecord {

	record := UsageRecord{Tokens: tokens}

	switch {
	case !found:
		record.Source = UsageEstimate

	case counts.hasSplit:
		record.Source = UsageSplit
		record.PromptTokens = counts.promptTokens
		record.CachedTokens = counts.cachedTokens
		record.CompletionTokens = counts.completionTokens

		if rates == nil {
			break
		}
		if _, weighted := weightedDebit(counts, rates); !weighted {
			break
		}

		r := rates.rates()
		record.Weighted = true
		record.InputMsatPerThousandTokens = satInt64(r.input)
		record.OutputMsatPerThousandTokens = satInt64(r.output)
		record.CachedInputPermille = r.cachedPermille

	default:
		record.Source = UsageTotal
		record.TotalTokens = counts.totalTokens
	}

	return record
}
