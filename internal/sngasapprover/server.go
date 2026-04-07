package sngasapprover

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/worker"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/grassrootseconomics/ethutils"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

type (
	ServerOpts struct {
		ListenAddress string
		Store         store.Store
		Worker        *worker.WorkerContainer
		Logg          *slog.Logger
	}

	Server struct {
		listenAddress string
		store         store.Store
		worker        *worker.WorkerContainer
		logg          *slog.Logger
		router        *echo.Echo
	}

	ApproveRequest struct {
		Address string `json:"address"`
	}
)

func New(o ServerOpts) *Server {
	router := echo.New()
	router.HideBanner = true
	router.HidePort = true
	router.Use(middleware.Recover())
	router.Use(middleware.BodyLimit("1M"))

	server := &Server{
		listenAddress: o.ListenAddress,
		store:         o.Store,
		worker:        o.Worker,
		logg:          o.Logg,
		router:        router,
	}

	router.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	})
	router.POST("/approve", server.approveHandler)

	router.HTTPErrorHandler = server.httpErrorHandler

	return server
}

func (s *Server) Start() error {
	s.logg.Info("starting sn-gas-approver HTTP server", "listen_address", s.listenAddress)
	return s.router.Start(s.listenAddress)
}

func (s *Server) Stop(ctx context.Context) error {
	s.logg.Info("shutting down sn-gas-approver HTTP server")
	return s.router.Shutdown(ctx)
}

func (s *Server) approveHandler(c echo.Context) error {
	var req ApproveRequest

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			Description: "Invalid or malformed request",
			ErrCode:     apiresp.ErrCodeInvalidJSON,
		})
	}

	if !common.IsHexAddress(req.Address) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			Description: "Validation failed on one or more fields",
			ErrCode:     apiresp.ErrCodeValidationFailed,
		})
	}

	address := common.HexToAddress(req.Address).Hex()
	trackingID := uuid.NewString()

	tx, err := s.store.Pool().Begin(c.Request().Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(c.Request().Context())

	if err := s.store.InsertSNGasApproverAccount(c.Request().Context(), tx, store.SNGasApproverAccount{
		TrackingID: trackingID,
		Address:    address,
		UsersIndex: s.worker.Registry()[ethutils.AccountIndex].Hex(),
		Active:     true,
	}); err != nil {
		return err
	}

	_, err = s.worker.Client().InsertTx(c.Request().Context(), tx, worker.UsersIndexAddArgs{
		TrackingID: trackingID,
		Address:    address,
	}, nil)
	if err != nil {
		return err
	}

	if err := tx.Commit(c.Request().Context()); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "User queued for users index approval",
		Result: map[string]any{
			"address":    address,
			"trackingId": trackingID,
		},
	})
}

func (s *Server) httpErrorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}

	s.logg.Error("sn-gas-approver: echo error", "path", c.Path(), "err", err)
	_ = c.JSON(http.StatusInternalServerError, apiresp.ErrResponse{
		Ok:          false,
		Description: "Internal server error",
		ErrCode:     apiresp.ErrCodeInternalServerError,
	})
}
