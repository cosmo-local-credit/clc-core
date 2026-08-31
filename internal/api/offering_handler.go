package api

import (
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/labstack/echo/v5"
)

// offeringPricePattern admits plain decimal text and nothing else.
//
// Deliberately not a numeric parse: the value travels as text from the form to
// NUMERIC and back out through ::TEXT, so it is never rounded. Parsing it into a
// float64 to check it would reintroduce exactly the rounding the string exists to
// avoid. `1e3`, `1_000` and `+1` are rejected rather than normalised, because a
// caller that meant one of those should say which.
var (
	offeringPricePattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
)

// pgCheckViolation and pgForeignKeyViolation are the two constraint classes an
// offering write can trip with a caller-fixable cause: a non-HTTP image URL and a
// unit that is not in the lookup. Both are validated in the handler first, so
// reaching one means a race or a gap — a 400 naming the field beats a 500.
const (
	pgCheckViolation      = "23514"
	pgForeignKeyViolation = "23503"
)

// voucherOfferingHandler serves the public item read of one offering under its
// voucher.
//
// Same filters as the public list: an unlisted row or a retired voucher is a 404,
// never a readable object carrying `available: false`. An id belonging to a
// different voucher is also a 404 rather than a redirect to its real parent —
// the URL asserts a relationship, and the answer is that this one does not hold.
func (a *API) voucherOfferingHandler(c *echo.Context) error {
	address, id, handled, err := offeringRouteParams(c)
	if handled || err != nil {
		return err
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	offering, err := a.store.GetVoucherOffering(ctx, tx, address, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return offeringNotFoundError(c)
		}
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	setMarketCacheHeaders(c)
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Offering",
		Result:      map[string]any{"offering": offering},
	})
}

// voucherOfferingCreateHandler creates one offering against the voucher named in
// the route.
//
// Authorization is decided from the voucher row locked in this transaction, not
// from any request field and not from a cached client copy: a caller who could
// name a voucher they do not own would otherwise be able to publish under it.
//
// The seller is server-selected. Normally it is the locked voucher's owner, which
// keeps the non-null provenance column honest without letting a caller claim
// another address. An imported voucher with no owner yet can only be written by a
// superadmin, and then the seller is that operator's own session address — the
// row stays operator-managed until ownership is corrected.
func (a *API) voucherOfferingCreateHandler(c *echo.Context) error {
	address, handled, err := offeringVoucherParam(c)
	if handled || err != nil {
		return err
	}

	write, handled, err := a.bindOfferingWrite(c)
	if handled || err != nil {
		return err
	}

	_, callerAddress, callerRole := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	voucher, err := a.store.LockVoucherForOfferingCreate(ctx, tx, address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return offeringVoucherNotFoundError(c)
		}
		return handlePostgresError(c, err)
	}
	if !voucher.Active {
		return offeringConflictError(c, "voucher is retired and cannot receive offerings")
	}

	seller, ok := offeringSeller(callerAddress, callerRole, voucher.OwnerAddress)
	if !ok {
		return handleForbiddenError(c,
			"only the voucher owner or a superadmin may publish offerings for this voucher")
	}

	id, err := a.store.CreateOffering(ctx, tx, voucher.ID, seller, write)
	if err != nil {
		return handleOfferingWriteError(c, err)
	}
	saved, err := a.store.GetOfferingForMutation(ctx, tx, id)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return respondOffering(c, http.StatusCreated, "Offering created", saved)
}

// voucherOfferingUpdateHandler is assign-all PUT of the product fields.
//
// Omission is not preservation: there is no PATCH, so a body without `imageUrl`
// clears the photo. That is the whole point of assign-all — a form that shows
// every field it can change has no way to express "leave this one alone", and a
// silent preserve-on-omission rule would make a cleared photo indistinguishable
// from a field the client forgot.
func (a *API) voucherOfferingUpdateHandler(c *echo.Context) error {
	address, id, handled, err := offeringRouteParams(c)
	if handled || err != nil {
		return err
	}

	write, handled, err := a.bindOfferingWrite(c)
	if handled || err != nil {
		return err
	}

	_, callerAddress, callerRole := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	parent, handled, err := a.authorizeOfferingMutation(c, tx, id, address, callerAddress, callerRole)
	if handled || err != nil {
		return err
	}

	if err := a.store.UpdateOffering(ctx, tx, parent.ID, write); err != nil {
		return handleOfferingWriteError(c, err)
	}
	saved, err := a.store.GetOfferingForMutation(ctx, tx, parent.ID)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return respondOffering(c, http.StatusOK, "Offering updated", saved)
}

// voucherOfferingDeleteHandler unlists an offering.
//
// The row survives with `available = false`, which every public read already
// filters. Keeping it preserves the Sarafu source_id a later catalog import needs
// to reconcile against, and the timestamps that make the change auditable. A
// recurring import must therefore respect a locally unlisted row rather than
// restoring the source's own `available`.
//
// The stored photo is not deleted. Bucket objects are immutable and the storage
// service has no delete endpoint; a domain request must not acquire bucket
// credentials or become a distributed transaction to tidy one object.
func (a *API) voucherOfferingDeleteHandler(c *echo.Context) error {
	address, id, handled, err := offeringRouteParams(c)
	if handled || err != nil {
		return err
	}

	_, callerAddress, callerRole := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	parent, handled, err := a.authorizeOfferingMutation(c, tx, id, address, callerAddress, callerRole)
	if handled || err != nil {
		return err
	}

	deleted, err := a.store.DeleteOffering(ctx, tx, parent.ID)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Offering deleted",
		Result:      map[string]any{"id": deleted},
	})
}

// authorizeOfferingMutation locks the offering joined to the voucher named in the
// route and decides whether the caller may write it.
//
// Existence and authorization are one step because the lock is what makes the
// decision race-free: reading the owner and then updating would let a transfer
// land in between. A missing id, an id under a different voucher, and an
// already-unlisted id are all the same 404 — distinguishing them would tell a
// caller that an id exists somewhere they cannot see.
func (a *API) authorizeOfferingMutation(
	c *echo.Context,
	tx pgx.Tx,
	id uint64,
	address, callerAddress, callerRole string,
) (*store.OfferingParent, bool, error) {
	parent, err := a.store.LockOfferingVoucherOwner(c.Request().Context(), tx, id, address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, true, offeringNotFoundError(c)
		}
		return nil, true, handlePostgresError(c, err)
	}
	if !parent.Available {
		return nil, true, offeringNotFoundError(c)
	}
	// Activity is checked before ownership on purpose: a retired voucher is a
	// state the owner needs told about, and it is not secret — the voucher's own
	// public record already carries it.
	if !parent.Active {
		return nil, true, offeringConflictError(c, "voucher is retired and its offerings cannot be changed")
	}
	if !canWriteVoucherOfferings(callerAddress, callerRole, parent.OwnerAddress) {
		return nil, true, handleForbiddenError(c,
			"only the voucher owner or a superadmin may change this offering")
	}
	return parent, false, nil
}

// canWriteVoucherOfferings decides authority from the voucher relationship.
//
// Not from `offerings.seller_address`: on imported rows that column holds the
// historical Sarafu seller, which may be an address no longer connected to the
// voucher. In this slice a voucher is one issuer's catalog, so the current owner
// manages every offering under it and a superadmin manages any. Letting a
// historical seller write would half-enable a multi-seller marketplace through
// import data instead of designing one.
func canWriteVoucherOfferings(callerAddress, callerRole string, ownerAddress *string) bool {
	if callerRole == "SUPERADMIN" {
		return true
	}
	if callerAddress == "" || ownerAddress == nil {
		return false
	}
	return strings.EqualFold(callerAddress, *ownerAddress)
}

// offeringSeller returns the address a new row records as its seller, and whether
// the caller may create at all.
//
// A superadmin creating for an ownerless imported voucher becomes the seller
// themselves: the column is NOT NULL and format-checked, so it needs a real
// address, and attributing it to the operator who published it is the only
// honest answer available.
func offeringSeller(callerAddress, callerRole string, ownerAddress *string) (string, bool) {
	if ownerAddress != nil && *ownerAddress != "" {
		if !canWriteVoucherOfferings(callerAddress, callerRole, ownerAddress) {
			return "", false
		}
		return *ownerAddress, true
	}
	if callerRole != "SUPERADMIN" || !common.IsHexAddress(callerAddress) {
		return "", false
	}
	return callerAddress, true
}

// bindOfferingWrite validates request shape, then the normalisation and
// cross-field rules validator tags cannot express. Reports handled=true after
// writing a response, matching the catalog parsing helpers.
func (a *API) bindOfferingWrite(c *echo.Context) (store.OfferingWrite, bool, error) {
	var req apiresp.OfferingWriteRequest
	if err := c.Bind(&req); err != nil {
		return store.OfferingWrite{}, true, handleBindError(c, err)
	}

	// Whitespace is normalised once, here, before the length rules run: a name of
	// 120 spaces is blank, not a valid 120-character name.
	req.CommodityName = strings.TrimSpace(req.CommodityName)
	req.Description = strings.TrimSpace(req.Description)
	req.Unit = strings.TrimSpace(req.Unit)
	req.Price = strings.TrimSpace(req.Price)

	if err := c.Validate(req); err != nil {
		return store.OfferingWrite{}, true, a.handleValidateError(c, err)
	}

	if !offeringPricePattern.MatchString(req.Price) {
		return store.OfferingWrite{}, true, offeringValidationError(c,
			"price must be plain decimal text without an exponent or separators")
	}
	// Zero is a well-formed decimal and not a price. The pattern cannot say so,
	// and trimming leading zeros to compare would be a numeric parse by another
	// name, so the check is textual.
	if strings.Trim(strings.ReplaceAll(req.Price, ".", ""), "0") == "" {
		return store.OfferingWrite{}, true, offeringValidationError(c, "price must be greater than zero")
	}

	unit := strings.ToLower(req.Unit)
	if !slices.Contains(apiresp.OfferingUnits, unit) {
		return store.OfferingWrite{}, true, offeringValidationError(c,
			"unit must be one of the supported offering units")
	}

	imageURL, ok := normalizeOfferingImageURL(req.ImageURL)
	if !ok {
		return store.OfferingWrite{}, true, offeringValidationError(c, "imageUrl must be an http or https URL")
	}

	return store.OfferingWrite{
		CommodityName: req.CommodityName,
		CommodityType: req.CommodityType,
		Description:   req.Description,
		Unit:          unit,
		Price:         req.Price,
		ImageURL:      imageURL,
	}, false, nil
}

// normalizeOfferingImageURL treats an absent, null and empty photo as the same
// thing, and rejects any scheme the storage service could not have produced.
//
// The database check enforces this too, but reaching it would surface as a
// constraint violation rather than as the field that is wrong.
func normalizeOfferingImageURL(raw *string) (*string, bool) {
	if raw == nil {
		return nil, true
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return nil, true
	}
	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return nil, false
	}
	return &trimmed, true
}

// offeringRouteParams parses both path parameters of an item route.
func offeringRouteParams(c *echo.Context) (address string, id uint64, handled bool, err error) {
	address, handled, err = offeringVoucherParam(c)
	if handled || err != nil {
		return "", 0, handled, err
	}
	id, err = strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		return "", 0, true, offeringValidationError(c, "id must be a positive integer")
	}
	return address, id, false, nil
}

func offeringVoucherParam(c *echo.Context) (string, bool, error) {
	address := c.Param("address")
	if !common.IsHexAddress(address) {
		return "", true, offeringValidationError(c, "address must be a valid hex address")
	}
	return address, false, nil
}

// respondOffering returns the row as saved, read back inside the write's own
// transaction. A client therefore learns how its decimal and trimmed text were
// stored without a refetch that the public endpoint's cache could answer stale.
func respondOffering(c *echo.Context, status int, description string, offering *store.Offering) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(status, apiresp.OKResponse{
		Ok:          true,
		Description: description,
		Result:      map[string]any{"offering": offering},
	})
}

// handleOfferingWriteError turns the two caller-fixable constraint violations
// into the 400 that names the field, and leaves anything else to propagate.
func handleOfferingWriteError(c *echo.Context, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case pgForeignKeyViolation:
			return offeringValidationError(c, "unit must be one of the supported offering units")
		case pgCheckViolation:
			return offeringValidationError(c, "imageUrl must be an http or https URL")
		}
	}
	return handlePostgresError(c, err)
}

func offeringValidationError(c *echo.Context, description string) error {
	return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeValidationFailed,
		Description: description,
	})
}

func offeringNotFoundError(c *echo.Context) error {
	return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeNotFound,
		Description: "Offering not found",
	})
}

func offeringVoucherNotFoundError(c *echo.Context) error {
	return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeNotFound,
		Description: "Voucher not found",
	})
}

func offeringConflictError(c *echo.Context, description string) error {
	return c.JSON(http.StatusConflict, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeConflict,
		Description: description,
	})
}
