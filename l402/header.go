package l402

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/lightningnetwork/lnd/lntypes"
	"gopkg.in/macaroon.v2"
)

const (
	// HeaderAuthorization is the HTTP header field name that is used to
	// send the L402 by REST clients.
	HeaderAuthorization = "Authorization"

	// HeaderMacaroonMD is the HTTP header field name that is used to send
	// the L402 by certain REST and gRPC clients.
	HeaderMacaroonMD = "Grpc-Metadata-Macaroon"

	// HeaderMacaroon is the HTTP header field name that is used to send the
	// L402 by our own gRPC clients.
	HeaderMacaroon = "Macaroon"
)

var (
	// authRegex matches the supported Authorization credential format:
	//
	//     (LSAT / L402) 1*SP base64(macaroon) ":" 1*HEXDIG
	//
	// The L402 specification also allows multiple comma-separated
	// macaroons. Aperture currently supports exactly one macaroon per
	// credential.
	authRegex = regexp.MustCompile(
		"^(?i:(LSAT|L402))[ ]+([A-Za-z0-9+/=]+):" +
			"([0-9a-fA-F]+)$",
	)
	authFormatLegacy = "LSAT %s:%s"
	authFormat       = "L402 %s:%s"
)

// FromHeader tries to extract authentication information from HTTP headers.
// There are two supported formats that can be sent in four different header
// fields:
//  0. Authorization: LSAT <macBase64>:<preimageHex>
//  1. Authorization: L402 <macBase64>:<preimageHex>
//  2. Grpc-Metadata-Macaroon: <macHex>
//  3. Macaroon: <macHex>
//
// If only the macaroon is sent in header 2 or three then it is expected to have
// a caveat with the preimage attached to it.
func FromHeader(header *http.Header) (*macaroon.Macaroon, lntypes.Preimage, error) {
	var (
		macBase64   string
		macBytes    []byte
		preimageHex string
		err         error
	)

	authHeaders := header.Values(HeaderAuthorization)
	macaroonMDHeaders := header.Values(HeaderMacaroonMD)
	macaroonHeaders := header.Values(HeaderMacaroon)

	switch {
	// Header field 1 contains the macaroon and the preimage as distinct
	// values separated by a colon.
	case len(authHeaders) > 0:
		seenSchemes := make(map[string]struct{}, 2)
		for _, authHeader := range authHeaders {
			log.Debugf("Trying to authorize with header value "+
				"[%s].", authHeader)
			matches := authRegex.FindStringSubmatch(authHeader)
			if len(matches) != 4 {
				return nil, lntypes.Preimage{}, fmt.Errorf("invalid "+
					"auth header format: %s", authHeader)
			}

			currentScheme := strings.ToUpper(matches[1])
			currentMacBase64 := matches[2]
			currentPreimageHex := matches[3]
			if _, ok := seenSchemes[currentScheme]; ok {
				return nil, lntypes.Preimage{}, fmt.Errorf(
					"duplicate %s auth header", currentScheme,
				)
			}
			seenSchemes[currentScheme] = struct{}{}

			if len(seenSchemes) == 1 {
				macBase64 = currentMacBase64
				preimageHex = currentPreimageHex
				continue
			}

			if currentMacBase64 != macBase64 ||
				currentPreimageHex != preimageHex {
				return nil, lntypes.Preimage{}, errors.New(
					"authorization credentials do not match",
				)
			}
		}

	// Header field 2: Contains only the macaroon.
	case len(macaroonMDHeaders) > 0:
		if len(macaroonMDHeaders) != 1 {
			return nil, lntypes.Preimage{}, errors.New(
				"multiple macaroon metadata headers",
			)
		}
		macBytes, err = hex.DecodeString(macaroonMDHeaders[0])
		if err != nil {
			return nil, lntypes.Preimage{}, fmt.Errorf("hex decode of "+
				"macaroon failed: %v", err)
		}

	// Header field 3: Contains only the macaroon.
	case len(macaroonHeaders) > 0:
		if len(macaroonHeaders) != 1 {
			return nil, lntypes.Preimage{}, errors.New(
				"multiple macaroon headers",
			)
		}
		macBytes, err = hex.DecodeString(macaroonHeaders[0])
		if err != nil {
			return nil, lntypes.Preimage{}, fmt.Errorf("hex decode of "+
				"macaroon failed: %v", err)
		}

	default:
		return nil, lntypes.Preimage{}, fmt.Errorf("no auth header " +
			"provided")
	}

	if macBytes == nil {
		macBytes, err = base64.StdEncoding.DecodeString(macBase64)
		if err != nil {
			return nil, lntypes.Preimage{}, fmt.Errorf("base64 "+
				"decode of macaroon failed: %v", err)
		}
	}

	mac := &macaroon.Macaroon{}
	err = mac.UnmarshalBinary(macBytes)
	if err != nil {
		return nil, lntypes.Preimage{}, fmt.Errorf("unable to "+
			"unmarshal macaroon: %v", err)
	}

	if preimageHex == "" {
		var ok bool
		preimageHex, ok = HasCaveat(mac, PreimageKey)
		if !ok {
			return nil, lntypes.Preimage{}, errors.New(
				"preimage caveat not found",
			)
		}
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
