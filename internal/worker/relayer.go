package worker

import (
	"context"
	"errors"

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
		EphemeralAddress string                       `json:"ephemeralAddress" river:"unique"`
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

	// RelayRegistration is the WebAuthn credential a registration relay carries.
	RelayRegistration struct {
		CredentialID string
		PublicKeyX   string
		PublicKeyY   string
	}
)

func (RelayDelegateArgs) Kind() string { return store.RELAY_DELEGATE }

func (RelayDelegateArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		UniqueOpts: river.UniqueOpts{ByArgs: true},
	}
}

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

// Registration returns the credential carried by the relay, or nil when the
// relay is not a registration. All three fields must be present.
func (a RelayArgs) Registration() *RelayRegistration {
	if a.CredentialID == "" || a.PublicKeyX == "" || a.PublicKeyY == "" {
		return nil
	}
	return &RelayRegistration{CredentialID: a.CredentialID, PublicKeyX: a.PublicKeyX, PublicKeyY: a.PublicKeyY}
}

// HasPartialRegistration reports registration metadata that is present but
// incomplete, which is rejected rather than relayed as a plain batch.
func (a RelayArgs) HasPartialRegistration() bool {
	return a.Registration() == nil && (a.CredentialID != "" || a.PublicKeyX != "" || a.PublicKeyY != "")
}

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
		job.Args.SignatureType,
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

	// Record the WebAuthn credential when this relay carries a registration.
	// The API already proved the root key signed it; the proof is repeated here
	// because this is where the row is written. The row starts pending and in
	// the same transaction as the OTX it waits on, so it can only become usable
	// through that transaction's confirmation (store.UpdateDispatchTxStatus),
	// and a conflicting credential aborts the relay before anything is signed
	// onto the dispatch queue.
	if registration := job.Args.Registration(); registration != nil {
		keyHash, err := w.wc.relayer.VerifyRegistration(relayer.RegistrationProof{
			UserAddress:   userAddr,
			SignedBatch:   job.Args.SignedBatch,
			Signature:     common.FromHex(job.Args.Signature),
			SignatureType: job.Args.SignatureType,
			PublicKeyX:    registration.PublicKeyX,
			PublicKeyY:    registration.PublicKeyY,
		})
		if err != nil {
			return err
		}
		trackingID := job.Args.TrackingID
		if err := w.wc.store.RecordWebAuthnRegistration(ctx, tx, store.WebAuthnCredential{
			CredentialID: registration.CredentialID,
			Address:      userAddr.Hex(),
			KeyHash:      keyHash.Hex(),
			PublicKeyX:   registration.PublicKeyX,
			PublicKeyY:   registration.PublicKeyY,
			TrackingID:   &trackingID,
		}); err != nil {
			return err
		}
	} else if job.Args.HasPartialRegistration() {
		return errors.New("relay: credentialId, publicKeyX and publicKeyY must be sent together")
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
