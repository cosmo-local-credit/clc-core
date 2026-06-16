// Package relayer implements the EIP-7702 / Calibur transaction relayer.
//
// The relayer accepts client-signed Calibur batched calls and produces raw
// transactions ready for dispatch via the existing River DISPATCH worker. It
// also exposes WebAuthn challenge / verification primitives used by the
// recovery flow.
package relayer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"

	"github.com/cosmo-local-credit/clc-core/internal/gas"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/grassrootseconomics/ethutils"
	gocalibur "github.com/grassrootseconomics/go-calibur"
)

type Opts struct {
	Logg           *slog.Logger
	ChainProvider  *ethutils.Provider
	GasOracle      gas.GasOracle
	Store          store.Store
	ChainID        int64
	RPCEndpoint    string
	CaliburAddress string
	SetCodeGas     uint64
	DelegateGas    uint64
	RelyingPartyID string
	AllowedOrigins []string
}

// Relayer holds the chain-read facilities and EIP-7702 / Calibur
// configuration shared by the relay river workers and the read-only relayer
// API handlers.
//
// It deliberately does NOT hold the master signing key: every signature is
// produced inside a river worker's Work method, which loads the key from the
// store within its own transaction (see internal/worker/relayer.go).
type Relayer struct {
	logg            *slog.Logger
	chainProvider   *ethutils.Provider
	gasOracle       gas.GasOracle
	store           store.Store
	caliburProvider *gocalibur.Provider

	chainID        *big.Int
	caliburAddress common.Address

	setCodeGas  uint64
	delegateGas uint64

	challenges     *challengeStore
	relyingPartyID string
	allowedOrigins []string
}

func New(o Opts) (*Relayer, error) {
	if o.CaliburAddress == "" {
		return nil, errors.New("relayer: calibur_address is required")
	}
	if !common.IsHexAddress(o.CaliburAddress) {
		return nil, fmt.Errorf("relayer: invalid calibur_address %q", o.CaliburAddress)
	}

	caliburAddr := common.HexToAddress(o.CaliburAddress)

	caliburProvider, err := gocalibur.NewProvider(
		o.RPCEndpoint,
		o.ChainID,
		gocalibur.WithCaliburAddress(caliburAddr),
	)
	if err != nil {
		return nil, fmt.Errorf("relayer: calibur provider init: %w", err)
	}

	setCodeGas := o.SetCodeGas
	if setCodeGas == 0 {
		setCodeGas = 350_000
	}
	delegateGas := o.DelegateGas
	if delegateGas == 0 {
		delegateGas = 200_000
	}

	r := &Relayer{
		logg:            o.Logg,
		chainProvider:   o.ChainProvider,
		gasOracle:       o.GasOracle,
		store:           o.Store,
		caliburProvider: caliburProvider,
		chainID:         big.NewInt(o.ChainID),
		caliburAddress:  caliburAddr,
		setCodeGas:      setCodeGas,
		delegateGas:     delegateGas,
		challenges:      newChallengeStore(),
		relyingPartyID:  o.RelyingPartyID,
		allowedOrigins:  o.AllowedOrigins,
	}

	return r, nil
}

func (r *Relayer) CaliburAddress() common.Address { return r.caliburAddress }
func (r *Relayer) ChainID() *big.Int              { return new(big.Int).Set(r.chainID) }
func (r *Relayer) Provider() *gocalibur.Provider  { return r.caliburProvider }

// SetCodeGas returns the hardcoded gas limit for relays that include an
// AuthList (delegation has not yet taken effect, so estimation fails).
func (r *Relayer) SetCodeGas() uint64 { return r.setCodeGas }

// DelegateGas returns the gas limit for a bare SetCodeTx (no calldata).
func (r *Relayer) DelegateGas() uint64 { return r.delegateGas }

// Bootstrap fast-forwards the local noncestore for the master signer to the
// on-chain pending nonce. The relayer signs with the same master key the
// store already manages, so its keystore row and nonce seed already exist —
// here we only guard against on-chain drift (e.g. txs sent out of band)
// before the relay workers start acquiring nonces.
//
// Idempotent — safe to call on every startup.
func (r *Relayer) Bootstrap(ctx context.Context) error {
	tx, err := r.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	masterKey, err := r.store.LoadMasterSignerKey(ctx, tx)
	if err != nil {
		return fmt.Errorf("relayer: load master signer: %w", err)
	}
	signer := common.HexToAddress(masterKey.Public)

	chainNonce, err := r.caliburProvider.GetNonce(ctx, signer)
	if err != nil {
		return fmt.Errorf("relayer: read on-chain nonce: %w", err)
	}
	if err := r.store.SetNonceIfHigher(ctx, tx, signer.Hex(), chainNonce); err != nil {
		return fmt.Errorf("relayer: align nonce: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	r.logg.Info("relayer bootstrapped",
		"address", signer.Hex(),
		"calibur", r.caliburAddress.Hex(),
		"chainNonce", chainNonce,
	)
	return nil
}
