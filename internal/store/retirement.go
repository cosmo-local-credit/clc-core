package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// RetirementResource state is separate from request state: a pending request does not
// change catalog visibility, balances, or the creator's resource allowance.
type (
	RetirementResource struct {
		ID              uint64  `db:"id"`
		ResourceType    string  `db:"-"`
		Address         string  `db:"address"`
		ChainID         int64   `db:"chain_id"`
		Name            string  `db:"name"`
		Symbol          string  `db:"symbol"`
		Decimals        int     `db:"decimals"`
		OwnerAddress    string  `db:"owner_address"`
		Active          bool    `db:"active"`
		RegistryAddress *string `db:"registry_address"`
	}

	RetirementPreview struct {
		ResourceType          string    `json:"resourceType"`
		ResourceAddress       string    `json:"resourceAddress"`
		ChainID               int64     `json:"chainId"`
		OwnerAddress          string    `json:"ownerAddress"`
		Active                bool      `json:"active"`
		Symbol                string    `json:"symbol"`
		Decimals              int       `json:"decimals"`
		AmountOutsideOwnerRaw *string   `json:"amountOutsideOwnerRaw"`
		PositiveTokenCount    *int      `json:"positiveTokenCount"`
		HasPositiveBalances   bool      `json:"hasPositiveBalances"`
		BlockNumber           string    `json:"blockNumber"`
		CheckedAt             time.Time `json:"checkedAt"`
		Coverage              string    `json:"coverage"`
		CoverageNote          *string   `json:"coverageNote"`
	}

	RetirementRequest struct {
		ID               uint64             `db:"id" json:"id"`
		VoucherID        *uint64            `db:"voucher_id" json:"-"`
		PoolID           *uint64            `db:"pool_id" json:"-"`
		ResourceType     string             `db:"resource_type" json:"resourceType"`
		ResourceAddress  string             `db:"resource_address" json:"resourceAddress"`
		ResourceName     string             `db:"resource_name" json:"resourceName"`
		Symbol           string             `db:"symbol" json:"symbol"`
		ChainID          int64              `db:"chain_id" json:"chainId"`
		RequesterAddress string             `db:"requester_address" json:"requesterAddress"`
		OwnerAddress     string             `db:"owner_address" json:"ownerAddress"`
		Reason           string             `db:"reason" json:"reason"`
		Status           string             `db:"status" json:"status"`
		Snapshot         RetirementPreview  `db:"snapshot" json:"snapshot"`
		RequestedAt      time.Time          `db:"requested_at" json:"requestedAt"`
		DecisionAt       *time.Time         `db:"decision_at" json:"decisionAt"`
		DecisionBy       *string            `db:"decision_by" json:"decisionBy"`
		DecisionReason   *string            `db:"decision_reason" json:"decisionReason"`
		DecisionSnapshot *RetirementPreview `db:"decision_snapshot" json:"decisionSnapshot"`
	}

	RetirementEvent struct {
		ID           uint64    `db:"id" json:"id"`
		RequestID    *uint64   `db:"request_id" json:"requestId"`
		Action       string    `db:"action" json:"action"`
		ActorAddress string    `db:"actor_address" json:"actorAddress"`
		Reason       string    `db:"reason" json:"reason"`
		CreatedAt    time.Time `db:"created_at" json:"createdAt"`
	}

	RetirementFilter struct {
		Status       string
		ResourceType string
		OwnerAddress string
		Resource     *RetirementResource
		Before       uint64
		Limit        uint64
	}
)

const (
	retirementRequestSelect = `SELECT r.id,r.voucher_id,r.pool_id,
    CASE WHEN r.voucher_id IS NOT NULL THEN 'voucher' ELSE 'pool' END AS resource_type,
    COALESCE(v.address,p.address) AS resource_address,
    COALESCE(v.display_name,v.name,p.display_name,p.name,'') AS resource_name,
    COALESCE(v.symbol,p.symbol,'') AS symbol,COALESCE(v.chain_id,p.chain_id) AS chain_id,
    r.requester_address,r.owner_address,r.reason,r.status,r.snapshot,r.requested_at,
    r.decision_at,r.decision_by,r.decision_reason,r.decision_snapshot
    FROM retirement_requests r LEFT JOIN vouchers v ON v.id=r.voucher_id LEFT JOIN pools p ON p.id=r.pool_id`
)

var (
	ErrRetirementState = errors.New("retirement request is no longer pending")
)

func retirementTable(kind string) (table, column string, err error) {
	switch kind {
	case "voucher":
		return "vouchers", "voucher_id", nil
	case "pool":
		return "pools", "pool_id", nil
	default:
		return "", "", errors.New("invalid retirement resource type")
	}
}

func GetRetirementResource(ctx context.Context, tx pgx.Tx, kind, address string, chainID int64, lock bool) (RetirementResource, error) {
	table, _, err := retirementTable(kind)
	if err != nil {
		return RetirementResource{}, err
	}
	registry := "NULL::text"
	if kind == "pool" {
		registry = "token_registry_address"
	}
	query := `SELECT id,address,chain_id,COALESCE(display_name,name,'') AS name,
        COALESCE(symbol,'') AS symbol,COALESCE(decimals,0) AS decimals,
        COALESCE(owner_address,'') AS owner_address,active,` + registry + ` AS registry_address
        FROM ` + table + ` WHERE lower(address)=lower($1) AND ($2::bigint=0 OR chain_id=$2)`
	if lock {
		query += " FOR UPDATE"
	}
	r := RetirementResource{ResourceType: kind}
	err = pgxscan.Get(ctx, tx, &r, query, address, chainID)
	return r, err
}

func GetRetirementRequest(ctx context.Context, tx pgx.Tx, id uint64, lock bool) (RetirementRequest, error) {
	query := retirementRequestSelect + " WHERE r.id=$1"
	if lock {
		query += " FOR UPDATE OF r"
	}
	var r RetirementRequest
	err := pgxscan.Get(ctx, tx, &r, query, id)
	return r, err
}

func ListRetirementRequests(ctx context.Context, tx pgx.Tx, f RetirementFilter) ([]RetirementRequest, error) {
	args := &sqlArgs{}
	conditions := []string{"true"}
	if f.Status != "" && f.Status != "all" {
		conditions = append(conditions, "r.status="+args.bind(f.Status))
	}
	if f.ResourceType != "" {
		_, column, err := retirementTable(f.ResourceType)
		if err != nil {
			return nil, err
		}
		conditions = append(conditions, "r."+column+" IS NOT NULL")
	}
	if f.OwnerAddress != "" {
		owner := args.bind(strings.ToLower(f.OwnerAddress))
		// Requests on a Safe's resources reach every current signer.
		conditions = append(conditions, "(r.requester_address="+owner+" OR lower(COALESCE(v.owner_address,p.owner_address))="+owner+
			" OR (COALESCE(v.chain_id,p.chain_id), lower(COALESCE(v.owner_address,p.owner_address))) IN ("+
			"SELECT ss.chain_id, ss.safe_address FROM safe_signers ss WHERE ss.signer="+owner+"))")
	}
	if f.Resource != nil {
		_, column, err := retirementTable(f.Resource.ResourceType)
		if err != nil {
			return nil, err
		}
		conditions = append(conditions, "r."+column+"="+args.bind(f.Resource.ID))
	}
	if f.Before > 0 {
		conditions = append(conditions, "r.id<"+args.bind(f.Before))
	}
	query := retirementRequestSelect + " WHERE " + strings.Join(conditions, " AND ") + " ORDER BY r.id DESC"
	if f.Limit > 0 {
		query += " LIMIT " + args.bind(f.Limit)
	}
	result := []RetirementRequest{}
	err := pgxscan.Select(ctx, tx, &result, query, args.values...)
	return result, err
}

func retirementResourceIDs(resource RetirementResource) (voucherID, poolID *uint64) {
	if resource.ResourceType == "voucher" {
		return &resource.ID, nil
	}
	return nil, &resource.ID
}

func InsertRetirementEvent(ctx context.Context, tx pgx.Tx, resource RetirementResource, requestID *uint64, action, actor, reason string) error {
	voucherID, poolID := retirementResourceIDs(resource)
	_, err := tx.Exec(ctx, `INSERT INTO retirement_events(voucher_id,pool_id,request_id,action,actor_address,reason) VALUES($1,$2,$3,$4,$5,$6)`, voucherID, poolID, requestID, action, strings.ToLower(actor), reason)
	return err
}

func CreateRetirementRequest(ctx context.Context, tx pgx.Tx, resource RetirementResource, actor, reason string, preview RetirementPreview) (RetirementRequest, error) {
	voucherID, poolID := retirementResourceIDs(resource)
	var id uint64
	err := tx.QueryRow(ctx, `INSERT INTO retirement_requests(voucher_id,pool_id,requester_address,owner_address,reason,snapshot) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, voucherID, poolID, strings.ToLower(actor), strings.ToLower(resource.OwnerAddress), reason, preview).Scan(&id)
	if err != nil {
		return RetirementRequest{}, err
	}
	if err = InsertRetirementEvent(ctx, tx, resource, &id, "requested", actor, reason); err != nil {
		return RetirementRequest{}, err
	}
	return GetRetirementRequest(ctx, tx, id, false)
}

func DecideRetirementRequest(ctx context.Context, tx pgx.Tx, resource RetirementResource, id uint64, status, actor, reason string, preview *RetirementPreview) (RetirementRequest, error) {
	tag, err := tx.Exec(ctx, `UPDATE retirement_requests SET status=$2,decision_at=CURRENT_TIMESTAMP,decision_by=$3,decision_reason=$4,decision_snapshot=$5 WHERE id=$1 AND status='pending'`, id, status, strings.ToLower(actor), reason, preview)
	if err != nil {
		return RetirementRequest{}, err
	}
	if tag.RowsAffected() != 1 {
		return RetirementRequest{}, ErrRetirementState
	}
	if err = InsertRetirementEvent(ctx, tx, resource, &id, status, actor, reason); err != nil {
		return RetirementRequest{}, err
	}
	return GetRetirementRequest(ctx, tx, id, false)
}

func SetRetirementResourceActive(ctx context.Context, tx pgx.Tx, resource RetirementResource, active bool) error {
	table, _, err := retirementTable(resource.ResourceType)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE "+table+" SET active=$2,updated_at=CURRENT_TIMESTAMP WHERE id=$1", resource.ID, active)
	return err
}

func ListRetirementEvents(ctx context.Context, tx pgx.Tx, resource RetirementResource) ([]RetirementEvent, error) {
	_, column, err := retirementTable(resource.ResourceType)
	if err != nil {
		return nil, err
	}
	result := []RetirementEvent{}
	err = pgxscan.Select(ctx, tx, &result, `SELECT id,request_id,action,actor_address,reason,created_at FROM retirement_events WHERE `+column+`=$1 ORDER BY id DESC`, resource.ID)
	return result, err
}

// RetirementPoolTokens includes disallowed/retired vouchers. Public composition
// filters must never decide which balances a retirement warning checks.
func RetirementPoolTokens(ctx context.Context, tx pgx.Tx, poolID uint64) ([]string, error) {
	result := []string{}
	err := pgxscan.Select(ctx, tx, &result, `SELECT DISTINCT lower(v.address) FROM pool_tokens pt JOIN vouchers v ON v.id=pt.voucher_id WHERE pt.pool_id=$1`, poolID)
	return result, err
}

func (pg *Pg) RestorePoolWithinLimits(ctx context.Context, tx pgx.Tx, address string) error {
	_, err := pg.RestorePoolWithinLimitsChanged(ctx, tx, address)
	return err
}

func (pg *Pg) RestorePoolWithinLimitsChanged(ctx context.Context, tx pgx.Tx, address string) (bool, error) {
	resource, err := GetRetirementResource(ctx, tx, "pool", address, 0, false)
	if err != nil {
		return false, err
	}
	rows, err := tx.Query(ctx, `SELECT u.id FROM users u WHERE u.id IN (
        SELECT deployment.user_id FROM user_deployments deployment
        JOIN otx chain_tx ON chain_tx.tracking_id=deployment.tracking_id
        JOIN deployed_pools p ON p.otx_id=chain_tx.id
        WHERE p.pool_id=$1 AND deployment.resource_type='POOL') ORDER BY u.id FOR UPDATE OF u`, resource.ID)
	if err != nil {
		return false, err
	}
	users, err := pgx.CollectRows(rows, pgx.RowTo[uint64])
	if err != nil {
		return false, err
	}
	resource, err = GetRetirementResource(ctx, tx, "pool", address, resource.ChainID, true)
	if err != nil {
		return false, err
	}
	if resource.Active {
		return false, nil
	}
	before := map[uint64]ResourceQuota{}
	for _, id := range users {
		usage, e := pg.GetUserResourceUsage(ctx, tx, id)
		if e != nil {
			return false, e
		}
		before[id] = usage.Pools
	}
	if err = SetRetirementResourceActive(ctx, tx, resource, true); err != nil {
		return false, err
	}
	for _, id := range users {
		usage, e := pg.GetUserResourceUsage(ctx, tx, id)
		if e != nil {
			return false, e
		}
		after := usage.Pools
		if !after.Unlimited && after.Used > after.Limit && after.Used > before[id].Used {
			return false, &ResourceLimitError{Resource: "pools", Used: after.Used, Limit: after.Limit}
		}
	}
	return true, nil
}
