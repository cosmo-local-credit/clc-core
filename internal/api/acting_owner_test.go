package api

import (
	"context"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/jackc/pgx/v5"
)

type stateStore struct {
	store.Store
	state store.SafeState
	err   error
}

func (s stateStore) GetSafeState(context.Context, pgx.Tx, int64, string) (store.SafeState, error) {
	return s.state, s.err
}

func TestActingOwnerLetsASignerActAsTheOwnerSafe(t *testing.T) {
	const (
		safeAddr = "0x1111111111111111111111111111111111111111"
		signer   = "0x00000000000000000000000000000000000000a1"
		other    = "0x00000000000000000000000000000000000000b2"
	)
	owner := safeAddr
	fresh := store.SafeState{Signers: []string{signer}, Threshold: 2, RefreshedAt: time.Now()}

	cases := map[string]struct {
		store  stateStore
		caller string
		owner  *string
		want   string
	}{
		"signer acts as the Safe":    {stateStore{state: fresh}, signer, &owner, safeAddr},
		"non-signer stays itself":    {stateStore{state: fresh}, other, &owner, other},
		"owner itself is unchanged":  {stateStore{state: fresh}, safeAddr, &owner, safeAddr},
		"unknown Safe stays itself":  {stateStore{err: pgx.ErrNoRows}, signer, &owner, signer},
		"store error fails closed":   {stateStore{err: context.DeadlineExceeded}, signer, &owner, signer},
		"no recorded owner":          {stateStore{state: fresh}, signer, nil, signer},
		"stale set without a reader": {stateStore{state: store.SafeState{Signers: []string{signer}, RefreshedAt: time.Now().Add(-48 * time.Hour)}}, signer, &owner, signer},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := &API{store: tc.store}
			if got := a.actingOwner(context.Background(), nil, tc.caller, tc.owner); got != tc.want {
				t.Fatalf("actingOwner() = %s, want %s", got, tc.want)
			}
		})
	}
}
