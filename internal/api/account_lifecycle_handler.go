package api

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

type (
	// accountRetirementWrite is the body of a retire or restore request. A reason
	// is optional; the acknowledgement is required of the person retiring their
	// own account and ignored on operator requests.
	accountRetirementWrite struct {
		Reason       string `json:"reason"`
		Acknowledged bool   `json:"acknowledged"`
	}

	moderationWrite struct {
		Reason string `json:"reason"`
	}

	// sessionCapabilities is what the server lets this session do. The client
	// renders these rather than inferring permission from one status field.
	sessionCapabilities struct {
		CanUseApp            bool `json:"canUseApp"`
		CanRecoverAssets     bool `json:"canRecoverAssets"`
		CanTransferResources bool `json:"canTransferResources"`
		CanRestoreAccount    bool `json:"canRestoreAccount"`
	}

	retirementPreviewResource struct {
		ResourceType string `db:"resource_type" json:"resourceType"`
		Address      string `db:"address" json:"address"`
		Name         string `db:"name" json:"name"`
		Active       bool   `db:"active" json:"active"`
	}

	accountRetirementPreview struct {
		ClosesAt              time.Time                   `json:"closesAt"`
		RecoveryMonths        int                         `json:"recoveryMonths"`
		OwnedResources        []retirementPreviewResource `json:"ownedResources"`
		PendingResourceAsks   int                         `json:"pendingResourceRequests"`
		HiddenContent         accountHiddenContentCounts  `json:"hiddenContent"`
		BalancesCoverage      string                      `json:"balancesCoverage"`
		BalancesCoverageNote  string                      `json:"balancesCoverageNote"`
		PendingOperationsNote string                      `json:"pendingOperationsNote"`
	}

	accountHiddenContentCounts struct {
		Wants    int `json:"wants"`
		Catalogs int `json:"catalogs"`
		Posts    int `json:"posts"`
		Reviews  int `json:"reviews"`
	}
)

// lifecycleSessionCapabilities is the single place that maps a lifecycle state
// to permissions. A retiring account keeps the actions needed to recover funds,
// hand resources over and come back; everything else is closed.
func lifecycleSessionCapabilities(lifecycle string, closesAt *time.Time) sessionCapabilities {
	switch lifecycle {
	case "", store.LifecycleActive:
		return sessionCapabilities{CanUseApp: true, CanRecoverAssets: true, CanTransferResources: true}
	case store.LifecycleRetirementScheduled:
		return sessionCapabilities{
			CanRecoverAssets:     true,
			CanTransferResources: true,
			CanRestoreAccount:    closesAt != nil && time.Now().Before(*closesAt),
		}
	default:
		return sessionCapabilities{}
	}
}

func callerLifecycle(c *echo.Context) string {
	lifecycle, _ := c.Get("callerLifecycle").(string)
	if lifecycle == "" {
		return store.LifecycleActive
	}
	return lifecycle
}

// recoveryWrites are the only mutations a retiring account may make. The wallet
// operations themselves (sending, withdrawing, transferring ownership) are signed
// transactions submitted to the relayer, which this API does not gate; this list
// covers the account-bound requests around them.
var (
	recoveryWrites = map[string]struct{}{
		// A repeated retirement request answers with the current state.
		"POST " + apiVersion + "/users/me/retirement":                   {},
		"POST " + apiVersion + "/users/me/retirement/restore":           {},
		"POST " + apiVersion + "/vouchers/:address/retirement-requests": {},
		"POST " + apiVersion + "/pools/:address/retirement-requests":    {},
		"PUT " + apiVersion + "/retirement-requests/:id/cancel":         {},
		"POST " + apiVersion + "/safe/proposals":                        {},
		"POST " + apiVersion + "/safe/confirmations":                    {},
	}

	// recoveryBlockedReads are reads that belong to normal activity rather than
	// to recovering what the account holds.
	recoveryBlockedReads = []string{
		apiVersion + "/conversations",
		apiVersion + "/helpdesk",
		apiVersion + "/blocks",
	}
)

func recoveryAllows(method, route string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		for _, prefix := range recoveryBlockedReads {
			if strings.HasPrefix(route, prefix) {
				return false
			}
		}
		return true
	}
	_, ok := recoveryWrites[method+" "+route]
	return ok
}

// enforceRecoveryAccess limits a retiring account to recovery actions. The
// lifecycle comes from the same per-request lookup that authenticates the
// session, so a retirement or restore takes effect on every device at once.
func (a *API) enforceRecoveryAccess() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if callerType, _ := c.Get("callerType").(string); callerType != "user" {
				return next(c)
			}
			if callerLifecycle(c) == store.LifecycleActive {
				return next(c)
			}
			if recoveryAllows(c.Request().Method, c.Path()) {
				return next(c)
			}
			return c.JSON(http.StatusForbidden, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrCodeAccountRetired,
				Description: "this account is retired and limited to recovery actions",
			})
		}
	}
}

func accountReason(c *echo.Context) (accountRetirementWrite, error) {
	var body accountRetirementWrite
	if c.Request().ContentLength != 0 {
		if err := c.Bind(&body); err != nil {
			return body, echo.NewHTTPError(http.StatusBadRequest, "Invalid request")
		}
	}
	body.Reason = strings.TrimSpace(body.Reason)
	if utf8.RuneCountInString(body.Reason) > store.MaxAccountRetirementReason {
		return body, echo.NewHTTPError(http.StatusBadRequest, "Reason must be at most 2000 characters")
	}
	return body, nil
}

// mayActOnAccount is the role hierarchy for Ban, Unban and Retire: an Admin acts
// on Users, a Superadmin on Users and Admins, and nobody acts on a Superadmin or
// on themselves. The server enforces it; hidden buttons are only a convenience.
func mayActOnAccount(callerRole, callerAddress, targetRole, targetAddress string) bool {
	if strings.EqualFold(callerAddress, targetAddress) {
		return false
	}
	switch callerRole {
	case string(store.UserRoleSuperadmin):
		return targetRole == string(store.UserRoleUser) || targetRole == string(store.UserRoleAdmin)
	case string(store.UserRoleAdmin):
		return targetRole == string(store.UserRoleUser)
	}
	return false
}

func lifecycleOK(c *echo.Context, description string, result map[string]any) error {
	c.Response().Header().Set("Cache-Control", "private, no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Description: description, Result: result})
}

// accountRetirementPreviewFor reads what a retirement of the account at address
// would hide and what it leaves in place. Balances are not read here: the
// preview says so rather than presenting a partial list as complete.
func (a *API) accountRetirementPreviewFor(c *echo.Context, tx pgx.Tx, userID uint64, address string) (accountRetirementPreview, error) {
	ctx := c.Request().Context()
	// The closing date comes from the same PostgreSQL month arithmetic the
	// retirement insert uses, so the preview never promises a later date than the
	// one stored (Go's AddDate rolls 31 August + 6 months to 3 March).
	closesAt, err := store.RetirementClosingDate(ctx, tx)
	if err != nil {
		return accountRetirementPreview{}, err
	}
	preview := accountRetirementPreview{
		ClosesAt:              closesAt,
		RecoveryMonths:        store.RetirementRecoveryMonths,
		OwnedResources:        []retirementPreviewResource{},
		BalancesCoverage:      "unavailable",
		BalancesCoverageNote:  "Balances are not summarised here. Review your wallet before confirming.",
		PendingOperationsNote: "Unsettled transactions already sent to the network cannot be recalled.",
	}
	if err := pgxscan.Select(ctx, tx, &preview.OwnedResources, `
        SELECT 'voucher' AS resource_type, address, COALESCE(display_name, name, '') AS name, active
        FROM vouchers WHERE lower(owner_address) = lower($1)
        UNION ALL
        SELECT 'pool', address, COALESCE(display_name, name, ''), active
        FROM pools WHERE lower(owner_address) = lower($1)
        ORDER BY 1, 2 LIMIT 100`, address); err != nil {
		return preview, err
	}
	err = tx.QueryRow(ctx, `SELECT
        (SELECT count(*) FROM retirement_requests WHERE status = 'pending' AND requester_address = lower($2)),
        (SELECT count(*) FROM wants WHERE owner_user_id = $1 AND status = 'OPEN'),
        (SELECT count(*) FROM catalogs WHERE owner_user_id = $1),
        (SELECT count(*) FROM reports WHERE lower(author_address) = lower($2) AND status = 'APPROVED'),
        (SELECT count(*) FROM voucher_reviews WHERE lower(reviewer_address) = lower($2) AND NOT hidden)
          + (SELECT count(*) FROM pool_reviews WHERE lower(reviewer_address) = lower($2) AND NOT hidden)`,
		userID, address).Scan(&preview.PendingResourceAsks, &preview.HiddenContent.Wants,
		&preview.HiddenContent.Catalogs, &preview.HiddenContent.Posts, &preview.HiddenContent.Reviews)
	return preview, err
}

func (a *API) userRetirementPreviewHandler(c *echo.Context) error {
	userID, address, _ := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	preview, err := a.accountRetirementPreviewFor(c, tx, userID, address)
	if err != nil {
		return err
	}
	return lifecycleOK(c, "Account retirement preview", map[string]any{"preview": preview})
}

func retirementState(retirement *store.AccountRetirement) map[string]any {
	lifecycle := store.LifecycleActive
	var closesAt *time.Time
	if retirement != nil {
		lifecycle = retirement.State
		closesAt = &retirement.ClosesAt
	}
	return map[string]any{
		"accountLifecycle": lifecycle,
		"closesAt":         closesAt,
		"capabilities":     lifecycleSessionCapabilities(lifecycle, closesAt),
		"retirement":       retirement,
	}
}

// userRetireHandler retires the caller's own account. It takes effect at once:
// there is no approval step, because the account stays recoverable for the
// recovery period and an administrator cannot be a gate on a person closing
// their own account.
func (a *API) userRetireHandler(c *echo.Context) error {
	userID, address, _ := getUserClaims(c)
	body, err := accountReason(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// The role is read under the lock, not from the token: a user promoted to
	// SUPERADMIN since the token was issued is still protected.
	_, _, currentRole, err := store.LockUserForLifecycle(ctx, tx, address)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if store.IsSuperadminRole(currentRole) {
		return handleForbiddenError(c, "superadmin accounts cannot be retired from here")
	}
	if existing, ok, err := store.GetOpenAccountRetirement(ctx, tx, userID, false); err != nil {
		return err
	} else if ok {
		return lifecycleOK(c, "Account is already retired", retirementState(&existing))
	}
	if !body.Acknowledged {
		return echo.NewHTTPError(http.StatusBadRequest, "Acknowledge the closing date, hidden content and recovery limits to continue")
	}
	retirement, _, err := store.RetireAccount(ctx, tx, userID, userID, store.RetirementSourceSelf, body.Reason)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return lifecycleOK(c, "Account retired", retirementState(&retirement))
}

func (a *API) userRestoreHandler(c *echo.Context) error {
	userID, address, _ := getUserClaims(c)
	return a.restoreAccount(c, userID, address, userID, store.RetirementSourceSelf)
}

// restoreAccount reopens an account under an unrestored retirement. Restoring an
// account that is not retired succeeds without writing an event, and the account's
// moderation status is untouched: a restore never lifts a ban.
func (a *API) restoreAccount(c *echo.Context, userID uint64, address string, actorID uint64, source string) error {
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, _, _, err := store.LockUserForLifecycle(ctx, tx, address); err != nil {
		return handlePostgresError(c, err)
	}
	if _, err := store.RestoreAccount(ctx, tx, userID, actorID, source); err != nil {
		if errors.Is(err, store.ErrRetirementClosed) {
			return handleConflictError(c, err.Error())
		}
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return lifecycleOK(c, "Account restored", retirementState(nil))
}

// adminAccountTarget resolves the :address account and enforces the hierarchy.
// It reports handled = true when it has written the error response.
func (a *API) adminAccountTarget(c *echo.Context, tx pgx.Tx) (targetID uint64, address, targetStatus string, handled bool, err error) {
	address, handled, err = adminUserAddress(c)
	if handled || err != nil {
		return 0, "", "", true, err
	}
	ctx := c.Request().Context()
	var targetRole string
	targetID, targetStatus, targetRole, err = store.LockUserForLifecycle(ctx, tx, address)
	if err != nil {
		return 0, "", "", true, handlePostgresError(c, err)
	}
	_, callerAddress, callerRole := getUserClaims(c)
	if !mayActOnAccount(callerRole, callerAddress, targetRole, address) {
		return 0, "", "", true, handleForbiddenError(c, "you cannot act on this account")
	}
	return targetID, address, targetStatus, false, nil
}

func (a *API) adminAccountRetirementHandler(c *echo.Context) error {
	address, handled, err := adminUserAddress(c)
	if handled || err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var userID uint64
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE lower(blockchain_address)=lower($1)`, address).Scan(&userID); err != nil {
		return handlePostgresError(c, err)
	}
	retirement, ok, err := store.GetOpenAccountRetirement(ctx, tx, userID, false)
	if err != nil {
		return err
	}
	events, err := store.ListAccountLifecycleEvents(ctx, tx, userID)
	if err != nil {
		return err
	}
	preview, err := a.accountRetirementPreviewFor(c, tx, userID, address)
	if err != nil {
		return err
	}
	state := retirementState(nil)
	if ok {
		state = retirementState(&retirement)
	}
	state["events"] = events
	state["preview"] = preview
	return lifecycleOK(c, "Account retirement", state)
}

// adminRetireAccountHandler retires an account on an operator's behalf. Like a
// self-request it is immediate and idempotent.
func (a *API) adminRetireAccountHandler(c *echo.Context) error {
	body, err := accountReason(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	targetID, _, _, handled, err := a.adminAccountTarget(c, tx)
	if handled || err != nil {
		return err
	}
	actorID, _, _ := getUserClaims(c)
	retirement, _, err := store.RetireAccount(ctx, tx, targetID, actorID, store.RetirementSourceAdmin, body.Reason)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return lifecycleOK(c, "Account retired", retirementState(&retirement))
}

func (a *API) adminRestoreAccountHandler(c *echo.Context) error {
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	targetID, address, _, handled, err := a.adminAccountTarget(c, tx)
	tx.Rollback(ctx)
	if handled || err != nil {
		return err
	}
	actorID, _, _ := getUserClaims(c)
	return a.restoreAccount(c, targetID, address, actorID, store.RetirementSourceAdmin)
}

func (a *API) adminModerationHistoryHandler(c *echo.Context) error {
	address, handled, err := adminUserAddress(c)
	if handled || err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var userID uint64
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE lower(blockchain_address)=lower($1)`, address).Scan(&userID); err != nil {
		return handlePostgresError(c, err)
	}
	events, err := store.ListModerationEvents(ctx, tx, userID)
	if err != nil {
		return err
	}
	return lifecycleOK(c, "Moderation history", map[string]any{"events": events})
}
