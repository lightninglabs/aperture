package l402

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/lightningnetwork/lnd/lntypes"
	"gopkg.in/macaroon.v2"
)

const (
	// HeaderAuthorization is the HTTP header field name that is used to
	// send the L402 by REST clients.
	HeaderAuthorization = "Authorization"

	// HeaderTokenMD is the HTTP header field name that is used to send
	// the L402 by certain REST and gRPC clients using the current spec.
	HeaderTokenMD = "Grpc-Metadata-Token"

	// HeaderToken is the HTTP header field name that is used to send the
	// L402 by our own gRPC clients using the current spec.
	HeaderToken = "Token"

	// HeaderMacaroonMD is the legacy HTTP header field name that is used
	// to send the L402 by certain REST and gRPC clients. Kept for
	// backwards compatibility.
	HeaderMacaroonMD = "Grpc-Metadata-Macaroon"

	// HeaderMacaroon is the legacy HTTP header field name that is used to
	// send the L402 by our own gRPC clients. Kept for backwards
	// compatibility.
	HeaderMacaroon = "Macaroon"
)

var (
	authRegex        = regexp.MustCompile("(LSAT|L402) (.*?):([a-f0-9]{64})")
	authFormatLegacy = "LSAT %s:%s"
	authFormat       = "L402 %s:%s"
)

// FromHeader tries to extract authentication information from HTTP headers.
// There are multiple supported formats that can be sent in different header
// fields:
//  0. Authorization: LSAT <tokenBase64>:<preimageHex>
//  1. Authorization: L402 <tokenBase64>:<preimageHex>
//  2. Grpc-Metadata-Token: <tokenHex>    (current spec)
//  3. Token: <tokenHex>                  (current spec)
//  4. Grpc-Metadata-Macaroon: <macHex>   (legacy, backwards compat)
//  5. Macaroon: <macHex>                 (legacy, backwards compat)
//
// If only the token is sent in header fields 2-5, then it is expected to have
// a caveat with the preimage attached to it.
func FromHeader(header *http.Header) (*macaroon.Macaroon, lntypes.Preimage, error) {
	var authHeader string

	switch {
	// Header field 0/1 contains the token and the preimage as distinct
	// values separated by a colon.
	case header.Get(HeaderAuthorization) != "":
		// Parse the content of the header field and check that it is in
		// the correct format.
		var matches []string
		authHeaders := header.Values(HeaderAuthorization)
		for _, authHeader := range authHeaders {
			log.Debugf("Trying to authorize with header value "+
				"[%s].", authHeader)
			matches = authRegex.FindStringSubmatch(authHeader)
			if len(matches) != 4 {
				continue
			}
		}

		if len(matches) != 4 {
			return nil, lntypes.Preimage{}, fmt.Errorf("invalid "+
				"auth header format: %s", authHeader)
		}

		// Decode the content of the two parts of the header value.
		macBase64, preimageHex := matches[2], matches[3]
		macBytes, err := base64.StdEncoding.DecodeString(macBase64)
		if err != nil {
			return nil, lntypes.Preimage{}, fmt.Errorf("base64 "+
				"decode of token failed: %v", err)
		}
		mac := &macaroon.Macaroon{}
		err = mac.UnmarshalBinary(macBytes)
		if err != nil {
			return nil, lntypes.Preimage{}, fmt.Errorf("unable to "+
				"unmarshal token: %v", err)
		}
		preimage, err := lntypes.MakePreimageFromStr(preimageHex)
		if err != nil {
			return nil, lntypes.Preimage{}, fmt.Errorf("hex "+
				"decode of preimage failed: %v", err)
		}

		// All done, we don't need to extract anything from the
		// token since the preimage was presented separately.
		return mac, preimage, nil

	// Header field 2: Current spec gRPC metadata with "token" key.
	case header.Get(HeaderTokenMD) != "":
		authHeader = header.Get(HeaderTokenMD)

	// Header field 3: Current spec gRPC header with "Token" key.
	case header.Get(HeaderToken) != "":
		authHeader = header.Get(HeaderToken)

	// Header field 4: Legacy gRPC metadata with "macaroon" key.
	case header.Get(HeaderMacaroonMD) != "":
		authHeader = header.Get(HeaderMacaroonMD)

	// Header field 5: Legacy gRPC header with "Macaroon" key.
	case header.Get(HeaderMacaroon) != "":
		authHeader = header.Get(HeaderMacaroon)

	default:
		return nil, lntypes.Preimage{}, fmt.Errorf("no auth header " +
			"provided")
	}

	// For cases 2-5, we need to actually unmarshal the token to extract
	// the preimage from its caveats.
	macBytes, err := hex.DecodeString(authHeader)
	if err != nil {
		return nil, lntypes.Preimage{}, fmt.Errorf("hex decode of "+
			"token failed: %v", err)
	}
	mac := &macaroon.Macaroon{}
	err = mac.UnmarshalBinary(macBytes)
	if err != nil {
		return nil, lntypes.Preimage{}, fmt.Errorf("unable to "+
			"unmarshal token: %v", err)
	}
	preimageHex, ok := HasCaveat(mac, PreimageKey)
	if !ok {
		return nil, lntypes.Preimage{}, errors.New("preimage caveat " +
			"not found")
	}
	preimage, err := lntypes.MakePreimageFromStr(preimageHex)
	if err != nil {
		return nil, lntypes.Preimage{}, fmt.Errorf("hex decode of "+
			"preimage failed: %v", err)
	}

	return mac, preimage, nil
}

// SetHeader sets the provided authentication elements as the default/standard
// HTTP header for the L402 protocol.
func SetHeader(header *http.Header, mac *macaroon.Macaroon,
	preimage fmt.Stringer) error {

	macBytes, err := mac.MarshalBinary()
	if err != nil {
		return err
	}
	macStr := base64.StdEncoding.EncodeToString(macBytes)
	preimageStr := preimage.String()

	// Send "Authorization: LSAT..." header before sending
	// "Authorization: L402" header to be compatible with old aperture.
	// TODO: remove this after aperture is upgraded everywhere.
	legacyValue := fmt.Sprintf(authFormatLegacy, macStr, preimageStr)
	header.Set(HeaderAuthorization, legacyValue)

	value := fmt.Sprintf(authFormat, macStr, preimageStr)
	header.Add(HeaderAuthorization, value)

	return nil
}
