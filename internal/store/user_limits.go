package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type (
	UserLimitsUpdate struct {
		MaxPools    *int
		MaxTokens   *int
		MaxCatalogs *int
		MaxOffers   *int
	}

	ResourceQuota struct {
		Used      int  `json:"used"`
		Limit     int  `json:"limit"`
		Unlimited bool `json:"unlimited"`
		CanCreate bool `json:"canCreate"`
	}

	UserResourceUsage struct {
		Vouchers ResourceQuota `json:"vouchers"`
		Pools    ResourceQuota `json:"pools"`
		Catalogs ResourceQuota `json:"catalogs"`
		Offers   ResourceQuota `json:"offers"`
	}

	ResourceLimitError struct {
		Resource string
		Used     int
		Limit    int
	}
)

func (e *ResourceLimitError) Error() string {
	return fmt.Sprintf("%s creation limit reached (%d of %d used)", e.Resource, e.Used, e.Limit)
}

// GetUserResourceUsage uses one statement so all counts and allowances share a
// snapshot. Offer attribution is the immutable seller, including imported rows.
func (pg *Pg) GetUserResourceUsage(ctx context.Context, tx pgx.Tx, userID uint64) (UserResourceUsage, error) {
	var usage UserResourceUsage
	var role string
	err := tx.QueryRow(ctx, pg.queries.GetUserResourceUsage, userID).Scan(
		&role, &usage.Vouchers.Limit, &usage.Pools.Limit, &usage.Catalogs.Limit, &usage.Offers.Limit,
		&usage.Vouchers.Used, &usage.Pools.Used, &usage.Catalogs.Used, &usage.Offers.Used,
	)
	if err != nil {
		return usage, err
	}
	for _, quota := range []*ResourceQuota{&usage.Vouchers, &usage.Pools, &usage.Catalogs, &usage.Offers} {
		quota.Unlimited = role == "SUPERADMIN"
		quota.CanCreate = quota.Unlimited || quota.Used < quota.Limit
	}
	return usage, nil
}

// CheckUserResourceLimit must run before resource locks. The caller must insert
// the resource (or deployment plus job) in this transaction before committing.
// The separate count statement sees a preceding creator's commit after waiting
// for the user lock under READ COMMITTED. Admin allowance updates use this row too.
func (pg *Pg) CheckUserResourceLimit(ctx context.Context, tx pgx.Tx, userID uint64, resource string) error {
	var lockedID uint64
	if err := tx.QueryRow(ctx, pg.queries.LockQuotaUser, userID).Scan(&lockedID); err != nil {
		return err
	}
	usage, err := pg.GetUserResourceUsage(ctx, tx, userID)
	if err != nil {
		return err
	}
	var quota ResourceQuota
	switch resource {
	case "vouchers":
		quota = usage.Vouchers
	case "pools":
		quota = usage.Pools
	case "catalogs":
		quota = usage.Catalogs
	case "offers":
		quota = usage.Offers
	default:
		return fmt.Errorf("unknown quota resource %q", resource)
	}
	if !quota.CanCreate {
		return &ResourceLimitError{Resource: resource, Used: quota.Used, Limit: quota.Limit}
	}
	return nil
}
