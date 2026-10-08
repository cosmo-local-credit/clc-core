package api

import (
	"bytes"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
)

func relayOperationTestRequest() apiresp.RelayRequest {
	return apiresp.RelayRequest{
		OperationID: "0b6c3f1e-6f1a-4c55-9d3e-2f6b8a1c7d10",
		UserAddress: "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
		SignedBatch: apiresp.CaliburSignedBatch{
			BatchedCall: apiresp.CaliburBatchedCall{
				Calls: []apiresp.CaliburCall{{
					To:    "0x70997970C51812dc3A010C7d01b50e0d17dc79C8",
					Value: "0",
					Data:  "0xa9059cbb",
				}},
				RevertOnFailure: true,
			},
			Nonce:    "7",
			KeyHash:  "0x0000000000000000000000000000000000000000000000000000000000000000",
			Executor: "0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC",
			Deadline: "1700000300",
		},
		Signature:     "0xabcdef",
		SignatureType: "secp256k1",
	}
}

func mustRelayOperationDigest(t *testing.T, chainID int64, req apiresp.RelayRequest) []byte {
	t.Helper()
	digest, err := relayOperationDigest(chainID, req)
	if err != nil {
		t.Fatalf("relayOperationDigest() error = %v", err)
	}
	if len(digest) != 32 {
		t.Fatalf("digest length = %d, want 32", len(digest))
	}
	return digest
}

func TestRelayOperationDigestIsDeterministic(t *testing.T) {
	a := mustRelayOperationDigest(t, 100, relayOperationTestRequest())
	b := mustRelayOperationDigest(t, 100, relayOperationTestRequest())
	if !bytes.Equal(a, b) {
		t.Fatal("the same request must produce the same digest")
	}
}

func TestRelayOperationDigestIgnoresOnlyTheOperationID(t *testing.T) {
	base := mustRelayOperationDigest(t, 100, relayOperationTestRequest())

	sameRequestOtherID := relayOperationTestRequest()
	sameRequestOtherID.OperationID = "9d8f1c2a-0000-4000-8000-000000000000"
	if !bytes.Equal(base, mustRelayOperationDigest(t, 100, sameRequestOtherID)) {
		t.Fatal("the operation id must not be part of the request digest")
	}

	if bytes.Equal(base, mustRelayOperationDigest(t, 42220, relayOperationTestRequest())) {
		t.Fatal("the digest must bind the chain")
	}

	mutations := map[string]func(*apiresp.RelayRequest){
		"payer":          func(r *apiresp.RelayRequest) { r.UserAddress = "0x70997970C51812dc3A010C7d01b50e0d17dc79C8" },
		"call recipient": func(r *apiresp.RelayRequest) { r.SignedBatch.BatchedCall.Calls[0].To = r.UserAddress },
		"call data":      func(r *apiresp.RelayRequest) { r.SignedBatch.BatchedCall.Calls[0].Data = "0xa9059cbc" },
		"call value":     func(r *apiresp.RelayRequest) { r.SignedBatch.BatchedCall.Calls[0].Value = "1" },
		"revert flag":    func(r *apiresp.RelayRequest) { r.SignedBatch.BatchedCall.RevertOnFailure = false },
		"nonce":          func(r *apiresp.RelayRequest) { r.SignedBatch.Nonce = "8" },
		"deadline":       func(r *apiresp.RelayRequest) { r.SignedBatch.Deadline = "1700000301" },
		"executor":       func(r *apiresp.RelayRequest) { r.SignedBatch.Executor = r.UserAddress },
		"signature":      func(r *apiresp.RelayRequest) { r.Signature = "0xabcdee" },
		"signature case": func(r *apiresp.RelayRequest) { r.Signature = "0xABCDEF" },
		"signature type": func(r *apiresp.RelayRequest) { r.SignatureType = "webauthn" },
		"authorization": func(r *apiresp.RelayRequest) {
			r.Authorization = &apiresp.EIP7702Authorization{
				ChainID:         "100",
				ContractAddress: "0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC",
				Nonce:           "0",
				R:               "0x" + "11" + "00000000000000000000000000000000000000000000000000000000000000",
				S:               "0x" + "22" + "00000000000000000000000000000000000000000000000000000000000000",
			}
		},
		"credential id": func(r *apiresp.RelayRequest) { r.CredentialID = "cred" },
		"public key x":  func(r *apiresp.RelayRequest) { r.PublicKeyX = "1" },
		"public key y":  func(r *apiresp.RelayRequest) { r.PublicKeyY = "1" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			req := relayOperationTestRequest()
			req.SignedBatch.BatchedCall.Calls = append([]apiresp.CaliburCall(nil), req.SignedBatch.BatchedCall.Calls...)
			mutate(&req)
			if bytes.Equal(base, mustRelayOperationDigest(t, 100, req)) {
				t.Fatalf("changing the %s must change the digest", name)
			}
		})
	}
}

func TestIsRelayOperationReplay(t *testing.T) {
	req := relayOperationTestRequest()
	digest := mustRelayOperationDigest(t, 100, req)
	existing := store.RelayOperation{UserAddress: req.UserAddress, RequestDigest: digest}

	if !isRelayOperationReplay(existing, req.UserAddress, digest) {
		t.Fatal("the same payer and digest is a replay")
	}
	if isRelayOperationReplay(existing, "0x70997970C51812dc3A010C7d01b50e0d17dc79C8", digest) {
		t.Fatal("a different payer under the same id is a reuse, not a replay")
	}
	other := append([]byte(nil), digest...)
	other[0] ^= 0xff
	if isRelayOperationReplay(existing, req.UserAddress, other) {
		t.Fatal("a different request under the same id is a reuse, not a replay")
	}
}
