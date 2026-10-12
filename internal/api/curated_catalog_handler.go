package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

type (
	curatedValidationError string
)

var (
	errCuratedOwner   = errors.New("only the catalog owner may change this catalog")
	errCuratedHandled = errors.New("catalog response already written")
)

func (e curatedValidationError) Error() string { return string(e) }

// All catalog reads and writes are bounded transactions. Public detail is not
// radius-gated; only an explicitly nearby list resolves the saved viewer origin.
func (a *API) curatedResponse(c *echo.Context, run func(pgx.Tx) (map[string]any, error)) error {
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = a.store.SetCatalogStatementTimeout(ctx, tx); err != nil {
		return err
	}
	result, err := run(tx)
	if err != nil {
		if errors.Is(err, errCuratedHandled) {
			return nil
		}
		var validation curatedValidationError
		if errors.As(err, &validation) {
			return respondCatalogSearchError(c, err)
		}
		if errors.Is(err, errCuratedOwner) {
			return handleForbiddenError(c, err.Error())
		}
		return handlePostgresError(c, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Result: result})
}

func curatedID(raw string) (uint64, error) {
	id, err := strconv.ParseUint(raw, 10, 31)
	if err != nil || id == 0 {
		return 0, curatedValidationError("id must be a positive integer")
	}
	return id, nil
}
func curatedPageSize(c *echo.Context) (uint64, error) {
	raw := c.QueryParam("perPage")
	if raw == "" {
		return 20, nil
	}
	size, err := strconv.ParseUint(raw, 10, 8)
	if err != nil || size == 0 || size > 100 {
		return 0, curatedValidationError("perPage must be between 1 and 100")
	}
	return size, nil
}
func curatedCursor(raw, origin string) (store.CuratedCatalogCursor, error) {
	var cursor store.CuratedCatalogCursor
	if raw == "" {
		return cursor, nil
	}
	if len(raw) > 1024 {
		return cursor, curatedValidationError("invalid catalog cursor")
	}
	bytes, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return cursor, curatedValidationError("invalid catalog cursor")
	}
	err = json.Unmarshal(bytes, &cursor)
	if err != nil || cursor.ID == 0 || cursor.ID > math.MaxInt32 || cursor.Origin != origin ||
		math.IsNaN(cursor.Distance) || math.IsInf(cursor.Distance, 0) || cursor.Distance < 0 {
		return cursor, curatedValidationError("catalog cursor does not match this list and location; restart pagination")
	}
	return cursor, nil
}
func encodeCuratedCursor(cursor store.CuratedCatalogCursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}
func curatedOrigin(user uint64, location *store.CatalogLocation, term string) string {
	if location == nil && term == "" {
		return "public"
	}
	lat, lng := 0.0, 0.0
	if location != nil {
		lat, lng = location.Lat, location.Lng
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%d:%t:%.17g:%.17g:%s", user, location != nil, lat, lng, term))
	return hex.EncodeToString(sum[:])
}
func validateCuratedWrite(w *store.CuratedCatalogWrite) error {
	w.Name = strings.TrimSpace(w.Name)
	w.Description = strings.TrimSpace(w.Description)
	w.LocationName = strings.TrimSpace(w.LocationName)
	if utf8.RuneCountInString(w.Name) < 1 || utf8.RuneCountInString(w.Name) > 120 {
		return curatedValidationError("name must contain 1 to 120 characters")
	}
	if utf8.RuneCountInString(w.Description) > 500 {
		return curatedValidationError("description must contain at most 500 characters")
	}
	if utf8.RuneCountInString(w.LocationName) < 1 || utf8.RuneCountInString(w.LocationName) > 200 {
		return curatedValidationError("locationName must contain 1 to 200 characters")
	}
	if len(w.Tags) > store.CatalogMaxTags {
		return store.ErrCatalogTooManyTags
	}
	if _, err := store.NewCatalogLocation(w.GeoLat, w.GeoLng); err != nil {
		return curatedValidationError("valid geoLat and geoLng are required")
	}
	if w.ImageURL != nil && strings.TrimSpace(*w.ImageURL) == "" {
		w.ImageURL = nil
	}
	if w.ImageURL != nil {
		parsed, err := url.Parse(*w.ImageURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || strings.ContainsAny(*w.ImageURL, " \t\r\n") {
			return curatedValidationError("imageUrl must be an absolute http or https URL")
		}
	}
	return nil
}
func (a *API) requireCuratedOwner(c *echo.Context, tx pgx.Tx, id uint64) error {
	owner, err := a.store.LockCuratedCatalog(c.Request().Context(), tx, id)
	if err != nil {
		return err
	}
	caller, _, _ := getUserClaims(c)
	if caller == 0 || caller != owner {
		return errCuratedOwner
	}
	return nil
}
func (a *API) curatedCatalogListHandler(c *echo.Context) error {
	size, err := curatedPageSize(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	nearby := c.QueryParam("nearby")
	if nearby != "" && nearby != "true" && nearby != "false" {
		return respondCatalogSearchError(c, errors.New("nearby must be true or false"))
	}
	search, err := parseMarketDiscoverySearch(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	return a.curatedResponse(c, func(tx pgx.Tx) (map[string]any, error) {
		ctx := c.Request().Context()
		caller, address, _ := getUserClaims(c)
		var location *store.CatalogLocation
		if nearby == "true" {
			user, readErr := a.store.GetUserByAddress(ctx, tx, address)
			if readErr != nil {
				return nil, readErr
			}
			location, readErr = store.NewCatalogLocation(user.GeoLat, user.GeoLng)
			if readErr != nil {
				return nil, curatedValidationError(readErr.Error())
			}
		}
		if handled, readErr := a.rejectUnknownTags(c, tx, search.filter.Tags); handled || readErr != nil {
			if readErr != nil {
				return nil, readErr
			}
			return nil, errCuratedHandled
		}
		if handled, readErr := a.resolvePoolFilter(c, tx, &search); handled || readErr != nil {
			if readErr != nil {
				return nil, readErr
			}
			return nil, errCuratedHandled
		}
		origin := curatedOrigin(caller, location, fmt.Sprintf("%s:%v:%d:%v", search.filter.Term, search.filter.Tags, search.filter.TagMatch, search.filter.PoolIDs))
		cursor, readErr := curatedCursor(c.QueryParam("cursor"), origin)
		if readErr != nil {
			return nil, readErr
		}
		rows, readErr := a.store.ListCuratedCatalogs(ctx, tx, 0, search.filter, location, cursor, size+1)
		if readErr != nil {
			return nil, readErr
		}
		var next *string
		if uint64(len(rows)) > size {
			rows = rows[:size]
			last := rows[len(rows)-1]
			distance := 0.0
			if last.DistanceMeters != nil {
				distance = *last.DistanceMeters
			}
			token := encodeCuratedCursor(store.CuratedCatalogCursor{ID: last.ID, Distance: distance, Origin: origin})
			next = &token
		}
		result := map[string]any{"catalogs": rows, "nextCursor": next}
		if search.facets && cursor.ID == 0 {
			facets, facetErr := a.store.ListCuratedCatalogTagFacets(ctx, tx, 0, search.filter, location)
			if facetErr != nil {
				return nil, facetErr
			}
			result["tagFacets"] = facets
		}
		return result, nil
	})
}
func (a *API) myCuratedCatalogsHandler(c *echo.Context) error {
	return a.curatedResponse(c, func(tx pgx.Tx) (map[string]any, error) {
		owner, _, _ := getUserClaims(c)
		rows, err := a.store.ListCuratedCatalogs(c.Request().Context(), tx, owner, store.CatalogFilter{}, nil, store.CuratedCatalogCursor{}, 100)
		return map[string]any{"catalogs": rows, "nextCursor": nil}, err
	})
}
func (a *API) curatedCatalogDetailHandler(c *echo.Context) error {
	id, err := curatedID(c.Param("id"))
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	return a.curatedResponse(c, func(tx pgx.Tx) (map[string]any, error) {
		row, err := a.store.GetCuratedCatalog(c.Request().Context(), tx, id)
		return map[string]any{"catalog": row}, err
	})
}
func (a *API) curatedCatalogSaveHandler(c *echo.Context) error {
	var id uint64
	var err error
	if c.Request().Method == http.MethodPut {
		id, err = curatedID(c.Param("id"))
		if err != nil {
			return respondCatalogSearchError(c, err)
		}
	}
	var write store.CuratedCatalogWrite
	if err = c.Bind(&write); err != nil {
		return handleBindError(c, err)
	}
	if err = validateCuratedWrite(&write); err != nil {
		return respondCatalogSearchError(c, err)
	}
	return a.curatedResponse(c, func(tx pgx.Tx) (map[string]any, error) {
		ctx := c.Request().Context()
		if id != 0 {
			if err = a.requireCuratedOwner(c, tx, id); err != nil {
				return nil, err
			}
		}
		tags, handled, tagErr := a.ensureTokenTags(c, tx, write.Tags)
		if handled {
			if tagErr != nil {
				return nil, tagErr
			}
			return nil, errCuratedHandled
		}
		if id == 0 {
			owner, _, _ := getUserClaims(c)
			id, err = a.store.CreateCuratedCatalog(ctx, tx, owner, write)
		} else {
			err = a.store.UpdateCuratedCatalog(ctx, tx, id, write)
		}
		if err != nil {
			return nil, err
		}
		if err = a.store.ReplaceCuratedCatalogTags(ctx, tx, id, tags); err != nil {
			return nil, err
		}
		row, readErr := a.store.GetCuratedCatalog(ctx, tx, id)
		return map[string]any{"catalog": row}, readErr
	})
}
func (a *API) curatedMembersHandler(c *echo.Context) error {
	id, err := curatedID(c.Param("id"))
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	size, err := curatedPageSize(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	voucher := strings.HasSuffix(c.Path(), "/vouchers")
	origin := fmt.Sprintf("members:%d:%t", id, voucher)
	cursor, err := curatedCursor(c.QueryParam("cursor"), origin)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	return a.curatedResponse(c, func(tx pgx.Tx) (map[string]any, error) {
		ctx := c.Request().Context()
		if _, readErr := a.store.GetCuratedCatalog(ctx, tx, id); readErr != nil {
			return nil, readErr
		}
		rows, readErr := a.store.ListCuratedMembers(ctx, tx, id, voucher, cursor.ID, size+1)
		if readErr != nil {
			return nil, readErr
		}
		var next *string
		if uint64(len(rows)) > size {
			rows = rows[:size]
			token := encodeCuratedCursor(store.CuratedCatalogCursor{ID: rows[len(rows)-1].ID, Origin: origin})
			next = &token
		}
		return map[string]any{"items": rows, "nextCursor": next}, nil
	})
}
func (a *API) curatedMembershipHandler(c *echo.Context) error {
	id, err := curatedID(c.Param("id"))
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	voucher := c.Param("voucherId") != ""
	raw := c.Param("poolId")
	if voucher {
		raw = c.Param("voucherId")
	}
	if c.Request().Method == http.MethodGet {
		kind := c.QueryParam("kind")
		if kind != "pool" && kind != "voucher" {
			return respondCatalogSearchError(c, errors.New("kind must be pool or voucher"))
		}
		voucher = kind == "voucher"
		raw = c.QueryParam("resourceId")
	}
	resource, err := curatedID(raw)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	return a.curatedResponse(c, func(tx pgx.Tx) (map[string]any, error) {
		if ownerErr := a.requireCuratedOwner(c, tx, id); ownerErr != nil {
			return nil, ownerErr
		}
		ctx := c.Request().Context()
		if c.Request().Method != http.MethodGet {
			if writeErr := a.store.SetCuratedMembership(ctx, tx, id, voucher, resource, c.Request().Method == http.MethodPut); writeErr != nil {
				return nil, writeErr
			}
		}
		added, readErr := a.store.CuratedMembership(ctx, tx, id, voucher, resource)
		return map[string]any{"added": added}, readErr
	})
}
