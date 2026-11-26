package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

type (
	regionWriteRequest struct {
		Name         string          `json:"name"`
		CountryCode  *string         `json:"countryCode"`
		Active       bool            `json:"active"`
		Revision     int64           `json:"revision"`
		Boundary     json.RawMessage `json:"boundary"`
		PoolApproval bool            `json:"poolApproval"`
	}
)

const (
	maxRegionWriteBytes = 1024 * 1024
)

func regionActor(c *echo.Context) store.RegionActor {
	id, address, _ := getUserClaims(c)
	return store.RegionActor{ID: id, Address: address}
}

func privateRegionResponse(c *echo.Context) {
	c.Response().Header().Set("Cache-Control", "private, no-store")
}

func readRegionBody(c *echo.Context, request any) error {
	body := http.MaxBytesReader(nil, c.Request().Body, maxRegionWriteBytes)
	defer func() { _ = body.Close() }()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(request); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("unexpected content after the request")
		}
		return err
	}
	return nil
}

func invalidRegionRequest(c *echo.Context, message string) error {
	return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
		Ok: false, ErrCode: apiresp.ErrCodeValidationFailed, Description: message,
	})
}

func invalidRegionBody(c *echo.Context, err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return c.JSON(http.StatusRequestEntityTooLarge, apiresp.ErrResponse{
			Ok: false, ErrCode: apiresp.ErrCodePayloadTooLarge, Description: "region request exceeds 1 MiB",
		})
	}
	return invalidRegionRequest(c, "invalid regional request body")
}

func regionResponseError(c *echo.Context, err error) error {
	var overlap *store.RegionOverlapError
	switch {
	case errors.Is(err, store.ErrRegionAuthority):
		return handleForbiddenError(c, err.Error())
	case errors.Is(err, store.ErrRegionBoundary):
		return invalidRegionRequest(c, err.Error())
	case errors.As(err, &overlap),
		errors.Is(err, store.ErrRegionStale), errors.Is(err, store.ErrRegionInactive),
		errors.Is(err, store.ErrRegionAssignee), errors.Is(err, store.ErrRegionConversion),
		errors.Is(err, store.ErrRegionSuperadmin):
		return handleConflictError(c, err.Error())
	default:
		return handlePostgresError(c, err)
	}
}

func (a *API) myRegions(c *echo.Context) error {
	return a.readRegions(c, false)
}

func (a *API) listAdminRegions(c *echo.Context) error {
	return a.readRegions(c, true)
}

func (a *API) readRegions(c *echo.Context, global bool) error {
	privateRegionResponse(c)
	regions, err := a.store.ListRegions(c.Request().Context(), regionActor(c), global)
	if err != nil {
		return regionResponseError(c, err)
	}
	if regions == nil {
		regions = []store.MarketplaceRegion{}
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Result: map[string]any{"regions": regions}})
}

func parseRegionID(c *echo.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	return id, err == nil && id > 0
}

func (a *API) createAdminRegion(c *echo.Context) error {
	return a.writeAdminRegion(c, 0)
}

func (a *API) updateAdminRegion(c *echo.Context) error {
	id, ok := parseRegionID(c)
	if !ok {
		return invalidRegionRequest(c, "invalid region ID")
	}
	return a.writeAdminRegion(c, id)
}

func (a *API) writeAdminRegion(c *echo.Context, id int64) error {
	privateRegionResponse(c)
	var req regionWriteRequest
	if err := readRegionBody(c, &req); err != nil {
		return invalidRegionBody(c, err)
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || utf8.RuneCountInString(req.Name) > 100 || !utf8.ValidString(req.Name) || strings.ContainsRune(req.Name, 0) {
		return invalidRegionRequest(c, "region name must contain 1 to 100 valid characters")
	}
	if (id > 0 && req.Revision < 1) || (id == 0 && req.Revision != 0) {
		return invalidRegionRequest(c, "a current revision is required when updating an existing region")
	}
	if req.PoolApproval {
		return handleConflictError(c, "regional listing approval is not available")
	}
	if req.CountryCode != nil {
		code := strings.ToUpper(strings.TrimSpace(*req.CountryCode))
		if len(req.Boundary) > 0 && string(req.Boundary) != "null" {
			return invalidRegionRequest(c, "country presets use the published country boundary")
		}
		preset, ok, err := lookupRegionCountry(code)
		if err != nil {
			return err
		}
		if !ok || len(preset.Boundary) == 0 {
			return invalidRegionRequest(c, "unknown country or territory code")
		}
		req.CountryCode = &code
		req.Boundary = preset.Boundary
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	region, err := a.store.SaveRegion(ctx, tx, regionActor(c), id, store.RegionWrite{
		Name: req.Name, CountryCode: req.CountryCode, Active: req.Active, Revision: req.Revision, Boundary: req.Boundary,
	})
	if err != nil {
		return regionResponseError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	status := http.StatusOK
	if id == 0 {
		status = http.StatusCreated
	}
	return c.JSON(status, apiresp.OKResponse{Ok: true, Result: map[string]any{"region": region}})
}

func (a *API) setAdminRegionAssignment(c *echo.Context) error {
	privateRegionResponse(c)
	id, ok := parseRegionID(c)
	if !ok {
		return invalidRegionRequest(c, "invalid region ID")
	}
	address, handled, err := adminUserAddress(c)
	if handled || err != nil {
		return err
	}
	var req struct {
		Assigned                *bool `json:"assigned"`
		ConfirmGlobalConversion bool  `json:"confirmGlobalConversion"`
	}
	if err := readRegionBody(c, &req); err != nil {
		return invalidRegionBody(c, err)
	}
	if req.Assigned == nil {
		return invalidRegionRequest(c, "assigned must be true or false")
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	region, err := a.store.SetRegionAssignment(ctx, tx, regionActor(c), id, address, *req.Assigned, req.ConfirmGlobalConversion)
	if err != nil {
		return regionResponseError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Result: map[string]any{"region": region}})
}
