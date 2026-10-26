package api

import (
	"context"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/safe"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
)

type (
	stateStore struct {
		store.Store

		state store.SafeState
		err   error
	}

	countingSafeReader struct {
		reads       int
		err         error
		hadDeadline bool
	}
)

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
		"signer acts as the Safe":              {stateStore{state: fresh}, signer, &owner, safeAddr},
		"non-signer stays itself":              {stateStore{state: fresh}, other, &owner, other},
		"owner itself is unchanged":            {stateStore{state: fresh}, safeAddr, &owner, safeAddr},
		"unknown Safe stays itself":            {stateStore{err: pgx.ErrNoRows}, signer, &owner, signer},
		"store error fails closed":             {stateStore{err: context.DeadlineExceeded}, signer, &owner, signer},
		"no recorded owner":                    {stateStore{state: fresh}, signer, nil, signer},
		"persisted membership does not expire": {stateStore{state: store.SafeState{Signers: []string{signer}, RefreshedAt: time.Now().Add(-48 * time.Hour)}}, signer, &owner, safeAddr},
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

func (r *countingSafeReader) ReadSafeLatest(ctx context.Context, _ common.Address) (*safe.State, uint64, error) {
	r.reads++
	_, r.hadDeadline = ctx.Deadline()
	return nil, 0, r.err
}

// A non-owner write on an EOA-owned asset must not repeat head reads inside
// its transaction; superadmins need no lookup at all.
func TestActingOwnerRemembersOwnersThatAreNotSafes(t *testing.T) {
	const (
		eoaOwner = "0x2222222222222222222222222222222222222222"
		caller   = "0x00000000000000000000000000000000000000a1"
	)
	owner := eoaOwner
	clock := time.Unix(1_700_000_000, 0)
	reader := &countingSafeReader{err: safe.ErrNotSafe}
	a := &API{store: stateStore{err: pgx.ErrNoRows}, safeReader: reader, safeMisses: newSafeMissCache(func() time.Time { return clock })}
	ctx := context.Background()

	for range 3 {
		if got := a.actingOwner(ctx, nil, caller, &owner); got != caller {
			t.Fatalf("actingOwner() = %s, want %s", got, caller)
		}
	}
	if reader.reads != 1 || !reader.hadDeadline {
		t.Fatalf("reads = %d (deadline %v), want one bounded read", reader.reads, reader.hadDeadline)
	}
	if _, err := a.ensureSafeState(ctx, nil, eoaOwner, true); err != nil || reader.reads != 2 {
		t.Fatalf("inspection must read through the miss cache: reads = %d, err = %v", reader.reads, err)
	}
	clock = clock.Add(safeMissTTL)
	a.actingOwner(ctx, nil, caller, &owner)
	if reader.reads != 3 {
		t.Fatalf("an expired miss must be read again: reads = %d", reader.reads)
	}
	if !a.callerMayManageRecord(ctx, nil, caller, "SUPERADMIN", &owner) || reader.reads != 3 {
		t.Fatalf("a superadmin needs no signer lookup: reads = %d", reader.reads)
	}

	// A failed read is not a miss: the next write tries again.
	reader.err = context.DeadlineExceeded
	other := "0x3333333333333333333333333333333333333333"
	a.actingOwner(ctx, nil, caller, &other)
	a.actingOwner(ctx, nil, caller, &other)
	if reader.reads != 5 {
		t.Fatalf("reads = %d, want a retry after each failed read", reader.reads)
	}
}
