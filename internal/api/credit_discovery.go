package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/router"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

const (
	// noReachablePool stands in for an empty reachable set. Pool ids start at 1,
	// so a filter on it matches nothing, where an empty filter would match
	// everything.
	noReachablePool = 0
)

var (
	errCreditDepthWithPools = errors.New("creditDepth replaces creditablePools; send one or the other")
)

type (
	// creditDepthFilter is the server-resolved Credit filter: the
	// pools the signed-in wallet can reach at creditDepth hops, so eligibility
	// applies before paging, counts, facets and clustering.
	creditDepthFilter struct {
		poolIDs []uint64
		wallet  string
		depth   int
		digest  string
	}
)

// resolveCreditDepth reads creditDepth: max, or 1 to the router's maximum.
// Absent, it returns nil. Present, it needs a user session and the router;
// the answer is private to the wallet, so the response must not be cached
// publicly. When handled is true the response has been written and the
// handler returns err.
func (a *API) resolveCreditDepth(c *echo.Context, creditablePools bool) (filter *creditDepthFilter, handled bool, err error) {
	raw := strings.TrimSpace(c.QueryParam("creditDepth"))
	if raw == "" {
		return nil, false, nil
	}
	if creditablePools {
		return nil, true, c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok: false, ErrCode: apiresp.ErrCodeValidationFailed, Description: errCreditDepthWithPools.Error(),
		})
	}
	maxHops := router.DefaultMaxHops
	if a.creditRouter != nil {
		maxHops = a.creditRouter.Config().MaxHops
	}
	depth, err := strconv.Atoi(raw)
	if raw == "max" {
		depth, err = maxHops, nil
	}
	if err != nil || depth < 1 || depth > maxHops {
		return nil, true, c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok: false, ErrCode: apiresp.ErrCodeValidationFailed,
			Description: "creditDepth must be max or between 1 and " + strconv.Itoa(maxHops),
		})
	}
	c.Response().Header().Set("Cache-Control", "private, no-store")
	if !a.authenticateCaller(c) {
		return nil, true, handleJWTAuthError(c, "creditDepth needs a signed-in wallet")
	}
	if caller, _ := c.Get("callerType").(string); caller != "user" {
		return nil, true, handleForbiddenError(c, "creditDepth needs a user session")
	}
	wallet, ok := sessionWallet(c)
	if !ok {
		return nil, true, handleForbiddenError(c, "creditDepth needs a user session")
	}
	if a.creditRouter == nil {
		return nil, true, respondRouterError(c, router.ErrUnavailable)
	}
	ids, digest, err := a.creditRouter.ReachablePoolIDs(c.Request().Context(), wallet, depth)
	if err != nil {
		return nil, true, respondRouterError(c, err)
	}
	if len(ids) == 0 {
		ids = []uint64{noReachablePool}
	}
	return &creditDepthFilter{poolIDs: ids, wallet: wallet.Hex(), depth: depth, digest: digest.Hex()}, false, nil
}
