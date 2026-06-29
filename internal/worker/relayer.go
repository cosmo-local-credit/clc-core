package worker

import (
	"context"
	"errors"
	"math/big"

	"github.com/cosmo-local-credit/clc-core/internal/relayer"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/riverqueue/river"
)

type (
	RelayDelegateArgs struct {
		TrackingID       string                       `json:"trackingId"`
		EphemeralAddress string                       `json:"ephemeralAddress"`
		Authorization    apiresp.EIP7702Authorization `json:"authorization"`
	}

	RelayDelegateWorker struct {
		river.WorkerDefaults[RelayDelegateArgs]
		wc *WorkerContainer
	}

	RelayArgs struct {
		TrackingID    string                        `json:"trackingId"`
		UserAddress   string                        `json:"userAddress"`
		SignedBatch   apiresp.CaliburSignedBatch    `json:"signedBatch"`
		Signature     string                        `json:"signature"`
		SignatureType string                        `json:"signatureType"`
		Authorization *apiresp.EIP7702Authorization `json:"authorization,omitempty"`
		CredentialID  string                        `json:"credentialId,omitempty"`
		PublicKeyX    string                        `json:"publicKeyX,omitempty"`
		PublicKeyY    string                        `json:"publicKeyY,omitempty"`
	}

	RelayWorker struct {
		river.WorkerDefaults[RelayArgs]
		wc *WorkerContainer
	}
)

func (RelayDelegateArgs) Kind() string { return store.RELAY_DELEGATE }

// Work signs a bare EIP-7702 delegation for the ephemeral address and queues
// it for dispatch. The master key is loaded, the nonce acquired, the tx
// signed and tracked, and the dispatch job enqueued — all inside a single
// transaction so the OTX, dispatch row, and queued job commit atomically.
func (w *RelayDelegateWorker) Work(ctx context.Context, job *river.Job[RelayDelegateArgs]) error {
	tx, err := w.wc.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	systemKeypair, err := w.wc.store.LoadMasterSignerKey(ctx, tx)
	if err != nil {
		return err
	}

	privateKey, err := crypto.HexToECDSA(systemKeypair.Private)
	if err != nil {
		return err
	}

	nonce, err := w.wc.store.AcquireNonce(ctx, tx, systemKeypair.Public)
	if err != nil {
		return err
	}

	signedTx, err := w.wc.relayer.BuildAndSignDelegate(
		ctx,
		privateKey,
		common.HexToAddress(job.Args.EphemeralAddress),
		job.Args.Authorization,
		nonce,
	)
	if err != nil {
		return err
	}

	prepared, err := w.wc.insertTrackedTx(ctx, tx, job.Args.TrackingID, store.RELAY_DELEGATE, systemKeypair.Public, nonce, signedTx)
	if err != nil {
		return err
	}

	if _, err := w.wc.queueClient.InsertTx(ctx, tx, DispatchArgs{
		TrackingID: job.Args.TrackingID,
		OTXID:      prepared.OTXID,
		RawTx:      prepared.RawHex,
		TxHash:     prepared.Hash,
	}, nil); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (RelayArgs) Kind() string { return "RELAY" }

// Work relays a client-signed Calibur batch. It decides between a plain
// execution (already delegated) and a delegate+execute (first relay), signs
// the resulting tx with the master key, persists any WebAuthn credential
// carried by a registration relay, and enqueues dispatch — all in one
// transaction.
func (w *RelayWorker) Work(ctx context.Context, job *river.Job[RelayArgs]) error {
	tx, err := w.wc.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	userAddr := common.HexToAddress(job.Args.UserAddress)

	isDelegated, err := w.wc.relayer.IsDelegated(ctx, userAddr)
	if err != nil {
		return err
	}
	if !isDelegated && job.Args.Authorization == nil {
		return errors.New("relay: user not delegated and no authorization supplied")
	}

	systemKeypair, err := w.wc.store.LoadMasterSignerKey(ctx, tx)
	if err != nil {
		return err
	}

	privateKey, err := crypto.HexToECDSA(systemKeypair.Private)
	if err != nil {
		return err
	}

	nonce, err := w.wc.store.AcquireNonce(ctx, tx, systemKeypair.Public)
	if err != nil {
		return err
	}

	result, err := w.wc.relayer.BuildAndSignRelay(
		ctx,
		privateKey,
		common.HexToAddress(systemKeypair.Public),
		userAddr,
		job.Args.SignedBatch,
		common.FromHex(job.Args.Signature),
		job.Args.Authorization,
		isDelegated,
		nonce,
	)
	if err != nil {
		return err
	}

	prepared, err := w.wc.insertTrackedTx(ctx, tx, job.Args.TrackingID, result.OTXType, systemKeypair.Public, nonce, result.Tx)
	if err != nil {
		return err
	}

	// Persist the WebAuthn credential mapping when this relay carries a
	// registration payload (registration is signed by the ephemeral
	// secp256k1 key while the WebAuthn passkey doesn't exist in our records
	// yet). This is the third table touched in this tx, so it must commit
	// atomically with the OTX and dispatch rows.
	if job.Args.SignatureType == "secp256k1" && job.Args.CredentialID != "" && job.Args.PublicKeyX != "" && job.Args.PublicKeyY != "" {
		x, okX := new(big.Int).SetString(job.Args.PublicKeyX, 10)
		y, okY := new(big.Int).SetString(job.Args.PublicKeyY, 10)
		if !okX || !okY {
			return errors.New("relay: publicKeyX / publicKeyY must be decimal integers")
		}
		keyHash := relayer.ComputeP256KeyHash(x, y)
		if err := w.wc.store.UpsertWebAuthnCredential(ctx, tx, store.WebAuthnCredential{
			CredentialID: job.Args.CredentialID,
			Address:      userAddr.Hex(),
			KeyHash:      keyHash.Hex(),
			PublicKeyX:   job.Args.PublicKeyX,
			PublicKeyY:   job.Args.PublicKeyY,
		}); err != nil {
			return err
		}
	}

	// On first relay (delegate+exec), the wallet is being activated —
	// enqueue an onchain AccountIndex.add(userAddress) registration job
	// in the same transaction so it commits atomically with the relay.
	if result.OTXType == store.RELAY_DELEGATE_EXEC {
		registerTrackingID := uuid.NewString()
		if _, err := w.wc.queueClient.InsertTx(ctx, tx, RegisterUserArgs{
			TrackingID:  registerTrackingID,
			UserAddress: job.Args.UserAddress,
		}, &river.InsertOpts{MaxAttempts: 1}); err != nil {
			return err
		}
	}

	if _, err := w.wc.queueClient.InsertTx(ctx, tx, DispatchArgs{
		TrackingID: job.Args.TrackingID,
		OTXID:      prepared.OTXID,
		RawTx:      prepared.RawHex,
		TxHash:     prepared.Hash,
	}, nil); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
