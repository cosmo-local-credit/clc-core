package routertest

import (
	"context"
	"math/rand/v2"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type (
	// Network is a small graph seeded identically into Postgres and the chain:
	//
	//	P1 {A, B}  parity
	//	P2 {B, C}  parity
	//	P3 {A, C}  C priced at twice A, so the direct route pays half
	//	P4 {C, D}  parity
	//
	// Every pool has a registry, limiter, relative quoter, a zero default fee and
	// a 2 % protocol fee. From A, depth 1 reaches B (P1) and C (P3); depth 2 adds
	// D (P3 then P4). A to C pays better through B.
	Network struct {
		ChainID        int64
		Chain          *Chain
		Wallet         common.Address
		Calibur        common.Address
		Relayer        common.Address
		A, B, C, D     common.Address
		P1, P2, P3, P4 common.Address
		PoolIDs        map[common.Address]uint64
		VoucherIDs     map[common.Address]uint64
		pool           *pgxpool.Pool
	}
)

const (
	// Liquidity is what each pool holds of each member, in raw units.
	Liquidity = 1_000_000_000

	// Limit is each member's deposit cap.
	Limit = 10_000_000_000
)

func randomAddress(r *rand.Rand) common.Address {
	var a common.Address
	for i := range a {
		a[i] = byte(r.UintN(256))
	}
	return a
}

// SeedNetwork inserts the network into db under a chain id no other suite
// uses, configures a matching chain, and deletes the rows when the test ends.
// The database holds membership and caps only; price indexes, balances and
// pool parameters arrive through reconciliation, as on a fresh deployment.
func SeedNetwork(t *testing.T, db *pgxpool.Pool) *Network {
	t.Helper()
	ctx := context.Background()
	r := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	n := &Network{
		ChainID:    int64(800_000 + r.IntN(99_000)),
		Chain:      New(),
		Wallet:     randomAddress(r),
		Calibur:    common.HexToAddress("0x1289fE793392fb1a905f78Ea23f632EDD3d15b85"),
		Relayer:    randomAddress(r),
		PoolIDs:    map[common.Address]uint64{},
		VoucherIDs: map[common.Address]uint64{},
		pool:       db,
	}
	n.A, n.B, n.C, n.D = randomAddress(r), randomAddress(r), randomAddress(r), randomAddress(r)
	n.P1, n.P2, n.P3, n.P4 = randomAddress(r), randomAddress(r), randomAddress(r), randomAddress(r)
	suffix := common.Bytes2Hex(randomAddress(r).Bytes()[:4])

	for i, token := range []common.Address{n.A, n.B, n.C, n.D} {
		var id uint64
		require.NoError(t, db.QueryRow(ctx, `
			INSERT INTO vouchers (chain_id, address, name, symbol, decimals, voucher_type, active)
			VALUES ($1, $2, $3, $4, 6, 'GIFTABLE', TRUE) RETURNING id`,
			n.ChainID, token.Hex(), "Route voucher "+string(rune('A'+i))+" "+suffix,
			"RV"+string(rune('A'+i))+suffix).Scan(&id))
		n.VoucherIDs[token] = id
		n.Chain.Decimals[token] = 6
	}

	type poolSpec struct {
		address common.Address
		members []common.Address
		index   map[common.Address]uint64
	}
	specs := []poolSpec{
		{n.P1, []common.Address{n.A, n.B}, nil},
		{n.P2, []common.Address{n.B, n.C}, nil},
		{n.P3, []common.Address{n.A, n.C}, map[common.Address]uint64{n.A: 1_000_000, n.C: 2_000_000}},
		{n.P4, []common.Address{n.C, n.D}, nil},
	}
	for i, spec := range specs {
		registry, limiter, quoter := randomAddress(r), randomAddress(r), randomAddress(r)
		feePolicy, controller := randomAddress(r), randomAddress(r)
		var id uint64
		require.NoError(t, db.QueryRow(ctx, `
			INSERT INTO pools (chain_id, address, name, symbol, token_registry_address, limiter_address,
			    quoter_address, quoter_type, fee_policy_address, protocol_fee_controller_address, fees_decoupled, active)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'relative', $8, $9, FALSE, TRUE) RETURNING id`,
			n.ChainID, spec.address.Hex(), "Route pool "+string(rune('1'+i))+" "+suffix, "RP"+string(rune('1'+i))+suffix,
			registry.Hex(), limiter.Hex(), quoter.Hex(), feePolicy.Hex(), controller.Hex()).Scan(&id))
		n.PoolIDs[spec.address] = id

		p := n.Chain.AddPool(spec.address, &Pool{
			Registry: registry, Limiter: limiter, Quoter: quoter, FeePolicy: feePolicy, Controller: controller,
			ProtocolFee: 20_000, Recipient: randomAddress(r), Members: spec.members,
		})
		for _, token := range spec.members {
			p.Limits[token] = *uint256.NewInt(Limit)
			if v, ok := spec.index[token]; ok {
				p.Index[token] = *uint256.NewInt(v)
			}
			n.Chain.SetBalance(token, spec.address, Liquidity)
			_, err := db.Exec(ctx, `
				INSERT INTO pool_tokens (pool_id, voucher_id, is_allowed, token_limit)
				VALUES ($1, $2, TRUE, $3::NUMERIC)`, id, n.VoucherIDs[token], uint64(Limit))
			require.NoError(t, err)
		}
	}
	n.Chain.SetBalance(n.A, n.Wallet, 5_000_000)
	n.Chain.Delegate(n.Wallet, n.Calibur)

	t.Cleanup(func() {
		ctx := context.Background()
		for _, id := range n.PoolIDs {
			_, _ = db.Exec(ctx, `DELETE FROM pools WHERE id = $1`, id)
		}
		for _, id := range n.VoucherIDs {
			_, _ = db.Exec(ctx, `DELETE FROM vouchers WHERE id = $1`, id)
		}
	})
	return n
}

// Relay returns a relayer stub for the network.
func (n *Network) Relay() Relay {
	return Relay{Signer: n.Relayer, Calibur: n.Calibur}
}
