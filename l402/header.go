package l402

import (
	"bytes"
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
	var authHeader string

	switch {
	// Header field 1 contains the macaroon and the preimage as distinct
	// values separated by a colon.
	case header.Get(HeaderAuthorization) != "":
		var (
			mac         *macaroon.Macaroon
			macBytes    []byte
			preimage    lntypes.Preimage
			seenSchemes = make(map[string]struct{}, 2)
		)

		authHeaders := header.Values(HeaderAuthorization)
		for _, authHeader := range authHeaders {
			log.Debugf("Trying to authorize with header value "+
				"[%s].", authHeader)
			matches := authRegex.FindStringSubmatch(authHeader)
			if len(matches) != 4 {
				return nil, lntypes.Preimage{}, fmt.Errorf("invalid "+
					"auth header format: %s", authHeader)
			}

			scheme, macBase64, preimageHex := strings.ToUpper(matches[1]),
				matches[2], matches[3]
			if _, ok := seenSchemes[scheme]; ok {
				return nil, lntypes.Preimage{}, fmt.Errorf(
					"duplicate %s auth header", scheme,
				)
			}
			seenSchemes[scheme] = struct{}{}

			currentMacBytes, err := base64.StdEncoding.DecodeString(
				macBase64,
			)
			if err != nil {
				return nil, lntypes.Preimage{}, fmt.Errorf("base64 "+
					"decode of macaroon failed: %v", err)
			}

			currentMac := &macaroon.Macaroon{}
			err = currentMac.UnmarshalBinary(currentMacBytes)
			if err != nil {
				return nil, lntypes.Preimage{}, fmt.Errorf("unable to "+
					"unmarshal macaroon: %v", err)
			}

			currentPreimage, err := lntypes.MakePreimageFromStr(
				preimageHex,
			)
			if err != nil {
				return nil, lntypes.Preimage{}, fmt.Errorf("hex "+
					"decode of preimage failed: %v", err)
			}

			if mac == nil {
				mac = currentMac
				macBytes = currentMacBytes
				preimage = currentPreimage
			} else if !bytes.Equal(macBytes, currentMacBytes) ||
				preimage != currentPreimage {

				return nil, lntypes.Preimage{}, errors.New(
					"authorization credentials do not match",
				)
			}
		}

		// All done, we don't need to extract anything from the
		// macaroon since the preimage was presented separately.
		return mac, preimage, nil

	// Header field 2: Contains only the macaroon.
	case header.Get(HeaderMacaroonMD) != "":
		authHeader = header.Get(HeaderMacaroonMD)

	// Header field 3: Contains only the macaroon.
	case header.Get(HeaderMacaroon) != "":
		authHeader = header.Get(HeaderMacaroon)

	default:
		return nil, lntypes.Preimage{}, fmt.Errorf("no auth header " +
			"provided")
	}

	// For case 2 and 3, we need to actually unmarshal the macaroon to
	// extract the preimage.
	macBytes, err := hex.DecodeString(authHeader)
	if err != nil {
		return nil, lntypes.Preimage{}, fmt.Errorf("hex decode of "+
			"macaroon failed: %v", err)
	}
	mac := &macaroon.Macaroon{}
	err = mac.UnmarshalBinary(macBytes)
	if err != nil {
		return nil, lntypes.Preimage{}, fmt.Errorf("unable to "+
			"unmarshal macaroon: %v", err)
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
