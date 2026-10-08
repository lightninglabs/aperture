package auth

import (
	"encoding/json"
	"fmt"

	"github.com/lightninglabs/aperture/mpp"
)

// mppChallengeBinding is the server-defined state carried by a Payment
// challenge. Resource is the exact name Aperture used for authorization and
// pricing, including the request path for a dynamic-price service.
type mppChallengeBinding struct {
	Resource string `json:"resource"`
}

// encodeMPPChallengeBinding serializes the resource binding into the opaque
// challenge parameter. Opaque is covered by the challenge HMAC and echoed by
// the client, so the binding cannot be removed or changed after issuance.
func encodeMPPChallengeBinding(resourceName string) (string, error) {
	canonical, err := mpp.Canonicalize(mppChallengeBinding{
		Resource: resourceName,
	})
	if err != nil {
		return "", fmt.Errorf("encode MPP resource binding: %w", err)
	}

	return mpp.Base64URLEncode(canonical), nil
}

// verifyMPPChallengeBinding checks that a challenge was issued for the resource
// the credential is now authorizing. A missing binding is rejected because it
// describes a challenge minted by code that did not constrain its target.
func verifyMPPChallengeBinding(opaque, resourceName string) error {
	encoded, err := mpp.Base64URLDecode(opaque)
	if err != nil {
		return fmt.Errorf("decode MPP resource binding: %w", err)
	}

	var binding mppChallengeBinding
	if err := json.Unmarshal(encoded, &binding); err != nil {
		return fmt.Errorf("parse MPP resource binding: %w", err)
	}
	if binding.Resource != resourceName {
		return fmt.Errorf("MPP challenge is bound to resource %q, not %q",
			binding.Resource, resourceName)
	}

	return nil
}
