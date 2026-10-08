package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

const (
	// relayOperationDigestDomain keeps this digest from colliding with any
	// other sha256 the service computes over similar bytes, and versions the
	// encoding.
	relayOperationDigestDomain = "clc-core/relay-operation/v1"
)

type (
	// relayOperationPayload is everything an operation id is bound to: the chain,
	// the payer, and every field of the signed request except the id itself.
	// encoding/json marshals struct fields in declaration order, so the same
	// request always encodes to the same bytes.
	relayOperationPayload struct {
		ChainID       int64                         `json:"chainId"`
		UserAddress   string                        `json:"userAddress"`
		SignedBatch   apiresp.CaliburSignedBatch    `json:"signedBatch"`
		Signature     string                        `json:"signature"`
		SignatureType string                        `json:"signatureType"`
		Authorization *apiresp.EIP7702Authorization `json:"authorization"`
		CredentialID  string                        `json:"credentialId"`
		PublicKeyX    string                        `json:"publicKeyX"`
		PublicKeyY    string                        `json:"publicKeyY"`
	}
)

// relayOperationDigest fingerprints a relay request for idempotent
// resubmission. It is exact: any change to any field, including the case of a
// hex string, is a different request.
func relayOperationDigest(chainID int64, req apiresp.RelayRequest) ([]byte, error) {
	encoded, err := json.Marshal(relayOperationPayload{
		ChainID:       chainID,
		UserAddress:   req.UserAddress,
		SignedBatch:   req.SignedBatch,
		Signature:     req.Signature,
		SignatureType: req.SignatureType,
		Authorization: req.Authorization,
		CredentialID:  req.CredentialID,
		PublicKeyX:    req.PublicKeyX,
		PublicKeyY:    req.PublicKeyY,
	})
	if err != nil {
		return nil, err
	}

	h := sha256.New()
	h.Write([]byte(relayOperationDigestDomain))
	h.Write([]byte{0})
	h.Write(encoded)
	return h.Sum(nil), nil
}

// isRelayOperationReplay reports whether existing was claimed by exactly this
// request. Anything else under the same id is a reuse and must be refused.
func isRelayOperationReplay(existing store.RelayOperation, userAddress string, digest []byte) bool {
	return existing.UserAddress == userAddress && bytes.Equal(existing.RequestDigest, digest)
}

// relayOperationReplayed answers a resubmission with the tracking id its first
// submission queued. Nothing new is queued.
func relayOperationReplayed(c *echo.Context, operationID, trackingID string) error {
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Relay request already accepted",
		Result: map[string]any{
			"trackingId":  trackingID,
			"operationId": operationID,
			"replayed":    true,
		},
	})
}

// relayOperationConflict refuses an operation id already bound to a different
// request. It says nothing about that request: an id alone is not a proof.
func relayOperationConflict(c *echo.Context) error {
	return handleConflictError(c, "operationId was already used for a different relay request")
}
