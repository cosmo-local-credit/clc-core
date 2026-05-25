// Package relayer implements the EIP-7702 / Calibur transaction relayer.
//
// The relayer accepts client-signed Calibur batched calls and produces raw
// transactions ready for dispatch via the existing River DISPATCH worker. It
// also exposes WebAuthn challenge / verification primitives used by the
// recovery flow.
package relayer

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"log/slog"
	"math/big"

	"github.com/cosmo-local-credit/clc-core/internal/gas"
	"github.com/cosmo-local-credit/clc-core/internal/keypair"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/grassrootseconomics/ethutils"
	gocalibur "github.com/grassrootseconomics/go-calibur"
	"github.com/jackc/pgx/v5"
)

type Opts struct {
	Logg           *slog.Logger
	ChainProvider  *ethutils.Provider
	GasOracle      gas.GasOracle
	Store          store.Store
	ChainID        int64
	RPCEndpoint    string
	PrivateKey     string
	CaliburAddress string
	SetCodeGas     uint64
	DelegateGas    uint64
	RelyingPartyID string
	AllowedOrigins []string
}

type Relayer struct {
	logg            *slog.Logger
	chainProvider   *ethutils.Provider
	gasOracle       gas.GasOracle
	store           store.Store
	caliburProvider *gocalibur.Provider

	privateKey     *ecdsa.PrivateKey
	address        common.Address
	chainID        *big.Int
	caliburAddress common.Address

	setCodeGas  uint64
	delegateGas uint64

	challenges     *challengeStore
	relyingPartyID string
	allowedOrigins []string
}

func New(o Opts) (*Relayer, error) {
	if o.PrivateKey == "" {
		return nil, errors.New("relayer: master signing key is empty — store.Bootstrap must run before loadRelayer")
	}
	if o.CaliburAddress == "" {
		return nil, errors.New("relayer: calibur_address is required")
	}
	if !common.IsHexAddress(o.CaliburAddress) {
		return nil, fmt.Errorf("relayer: invalid calibur_address %q", o.CaliburAddress)
	}

	pk := o.PrivateKey
	if len(pk) >= 2 && pk[:2] == "0x" {
		pk = pk[2:]
	}
	privateKey, err := crypto.HexToECDSA(pk)
	if err != nil {
		return nil, fmt.Errorf("relayer: invalid private_key: %w", err)
	}
	address := crypto.PubkeyToAddress(privateKey.PublicKey)

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
		privateKey:      privateKey,
		address:         address,
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

func (r *Relayer) Address() common.Address        { return r.address }
func (r *Relayer) CaliburAddress() common.Address { return r.caliburAddress }
func (r *Relayer) ChainID() *big.Int              { return new(big.Int).Set(r.chainID) }
func (r *Relayer) PrivateKey() *ecdsa.PrivateKey  { return r.privateKey }
func (r *Relayer) Provider() *gocalibur.Provider  { return r.caliburProvider }

func (r *Relayer) Store() store.Store    { return r.store }
func (r *Relayer) GasOracle() gas.GasOracle { return r.gasOracle }

// SetCodeGas returns the hardcoded gas limit for relays that include an
// AuthList (delegation has not yet taken effect, so estimation fails).
func (r *Relayer) SetCodeGas() uint64 { return r.setCodeGas }

// DelegateGas returns the gas limit for a bare SetCodeTx (no calldata).
func (r *Relayer) DelegateGas() uint64 { return r.delegateGas }

// Bootstrap ensures the relayer signer has a keystore row (so the
// noncestore trigger seeds a nonce row) and fast-forwards the local
// noncestore to the on-chain pending nonce.
//
// Idempotent — safe to call on every startup.
func (r *Relayer) Bootstrap(ctx context.Context) error {
	tx, err := r.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	active, err := r.store.CheckKeypair(ctx, tx, r.address.Hex())
	if errors.Is(err, pgx.ErrNoRows) {
		// Row doesn't exist yet — insert it (triggers noncestore seed) and activate.
		if err := r.store.InsertKeyPair(ctx, tx, keypair.Key{
			Public:  r.address.Hex(),
			Private: "",
		}); err != nil {
			return fmt.Errorf("relayer: insert keypair: %w", err)
		}
		if err := r.store.ActivateKeyPair(ctx, tx, r.address.Hex()); err != nil {
			return fmt.Errorf("relayer: activate keypair: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("relayer: check keypair: %w", err)
	} else if !active {
		// Row exists but is inactive (e.g. was disabled) — just re-activate.
		if err := r.store.ActivateKeyPair(ctx, tx, r.address.Hex()); err != nil {
			return fmt.Errorf("relayer: activate keypair: %w", err)
		}
	}

	chainNonce, err := r.caliburProvider.GetNonce(ctx, r.address)
	if err != nil {
		return fmt.Errorf("relayer: read on-chain nonce: %w", err)
	}
	if err := r.store.SetNonceIfHigher(ctx, tx, r.address.Hex(), chainNonce); err != nil {
		return fmt.Errorf("relayer: align nonce: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	r.logg.Info("relayer bootstrapped",
		"address", r.address.Hex(),
		"calibur", r.caliburAddress.Hex(),
		"chainNonce", chainNonce,
	)
	return nil
}
