package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type (
	UserLimitsUpdate struct {
		MaxPools    *int
		MaxTokens   *int
		MaxCatalogs *int
		MaxOffers   *int
		MaxWants    *int
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
		Wants    ResourceQuota `json:"wants"`
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
// snapshot. Offers count only while available on an active voucher, attributed
// to their immutable seller (including imported rows).
func (pg *Pg) GetUserResourceUsage(ctx context.Context, tx pgx.Tx, userID uint64) (UserResourceUsage, error) {
	var usage UserResourceUsage
	var role string
	err := tx.QueryRow(ctx, pg.queries.GetUserResourceUsage, userID).Scan(
		&role, &usage.Vouchers.Limit, &usage.Pools.Limit, &usage.Catalogs.Limit, &usage.Offers.Limit, &usage.Wants.Limit,
		&usage.Vouchers.Used, &usage.Pools.Used, &usage.Catalogs.Used, &usage.Offers.Used, &usage.Wants.Used,
	)
	if err != nil {
		return usage, err
	}
	for _, quota := range []*ResourceQuota{&usage.Vouchers, &usage.Pools, &usage.Catalogs, &usage.Offers, &usage.Wants} {
		quota.Unlimited = IsAdministrativeRole(role)
		quota.CanCreate = quota.Unlimited || quota.Used < quota.Limit
	}
	return usage, nil
}

// LockQuotaUser serializes allowance checks and admin limit changes for one
// account. Offering creation takes this lock before the voucher lock, then
// counts the locked voucher, so concurrent creates cannot both claim the final
// per-voucher slot.
func (pg *Pg) LockQuotaUser(ctx context.Context, tx pgx.Tx, userID uint64) error {
	var lockedID uint64
	return tx.QueryRow(ctx, pg.queries.LockQuotaUser, userID).Scan(&lockedID)
}

func voucherOfferingQuota(row pgx.Row) (ResourceQuota, error) {
	var quota ResourceQuota
	var role string
	var used int64
	if err := row.Scan(&role, &quota.Limit, &used); err != nil {
		return quota, err
	}
	quota.Used = int(used)
	quota.Unlimited = IsAdministrativeRole(role)
	quota.CanCreate = quota.Unlimited || quota.Used < quota.Limit
	return quota, nil
}

// GetUserResourceUsageForVoucher preserves the legacy aggregate resource
// response while replacing only Offers with the authorized voucher's exact
// count. An unknown voucher and a voucher belonging to another user both return
// pgx.ErrNoRows, so the handler does not disclose catalog membership.
func (pg *Pg) GetUserResourceUsageForVoucher(
	ctx context.Context,
	tx pgx.Tx,
	userID uint64,
	voucherAddress string,
) (UserResourceUsage, error) {
	usage, err := pg.GetUserResourceUsage(ctx, tx, userID)
	if err != nil {
		return usage, err
	}
	usage.Offers, err = voucherOfferingQuota(
		tx.QueryRow(ctx, pg.queries.GetVoucherOfferingQuota, userID, voucherAddress),
	)
	return usage, err
}

// CheckVoucherOfferingLimit applies max_offers independently to one voucher.
// The caller must already hold the quota-user lock and the voucher row lock.
func (pg *Pg) CheckVoucherOfferingLimit(
	ctx context.Context,
	tx pgx.Tx,
	userID, voucherID uint64,
) error {
	quota, err := voucherOfferingQuota(
		tx.QueryRow(ctx, pg.queries.GetVoucherOfferingQuotaByID, userID, voucherID),
	)
	if err != nil {
		return err
	}
	if !quota.CanCreate {
		return &ResourceLimitError{Resource: "offers", Used: quota.Used, Limit: quota.Limit}
	}
	return nil
}

// CheckUserResourceLimit must run before resource locks. The caller must insert
// the resource (or deployment plus job) in this transaction before committing.
// The separate count statement sees a preceding creator's commit after waiting
// for the user lock under READ COMMITTED. Admin allowance updates use this row too.
func (pg *Pg) CheckUserResourceLimit(ctx context.Context, tx pgx.Tx, userID uint64, resource string) error {
	if err := pg.LockQuotaUser(ctx, tx, userID); err != nil {
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
	case "wants":
		quota = usage.Wants
	default:
		return fmt.Errorf("unknown quota resource %q", resource)
	}
	if !quota.CanCreate {
		return &ResourceLimitError{Resource: resource, Used: quota.Used, Limit: quota.Limit}
	}
	return nil
}

// RestoreVoucherWithinLimits reactivates retained offers with their voucher.
// The caller must roll back on error. Read usage after taking quota locks and
// compare it after activation: unrelated, pre-existing excess must not block a
// restoration that does not increase that quota.
func (pg *Pg) RestoreVoucherWithinLimits(ctx context.Context, tx pgx.Tx, address string) error {
	_, err := pg.RestoreVoucherWithinLimitsChanged(ctx, tx, address)
	return err
}

// RestoreVoucherWithinLimitsChanged reports the state transition under the
// voucher lock so an audit event cannot be lost to a concurrent retirement.
func (pg *Pg) RestoreVoucherWithinLimitsChanged(ctx context.Context, tx pgx.Tx, address string) (bool, error) {
	voucher, err := pg.GetAdminVoucherByAddress(ctx, tx, address)
	if err != nil {
		return false, err
	}
	rows, err := tx.Query(ctx, pg.queries.LockVoucherRestoreUsers, voucher.ID)
	if err != nil {
		return false, err
	}
	userIDs, err := pgx.CollectRows(rows, pgx.RowTo[uint64])
	if err != nil {
		return false, err
	}
	parent, err := pg.LockVoucherForOfferingCreate(ctx, tx, address)
	if err != nil {
		return false, err
	}
	if parent.Active {
		return false, nil
	}
	before := make(map[uint64]UserResourceUsage, len(userIDs))
	for _, id := range userIDs {
		before[id], err = pg.GetUserResourceUsage(ctx, tx, id)
		if err != nil {
			return false, err
		}
	}
	if err := pg.SetVoucherActive(ctx, tx, voucher.ID, true); err != nil {
		return false, err
	}
	for _, id := range userIDs {
		after, err := pg.GetUserResourceUsage(ctx, tx, id)
		if err != nil {
			return false, err
		}
		for _, check := range []struct {
			resource string
			before   ResourceQuota
			after    ResourceQuota
		}{
			{"vouchers", before[id].Vouchers, after.Vouchers},
		} {
			if !check.after.Unlimited && check.after.Used > check.after.Limit && check.after.Used > check.before.Used {
				return false, &ResourceLimitError{Resource: check.resource, Used: check.after.Used, Limit: check.after.Limit}
			}
		}
	}
	// Restoring an inactive voucher makes every retained available offering
	// public again. Validate that voucher against its current registered owner;
	// historical sellers and the original deployer no longer define this quota.
	if parent.OwnerAddress != nil {
		owner, ownerErr := pg.GetUserByAddress(ctx, tx, *parent.OwnerAddress)
		if ownerErr != nil && !errors.Is(ownerErr, pgx.ErrNoRows) {
			return false, ownerErr
		}
		if ownerErr == nil {
			quota, quotaErr := voucherOfferingQuota(
				tx.QueryRow(ctx, pg.queries.GetVoucherOfferingQuotaByID, owner.ID, voucher.ID),
			)
			if quotaErr != nil {
				return false, quotaErr
			}
			if !quota.Unlimited && quota.Used > quota.Limit {
				return false, &ResourceLimitError{Resource: "offers", Used: quota.Used, Limit: quota.Limit}
			}
		}
	}
	return true, nil
}
