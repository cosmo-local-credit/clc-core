package relayer

import (
	"context"
	"math/big"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
)

// VerifyLegacyWebAuthnCredentials settles every credential row written before
// migration 038, when rows were recorded before their register() call was sent
// and kept even if it reverted. A row becomes active only when the chain
// confirms the key: the wallet delegates to Calibur, isRegistered(keyHash) is
// true there, and the stored hash is the one its coordinates produce. Anything
// else becomes unverified and stays in the table for investigation.
//
// It runs once per boot and is a no-op after the first successful pass, since
// resolved rows leave the 'legacy' state. A row whose chain read fails stays
// legacy — and therefore unusable for sign-in — until the next boot retries
// it. This is the only chain read on the credential lifecycle; neither sign-in
// nor the Settings status endpoint makes one.
func (r *Relayer) VerifyLegacyWebAuthnCredentials(ctx context.Context) error {
	legacy, err := r.listLegacyCredentials(ctx)
	if err != nil {
		return err
	}
	if len(legacy) == 0 {
		return nil
	}

	var activated, unverified, deferred int
	for _, cred := range legacy {
		verdict, err := r.legacyVerdict(ctx, cred)
		if err != nil {
			deferred++
			r.logg.Warn("legacy webauthn credential check deferred",
				"credentialId", cred.CredentialID, "address", cred.Address, "err", err)
			continue
		}
		if err := r.resolveLegacyCredential(ctx, cred.CredentialID, verdict); err != nil {
			return err
		}
		if verdict == store.WebAuthnActive {
			activated++
		} else {
			unverified++
			r.logg.Warn("legacy webauthn credential not registered on chain",
				"credentialId", cred.CredentialID, "address", cred.Address, "keyHash", cred.KeyHash)
		}
	}

	r.logg.Info("legacy webauthn credentials checked",
		"activated", activated, "unverified", unverified, "deferred", deferred)
	return nil
}

func (r *Relayer) legacyVerdict(ctx context.Context, cred store.WebAuthnCredential) (string, error) {
	x, okX := new(big.Int).SetString(cred.PublicKeyX, 10)
	y, okY := new(big.Int).SetString(cred.PublicKeyY, 10)
	if !okX || !okY || !common.IsHexAddress(cred.Address) {
		return store.WebAuthnUnverified, nil
	}
	keyHash := ComputeP256KeyHash(x, y)
	if keyHash != common.HexToHash(cred.KeyHash) {
		return store.WebAuthnUnverified, nil
	}

	address := common.HexToAddress(cred.Address)
	delegated, err := r.IsDelegated(ctx, address)
	if err != nil {
		return "", err
	}
	if !delegated {
		// No Calibur code at the address: nothing can have been registered, and
		// isRegistered would not decode.
		return store.WebAuthnUnverified, nil
	}

	registered, err := r.IsKeyRegistered(ctx, address, keyHash)
	if err != nil {
		return "", err
	}
	if registered {
		return store.WebAuthnActive, nil
	}
	return store.WebAuthnUnverified, nil
}

func (r *Relayer) listLegacyCredentials(ctx context.Context) ([]store.WebAuthnCredential, error) {
	tx, err := r.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	creds, err := r.store.ListLegacyWebAuthnCredentials(ctx, tx)
	if err != nil {
		return nil, err
	}
	return creds, tx.Commit(ctx)
}

func (r *Relayer) resolveLegacyCredential(ctx context.Context, credentialID, state string) error {
	tx, err := r.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := r.store.ResolveLegacyWebAuthnCredential(ctx, tx, credentialID, state); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
