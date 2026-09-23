package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/golang-jwt/jwt/v5"
)

const (
	reportAuthorAddress = "0x0000000000000000000000000000000000000011"
	reportOtherAddress  = "0x0000000000000000000000000000000000000022"
	reportAdminAddress  = "0x0000000000000000000000000000000000000033"
)

func TestCanUpdateReport(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		callerAddress string
		role          string
		authorAddress string
		want          bool
	}{
		{name: "original author", callerAddress: reportAuthorAddress, role: "USER", authorAddress: reportAuthorAddress, want: true},
		{name: "address casing", callerAddress: "0x00000000000000000000000000000000000000AA", role: "USER", authorAddress: "0x00000000000000000000000000000000000000aa", want: true},
		{name: "another user", callerAddress: reportOtherAddress, role: "USER", authorAddress: reportAuthorAddress},
		{name: "superadmin", callerAddress: reportAdminAddress, role: "SUPERADMIN", authorAddress: reportAuthorAddress, want: true},
		{name: "empty caller", role: "USER", authorAddress: reportAuthorAddress},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := canUpdateReport(testCase.callerAddress, testCase.role, testCase.authorAddress); got != testCase.want {
				t.Fatalf("canUpdateReport() = %t, want %t", got, testCase.want)
			}
		})
	}
}

func TestNormalizeReportWriteCanonicalisesRelations(t *testing.T) {
	blank := "  "
	caption := "  First image  "
	filename := " report.pdf "
	from := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	report, refs, err := normalizeReportWrite(apiresp.ReportWriteRequest{
		Title:       "  Community harvest  ",
		Body:        "## Harvest\n\nDone.",
		Description: "A short summary",
		ImageURL:    new(" https://content.example/cover.jpg "),
		PeriodFrom:  &from,
		PeriodTo:    &to,
		HostName:    &blank,
		Tags:        []string{"Youth", " Agriculture "},
		Vouchers: []apiresp.ReportVoucherRefRequest{
			{ChainID: 42220, Address: reportOtherAddress},
		},
		Images: []apiresp.ReportImageRequest{
			{URL: " https://content.example/one.jpg ", Caption: &caption},
		},
		Attachments: []apiresp.ReportAttachmentRequest{
			{Kind: "FILE", URL: "https://content.example/report.pdf", Filename: &filename},
		},
	})
	if err != nil {
		t.Fatalf("normalizeReportWrite() error = %v", err)
	}

	if report.Title != "Community harvest" {
		t.Fatalf("title = %q", report.Title)
	}
	if report.HostName != nil {
		t.Fatalf("blank hostName = %q, want nil", *report.HostName)
	}
	if got, want := report.Tags, []string{"Agriculture", "Youth"}; !slices.Equal(got, want) {
		t.Fatalf("tags = %v, want %v", got, want)
	}
	if len(refs) != 1 || refs[0].Address != reportOtherAddress {
		t.Fatalf("voucher refs = %+v", refs)
	}
	if len(report.Images) != 1 || report.Images[0].URL != "https://content.example/one.jpg" ||
		report.Images[0].Caption == nil || *report.Images[0].Caption != "First image" {
		t.Fatalf("images = %+v", report.Images)
	}
	if len(report.Attachments) != 1 || report.Attachments[0].Filename == nil ||
		*report.Attachments[0].Filename != "report.pdf" {
		t.Fatalf("attachments = %+v", report.Attachments)
	}
}

func TestNormalizeReportWriteRejectsCrossFieldViolations(t *testing.T) {
	from := time.Date(2026, 8, 2, 8, 0, 0, 0, time.UTC)
	to := from.Add(-time.Hour)
	lat := 1.0

	for _, testCase := range []struct {
		name string
		req  apiresp.ReportWriteRequest
	}{
		{name: "blank title", req: apiresp.ReportWriteRequest{Title: "  "}},
		{name: "half coordinate", req: apiresp.ReportWriteRequest{Title: "Post", GeoLat: &lat}},
		{name: "reversed period", req: apiresp.ReportWriteRequest{Title: "Post", PeriodFrom: &from, PeriodTo: &to}},
		{name: "non-http cover", req: apiresp.ReportWriteRequest{Title: "Post", ImageURL: new("data:image/png;base64,abc")}},
		{name: "bad voucher", req: apiresp.ReportWriteRequest{Title: "Post", Vouchers: []apiresp.ReportVoucherRefRequest{{ChainID: 42220, Address: "nope"}}}},
		{name: "duplicate image", req: apiresp.ReportWriteRequest{Title: "Post", Images: []apiresp.ReportImageRequest{{URL: "https://example.org/a.jpg"}, {URL: " https://example.org/a.jpg "}}}},
		{name: "non-http attachment", req: apiresp.ReportWriteRequest{Title: "Post", Attachments: []apiresp.ReportAttachmentRequest{{Kind: "FILE", URL: "ftp://example.org/a.pdf"}}}},
		// All four collections answer a duplicate the same way. They used to
		// disagree: tags and vouchers deduped silently, images failed, and
		// attachments were unchecked.
		{name: "duplicate tag", req: apiresp.ReportWriteRequest{Title: "Post", Tags: []string{"Youth", " Youth "}}},
		{name: "duplicate voucher", req: apiresp.ReportWriteRequest{Title: "Post", Vouchers: []apiresp.ReportVoucherRefRequest{
			{ChainID: 42220, Address: reportOtherAddress},
			{ChainID: 42220, Address: strings.ToUpper(reportOtherAddress)},
		}}},
		{name: "duplicate attachment", req: apiresp.ReportWriteRequest{Title: "Post", Attachments: []apiresp.ReportAttachmentRequest{
			{Kind: "FILE", URL: "https://example.org/a.pdf"},
			{Kind: "FILE", URL: " https://example.org/a.pdf "},
		}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, _, err := normalizeReportWrite(testCase.req); err == nil {
				t.Fatal("normalizeReportWrite() error = nil")
			}
		})
	}
}

func TestNormalizeReportWriteKeepsAttachmentKindsApart(t *testing.T) {
	report, _, err := normalizeReportWrite(apiresp.ReportWriteRequest{
		Title: "Post",
		Attachments: []apiresp.ReportAttachmentRequest{
			{Kind: "FILE", URL: "https://example.org/clip.mp4"},
			{Kind: "EMBED", URL: "https://example.org/clip.mp4"},
		},
	})
	if err != nil {
		t.Fatalf("normalizeReportWrite() error = %v", err)
	}
	if len(report.Attachments) != 2 {
		t.Fatalf("attachments = %+v, want both kinds", report.Attachments)
	}
}

func TestReportUpdateStatusPublishesForAuthorsOnly(t *testing.T) {
	if reportUpdateStatus("SUPERADMIN") != nil {
		t.Fatal("superadmin edit assigns a status, want the row's own preserved")
	}
	status := reportUpdateStatus("USER")
	if status == nil || *status != store.ReportStatusApproved {
		t.Fatalf("author edit status = %v, want APPROVED", status)
	}
}

func TestReportMutationRoutesEnforceCredentialKindsBeforeHandlers(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	a := New(APIOpts{
		Logg:         slog.New(slog.DiscardHandler),
		CORS:         []string{"https://app.example"},
		SigningKey:   private,
		VerifyingKey: public,
	})

	userToken := mintReportToken(t, private, reportAuthorAddress, "USER")
	adminToken := mintReportToken(t, private, reportAdminAddress, "SUPERADMIN")
	serviceClaims := JWTCustomClaims{
		PublicKey: reportOtherAddress,
		Service:   true,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	serviceToken, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, serviceClaims).SignedString(private)
	if err != nil {
		t.Fatalf("sign service token: %v", err)
	}

	request := func(method, path, token string) int {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(`{"title":"Post"}`))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		return res.Code
	}

	for _, methodPath := range [][2]string{
		{http.MethodPost, "/api/v2/reports"},
		{http.MethodPut, "/api/v2/reports/not-an-id"},
		{http.MethodPut, "/api/v2/reports/not-an-id/approve"},
		{http.MethodDelete, "/api/v2/reports/not-an-id"},
	} {
		if status := request(methodPath[0], methodPath[1], ""); status != http.StatusUnauthorized {
			t.Fatalf("%s %s without auth = %d, want 401", methodPath[0], methodPath[1], status)
		}
	}
	if status := request(http.MethodPost, "/api/v2/reports", serviceToken); status != http.StatusForbidden {
		t.Fatalf("service create status = %d, want 403", status)
	}
	if status := request(http.MethodDelete, "/api/v2/reports/not-an-id", userToken); status != http.StatusForbidden {
		t.Fatalf("author delete status = %d, want 403", status)
	}
	if status := request(http.MethodPut, "/api/v2/reports/not-an-id/approve", userToken); status != http.StatusForbidden {
		t.Fatalf("author approve status = %d, want 403", status)
	}
	if status := request(http.MethodPut, "/api/v2/reports/not-an-id/approve", adminToken); status != http.StatusBadRequest {
		t.Fatalf("admin approve status = %d, want handler validation 400", status)
	}
	if status := request(http.MethodDelete, "/api/v2/reports/not-an-id", adminToken); status != http.StatusBadRequest {
		t.Fatalf("admin delete status = %d, want handler validation 400", status)
	}
}

// End-to-end coverage of create, owner/admin edit and admin-only delete through
// the real route table and database. It follows the package's live-test contract
// and is skipped when CORE_TEST_DSN is absent.
func TestReportMutationAuthorizationThroughRoutes(t *testing.T) {
	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping live report mutation tests")
	}

	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	pgStore, err := store.NewPgStore(store.PgOpts{
		Logg:                 slog.New(slog.DiscardHandler),
		DSN:                  dsn,
		MigrationsFolderPath: "../../migrations",
		QueriesFolderPath:    "../../core_queries.sql",
	})
	if err != nil {
		t.Fatalf("connect store: %v", err)
	}
	defer pgStore.Pool().Close()

	a := New(APIOpts{
		Logg:         slog.New(slog.DiscardHandler),
		CORS:         []string{"https://app.example"},
		Store:        pgStore,
		SigningKey:   private,
		VerifyingKey: public,
	})
	authorToken := mintReportToken(t, private, reportAuthorAddress, "USER")
	otherToken := mintReportToken(t, private, reportOtherAddress, "USER")
	adminToken := mintReportToken(t, private, reportAdminAddress, "SUPERADMIN")
	marker := "Zqreportmutation" + strconv.FormatInt(time.Now().UnixNano(), 10)

	defer func() {
		_, _ = pgStore.Pool().Exec(context.Background(), `DELETE FROM reports WHERE title LIKE $1`, marker+"%")
	}()

	call := func(method, path, token, title string) *httptest.ResponseRecorder {
		body := []byte(nil)
		if method == http.MethodPost || method == http.MethodPut {
			body, err = json.Marshal(apiresp.ReportWriteRequest{
				Title:       title,
				Body:        "## Body",
				Description: "Description",
			})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		return res
	}

	created := call(http.MethodPost, "/api/v2/reports", authorToken, marker+" created")
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var createEnvelope struct {
		Result struct {
			Report struct {
				ID            uint64 `json:"id"`
				AuthorAddress string `json:"authorAddress"`
				Status        string `json:"status"`
			} `json:"report"`
		} `json:"result"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createEnvelope); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	id := createEnvelope.Result.Report.ID
	// Posts are auto-approved: a normal user's create is published at once.
	if id == 0 || createEnvelope.Result.Report.AuthorAddress != reportAuthorAddress ||
		createEnvelope.Result.Report.Status != "APPROVED" {
		t.Fatalf("created report = %+v", createEnvelope.Result.Report)
	}
	path := "/api/v2/reports/" + strconv.FormatUint(id, 10)

	if res := call(http.MethodPut, path, otherToken, marker+" other"); res.Code != http.StatusForbidden {
		t.Fatalf("other edit status = %d, body = %s", res.Code, res.Body.String())
	}
	if res := call(http.MethodPut, path, authorToken, marker+" author edit"); res.Code != http.StatusOK {
		t.Fatalf("author edit status = %d, body = %s", res.Code, res.Body.String())
	} else if status := reportStatus(t, res); status != "APPROVED" {
		t.Fatalf("status after author edit = %q, want APPROVED", status)
	}
	if res := call(http.MethodGet, path, authorToken, ""); res.Code != http.StatusOK {
		t.Fatalf("detail after author edit = %d, body = %s", res.Code, res.Body.String())
	}
	if res := call(http.MethodDelete, path, authorToken, ""); res.Code != http.StatusForbidden {
		t.Fatalf("author delete status = %d, body = %s", res.Code, res.Body.String())
	}
	if res := call(http.MethodPut, path+"/approve", adminToken, ""); res.Code != http.StatusOK {
		t.Fatalf("admin approve status = %d, body = %s", res.Code, res.Body.String())
	} else if status := reportStatus(t, res); status != "APPROVED" {
		t.Fatalf("approved status = %q, want APPROVED", status)
	}

	// An author's edit keeps the post published: there is no review queue for
	// an edit to fall back into.
	if res := call(http.MethodPut, path, authorToken, marker+" approved author edit"); res.Code != http.StatusOK {
		t.Fatalf("approved author edit status = %d, body = %s", res.Code, res.Body.String())
	} else if status := reportStatus(t, res); status != "APPROVED" {
		t.Fatalf("status after approved author edit = %q, want APPROVED", status)
	}
	if res := call(http.MethodGet, path, authorToken, ""); res.Code != http.StatusOK {
		t.Fatalf("detail after approved author edit = %d, body = %s", res.Code, res.Body.String())
	}

	// The author can reach their own post through the management read, and so
	// can an operator; another user cannot.
	ownPath := "/api/v2/users/me/reports/" + strconv.FormatUint(id, 10)
	if res := call(http.MethodGet, ownPath, authorToken, ""); res.Code != http.StatusOK {
		t.Fatalf("own detail status = %d, body = %s", res.Code, res.Body.String())
	} else if status := reportStatus(t, res); status != "APPROVED" {
		t.Fatalf("own detail status field = %q, want APPROVED", status)
	}
	if res := call(http.MethodGet, ownPath, otherToken, ""); res.Code != http.StatusNotFound {
		t.Fatalf("other author own-detail status = %d, want 404", res.Code)
	}
	adminPath := "/api/v2/admin/reports/" + strconv.FormatUint(id, 10)
	if res := call(http.MethodGet, adminPath, adminToken, ""); res.Code != http.StatusOK {
		t.Fatalf("admin detail status = %d, body = %s", res.Code, res.Body.String())
	}
	if res := call(http.MethodGet, "/api/v2/admin/reports?status=approved&perPage=100", adminToken, ""); res.Code != http.StatusOK {
		t.Fatalf("admin list status = %d, body = %s", res.Code, res.Body.String())
	} else if !managedListContains(t, res, id) {
		t.Fatalf("admin APPROVED list is missing report %d", id)
	}
	if res := call(http.MethodGet, "/api/v2/users/me/reports?perPage=100", authorToken, ""); res.Code != http.StatusOK {
		t.Fatalf("own list status = %d, body = %s", res.Code, res.Body.String())
	} else if !managedListContains(t, res, id) {
		t.Fatalf("author's own list is missing report %d", id)
	}
	if res := call(http.MethodGet, "/api/v2/users/me/reports?perPage=100", otherToken, ""); res.Code != http.StatusOK {
		t.Fatalf("other author list status = %d, body = %s", res.Code, res.Body.String())
	} else if managedListContains(t, res, id) {
		t.Fatalf("another author's list leaked report %d", id)
	}

	// An operator's edit preserves status, so a correction does not unpublish.
	if res := call(http.MethodPut, path+"/approve", adminToken, ""); res.Code != http.StatusOK {
		t.Fatalf("reapprove status = %d, body = %s", res.Code, res.Body.String())
	}
	if res := call(http.MethodPut, path, adminToken, marker+" admin"); res.Code != http.StatusOK {
		t.Fatalf("admin edit status = %d, body = %s", res.Code, res.Body.String())
	} else if status := reportStatus(t, res); status != "APPROVED" {
		t.Fatalf("status after admin edit = %q, want APPROVED", status)
	}
	if res := call(http.MethodGet, path, authorToken, ""); res.Code != http.StatusOK {
		t.Fatalf("detail after admin edit = %d, body = %s", res.Code, res.Body.String())
	}
	if res := call(http.MethodDelete, path, adminToken, ""); res.Code != http.StatusOK {
		t.Fatalf("admin delete status = %d, body = %s", res.Code, res.Body.String())
	}
	if res := call(http.MethodGet, adminPath, adminToken, ""); res.Code != http.StatusNotFound {
		t.Fatalf("admin detail after delete = %d, want 404", res.Code)
	}

	adminCreated := call(http.MethodPost, "/api/v2/reports", adminToken, marker+" admin created")
	if adminCreated.Code != http.StatusCreated || reportStatus(t, adminCreated) != "APPROVED" {
		t.Fatalf("admin create status = %d, body = %s", adminCreated.Code, adminCreated.Body.String())
	}
}

func managedListContains(t *testing.T, response *httptest.ResponseRecorder, id uint64) bool {
	t.Helper()
	var envelope struct {
		Result struct {
			Reports []struct {
				ID     uint64 `json:"id"`
				Status string `json:"status"`
			} `json:"reports"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode managed list: %v", err)
	}
	for _, report := range envelope.Result.Reports {
		if report.ID != id {
			continue
		}
		if report.Status == "" {
			t.Fatalf("managed summary %d carries no status", id)
		}
		return true
	}
	return false
}

func reportStatus(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Result struct {
			Report struct {
				Status string `json:"status"`
			} `json:"report"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode report status: %v", err)
	}
	return envelope.Result.Report.Status
}

func mintReportToken(t *testing.T, private ed25519.PrivateKey, address, role string) string {
	t.Helper()
	claims := UserJWTClaims{
		UserID:          1,
		EthereumAddress: address,
		Role:            role,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(private)
	if err != nil {
		t.Fatalf("sign user token: %v", err)
	}
	return token
}

// A post may only link vouchers its author owns unless the author is a
// superadmin. Live because ownership is a column on the vouchers table, and the
// rule is only as good as the query that reads it. Skipped without CORE_TEST_DSN.
func TestReportVoucherLinksRequireOwnershipThroughRoutes(t *testing.T) {
	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping live report voucher ownership tests")
	}

	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	pgStore, err := store.NewPgStore(store.PgOpts{
		Logg:                 slog.New(slog.DiscardHandler),
		DSN:                  dsn,
		MigrationsFolderPath: "../../migrations",
		QueriesFolderPath:    "../../core_queries.sql",
	})
	if err != nil {
		t.Fatalf("connect store: %v", err)
	}
	defer pgStore.Pool().Close()

	a := New(APIOpts{
		Logg:         slog.New(slog.DiscardHandler),
		CORS:         []string{"https://app.example"},
		Store:        pgStore,
		SigningKey:   private,
		VerifyingKey: public,
	})
	authorToken := mintReportToken(t, private, reportAuthorAddress, "USER")
	otherToken := mintReportToken(t, private, reportOtherAddress, "USER")
	adminToken := mintReportToken(t, private, reportAdminAddress, "SUPERADMIN")
	marker := "Zqreportowner" + strconv.FormatInt(time.Now().UnixNano(), 10)

	ctx := context.Background()
	var voucherID uint64
	var voucherAddress string
	if err := pgStore.Pool().QueryRow(ctx, `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, owner_address, active)
		VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
			$1, 'ZQO', 6, $2, TRUE)
		RETURNING id, address`, marker+" Voucher", reportAuthorAddress).
		Scan(&voucherID, &voucherAddress); err != nil {
		t.Fatalf("seed voucher: %v", err)
	}
	defer func() {
		_, _ = pgStore.Pool().Exec(ctx, `DELETE FROM reports WHERE title LIKE $1`, marker+"%")
		_, _ = pgStore.Pool().Exec(ctx, `DELETE FROM vouchers WHERE id = $1`, voucherID)
	}()

	create := func(token, title string) *httptest.ResponseRecorder {
		body, err := json.Marshal(apiresp.ReportWriteRequest{
			Title:    title,
			Vouchers: []apiresp.ReportVoucherRefRequest{{ChainID: 42220, Address: voucherAddress}},
		})
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v2/reports", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		return res
	}

	if res := create(authorToken, marker+" owner"); res.Code != http.StatusCreated {
		t.Fatalf("owner create status = %d, body = %s", res.Code, res.Body.String())
	} else if status := reportStatus(t, res); status != "APPROVED" {
		t.Fatalf("owner create status field = %q, want APPROVED", status)
	}
	if res := create(otherToken, marker+" other"); res.Code != http.StatusForbidden {
		t.Fatalf("non-owner create status = %d, want 403; body = %s", res.Code, res.Body.String())
	}
	if res := create(adminToken, marker+" admin"); res.Code != http.StatusCreated {
		t.Fatalf("superadmin create status = %d, body = %s", res.Code, res.Body.String())
	}
}
