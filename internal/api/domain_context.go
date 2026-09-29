package api

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"golang.org/x/net/idna"
)

type (
	// domainContext identifies the visited space. It grants no membership or role.
	domainContext struct {
		Domain   store.Domain
		Hostname string
	}
)

const (
	domainIDHeader = "X-CLC-Domain-ID"
)

var (
	domainHostnamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
)

func normalizeDomainHostname(raw string) (string, error) {
	host, err := idna.Lookup.ToASCII(strings.TrimSuffix(strings.ToLower(raw), "."))
	if err != nil || len(host) > 253 || !domainHostnamePattern.MatchString(host) || net.ParseIP(host) != nil {
		return "", errors.New("a DNS hostname without scheme, port, or path is required")
	}
	return host, nil
}

// Only HTTPS on its standard port is a registered browser origin. No forwarded
// header or API Host fallback is trusted; ingress attestation is not configured.
func domainOriginHostname(origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(origin, "#") || u.Opaque != "" || (u.Port() != "" && u.Port() != "443") || strings.HasSuffix(u.Host, ":") {
		return "", errors.New("a registered HTTPS Origin is required")
	}
	return normalizeDomainHostname(u.Hostname())
}

func parseDomainID(raw string) (uint64, error) {
	id, err := strconv.ParseUint(raw, 10, 31)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != raw {
		return 0, errors.New("a positive domain ID is required")
	}
	return id, nil
}

func domainRevision(c *echo.Context) (uint64, error) {
	values := c.Request().Header.Values("If-Match")
	if len(values) != 1 || len(values[0]) < 3 || values[0][0] != '"' || values[0][len(values[0])-1] != '"' {
		return 0, errors.New("If-Match must contain one quoted domain revision, for example \"0\"")
	}
	raw := values[0][1 : len(values[0])-1]
	revision, err := strconv.ParseUint(raw, 10, 63)
	if err != nil || strconv.FormatUint(revision, 10) != raw {
		return 0, errors.New("If-Match must contain one quoted domain revision")
	}
	return revision, nil
}

func (a *API) resolveDomainContext(c *echo.Context, tx pgx.Tx) (domainContext, error) {
	var result domainContext
	if len(c.Request().Header.Values(domainIDHeader)) != 1 || len(c.Request().Header.Values("Origin")) != 1 {
		return result, echo.NewHTTPError(http.StatusBadRequest, "one X-CLC-Domain-ID and registered Origin are required")
	}
	id, err := parseDomainID(c.Request().Header.Get(domainIDHeader))
	if err != nil {
		return result, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	host, err := domainOriginHostname(c.Request().Header.Get("Origin"))
	if err != nil {
		return result, echo.NewHTTPError(http.StatusForbidden, err.Error())
	}
	domain, err := a.store.GetActiveDomainByHostname(c.Request().Context(), tx, host)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, echo.NewHTTPError(http.StatusForbidden, "Origin is not an active verified space")
	}
	if err != nil {
		return result, err
	}
	if domain.ID != id {
		return result, echo.NewHTTPError(http.StatusForbidden, "Origin does not match the selected space")
	}
	return domainContext{Domain: domain, Hostname: host}, nil
}

// Reload account status instead of treating a long-lived JWT as approval.
func (a *API) domainUser(c *echo.Context, tx pgx.Tx) (store.User, error) {
	id, address, _ := getUserClaims(c)
	user, err := a.store.GetUserByID(c.Request().Context(), tx, id)
	if err != nil {
		return user, err
	}
	if !strings.EqualFold(user.EthereumAddress, address) || user.Status != "APPROVED" {
		return user, store.ErrDomainAccountRestricted
	}
	return user, nil
}

func handleDomainError(c *echo.Context, err error) error {
	status := http.StatusConflict
	var code string
	switch {
	case errors.Is(err, store.ErrDomainRevisionConflict):
		code = "DOMAIN_REVISION_CONFLICT"
	case errors.Is(err, store.ErrGlobalDomainRequired):
		code = "GLOBAL_DOMAIN_REQUIRED"
	case errors.Is(err, store.ErrDomainOnboardingRequired):
		code = "DOMAIN_ONBOARDING_REQUIRED"
	case errors.Is(err, store.ErrDomainRegistrationConflict):
		code = "DOMAIN_REGISTRATION_CONFLICT"
	case errors.Is(err, store.ErrDomainMembershipSuspended), errors.Is(err, store.ErrDomainAccountRestricted):
		status, code = http.StatusForbidden, apiresp.ErrCodeForbidden
	case errors.Is(err, store.ErrDomainFallbackUnavailable), errors.Is(err, store.ErrDomainRepairRequired):
		status, code = http.StatusServiceUnavailable, "DOMAIN_FALLBACK_UNAVAILABLE"
	case errors.Is(err, store.ErrDomainUnavailable), errors.Is(err, pgx.ErrNoRows):
		status, code = http.StatusNotFound, "DOMAIN_UNAVAILABLE"
	default:
		var httpErr *echo.HTTPError
		if errors.As(err, &httpErr) {
			return c.JSON(httpErr.StatusCode(), apiresp.ErrResponse{Ok: false, ErrCode: "DOMAIN_CONTEXT_INVALID", Description: httpErr.Message})
		}
		if conflict, ok := userIdentityConflict(err); ok {
			return c.JSON(http.StatusConflict, conflict)
		}
		return err
	}
	return c.JSON(status, apiresp.ErrResponse{Ok: false, ErrCode: code, Description: err.Error()})
}
