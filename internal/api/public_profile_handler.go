package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

type (
	publicProfileSections struct {
		Vouchers bool `json:"vouchers"`
		Offers   bool `json:"offers"`
		Wants    bool `json:"wants"`
		Pools    bool `json:"pools"`
		Posts    bool `json:"posts"`
		Catalogs bool `json:"catalogs"`
		Activity bool `json:"activity"`
	}

	publicProfileResponse struct {
		Address           string                `json:"address"`
		Username          *string               `json:"username"`
		AvatarURL         *string               `json:"avatarUrl,omitempty"`
		Name              *string               `json:"name,omitempty"`
		Bio               *string               `json:"bio,omitempty"`
		Website           *string               `json:"website,omitempty"`
		Country           *string               `json:"country,omitempty"`
		PreferredCurrency *string               `json:"preferredCurrency,omitempty"`
		Sections          publicProfileSections `json:"sections"`
		HasPrivateDetails bool                  `json:"hasPrivateDetails"`
	}

	publicProfileDetails struct {
		Email        *string  `json:"email,omitempty"`
		Phone        *string  `json:"phone,omitempty"`
		LocationName *string  `json:"locationName,omitempty"`
		GeoLat       *float64 `json:"geoLat,omitempty"`
		GeoLng       *float64 `json:"geoLng,omitempty"`
	}
)

func publicProfileNoStoreMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			c.Response().Header().Set("Cache-Control", "no-store")
			return next(c)
		}
	}
}

func visibleProfileEmail(profile store.PublicProfile) *string {
	if !profile.ProfileVisibility.Email || profile.Email == nil || *profile.Email == "" || strings.HasSuffix(strings.ToLower(*profile.Email), "@clc.local") {
		return nil
	}
	return profile.Email
}

func profileHasPrivateDetails(profile store.PublicProfile) bool {
	return visibleProfileEmail(profile) != nil ||
		(profile.ProfileVisibility.Phone && profile.Phone != nil && strings.TrimSpace(*profile.Phone) != "") ||
		(profile.ProfileVisibility.LocationName && profile.LocationName != nil && strings.TrimSpace(*profile.LocationName) != "") ||
		(profile.PreciseLocation && profile.GeoLat != nil && profile.GeoLng != nil)
}

func projectPublicProfile(profile store.PublicProfile) publicProfileResponse {
	result := publicProfileResponse{
		Address:  profile.Address,
		Username: profile.Username,
		Sections: publicProfileSections{
			Vouchers: profile.Vouchers, Offers: profile.Offers, Wants: profile.Wants,
			Pools: profile.Pools, Posts: profile.Posts, Catalogs: profile.Catalogs,
		},
		HasPrivateDetails: profileHasPrivateDetails(profile),
	}
	if profile.Avatar {
		result.AvatarURL = profile.AvatarURL
	}
	if profile.ProfileVisibility.Name && strings.TrimSpace(profile.Name) != "" {
		result.Name = &profile.Name
	}
	if profile.ProfileVisibility.Bio {
		result.Bio = profile.Bio
	}
	if profile.ProfileVisibility.Website {
		result.Website = profile.Website
	}
	if profile.ProfileVisibility.Country && strings.TrimSpace(profile.Country) != "" {
		result.Country = &profile.Country
	}
	if profile.ProfileVisibility.PreferredCurrency {
		result.PreferredCurrency = profile.PreferredCurrency
	}
	return result
}

func projectPublicProfileDetails(profile store.PublicProfile) publicProfileDetails {
	result := publicProfileDetails{}
	result.Email = visibleProfileEmail(profile)
	if profile.ProfileVisibility.Phone && profile.Phone != nil && strings.TrimSpace(*profile.Phone) != "" {
		result.Phone = profile.Phone
	}
	if profile.ProfileVisibility.LocationName {
		result.LocationName = profile.LocationName
	}
	if profile.PreciseLocation && profile.GeoLat != nil && profile.GeoLng != nil {
		result.GeoLat = profile.GeoLat
		result.GeoLng = profile.GeoLng
	}
	return result
}

func (a *API) loadPublicProfile(c *echo.Context, address string) (store.PublicProfile, error) {
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return store.PublicProfile{}, err
	}
	defer tx.Rollback(ctx)
	profile, err := a.store.GetPublicProfile(ctx, tx, address)
	if err != nil {
		return store.PublicProfile{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.PublicProfile{}, err
	}
	return profile, nil
}

func publicProfileNotFound(c *echo.Context) error {
	return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
		Ok: false, ErrCode: apiresp.ErrCodeNotFound, Description: "public profile not found",
	})
}

func bindProfileAddress(c *echo.Context) (string, error) {
	req := apiresp.AccountAddressParam{Address: c.Param("address")}
	if err := c.Validate(req); err != nil {
		return "", err
	}
	return req.Address, nil
}

func (a *API) publicProfileHandler(c *echo.Context) error {
	address, err := bindProfileAddress(c)
	if err != nil {
		return a.handleValidateError(c, err)
	}
	profile, err := a.loadPublicProfile(c, address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return publicProfileNotFound(c)
		}
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Description: "Public profile",
		Result: map[string]any{"profile": projectPublicProfile(profile)},
	})
}

func (a *API) publicProfileDetailsHandler(c *echo.Context) error {
	c.Response().Header().Set("Cache-Control", "private, no-store")
	address, err := bindProfileAddress(c)
	if err != nil {
		return a.handleValidateError(c, err)
	}
	profile, err := a.loadPublicProfile(c, address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return publicProfileNotFound(c)
		}
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Description: "Selected profile details",
		Result: map[string]any{"details": projectPublicProfileDetails(profile)},
	})
}

func profileSectionEnabled(profile store.PublicProfile, section string) bool {
	switch section {
	case "vouchers":
		return profile.Vouchers
	case "offers":
		return profile.Offers
	case "wants":
		return profile.Wants
	case "pools":
		return profile.Pools
	case "posts":
		return profile.Posts
	case "catalogs":
		return profile.Catalogs
	default:
		return false
	}
}

func (a *API) publicProfileResourcesHandler(c *echo.Context, section string) error {
	address, err := bindProfileAddress(c)
	if err != nil {
		return a.handleValidateError(c, err)
	}
	profile, err := a.loadPublicProfile(c, address)
	if err != nil || !profileSectionEnabled(profile, section) {
		if err == nil || errors.Is(err, pgx.ErrNoRows) {
			return publicProfileNotFound(c)
		}
		return handlePostgresError(c, err)
	}
	request := apiresp.CursorPaginationRequest{}
	if err := c.Bind(&request); err != nil {
		return handleBindError(c, err)
	}
	page := normalizePaginationWithMax(request, 50)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	resources, err := a.store.ListPublicProfileResources(ctx, tx, address, section, page.Cursor, page.PerPage+1)
	if err != nil {
		if errors.Is(err, store.ErrUnsupportedProfileSection) || errors.Is(err, pgx.ErrNoRows) {
			return publicProfileNotFound(c)
		}
		return handlePostgresError(c, err)
	}
	var nextCursor *string
	if uint64(len(resources)) > page.PerPage {
		value := strconv.FormatUint(resources[page.PerPage-1].ID, 10)
		nextCursor = &value
		resources = resources[:page.PerPage]
	}
	if resources == nil {
		resources = []store.PublicProfileResource{}
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Description: "Public profile " + section,
		Result: map[string]any{"items": resources, "nextCursor": nextCursor},
	})
}

func (a *API) publicProfileVouchersHandler(c *echo.Context) error {
	return a.publicProfileResourcesHandler(c, "vouchers")
}

func (a *API) publicProfileOffersHandler(c *echo.Context) error {
	return a.publicProfileResourcesHandler(c, "offers")
}

func (a *API) publicProfileWantsHandler(c *echo.Context) error {
	return a.publicProfileResourcesHandler(c, "wants")
}

func (a *API) publicProfilePoolsHandler(c *echo.Context) error {
	return a.publicProfileResourcesHandler(c, "pools")
}

func (a *API) publicProfilePostsHandler(c *echo.Context) error {
	return a.publicProfileResourcesHandler(c, "posts")
}

func (a *API) publicProfileCatalogsHandler(c *echo.Context) error {
	return a.publicProfileResourcesHandler(c, "catalogs")
}

func (a *API) publicResourceProfilesHandler(c *echo.Context) error {
	kind := strings.ToLower(strings.TrimSpace(c.Param("kind")))
	switch kind {
	case "voucher", "offer", "want", "pool", "post", "catalog":
	default:
		return publicProfileNotFound(c)
	}
	resourceID := strings.TrimSpace(c.Param("id"))
	if resourceID == "" || len(resourceID) > 128 {
		return publicProfileNotFound(c)
	}
	address := ""
	numericID := int64(0)
	if kind == "voucher" || kind == "pool" {
		if !common.IsHexAddress(resourceID) {
			return publicProfileNotFound(c)
		}
		address = common.HexToAddress(resourceID).Hex()
	} else {
		parsedID, parseErr := strconv.ParseInt(resourceID, 10, 64)
		if parseErr != nil || parsedID <= 0 {
			return publicProfileNotFound(c)
		}
		numericID = parsedID
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	profiles, err := a.store.ResolvePublicResourceProfiles(ctx, tx, kind, address, numericID, a.chainID)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if profiles == nil {
		profiles = []store.PublicProfileReference{}
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Description: "Profiles for resource",
		Result: map[string]any{"profiles": profiles},
	})
}
