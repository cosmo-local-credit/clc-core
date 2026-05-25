package relayer

import (
	"context"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/jackc/pgx/v5"
)

// PreparedTx is the minimum the API layer needs to enqueue a DISPATCH job.
type PreparedTx struct {
	OTXID  uint64
	Hash   string
	RawHex string
}

// AcquireNonce returns the next sequential nonce for the relayer signer.
// Mirrors the master signer's nonce-acquire pattern so worker retries don't
// collide with the same row.
func (r *Relayer) AcquireNonce(ctx context.Context, tx pgx.Tx) (uint64, error) {
	return r.store.AcquireNonce(ctx, tx, r.address.Hex())
}

// InsertTrackedTx persists the signed tx into the OTX table and enqueues a
// pending dispatch row, matching how worker.insertTrackedTx behaves for the
// deploy flows.
func (r *Relayer) InsertTrackedTx(
	ctx context.Context,
	tx pgx.Tx,
	trackingID string,
	otxType string,
	nonce uint64,
	signedTx *types.Transaction,
) (*PreparedTx, error) {
	raw, err := signedTx.MarshalBinary()
	if err != nil {
		return nil, err
	}
	rawHex := hexutil.Encode(raw)

	otxID, err := r.store.InsertOTX(ctx, tx, store.OTX{
		TrackingID:    trackingID,
		OTXType:       otxType,
		SignerAccount: r.address.Hex(),
		RawTx:         rawHex,
		TxHash:        signedTx.Hash().Hex(),
		Nonce:         nonce,
	})
	if err != nil {
		return nil, err
	}

	if err := r.store.InsertDispatchTx(ctx, tx, store.DispatchTx{
		OTXID:  otxID,
		Status: store.PENDING,
	}); err != nil {
		return nil, err
	}

	return &PreparedTx{
		OTXID:  otxID,
		Hash:   signedTx.Hash().Hex(),
		RawHex: rawHex,
	}, nil
}
