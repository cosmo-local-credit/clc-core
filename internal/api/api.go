package api

import (
	"context"
	"crypto"
	"log/slog"
	"net/http"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/relayer"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/util"
	"github.com/go-playground/validator/v10"
	"github.com/grassrootseconomics/ethutils"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/riverqueue/river"
)

type (
	APIOpts struct {
		ListenAddress   string
		Build           string
		Prod            bool
		CORS            []string
		VerifyingKey    crypto.PublicKey
		SigningKey      crypto.PrivateKey
		Store           store.Store
		FederationStore store.FederationStore
		Logg            *slog.Logger
		ChainProvider   *ethutils.Provider
		QueueClient     *river.Client[pgx.Tx]
		ChainID         int64
		Relayer         *relayer.Relayer
	}

	API struct {
		listenAddress   string
		build           string
		prod            bool
		signingKey      crypto.PrivateKey
		verifyingKey    crypto.PublicKey
		siweNonces      *util.SIWENonceStore
		store           store.Store
		federationStore store.FederationStore
		logg            *slog.Logger
		chainProvider   *ethutils.Provider
		server          *http.Server
		queueClient     *river.Client[pgx.Tx]
		chainID         int64
		relayer         *relayer.Relayer
	}
)

const (
	apiVersion  = "/api/v2"
	maxBodySize = 1 << 20 // 1MB
)

func New(o APIOpts) *API {
	siweNonces := util.NewSIWENonceStore()
	siweNonces.StartCleanup(time.Minute)

	a := &API{
		prod:            o.Prod,
		build:           o.Build,
		signingKey:      o.SigningKey,
		verifyingKey:    o.VerifyingKey,
		siweNonces:      siweNonces,
		listenAddress:   o.ListenAddress,
		logg:            o.Logg,
		store:           o.Store,
		federationStore: o.FederationStore,
		chainProvider:   o.ChainProvider,
		queueClient:     o.QueueClient,
		chainID:         o.ChainID,
		relayer:         o.Relayer,
	}

	customValidator := validator.New(validator.WithRequiredStructEnabled())
	router := echo.New()
	router.Validator = &Validator{
		ValidatorProvider: customValidator,
	}
	router.HTTPErrorHandler = a.customHTTPErrorHandler

	corsConfig := newCORSConfig(o.CORS)

	router.Use(middleware.Recover())
	router.Use(middleware.BodyLimit(maxBodySize))
	router.Use(middleware.ContextTimeout(util.SLATimeout))
	router.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogStatus:   true,
		LogURI:      true,
		HandleError: true,
		LogValuesFunc: func(c *echo.Context, v middleware.RequestLoggerValues) error {
			errMsg := ""
			if v.Error != nil {
				errMsg = v.Error.Error()
			}

			switch {
			case v.Status >= http.StatusInternalServerError:
				o.Logg.LogAttrs(context.Background(), slog.LevelError, http.StatusText(v.Status),
					slog.String("uri", v.URI),
					slog.Int("status", v.Status),
					slog.String("err", errMsg),
				)
			case v.Status == http.StatusBadRequest || v.Status == http.StatusForbidden:
				o.Logg.LogAttrs(context.Background(), slog.LevelWarn, http.StatusText(v.Status),
					slog.String("uri", v.URI),
					slog.Int("status", v.Status),
					slog.String("err", errMsg),
				)
			default:
				o.Logg.LogAttrs(context.Background(), slog.LevelInfo, http.StatusText(v.Status),
					slog.String("uri", v.URI),
					slog.Int("status", v.Status),
				)
			}
			return nil
		},
	}))
	router.Use(middleware.CORSWithConfig(corsConfig))

	// Public auth routes (no JWT required)
	authGroup := router.Group("/auth")
	authGroup.POST("/nonce", a.nonceHandler)
	authGroup.POST("/signup", a.signupHandler)
	authGroup.POST("/signin", a.signinHandler)
	authGroup.POST("/logout", a.logoutHandler)
	authGroup.GET("/session", a.sessionHandler)

	// SIWE unified verify (public — returns JWT for approved users)
	siweGroup := router.Group("/auth/siwe")
	siweGroup.POST("/nonce", a.nonceHandler)
	siweGroup.POST("/verify", a.siweVerifyHandler)

	// Authenticated profile routes
	profileAuthGroup := router.Group("/auth", a.flexibleAuthMiddleware(), a.requireUserAuth())
	profileAuthGroup.GET("/profile", a.profileGetHandler)
	profileAuthGroup.POST("/profile", a.profileUpdateHandler)

	// Protected routes — user JWT cookie or service JWT header/cookie required
	apiGroup := router.Group(apiVersion)
	apiGroup.Use(a.flexibleAuthMiddleware())

	apiGroup.POST("/contracts/token", a.deployTokenHandler)
	apiGroup.POST("/contracts/pool", a.deployPoolHandler)
	apiGroup.POST("/contracts/version", a.contractsVersionHandler)
	apiGroup.POST("/contracts/upgrade/token", a.upgradeTokenHandler)
	apiGroup.POST("/contracts/upgrade/pool", a.upgradePoolHandler)

	apiGroup.GET("/token-tags", a.tokenTagsHandler)
	apiGroup.GET("/system", a.systemHandler)
	apiGroup.GET("/otx/track/:trackingId", a.trackOTXHandler)
	apiGroup.GET("/otx/track/:trackingId/contract", a.trackContractHandler)
	apiGroup.GET("/account/otx/:address", a.accountOTXHandler)
	apiGroup.GET("/account/status/:address", a.accountStatusHandler)

	// User-only routes
	userGroup := apiGroup.Group("/users", a.requireUserAuth())
	userGroup.GET("/me", a.userMeHandler)
	userGroup.GET("/me/tokens", a.userTokensHandler)
	userGroup.GET("/me/tokens/:address", a.userTokenByAddressHandler)
	userGroup.GET("/me/pools", a.userPoolsHandler)
	userGroup.GET("/me/pools/:address", a.userPoolByAddressHandler)
	userGroup.GET("/me/deployments", a.userDeploymentsHandler)

	// Superadmin-only routes
	adminGroup := apiGroup.Group("/admin", a.requireSuperAdmin())
	adminGroup.GET("/users", a.adminListUsersHandler)
	adminGroup.PUT("/users/:address/approve", a.adminApproveUserHandler)
	adminGroup.PUT("/users/:address/reject", a.adminRejectUserHandler)
	adminGroup.PUT("/users/:address/limits", a.adminUpdateUserLimitsHandler)
	adminGroup.GET("/tokens", a.adminListTokensHandler)
	adminGroup.GET("/pools", a.adminListPoolsHandler)
	adminGroup.GET("/deployments", a.adminListDeploymentsHandler)

	// Federation data routes — only if federation store is available
	if a.federationStore != nil {
		fedGroup := apiGroup.Group("/federation")
		fedGroup.GET("/system", a.federationSystemHandler)
	}

	// Relayer (EIP-7702 / Calibur) routes. Mounted on a fresh group outside
	// flexibleAuthMiddleware because first-time WebAuthn registration
	// happens pre-JWT.
	relayerGroup := router.Group(apiVersion + "/relayer")
	relayerGroup.GET("/health", a.relayerHealthHandler)
	relayerGroup.GET("/nonce/:address", a.relayerEOANonceHandler)
	relayerGroup.GET("/calibur-nonce/:address", a.caliburNonceHandler)
	relayerGroup.GET("/calibur-nonce/:address/:keyHash", a.caliburNonceHandler)
	relayerGroup.GET("/track/:trackingId", a.trackRelayerOTXHandler)
	relayerGroup.POST("/delegate", a.delegateHandler)
	relayerGroup.POST("/relay", a.relayHandler)
	relayerGroup.GET("/webauthn/challenge", a.webAuthnChallengeHandler)
	relayerGroup.POST("/webauthn/recover", a.webAuthnRecoverHandler)

	a.server = &http.Server{
		Addr:    o.ListenAddress,
		Handler: router,
	}

	// Log all registered routes for diagnostics
	routes := router.Router().Routes()
	a.logg.Debug("API routes registered", "count", len(routes))
	for _, r := range routes {
		a.logg.Debug("  route", "method", r.Method, "path", r.Path, "name", r.Name)
	}

	a.logg.Debug("API initialized", "listen_address", a.listenAddress)
	return a
}

func (a *API) Start() error {
	a.logg.Info("Starting API HTTP server", "listen_address", a.listenAddress)
	return a.server.ListenAndServe()
}

func (a *API) Stop(ctx context.Context) error {
	a.logg.Info("Shutting down API server")
	return a.server.Shutdown(ctx)
}
