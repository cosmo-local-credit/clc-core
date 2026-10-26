package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

var (
	wantMarkerIcons  = []string{"message", "question", "request"}
	wantMarkerColors = []string{"blue", "brown", "cyan", "orange", "pink", "purple"}
)

type (
	wantWriteRequest struct {
		Name         string     `json:"name"`
		Description  string     `json:"description"`
		LocationName string     `json:"locationName"`
		GeoLat       float64    `json:"geoLat"`
		GeoLng       float64    `json:"geoLng"`
		StartsAt     time.Time  `json:"startsAt"`
		ExpiresAt    *time.Time `json:"expiresAt"`
		ImageURL     *string    `json:"imageUrl"`
		ContactText  *string    `json:"contactText"`
		ShareEmail   bool       `json:"shareEmail"`
		SharePhone   bool       `json:"sharePhone"`
		MarkerIcon   string     `json:"markerIcon"`
		MarkerColor  string     `json:"markerColor"`
		Tags         []string   `json:"tags"`
	}
)

func wantIDParam(c *echo.Context) (uint64, error) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		return 0, respondCatalogSearchError(c, errors.New("id must be a positive integer"))
	}
	return id, nil
}

func normalizeWantWrite(req wantWriteRequest) (store.WantWrite, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Description = strings.TrimSpace(req.Description)
	req.LocationName = strings.TrimSpace(req.LocationName)
	if req.ContactText != nil {
		trimmed := strings.TrimSpace(*req.ContactText)
		if trimmed == "" {
			req.ContactText = nil
		} else {
			req.ContactText = &trimmed
		}
	}
	if req.ImageURL != nil {
		trimmed := strings.TrimSpace(*req.ImageURL)
		if trimmed == "" {
			req.ImageURL = nil
		} else {
			parsed, err := url.Parse(trimmed)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				return store.WantWrite{}, errors.New("imageUrl must be an uploaded HTTP or HTTPS URL")
			}
			req.ImageURL = &trimmed
		}
	}
	if utf8.RuneCountInString(req.Name) < 1 || utf8.RuneCountInString(req.Name) > 120 {
		return store.WantWrite{}, errors.New("name must contain between 1 and 120 characters")
	}
	if utf8.RuneCountInString(req.Description) < 1 || utf8.RuneCountInString(req.Description) > 2000 {
		return store.WantWrite{}, errors.New("description must contain between 1 and 2,000 characters")
	}
	if req.LocationName == "" || req.GeoLat < -90 || req.GeoLat > 90 || req.GeoLng < -180 || req.GeoLng > 180 {
		return store.WantWrite{}, errors.New("a valid location is required")
	}
	if req.StartsAt.IsZero() {
		return store.WantWrite{}, errors.New("startsAt must be an RFC3339 timestamp")
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.After(req.StartsAt) {
		return store.WantWrite{}, errors.New("expiresAt must be after startsAt")
	}
	if req.ContactText != nil && utf8.RuneCountInString(*req.ContactText) > 500 {
		return store.WantWrite{}, errors.New("contactText must be 500 characters or fewer")
	}
	if !req.ShareEmail && !req.SharePhone && req.ContactText == nil {
		return store.WantWrite{}, errors.New("choose email, phone, or enter contact instructions")
	}
	if !slices.Contains(wantMarkerIcons, req.MarkerIcon) {
		return store.WantWrite{}, errors.New("markerIcon must be question, message, or request")
	}
	if !slices.Contains(wantMarkerColors, req.MarkerColor) {
		return store.WantWrite{}, errors.New("markerColor is not supported")
	}
	filter, err := store.NewCatalogFilter("", req.Tags, store.TagMatchAny)
	if err != nil {
		return store.WantWrite{}, err
	}
	permission := 0
	if req.ShareEmail {
		permission |= store.WantContactEmail
	}
	if req.SharePhone {
		permission |= store.WantContactPhone
	}
	return store.WantWrite{
		Name: req.Name, Description: req.Description, LocationName: req.LocationName,
		GeoLat: req.GeoLat, GeoLng: req.GeoLng, StartsAt: req.StartsAt.UTC(), ExpiresAt: req.ExpiresAt,
		ImageURL: req.ImageURL, ContactPermission: permission, ContactText: req.ContactText,
		MarkerIcon: req.MarkerIcon, MarkerColor: req.MarkerColor, Tags: filter.Tags,
	}, nil
}

func validateWantProfile(write store.WantWrite, user store.User) error {
	if write.ContactPermission&store.WantContactEmail != 0 &&
		(user.Email == "" || strings.HasSuffix(strings.ToLower(user.Email), "@clc.local")) {
		return errors.New("email is not available in your profile")
	}
	if write.ContactPermission&store.WantContactPhone != 0 && strings.TrimSpace(user.Phone) == "" {
		return errors.New("phone is not available in your profile")
	}
	return nil
}

func (a *API) wantsListHandler(c *echo.Context) error {
	var req apiresp.CursorPaginationRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	search, err := parseMarketDiscoverySearch(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	pagination := normalizePaginationWithMax(req, marketMaxPerPage)
	return cursorPaginatedCatalogList(c, a, paginatedCatalogListCallbacks[store.Want]{
		firstPage: func(ctx context.Context, tx pgx.Tx, limit uint64) ([]store.Want, error) {
			rows, err := a.store.ListWants(ctx, tx, search.filter, nil, store.CatalogFirstPage, 0, limit)
			redactWantContacts(rows)
			return rows, err
		},
		nextPage: func(ctx context.Context, tx pgx.Tx, cursor, limit uint64) ([]store.Want, error) {
			rows, err := a.store.ListWants(ctx, tx, search.filter, nil, store.CatalogNextPage, cursor, limit)
			redactWantContacts(rows)
			return rows, err
		},
		prevPage: func(ctx context.Context, tx pgx.Tx, cursor, limit uint64) ([]store.Want, error) {
			rows, err := a.store.ListWants(ctx, tx, search.filter, nil, store.CatalogPrevPage, cursor, limit)
			redactWantContacts(rows)
			return rows, err
		},
	}, pagination, "wants", "Wants", func(w store.Want) uint64 { return w.ID }, catalogListHooks{
		validate: func(tx pgx.Tx) (bool, error) {
			if handled, readErr := a.resolveNearbyCatalog(c, tx, &search); handled || readErr != nil {
				return handled, readErr
			}
			if handled, readErr := a.resolvePoolFilter(c, tx, &search); handled || readErr != nil {
				return handled, readErr
			}
			return a.rejectUnknownTags(c, tx, search.filter.Tags)
		},
		extras: catalogTagFacetExtras(search, pagination, func(tx pgx.Tx) ([]store.TagFacet, error) {
			return a.store.ListWantTagFacets(c.Request().Context(), tx, search.filter, nil)
		}),
		onSuccess: setMarketCacheHeaders,
	})
}

func (a *API) wantDetailHandler(c *echo.Context) error {
	id, err := wantIDParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	want, err := a.store.GetWant(ctx, tx, id, nil)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if want == nil {
		return wantNotFound(c)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	c.Response().Header().Set("Cache-Control", "private, no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Description: "Want", Result: map[string]any{
		"want": publicResource(*want, publicWantContactFields...),
	}})
}

func activeWantListValidation(c *echo.Context, a *API, id uint64) func(pgx.Tx) (bool, error) {
	return func(tx pgx.Tx) (bool, error) {
		want, err := a.store.GetWant(c.Request().Context(), tx, id, nil)
		if err != nil {
			return false, handlePostgresError(c, err)
		}
		if want == nil {
			return true, wantNotFound(c)
		}
		return false, nil
	}
}

func (a *API) wantCreatorVouchersListHandler(c *echo.Context) error {
	return wantCreatorResourceListHandler(c, a, "vouchers", "Want creator vouchers",
		func(v store.WantCreatorVoucher) uint64 { return v.ID }, a.store.ListWantCreatorVouchers)
}

func (a *API) wantCreatorPoolsListHandler(c *echo.Context) error {
	return wantCreatorResourceListHandler(c, a, "pools", "Want creator creditable pools",
		func(p store.WantCreatorPool) uint64 { return p.ID }, a.store.ListWantCreatorPools)
}

func wantCreatorResourceListHandler[T any](
	c *echo.Context,
	a *API,
	resource, description string,
	idOf func(T) uint64,
	list func(context.Context, pgx.Tx, uint64, store.CatalogPage, uint64, uint64) ([]T, error),
) error {
	id, err := wantIDParam(c)
	if err != nil {
		return err
	}
	var req apiresp.CursorPaginationRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	pagination := normalizePagination(req)
	return cursorPaginatedCatalogList(c, a, paginatedCatalogListCallbacks[T]{
		firstPage: func(ctx context.Context, tx pgx.Tx, limit uint64) ([]T, error) {
			return list(ctx, tx, id, store.CatalogFirstPage, 0, limit)
		},
		nextPage: func(ctx context.Context, tx pgx.Tx, cursor, limit uint64) ([]T, error) {
			return list(ctx, tx, id, store.CatalogNextPage, cursor, limit)
		},
		prevPage: func(ctx context.Context, tx pgx.Tx, cursor, limit uint64) ([]T, error) {
			return list(ctx, tx, id, store.CatalogPrevPage, cursor, limit)
		},
	}, pagination, resource, description, idOf, catalogListHooks{
		validate:  activeWantListValidation(c, a, id),
		onSuccess: setMarketCacheHeaders,
	})
}

func (a *API) poolWantsListHandler(c *echo.Context) error {
	return catalogAddressListHandler(c, a, "wants", "Wants in pool %s",
		func(w store.Want) uint64 { return w.ID },
		func(ctx context.Context, tx pgx.Tx, address string, limit uint64) ([]store.Want, error) {
			rows, err := a.store.ListPoolWants(ctx, tx, address, store.CatalogFirstPage, 0, limit)
			redactWantContacts(rows)
			return rows, err
		},
		func(ctx context.Context, tx pgx.Tx, address string, cursor, limit uint64) ([]store.Want, error) {
			rows, err := a.store.ListPoolWants(ctx, tx, address, store.CatalogNextPage, cursor, limit)
			redactWantContacts(rows)
			return rows, err
		},
		func(ctx context.Context, tx pgx.Tx, address string, cursor, limit uint64) ([]store.Want, error) {
			rows, err := a.store.ListPoolWants(ctx, tx, address, store.CatalogPrevPage, cursor, limit)
			redactWantContacts(rows)
			return rows, err
		}, catalogListHooks{onSuccess: setMarketCacheHeaders})
}

func redactWantContacts(wants []store.Want) {
	for index := range wants {
		wants[index].ContactPermission = 0
		wants[index].ContactText = nil
		wants[index].Email = nil
		wants[index].Phone = nil
	}
}

func (a *API) ownedWantsListHandler(c *echo.Context) error {
	var req apiresp.CursorPaginationRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	userID, _, _ := getUserClaims(c)
	pagination := normalizePagination(req)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	wants, err := a.store.ListOwnedWants(ctx, tx, userID, pagination.Cursor, pagination.PerPage)
	if err != nil {
		return handlePostgresError(c, err)
	}
	first, last := uint64(0), uint64(0)
	if len(wants) > 0 {
		first, last = wants[0].ID, wants[len(wants)-1].ID
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Description: "My wants", Result: map[string]any{"wants": wants, "first": first, "last": last}})
}

func (a *API) wantCreateHandler(c *echo.Context) error {
	var req wantWriteRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	write, err := normalizeWantWrite(req)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	userID, _, _ := getUserClaims(c)
	return a.saveWant(c, 0, userID, write, true, false)
}

func (a *API) wantUpdateHandler(c *echo.Context) error {
	id, err := wantIDParam(c)
	if err != nil {
		return err
	}
	var req wantWriteRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	write, err := normalizeWantWrite(req)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	userID, _, _ := getUserClaims(c)
	return a.saveWant(c, id, userID, write, false, false)
}

func (a *API) wantReopenHandler(c *echo.Context) error {
	id, err := wantIDParam(c)
	if err != nil {
		return err
	}
	var req wantWriteRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	write, err := normalizeWantWrite(req)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	userID, _, _ := getUserClaims(c)
	return a.saveWant(c, id, userID, write, false, true)
}

func (a *API) saveWant(c *echo.Context, id, userID uint64, write store.WantWrite, create, reopen bool) error {
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	user, err := a.store.GetUserByID(ctx, tx, userID)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := validateWantProfile(write, user); err != nil {
		return respondCatalogSearchError(c, err)
	}
	tags, handled, tagErr := a.ensureTokenTags(c, tx, write.Tags)
	if handled {
		return tagErr
	}
	write.Tags = tags
	now := time.Now()
	if reopen && write.ExpiresAt != nil && !write.ExpiresAt.After(now) {
		return respondCatalogSearchError(c, errors.New("expiresAt must be in the future when reopening a Want"))
	}
	if create {
		if err := a.store.CheckUserResourceLimit(ctx, tx, userID, "wants"); err != nil {
			return handleResourceLimitError(c, err)
		}
		id, err = a.store.CreateWant(ctx, tx, userID, write)
	} else {
		ownerID, lockErr := a.store.LockWantOwner(ctx, tx, id)
		if errors.Is(lockErr, pgx.ErrNoRows) {
			return wantNotFound(c)
		}
		if lockErr != nil {
			return handlePostgresError(c, lockErr)
		}
		if ownerID != userID {
			return handleForbiddenError(c, "only the Want creator may change it")
		}
		current, readErr := a.store.GetWant(ctx, tx, id, &userID)
		if readErr != nil {
			return handlePostgresError(c, readErr)
		}
		currentlyCounts := current != nil && current.Status == store.WantStatusOpen && (current.ExpiresAt == nil || current.ExpiresAt.After(now))
		nextCounts := reopen || (current != nil && current.Status == store.WantStatusOpen)
		nextCounts = nextCounts && (write.ExpiresAt == nil || write.ExpiresAt.After(now))
		if nextCounts && !currentlyCounts {
			if err := a.store.CheckUserResourceLimit(ctx, tx, userID, "wants"); err != nil {
				return handleResourceLimitError(c, err)
			}
		}
		err = a.store.UpdateWant(ctx, tx, id, write, reopen)
	}
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := a.store.ReplaceWantTags(ctx, tx, id, write.Tags); err != nil {
		return handlePostgresError(c, err)
	}
	want, err := a.store.GetWant(ctx, tx, id, &userID)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	return c.JSON(status, apiresp.OKResponse{Ok: true, Description: "Want saved", Result: map[string]any{"want": want}})
}

func (a *API) wantCloseHandler(c *echo.Context) error  { return a.finishWant(c, false) }
func (a *API) wantDeleteHandler(c *echo.Context) error { return a.finishWant(c, true) }

func (a *API) finishWant(c *echo.Context, remove bool) error {
	id, err := wantIDParam(c)
	if err != nil {
		return err
	}
	userID, _, _ := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	ownerID, err := a.store.LockWantOwner(ctx, tx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return wantNotFound(c)
	}
	if err != nil {
		return handlePostgresError(c, err)
	}
	if ownerID != userID {
		return handleForbiddenError(c, "only the Want creator may change it")
	}
	if remove {
		err = a.store.DeleteWant(ctx, tx, id)
	} else {
		err = a.store.SetWantStatus(ctx, tx, id, store.WantStatusClosed)
	}
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Description: "Want updated"})
}

func wantNotFound(c *echo.Context) error {
	return c.JSON(http.StatusNotFound, apiresp.ErrResponse{Ok: false, ErrCode: apiresp.ErrCodeNotFound, Description: "Want not found"})
}
