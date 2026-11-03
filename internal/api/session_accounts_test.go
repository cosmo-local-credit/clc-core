package api

import (
	"context"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/jackc/pgx/v5"
)

// sessionAccounts stands in for the users table in tests without a database.
// authenticateCaller looks every user token up by id, and a key rotation is
// what makes that lookup necessary: these accounts answer with the address
// each token was minted for. Any other store call panics, as with a nil store.
type (
	sessionAccounts struct {
		store.Store

		accounts map[uint64]store.UserAccess
	}
)

func (s sessionAccounts) GetUserAccessByID(_ context.Context, id uint64) (store.UserAccess, error) {
	if access, ok := s.accounts[id]; ok {
		return access, nil
	}
	return store.UserAccess{}, pgx.ErrNoRows
}

// sessionFixtureAccounts is the account behind sessionCookie.
func sessionFixtureAccounts() sessionAccounts {
	return sessionAccounts{accounts: map[uint64]store.UserAccess{
		42: {EthereumAddress: "0x1111111111111111111111111111111111111111", Role: "USER", Status: "APPROVED"},
	}}
}

// withSessionAccounts gives an API without a database the accounts behind
// tokens. Tokens sharing an id keep the first one's address, so the others are
// treated as sessions of an account that has since moved.
func withSessionAccounts(t *testing.T, a *API, tokens ...string) {
	t.Helper()
	accounts := map[uint64]store.UserAccess{}
	for _, token := range tokens {
		claims, err := a.parseUserJWT(token)
		if err != nil {
			t.Fatalf("parse fixture token: %v", err)
		}
		if _, taken := accounts[claims.UserID]; taken {
			continue
		}
		accounts[claims.UserID] = store.UserAccess{EthereumAddress: claims.EthereumAddress, Role: claims.Role, Status: "APPROVED"}
	}
	a.store = sessionAccounts{accounts: accounts}
}
