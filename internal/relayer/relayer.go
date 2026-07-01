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

type (
	Opts struct {
		Logg           *slog.Logger
		ChainProvider  *ethutils.Provider
		GasOracle      gas.GasOracle
		Store          store.Store
		ChainID        int64
		RPCEndpoint    string
		CaliburAddress string
		// Gas model for relayed batches. The limit is computed as
		// BaseExecGas + PerCallGas*len(calls) (+ DelegationOverheadGas on the
		// first relay, which also delegates) with a +10% safety bump, clamped to
		// MaxRelayGas. We cannot eth_estimateGas the first relay because the
		// delegation is not yet live, so we budget from known per-action averages.
		PerCallGas            uint64
		BaseExecGas           uint64
		DelegationOverheadGas uint64
		MaxRelayGas           uint64
		// WebAuthnVerifyGas is added when the batch is signed by a WebAuthn/P256
		// passkey: verifying a P-256 signature on chain costs far more than an
		// secp256k1 ecrecover (no cheap precompile on every chain).
		WebAuthnVerifyGas uint64
		// DelegateGas is the limit for a bare delegation (no calldata).
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
	Relayer struct {
	logg            *slog.Logger
	chainProvider   *ethutils.Provider
	gasOracle       gas.GasOracle
	store           store.Store
	caliburProvider *gocalibur.Provider

	chainID        *big.Int
	caliburAddress common.Address
	signerAddress  common.Address

	perCallGas            uint64
	baseExecGas           uint64
	delegationOverheadGas uint64
	maxRelayGas           uint64
	webAuthnVerifyGas     uint64
	delegateGas           uint64

	challenges     *challengeStore
	relyingPartyID string
	allowedOrigins []string
}
)

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

	perCallGas := o.PerCallGas
	if perCallGas == 0 {
		perCallGas = 80_000
	}
	baseExecGas := o.BaseExecGas
	if baseExecGas == 0 {
		baseExecGas = 50_000
	}
	delegationOverheadGas := o.DelegationOverheadGas
	if delegationOverheadGas == 0 {
		delegationOverheadGas = 150_000
	}
	maxRelayGas := o.MaxRelayGas
	if maxRelayGas == 0 {
		maxRelayGas = 3_000_000
	}
	webAuthnVerifyGas := o.WebAuthnVerifyGas
	if webAuthnVerifyGas == 0 {
		webAuthnVerifyGas = 400_000
	}
	delegateGas := o.DelegateGas
	if delegateGas == 0 {
		delegateGas = 200_000
	}

	r := &Relayer{
		logg:                  o.Logg,
		chainProvider:         o.ChainProvider,
		gasOracle:             o.GasOracle,
		store:                 o.Store,
		caliburProvider:       caliburProvider,
		chainID:               big.NewInt(o.ChainID),
		caliburAddress:        caliburAddr,
		perCallGas:            perCallGas,
		baseExecGas:           baseExecGas,
		delegationOverheadGas: delegationOverheadGas,
		maxRelayGas:           maxRelayGas,
		webAuthnVerifyGas:     webAuthnVerifyGas,
		delegateGas:           delegateGas,
		challenges:            newChallengeStore(),
		relyingPartyID:        o.RelyingPartyID,
		allowedOrigins:        o.AllowedOrigins,
	}

	return r, nil
}

func (r *Relayer) CaliburAddress() common.Address { return r.caliburAddress }
func (r *Relayer) SignerAddress() common.Address  { return r.signerAddress }
func (r *Relayer) ChainID() *big.Int              { return new(big.Int).Set(r.chainID) }
func (r *Relayer) Provider() *gocalibur.Provider  { return r.caliburProvider }

// DelegateGas returns the gas limit for a bare SetCodeTx (no calldata).
func (r *Relayer) DelegateGas() uint64 { return r.delegateGas }

// relayGasLimit budgets gas for a relayed batch from known per-action
// averages rather than eth_estimateGas: the first relay also delegates, and
// the Calibur code is not live at the EOA until that tx executes, so an
// estimate against current state would revert.
//
// limit = (baseExecGas + perCallGas*nCalls [+ delegationOverheadGas]
//
//	[+ webAuthnVerifyGas]) * 1.10, clamped to maxRelayGas.
func (r *Relayer) relayGasLimit(nCalls int, isDelegated, isWebAuthn bool) uint64 {
	g := r.baseExecGas + r.perCallGas*uint64(nCalls)
	if !isDelegated {
		g += r.delegationOverheadGas
	}
	if isWebAuthn {
		g += r.webAuthnVerifyGas
	}
	g += g / 10 // +10% safety bump
	if r.maxRelayGas > 0 && g > r.maxRelayGas {
		return r.maxRelayGas
	}
	return g
}

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
	r.signerAddress = signer
	r.logg.Info("relayer bootstrapped",
		"address", signer.Hex(),
		"calibur", r.caliburAddress.Hex(),
		"chainNonce", chainNonce,
	)
	return nil
}
