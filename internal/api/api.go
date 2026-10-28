package api

import (
	"context"
	"crypto"
	"log/slog"
	"net/http"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/pretium"
	"github.com/cosmo-local-credit/clc-core/internal/relayer"
	"github.com/cosmo-local-credit/clc-core/internal/safe"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/telemetry"
	"github.com/cosmo-local-credit/clc-core/internal/util"
	"github.com/go-playground/validator/v10"
	"github.com/grassrootseconomics/ethutils"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/riverqueue/river"
	"golang.org/x/sync/singleflight"
)

type (
	APIOpts struct {
		ListenAddress string
		Build         string
		Prod          bool
		CORS          []string
		// TrustProxy enables X-Forwarded-For as the rate-limit key when the
		// direct peer is the loopback/private Caddy hop.
		TrustProxy      bool
		VerifyingKey    crypto.PublicKey
		SigningKey      crypto.PrivateKey
		Store           store.Store
		FederationStore store.FederationStore
		Logg            *slog.Logger
		ChainProvider   *ethutils.Provider
		QueueClient     *river.Client[pgx.Tx]
		ChainID         int64
		Relayer         *relayer.Relayer
		Pretium         *pretium.PretiumClient
		// SafeMultiSend lists the pinned MultiSendCallOnly deployments. Empty
		// disables Safe execution through the relay.
		SafeMultiSend []string
		// SafeSTSURL and SafeSTSKey configure the Safe Transaction Service
		// proxy. With an empty URL or no pinned MultiSend the /safe routes are
		// not mounted.
		SafeSTSURL string
		SafeSTSKey string
		// SafeAutoExecute has clc-core execute a proposal once its signers
		// reach the threshold; the worker must run the matching executor.
		SafeAutoExecute bool
	}

	API struct {
		listenAddress           string
		build                   string
		prod                    bool
		signingKey              crypto.PrivateKey
		verifyingKey            crypto.PublicKey
		siweNonces              *util.SIWENonceStore
		store                   store.Store
		federationStore         store.FederationStore
		logg                    *slog.Logger
		chainProvider           *ethutils.Provider
		server                  *http.Server
		router                  *echo.Echo
		queueClient             *river.Client[pgx.Tx]
		chainID                 int64
		relayer                 *relayer.Relayer
		pretium                 *pretium.PretiumClient
		trustProxy              bool
		marketLimiter           *marketRateLimiter
		marketMapUserLimiter    *marketRateLimiter
		marketMapGuestLimiter   *marketRateLimiter
		poolLimiter             *marketRateLimiter
		recipientLimiter        *marketRateLimiter
		marketFacets            *marketFacetCache
		marketMapCache          *marketMapCache
		marketMapExecutor       *marketMapExecutor
		marketMapLoads          singleflight.Group
		retirementPreviewReader func(context.Context, pgx.Tx, store.RetirementResource) (store.RetirementPreview, error)
		safePolicy              *safe.Policy
		safeReader              safeStateReader
		safeMisses              *safeMissCache
		safeProxy               *safeService
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
		prod:                  o.Prod,
		build:                 o.Build,
		signingKey:            o.SigningKey,
		verifyingKey:          o.VerifyingKey,
		siweNonces:            siweNonces,
		listenAddress:         o.ListenAddress,
		logg:                  o.Logg,
		store:                 o.Store,
		federationStore:       o.FederationStore,
		chainProvider:         o.ChainProvider,
		queueClient:           o.QueueClient,
		chainID:               o.ChainID,
		relayer:               o.Relayer,
		pretium:               o.Pretium,
		trustProxy:            o.TrustProxy,
		marketLimiter:         newMarketRateLimiter(marketRateLimit, marketRateBurst, time.Now),
		marketMapUserLimiter:  newMarketRateLimiterWithCapacity(marketMapUserRateLimit, marketMapUserRateBurst, marketMapRateMaxClients, true, time.Now),
		marketMapGuestLimiter: newMarketRateLimiterWithCapacity(marketMapGuestRateLimit, marketMapGuestRateBurst, marketMapRateMaxClients, true, time.Now),
		poolLimiter:           newMarketRateLimiter(marketRateLimit, marketRateBurst, time.Now),
		recipientLimiter:      newMarketRateLimiter(recipientRateLimit, recipientRateBurst, time.Now),
		marketFacets:          newMarketFacetCache(time.Now),
		marketMapCache:        newMarketMapCache(time.Now),
		marketMapExecutor:     newMarketMapExecutor(),
	}
	a.safePolicy = a.newSafePolicy(o.SafeMultiSend)
	var safeReader *safe.Reader
	if o.ChainProvider != nil {
		safeReader = safe.NewReader(o.ChainProvider)
		a.safeReader = safeReader
		a.safeMisses = newSafeMissCache(time.Now)
	}
	if a.safePolicy != nil && o.SafeSTSURL != "" {
		a.safeProxy = &safeService{
			chainID: o.ChainID,
			policy:  a.safePolicy,
			sts:     safe.NewSTS(o.SafeSTSURL, o.SafeSTSKey),
			signers: storeSafeSigners{a: a},
		}
		if safeReader != nil {
			a.safeProxy.contracts = safeReader.ContractVerifierLatest
		}
		if o.SafeAutoExecute {
			a.safeProxy.execute = a.queueSafeExecution
			a.safeProxy.executing = a.safeExecutionsQueued
		}
	}
	a.marketLimiter.startSweeper()
	a.marketMapUserLimiter.startSweeper()
	a.marketMapGuestLimiter.startSweeper()
	a.poolLimiter.startSweeper()
	a.recipientLimiter.startSweeper()

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
	httpDuration := telemetry.NewHTTPServerDuration()
	router.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogStatus:    true,
		LogURI:       true,
		LogLatency:   true,
		LogMethod:    true,
		LogRoutePath: true,
		HandleError:  true,
		LogValuesFunc: func(c *echo.Context, v middleware.RequestLoggerValues) error {
			httpDuration.Record(v.Method, v.RoutePath, v.Status, v.Latency)

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
	authGroup.POST("/webauthn/verify", a.webAuthnSigninHandler)
	authGroup.GET("/session", a.sessionHandler)
	// Resolves the session inside the handler, like /session; see its comment.
	authGroup.GET("/webauthn/credentials", a.webAuthnCredentialsHandler)

	// SIWE unified verify (public — returns JWT for approved users)
	siweGroup := router.Group("/auth/siwe")
	siweGroup.POST("/nonce", a.nonceHandler)
	siweGroup.POST("/verify", a.siweVerifyHandler)

	// Authenticated profile routes
	profileAuthGroup := router.Group("/auth", a.flexibleAuthMiddleware(), a.requireUserAuth())
	profileAuthGroup.GET("/profile", a.profileGetHandler)
	profileAuthGroup.POST("/profile", a.profileUpdateHandler)
	// A readable copy of the session token, for uploads to the image service.
	// Mounted here rather than under /api/v2 because it is part of the session,
	// and behind requireUserAuth because a service token has no browser to hand
	// it to.
	profileAuthGroup.POST("/storage-token", a.storageTokenHandler)

	// Public market catalog. Registered on the root router, NOT on apiGroup:
	// flexibleAuthMiddleware is mandatory authentication despite its name, and
	// ordinary catalog/detail reads remain public. Nearby list reads opt into
	// user authentication. Catalog/detail reads retain their per-IP rate limit;
	// the unified map route applies its dedicated cache-aware limiter in-handler.
	router.GET(apiVersion+"/vouchers", a.vouchersListHandler, a.marketRateLimitMiddleware(), a.nearbyCatalogAuth())
	router.GET(apiVersion+"/vouchers/:address", a.voucherByAddressHandler, a.marketRateLimitMiddleware())
	router.GET(apiVersion+"/vouchers/:address/offerings", a.voucherOfferingsListHandler, a.marketRateLimitMiddleware())
	router.GET(apiVersion+"/vouchers/:address/offerings/:id", a.voucherOfferingHandler, a.marketRateLimitMiddleware())
	router.GET(apiVersion+"/vouchers/:address/reviews", a.voucherReviewsListHandler, a.marketRateLimitMiddleware())
	router.GET(apiVersion+"/offerings", a.offeringsListHandler, a.marketRateLimitMiddleware(), a.nearbyCatalogAuth())
	router.GET(apiVersion+"/wants", a.wantsListHandler, a.marketRateLimitMiddleware(), a.nearbyCatalogAuth())
	router.GET(apiVersion+"/wants/:id", a.wantDetailHandler, a.marketRateLimitMiddleware())
	router.GET(apiVersion+"/wants/:id/vouchers", a.wantCreatorVouchersListHandler, a.marketRateLimitMiddleware())
	router.GET(apiVersion+"/wants/:id/pools", a.wantCreatorPoolsListHandler, a.marketRateLimitMiddleware())
	router.GET(apiVersion+"/marketplace", a.marketplaceListHandler, a.marketRateLimitMiddleware(), a.nearbyCatalogAuth())
	router.GET(apiVersion+"/market-map", a.marketMapHandler)
	// User-curated catalogs are public to browse; nearby discovery uses the
	// viewer's saved profile location and therefore requires a user session.
	router.GET(apiVersion+"/catalogs", a.curatedCatalogListHandler, a.marketRateLimitMiddleware(), a.nearbyCatalogAuth())
	router.GET(apiVersion+"/catalogs/:id", a.curatedCatalogDetailHandler, a.marketRateLimitMiddleware())
	router.GET(apiVersion+"/catalogs/:id/pools", a.curatedMembersHandler, a.marketRateLimitMiddleware())
	router.GET(apiVersion+"/catalogs/:id/vouchers", a.curatedMembersHandler, a.marketRateLimitMiddleware())

	// Network pool catalog (imported + clc-core deploys; reads the pools
	// table). Distinct from /users/me/pools which is
	// membership/ownership for the authenticated user only. Public for the same
	// reason as the market catalog — the map's voucher card lists the pools that
	// hold a voucher — but a signed-in caller is not throttled; see
	// poolCatalogMiddleware. Nearby list reads need a session, as on the
	// market. Pool writes stay on apiGroup.
	router.GET(apiVersion+"/pools", a.poolsListHandler, a.poolCatalogMiddleware(), a.nearbyCatalogAuth())
	router.GET(apiVersion+"/pools/:address", a.poolsByAddressHandler, a.poolCatalogMiddleware())
	router.GET(apiVersion+"/pools/:address/vouchers", a.poolVouchersListHandler, a.poolCatalogMiddleware())
	router.GET(apiVersion+"/pools/:address/offerings", a.poolOfferingsListHandler, a.poolCatalogMiddleware())
	router.GET(apiVersion+"/pools/:address/wants", a.poolWantsListHandler, a.poolCatalogMiddleware())

	// Protected routes — user JWT cookie or service JWT header/cookie required
	apiGroup := router.Group(apiVersion)
	apiGroup.Use(a.flexibleAuthMiddleware())
	apiGroup.Use(a.revalidatePrivilegedRole())
	apiGroup.GET("/vouchers/:address/info", a.voucherInfoHandler, a.requireUserAuth())
	apiGroup.GET("/pools/:address/info", a.poolInfoHandler, a.requireUserAuth())
	apiGroup.GET("/wants/:id/contact", a.wantContactHandler, a.requireUserAuth())

	apiGroup.GET("/vouchers/:address/reviews/me", a.voucherReviewAccessHandler, a.requireUserAuth())
	apiGroup.PUT("/vouchers/:address/reviews/me", a.voucherReviewSaveHandler, a.requireUserAuth())
	apiGroup.PUT("/vouchers/:address/reviews/:id/reply", a.voucherReviewReplyHandler, a.requireUserAuth())
	apiGroup.POST("/vouchers/:address/reviews/:id/flag", a.voucherReviewFlagHandler, a.requireUserAuth())
	apiGroup.POST("/catalogs", a.curatedCatalogSaveHandler, a.requireUserAuth())
	apiGroup.PUT("/catalogs/:id", a.curatedCatalogSaveHandler, a.requireUserAuth())
	apiGroup.GET("/catalogs/:id/membership", a.curatedMembershipHandler, a.requireUserAuth())
	apiGroup.PUT("/catalogs/:id/pools/:poolId", a.curatedMembershipHandler, a.requireUserAuth())
	apiGroup.DELETE("/catalogs/:id/pools/:poolId", a.curatedMembershipHandler, a.requireUserAuth())
	apiGroup.PUT("/catalogs/:id/vouchers/:voucherId", a.curatedMembershipHandler, a.requireUserAuth())
	apiGroup.DELETE("/catalogs/:id/vouchers/:voucherId", a.curatedMembershipHandler, a.requireUserAuth())

	apiGroup.POST("/contracts/token", a.deployTokenHandler)
	apiGroup.POST("/contracts/pool", a.deployPoolHandler)
	apiGroup.POST("/contracts/version", a.contractsVersionHandler)
	apiGroup.POST("/contracts/upgrade/token", a.upgradeTokenHandler)
	apiGroup.POST("/contracts/upgrade/pool", a.upgradePoolHandler)

	apiGroup.GET("/token-tags", a.tokenTagsHandler)
	apiGroup.GET("/offering-units", a.offeringUnitsHandler)
	apiGroup.GET("/units-of-account", a.unitsOfAccountHandler, a.requireUserAuth())
	apiGroup.GET("/symbols", a.symbolsLookupHandler)
	apiGroup.GET("/names", a.namesLookupHandler)
	apiGroup.GET("/recipients", a.recipientSearchHandler, a.recipientRateLimitMiddleware())
	router.GET(apiVersion+"/recipients/suggest", a.usernameSuggestHandler, a.marketRateLimitMiddleware())
	apiGroup.GET("/recipients/resolve", a.recipientResolveHandler, a.recipientRateLimitMiddleware())
	apiGroup.GET("/recipients/resolve/:name", a.recipientResolveHandler, a.recipientRateLimitMiddleware())
	apiGroup.GET("/recipients/reverse/:address", a.recipientReverseHandler, a.recipientRateLimitMiddleware())
	apiGroup.GET("/oracle-feeds", a.oracleFeedsHandler)
	apiGroup.GET("/system", a.systemHandler)
	apiGroup.GET("/otx/track/:trackingId", a.trackOTXHandler)
	apiGroup.GET("/otx/track/:trackingId/contract", a.trackContractHandler)
	apiGroup.GET("/account/otx/:address", a.accountOTXHandler)
	apiGroup.GET("/account/status/:address", a.accountStatusHandler)

	// Approved post reads are public. Nearby reads and mutations require a user session.
	router.GET(apiVersion+"/reports", a.reportsListHandler, a.marketRateLimitMiddleware(), a.nearbyCatalogAuth())
	router.GET(apiVersion+"/reports/:id", a.reportByIDHandler, a.marketRateLimitMiddleware())
	apiGroup.POST("/reports", a.reportCreateHandler, a.requireUserAuth())
	apiGroup.PUT("/reports/:id", a.reportUpdateHandler, a.requireUserAuth())
	apiGroup.PUT("/reports/:id/approve", a.reportApproveHandler, a.requireAdmin())
	apiGroup.DELETE("/reports/:id", a.reportDeleteHandler, a.requireAdmin())

	// Owner catalog writes. On apiGroup with requireUserAuth, like report
	// mutations: a normal pool or voucher owner needs these capabilities, so they
	// are not admin-only routes.
	// Mounting a second copy under /api/v2/admin would give the console its own
	// handlers to drift from. Each owner/administrator decision happens inside the
	// handler, from the catalog row it locks — middleware only establishes who is
	// calling.
	apiGroup.POST("/vouchers/:address/offerings", a.voucherOfferingCreateHandler, a.requireUserAuth())
	apiGroup.PUT("/vouchers/:address/offerings/:id", a.voucherOfferingUpdateHandler, a.requireUserAuth())
	apiGroup.DELETE("/vouchers/:address/offerings/:id", a.voucherOfferingDeleteHandler, a.requireUserAuth())
	apiGroup.PUT("/pools/:address/profile", a.updatePoolProfileHandler, a.requireUserAuth())
	apiGroup.PUT("/vouchers/:address/display-name", a.updateVoucherDisplayNameHandler, a.requireUserAuth())
	apiGroup.PUT("/vouchers/:address/profile", a.updateVoucherProfileHandler, a.requireUserAuth())
	// The old immediate-retirement route now returns an upgrade error. Owners
	// request retirement; administrators decide and restore through the console.
	apiGroup.DELETE("/vouchers/:address", a.retireVoucherHandler, a.requireUserAuth())
	apiGroup.GET("/vouchers/:address/retirement-preview", a.retirementPreviewHandler, a.requireUserAuth())
	apiGroup.GET("/vouchers/:address/retirement-requests", a.retirementResourceHistoryHandler, a.requireUserAuth())
	apiGroup.POST("/vouchers/:address/retirement-requests", a.createRetirementRequestHandler, a.requireUserAuth())
	apiGroup.GET("/pools/:address/retirement-preview", a.retirementPreviewHandler, a.requireUserAuth())
	apiGroup.GET("/pools/:address/retirement-requests", a.retirementResourceHistoryHandler, a.requireUserAuth())
	apiGroup.POST("/pools/:address/retirement-requests", a.createRetirementRequestHandler, a.requireUserAuth())
	apiGroup.PUT("/retirement-requests/:id/cancel", a.decideRetirementRequestHandler, a.requireUserAuth())

	if a.pretium != nil {
		pretiumGroup := apiGroup.Group("/pretium")
		pretiumGroup.POST("/trigger-onramp", a.pretiumTriggerOnrampHandler)
		pretiumGroup.POST("/link", a.pretiumLinkHandler)
		pretiumGroup.GET("/link/:phoneNumber", a.pretiumPhoneLinksHandler, a.requireStaff())
		pretiumGroup.GET("/link-by-address/:address", a.pretiumLinkByAddressHandler)
		pretiumGroup.GET("/transactions/:phoneNumber", a.pretiumTransactionsHandler, a.requireStaff())
		pretiumGroup.GET("/transactions-by-address/:address", a.pretiumTransactionsByAddressHandler)
		pretiumGroup.GET("/transactions-recent", a.pretiumRecentTransactionsHandler, a.requireStaff())
		pretiumGroup.GET("/rates", a.pretiumRatesHandler)
	}

	// Federation account data (balances + transfers). Mounted on the authed
	// apiGroup so the SIWE user JWT cookie (or service token) is required.
	if a.federationStore != nil {
		apiGroup.GET("/accounts/:address/tokens/latest", a.accountLatestTokensHandler)
		apiGroup.GET("/accounts/:address/tokens/issued", a.accountIssuedTokensHandler)
		apiGroup.GET("/accounts/:address/tokens", a.accountTokensHandler)
		apiGroup.GET("/accounts/:address/transfers", a.accountTransfersHandler)
		apiGroup.GET("/accounts/:address/activity", a.accountActivityHandler)
	}

	// Public ownership inspection also discovers pre-existing compatible
	// multisigs. It is available without STS or gasless Safe execution.
	router.GET(apiVersion+"/owners/:address", a.safeOwnerHandler, a.marketRateLimitMiddleware())

	// Safe Transaction Service proxy: the API key stays in clc-core's config.
	if a.safeProxy != nil {
		safeGroup := apiGroup.Group("/safe", a.requireUserAuth())
		safeGroup.POST("/proposals", a.safeProposalHandler)
		safeGroup.POST("/confirmations", a.safeConfirmationHandler)
		safeGroup.GET("/transactions/:safeTxHash", a.safeTransactionHandler)
		safeGroup.GET("/:address/pending", a.safePendingHandler)
	}

	// User-only routes
	userGroup := apiGroup.Group("/users", a.requireUserAuth())
	userGroup.GET("/me", a.userMeHandler)
	userGroup.GET("/me/resource-usage", a.userResourceUsageHandler)
	userGroup.GET("/me/catalogs", a.myCuratedCatalogsHandler)
	userGroup.GET("/me/vouchers", a.userOwnedVouchersHandler)
	userGroup.GET("/me/wants", a.ownedWantsListHandler)
	userGroup.POST("/me/wants", a.wantCreateHandler)
	userGroup.PUT("/me/wants/:id", a.wantUpdateHandler)
	userGroup.POST("/me/wants/:id/close", a.wantCloseHandler)
	userGroup.POST("/me/wants/:id/reopen", a.wantReopenHandler)
	userGroup.DELETE("/me/wants/:id", a.wantDeleteHandler)
	userGroup.GET("/me/tokens", a.userTokensHandler)
	userGroup.GET("/me/tokens/:address", a.userTokenByAddressHandler)
	userGroup.GET("/me/pools", a.userPoolsHandler)
	userGroup.GET("/me/pools/:address", a.userPoolByAddressHandler)
	userGroup.GET("/me/owned-pools", a.ownerPoolsListHandler)
	userGroup.GET("/me/owned-pools/:address", a.ownerPoolByAddressHandler)
	userGroup.GET("/me/owned-pools/:address/vouchers", a.ownerPoolVouchersListHandler)
	userGroup.GET("/me/retirement-requests", a.ownRetirementRequestsHandler)
	userGroup.GET("/me/deployments", a.userDeploymentsHandler)
	userGroup.GET("/me/safes", a.userSafesHandler)
	userGroup.GET("/me/shares/eligibility/:address", a.userShareEligibilityHandler)
	userGroup.POST("/me/shares/:txHash", a.userShareCreateHandler)
	userGroup.GET("/me/shares/:txHash", a.userShareByTxHashHandler)

	// An author's own posts in every status. /reports and /reports/:id stay
	// approved-only; these are the reads that let an author find and reopen a
	// post the public feed is hiding.
	userGroup.GET("/me/reports", a.userReportsListHandler)
	userGroup.GET("/me/reports/:id", a.userReportByIDHandler)

	// Administrative routes. Role mutation adds a second, superadmin-only guard.
	adminGroup := apiGroup.Group("/admin", a.requireAdmin())
	adminGroup.GET("/users", a.adminListUsersHandler)
	adminGroup.PUT("/users/:address/role", a.adminUpdateUserRoleHandler, a.requireSuperAdmin())
	adminGroup.PUT("/users/:address/approve", a.adminApproveUserHandler)
	adminGroup.PUT("/users/:address/reject", a.adminRejectUserHandler)
	adminGroup.PUT("/users/:address/limits", a.adminUpdateUserLimitsHandler)
	adminGroup.PUT("/users/:address/profile", a.adminUpdateUserProfileHandler)
	adminGroup.GET("/users/:address/pools", a.adminAccountPoolsHandler)
	adminGroup.GET("/users/:address/stats", a.adminAccountStatsHandler)
	adminGroup.GET("/tokens", a.adminListTokensHandler)
	adminGroup.GET("/pools", a.adminListPoolsHandler)
	adminGroup.GET("/pool-catalog", a.adminPoolCatalogHandler)
	adminGroup.GET("/pools/:address", a.adminPoolByAddressHandler)
	adminGroup.PUT("/pools/:address/restore", a.adminRestorePoolHandler)
	adminGroup.GET("/pools/:address/retirement-history", a.retirementResourceHistoryHandler)
	adminGroup.GET("/vouchers/:address/retirement-history", a.retirementResourceHistoryHandler)
	adminGroup.GET("/retirement-requests", a.adminRetirementRequestsHandler)
	adminGroup.GET("/retirement-requests/:id", a.adminRetirementRequestHandler)
	adminGroup.PUT("/retirement-requests/:id/approve", a.decideRetirementRequestHandler)
	adminGroup.PUT("/retirement-requests/:id/reject", a.decideRetirementRequestHandler)
	adminGroup.GET("/pools/:address/vouchers", a.adminPoolVouchersListHandler)
	adminGroup.PUT("/pools/:address/profile", a.adminUpdatePoolProfileHandler)
	adminGroup.PUT("/vouchers/:address/profile", a.adminUpdateVoucherProfileHandler)
	adminGroup.GET("/vouchers", a.adminVouchersListHandler)
	adminGroup.GET("/vouchers/:address", a.adminVoucherByAddressHandler)
	adminGroup.PUT("/vouchers/:address/restore", a.adminRestoreVoucherHandler)
	adminGroup.GET("/deployments", a.adminListDeploymentsHandler)
	adminGroup.GET("/vocabulary", a.adminVocabularyHandler)
	adminGroup.PUT("/vocabulary/tags", a.adminRenameTokenTagHandler)
	adminGroup.PUT("/vocabulary/units", a.adminRenameOfferingUnitHandler)
	adminGroup.POST("/vocabulary/units-of-account", a.adminCreateUnitOfAccountHandler)
	adminGroup.PUT("/vocabulary/units-of-account", a.adminRenameUnitOfAccountHandler)
	adminGroup.DELETE("/vocabulary/tags", a.adminDeleteTokenTagHandler)
	adminGroup.DELETE("/vocabulary/units", a.adminDeleteOfferingUnitHandler)
	adminGroup.DELETE("/vocabulary/units-of-account", a.adminDeleteUnitOfAccountHandler)

	// Global post moderation reads. The write half stays on apiGroup with the
	// other report mutations, where the role check is already per-handler.
	adminGroup.GET("/review-notifications", a.reviewNotificationsHandler)
	adminGroup.PUT("/review-notifications/:id", a.reviewModerateHandler)
	adminGroup.GET("/reports", a.adminReportsListHandler)
	adminGroup.GET("/reports/:id", a.adminReportByIDHandler)

	// Dashboard aggregates are a separate group so catalog mutations and
	// ops totals do not share a route prefix that invites accidental reuse.
	dashboardGroup := adminGroup.Group("/dashboard")
	dashboardGroup.GET("/stats", a.adminDashboardStatsHandler)

	// Transaction pages. Three resources rather than one filtered list: the
	// first two read the chain in the two shapes it comes in, and the third
	// reads what this service signed, which is a different fact from what the
	// chain did with it.
	txGroup := adminGroup.Group("/transactions")
	txGroup.GET("/vouchers", a.adminVoucherTransactionsHandler)
	txGroup.GET("/pools", a.adminPoolTransactionsHandler)
	txGroup.GET("/otx", a.adminOTXHandler)

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
	relayerGroup.POST("/relay/preview", a.relayPreviewHandler)
	relayerGroup.GET("/webauthn/challenge", a.webAuthnChallengeHandler)
	relayerGroup.POST("/webauthn/recover", a.webAuthnRecoverHandler)

	a.router = router
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
