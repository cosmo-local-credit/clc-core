package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	// AdminOTX is one origin transaction as the operator console lists it.
	//
	// Distinct from OTX, which the per-account and per-tracking-ID reads answer
	// with: this omits raw_tx. A page of a hundred raw signed transactions is a
	// payload no screen reads, and it dwarfs everything else in the row.
	AdminOTX struct {
		ID         uint64 `db:"id"          json:"id"`
		TrackingID string `db:"tracking_id" json:"trackingId"`
		OTXType    string `db:"otx_type"    json:"otxType"`
		// The service signer that sent it. Every row here originates with clc-core,
		// so this is one of our keystore accounts rather than a user address.
		Signer    string    `db:"public_key" json:"signer"`
		TxHash    string    `db:"tx_hash"    json:"txHash"`
		Nonce     uint64    `db:"nonce"      json:"nonce"`
		Replaced  bool      `db:"replaced"   json:"replaced"`
		Status    string    `db:"status"     json:"status"`
		CreatedAt time.Time `db:"created_at" json:"createdAt"`
		UpdatedAt time.Time `db:"updated_at" json:"updatedAt"`
	}
)

const (
	adminOTXSearchPageProjection = `
	SELECT otx.id, otx.tracking_id, otx.otx_type, k.public_key, otx.tx_hash,
	    otx.nonce, otx.replaced, otx.created_at, otx.updated_at, d.status
	FROM page
	INNER JOIN otx ON otx.id = page.id
	INNER JOIN dispatch d ON otx.id = d.otx_id
	INNER JOIN keystore k ON otx.signer_account = k.id
	ORDER BY otx.id DESC;`
)

var (
	// OTXTypes is otx_tx_type, in the order migration 001 seeds it: the deploy
	// pipeline first, then the relayer, then account registration.
	OTXTypes = []string{
		TOKEN_PROXY_DEPLOY,
		"POOL_DEPLOY_STEP",
		POOL_PROXY_DEPLOY,
		POOL_TOKEN_INDEX_DEPLOY,
		POOL_LIMITER_DEPLOY,
		POOL_FEE_POLICY_DEPLOY,
		POOL_ORACLE_QUOTER_DEPLOY,
		POOL_RELATIVE_QUOTER_DEPLOY,
		PROXY_UPGRADE,
		RELAY_DELEGATE,
		RELAY_EXEC,
		RELAY_DELEGATE_EXEC,
		ACCOUNT_REGISTER,
	}

	// DispatchStatuses is dispatch_status_type: the lifecycle first, then the six
	// ways a dispatch fails to reach the chain.
	DispatchStatuses = []string{
		"PENDING",
		"IN_NETWORK",
		"SUCCESS",
		"REVERTED",
		"LOW_NONCE",
		"NO_GAS",
		"LOW_GAS_PRICE",
		"NETWORK_ERROR",
		"REPLACEMENT_UNDERPRICED",
		"UNKNOWN_RPC_ERROR",
	}
)

// ListAdminOTX answers one cursor page of origin transactions, newest first.
// An empty otxType or status means "every one"; both are validated by the
// caller against the slices above before they reach SQL. The unsearched path
// keeps its measured named statements, while a text search materializes its
// match set so its trigram indexes drive before keyset paging is applied.
func (pg *Pg) ListAdminOTX(ctx context.Context, tx pgx.Tx, otxType, status string, search AdminTransactionSearch, page CatalogPage, cursor, limit uint64) ([]AdminOTX, error) {
	var (
		rows  []AdminOTX
		query string
		args  []any
	)

	if search.Term == "" {
		switch page {
		case CatalogNextPage:
			query, args = pg.queries.ListOTXNext, []any{otxType, status, cursor, limit}
		case CatalogPrevPage:
			query, args = pg.queries.ListOTXPrev, []any{otxType, status, cursor, limit}
		default:
			query, args = pg.queries.ListOTX, []any{otxType, status, limit}
		}
	} else {
		composed := composeAdminOTXSearchPage(otxType, status, search, page, cursor, limit)
		query, args = composed.sql, composed.args
	}

	if err := pgxscan.Select(ctx, tx, &rows, query, args...); err != nil {
		return nil, err
	}
	return rows, nil
}

// composeAdminOTXSearchPage is the searched counterpart to the three named
// OTX statements. It is composed from typed page directions and constant column
// fragments only; every request value remains a bound parameter.
func composeAdminOTXSearchPage(
	otxType, status string,
	search AdminTransactionSearch,
	page CatalogPage,
	cursor, limit uint64,
) composed {
	args := &sqlArgs{}
	clauses := []string{
		search.predicate(
			[]string{"otx.tracking_id::text", "otx.tx_hash"},
			func(value string) string { return args.bind(value) },
			args.reuse,
		),
	}
	if otxType != "" {
		clauses = append(clauses, "otx.otx_type = "+args.bind(otxType))
	}
	if status != "" {
		clauses = append(clauses, "d.status = "+args.bind(status))
	}

	body := "WITH matches AS MATERIALIZED (\n" +
		"    SELECT otx.id\n" +
		"    FROM otx\n" +
		"    INNER JOIN dispatch d ON otx.id = d.otx_id" +
		whereClause(clauses...) +
		"\n),\npage AS MATERIALIZED (\n" +
		"    SELECT m.id\n" +
		"    FROM matches m" +
		whereClause(cursorPredicate(args, "m.id", page, cursor)) +
		"\n    ORDER BY m.id " + pageOrder(page) +
		"\n    LIMIT " + args.bind(limit) +
		"\n)"

	return composed{sql: body + adminOTXSearchPageProjection, args: args.values}
}
