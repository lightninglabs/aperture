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

	// HeaderMacaroonMD is the HTTP header field name that is used to send
	// the L402 by certain REST and gRPC clients.
	HeaderMacaroonMD = "Grpc-Metadata-Macaroon"

	// HeaderMacaroon is the HTTP header field name that is used to send the
	// L402 by our own gRPC clients.
	HeaderMacaroon = "Macaroon"
)

var (
	authRegex        = regexp.MustCompile("(LSAT|L402) (.*?):([a-f0-9]{64})")
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
//
// The returned discharge macaroons will be non-nil when the binary-encoded
// macaroon data contains more than one macaroon (as per the macaroon.Slice
// convention, the first is the root macaroon and the rest are discharges for
// its third-party caveats).
func FromHeader(header *http.Header) (*macaroon.Macaroon, lntypes.Preimage,
	[]*macaroon.Macaroon, error) {
	var authHeader string

	switch {
	// Header field 1 contains the macaroon and the preimage as distinct
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
			return nil, lntypes.Preimage{}, nil,
				fmt.Errorf("invalid auth header "+
					"format: %s", authHeader)
		}

		// Decode the content of the two parts of the header value.
		macBase64, preimageHex := matches[2], matches[3]
		macBytes, err := base64.StdEncoding.DecodeString(macBase64)
		if err != nil {
			return nil, lntypes.Preimage{}, nil,
				fmt.Errorf("base64 decode of macaroon "+
					"failed: %v", err)
		}

		// Use Slice to unmarshal so we can extract discharge
		// macaroons if present. By convention the first macaroon
		// is the root and the rest are discharges.
		var slice macaroon.Slice
		if err := slice.UnmarshalBinary(macBytes); err != nil {
			return nil, lntypes.Preimage{}, nil,
				fmt.Errorf("unable to unmarshal "+
					"macaroon: %v", err)
		}
		if len(slice) == 0 {
			return nil, lntypes.Preimage{}, nil,
				fmt.Errorf("no macaroon found in " +
					"auth header")
		}
		mac := slice[0]
		var discharges []*macaroon.Macaroon
		if len(slice) > 1 {
			discharges = slice[1:]
		}

		preimage, err := lntypes.MakePreimageFromStr(preimageHex)
		if err != nil {
			return nil, lntypes.Preimage{}, nil,
				fmt.Errorf("hex decode of preimage "+
					"failed: %v", err)
		}

		// All done, we don't need to extract anything from the
		// macaroon since the preimage was presented separately.
		return mac, preimage, discharges, nil

	// Header field 2: Contains only the macaroon.
	case header.Get(HeaderMacaroonMD) != "":
		authHeader = header.Get(HeaderMacaroonMD)

	// Header field 3: Contains only the macaroon.
	case header.Get(HeaderMacaroon) != "":
		authHeader = header.Get(HeaderMacaroon)

	default:
		return nil, lntypes.Preimage{}, nil, fmt.Errorf(
			"no auth header provided",
		)
	}

	// For case 2 and 3, we need to actually unmarshal the macaroon to
	// extract the preimage. Use Slice to support discharge macaroons.
	macBytes, err := hex.DecodeString(authHeader)
	if err != nil {
		return nil, lntypes.Preimage{}, nil, fmt.Errorf("hex decode "+
			"of macaroon failed: %v", err)
	}
	var slice macaroon.Slice
	if err := slice.UnmarshalBinary(macBytes); err != nil {
		return nil, lntypes.Preimage{}, nil, fmt.Errorf("unable to "+
			"unmarshal macaroon: %v", err)
	}
	if len(slice) == 0 {
		return nil, lntypes.Preimage{}, nil, fmt.Errorf(
			"no macaroon found in header",
		)
	}
	mac := slice[0]
	var discharges []*macaroon.Macaroon
	if len(slice) > 1 {
		discharges = slice[1:]
	}

	preimageHex, ok := HasCaveat(mac, PreimageKey)
	if !ok {
		return nil, lntypes.Preimage{}, nil, errors.New(
			"preimage caveat not found",
		)
	}
	preimage, err := lntypes.MakePreimageFromStr(preimageHex)
	if err != nil {
		return nil, lntypes.Preimage{}, nil, fmt.Errorf("hex decode "+
			"of preimage failed: %v", err)
	}

	return mac, preimage, discharges, nil
}

// SetHeader sets the provided authentication elements as the default/standard
// HTTP header for the L402 protocol. If discharges are provided, they are
// serialized alongside the root macaroon using the macaroon.Slice convention.
func SetHeader(header *http.Header, mac *macaroon.Macaroon,
	preimage fmt.Stringer, discharges []*macaroon.Macaroon) error {

	// Build a Slice with the root macaroon first, followed by any
	// discharge macaroons, then serialize.
	slice := macaroon.Slice{mac}
	slice = append(slice, discharges...)
	macBytes, err := slice.MarshalBinary()
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
