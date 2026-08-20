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
		Tags:        []string{"Youth", " Agriculture ", "Youth"},
		Vouchers: []apiresp.ReportVoucherRefRequest{
			{ChainID: 42220, Address: reportOtherAddress},
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
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, _, err := normalizeReportWrite(testCase.req); err == nil {
				t.Fatal("normalizeReportWrite() error = nil")
			}
		})
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
	if id == 0 || createEnvelope.Result.Report.AuthorAddress != reportAuthorAddress ||
		createEnvelope.Result.Report.Status != "SUBMITTED" {
		t.Fatalf("created report = %+v", createEnvelope.Result.Report)
	}
	path := "/api/v2/reports/" + strconv.FormatUint(id, 10)

	if res := call(http.MethodPut, path, otherToken, marker+" other"); res.Code != http.StatusForbidden {
		t.Fatalf("other edit status = %d, body = %s", res.Code, res.Body.String())
	}
	if res := call(http.MethodPut, path, authorToken, marker+" pending"); res.Code != http.StatusOK {
		t.Fatalf("author edit status = %d, body = %s", res.Code, res.Body.String())
	} else if status := reportStatus(t, res); status != "SUBMITTED" {
		t.Fatalf("status after pending author edit = %q, want SUBMITTED", status)
	}
	if res := call(http.MethodGet, path, authorToken, ""); res.Code != http.StatusNotFound {
		t.Fatalf("submitted detail status = %d, want 404; body = %s", res.Code, res.Body.String())
	}
	if res := call(http.MethodDelete, path, authorToken, ""); res.Code != http.StatusForbidden {
		t.Fatalf("author delete status = %d, body = %s", res.Code, res.Body.String())
	}
	if res := call(http.MethodPut, path+"/approve", adminToken, ""); res.Code != http.StatusOK {
		t.Fatalf("admin approve status = %d, body = %s", res.Code, res.Body.String())
	} else if status := reportStatus(t, res); status != "APPROVED" {
		t.Fatalf("approved status = %q, want APPROVED", status)
	}
	if res := call(http.MethodPut, path, authorToken, marker+" approved author edit"); res.Code != http.StatusOK {
		t.Fatalf("approved author edit status = %d, body = %s", res.Code, res.Body.String())
	} else if status := reportStatus(t, res); status != "APPROVED" {
		t.Fatalf("status after approved author edit = %q, want APPROVED", status)
	}
	if res := call(http.MethodGet, path, authorToken, ""); res.Code != http.StatusOK {
		t.Fatalf("approved detail status = %d, body = %s", res.Code, res.Body.String())
	}
	if res := call(http.MethodPut, path, adminToken, marker+" admin"); res.Code != http.StatusOK {
		t.Fatalf("admin edit status = %d, body = %s", res.Code, res.Body.String())
	}
	if res := call(http.MethodDelete, path, adminToken, ""); res.Code != http.StatusOK {
		t.Fatalf("admin delete status = %d, body = %s", res.Code, res.Body.String())
	}

	adminCreated := call(http.MethodPost, "/api/v2/reports", adminToken, marker+" admin created")
	if adminCreated.Code != http.StatusCreated || reportStatus(t, adminCreated) != "APPROVED" {
		t.Fatalf("admin create status = %d, body = %s", adminCreated.Code, adminCreated.Body.String())
	}
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
