package api

import (
	"net/http"
	"strconv"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

func (a *API) domainResolveHandler(c *echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	host, err := normalizeDomainHostname(c.QueryParam("hostname"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{Ok: false, ErrCode: apiresp.ErrCodeValidationFailed, Description: err.Error()})
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	domain, err := a.store.GetActiveDomainByHostname(ctx, tx, host)
	if err != nil {
		return handleDomainError(c, err)
	}
	entry := store.DomainDirectoryEntry{ID: domain.ID, Slug: domain.Slug, Name: domain.Name, Description: domain.Description, IsGlobal: domain.IsGlobal, Hostname: host}
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Description: "Space resolved", Result: map[string]any{"domain": entry}})
}

func (a *API) domainsListHandler(c *echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	var after uint64
	if raw := c.QueryParam("after"); raw != "" {
		var err error
		after, err = parseDomainID(raw)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{Ok: false, ErrCode: apiresp.ErrCodeValidationFailed, Description: err.Error()})
		}
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	entries, err := a.store.ListActiveDomains(ctx, tx, after, 101)
	if err != nil {
		return err
	}
	var next *uint64
	if len(entries) > 100 {
		entries = entries[:100]
		next = &entries[99].ID
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Description: "Active spaces", Result: map[string]any{"domains": entries, "next": next}})
}

func domainMembershipResult(state store.UserDomains) map[string]any {
	return map[string]any{
		"registrationDomainId": state.RegistrationDomainID,
		"domainRevision":       state.DomainRevision,
		"globalRequired":       state.GlobalRequired,
		"memberships":          state.Memberships,
	}
}

func domainMembershipResponse(c *echo.Context, state store.UserDomains, description string, result map[string]any) error {
	c.Response().Header().Set("ETag", `"`+strconv.FormatUint(state.DomainRevision, 10)+`"`)
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Description: description, Result: result})
}

func (a *API) userDomainsHandler(c *echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	user, err := a.domainUser(c, tx)
	if err != nil {
		return handleDomainError(c, err)
	}
	state, err := a.store.GetUserDomains(ctx, tx, user.ID)
	if err != nil {
		return handleDomainError(c, err)
	}
	return domainMembershipResponse(c, state, "Your spaces", domainMembershipResult(state))
}

func (a *API) domainMembershipHandler(c *echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	id, err := parseDomainID(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{Ok: false, ErrCode: apiresp.ErrCodeValidationFailed, Description: err.Error()})
	}
	revision, err := domainRevision(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{Ok: false, ErrCode: "DOMAIN_REVISION_REQUIRED", Description: err.Error()})
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	space, err := a.resolveDomainContext(c, tx)
	if err != nil {
		return handleDomainError(c, err)
	}
	user, err := a.domainUser(c, tx)
	if err != nil {
		return handleDomainError(c, err)
	}
	var state store.UserDomains
	if c.Request().Method == http.MethodPut {
		if id != space.Domain.ID {
			return handleForbiddenError(c, "join the space selected by the registered Origin")
		}
		state, err = a.store.JoinDomain(ctx, tx, user.ID, id, revision)
	} else {
		// Account management can leave another space, including an archived one.
		state, err = a.store.LeaveDomain(ctx, tx, user.ID, id, revision)
	}
	if err != nil {
		return handleDomainError(c, err)
	}
	result := domainMembershipResult(state)
	if c.Request().Method == http.MethodDelete {
		result["deactivatedDomainId"] = id
		if state.GlobalRequired {
			for _, membership := range state.Memberships {
				if membership.IsGlobal && membership.Status == "active" && membership.Hostname != nil {
					result["continueAt"] = "https://" + *membership.Hostname
				}
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return domainMembershipResponse(c, state, "Space membership updated", result)
}

func (a *API) domainOnboardingHandler(c *echo.Context, profile store.UserProfileUpdate) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	revision, err := domainRevision(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{Ok: false, ErrCode: "DOMAIN_REVISION_REQUIRED", Description: err.Error()})
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	space, err := a.resolveDomainContext(c, tx)
	if err != nil {
		return handleDomainError(c, err)
	}
	user, err := a.domainUser(c, tx)
	if err != nil {
		return handleDomainError(c, err)
	}
	state, err := a.store.CompleteDomainOnboarding(ctx, tx, user.ID, space.Domain.ID, revision, profile)
	if err != nil {
		return handleDomainError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return domainMembershipResponse(c, state, "Space onboarding completed", domainMembershipResult(state))
}
