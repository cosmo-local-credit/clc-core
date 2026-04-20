package api

import (
	"context"
	"crypto"
	"log/slog"
	"net/http"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/util"
	"github.com/go-playground/validator/v10"
	"github.com/grassrootseconomics/ethutils"
	"github.com/jackc/pgx/v5"
	echojwt "github.com/labstack/echo-jwt/v5"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/riverqueue/river"
)

type (
	APIOpts struct {
		ListenAddress string
		Build         string
		Prod          bool
		CORS          []string
		VerifyingKey  crypto.PublicKey
		SigningKey    crypto.PrivateKey
		Store         store.Store
		Logg          *slog.Logger
		ChainProvider *ethutils.Provider
		QueueClient   *river.Client[pgx.Tx]
		ChainID       int64
	}

	API struct {
		listenAddress string
		build         string
		prod          bool
		signingKey    crypto.PrivateKey
		verifyingKey  crypto.PublicKey
		store         store.Store
		logg          *slog.Logger
		chainProvider *ethutils.Provider
		server        *http.Server
		queueClient   *river.Client[pgx.Tx]
		chainID       int64
	}
)

const (
	apiVersion  = "/api/v2"
	maxBodySize = 1 << 20 // 1MB
)

func New(o APIOpts) *API {
	a := &API{
		prod:          o.Prod,
		build:         o.Build,
		signingKey:    o.SigningKey,
		verifyingKey:  o.VerifyingKey,
		listenAddress: o.ListenAddress,
		logg:          o.Logg,
		store:         o.Store,
		chainProvider: o.ChainProvider,
		queueClient:   o.QueueClient,
		chainID:       o.ChainID,
	}

	customValidator := validator.New(validator.WithRequiredStructEnabled())
	router := echo.New()
	router.Validator = &Validator{
		ValidatorProvider: customValidator,
	}
	router.HTTPErrorHandler = a.customHTTPErrorHandler

	corsConfig := middleware.CORSConfig{
		AllowOrigins:     o.CORS,
		AllowCredentials: true,
		AllowHeaders:     []string{echo.HeaderOrigin, echo.HeaderContentType, echo.HeaderAccept, echo.HeaderAuthorization},
		MaxAge:           86400,
	}

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

	apiGroup := router.Group(apiVersion)
	apiGroup.Use(echojwt.WithConfig(a.apiJWTAuthConfig()))
	apiGroup.Use(a.authStatusMiddleware())

	apiGroup.POST("/contracts/token", a.deployTokenHandler)

	a.server = &http.Server{
		Addr:    o.ListenAddress,
		Handler: router,
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
