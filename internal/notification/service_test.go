package notification

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/golang-jwt/jwt/v5"
)

const (
	testSubscriptionP256dh = "BNNL5ZaTfK81qhXOx23-wewhigUeFb632jN6LvRWCFH1ubQr77FE_9qV1FuojuRmHP42zmf34rXgW80OvUVDgTk"
	testSubscriptionAuth   = "zqbxT6JKstKSY9JKibZLSQ"
)

type recordingHTTPClient struct {
	body         []byte
	request      *http.Request
	responseBody string
	statusCode   int
}

func (client *recordingHTTPClient) Do(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	client.body = body
	client.request = request.Clone(request.Context())

	return &http.Response{
		Status:     fmt.Sprintf("%d %s", client.statusCode, http.StatusText(client.statusCode)),
		StatusCode: client.statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(client.responseBody)),
		Request:    request,
	}, nil
}

func TestNewRequiresStableVAPIDConfiguration(t *testing.T) {
	for name, config := range map[string]Config{
		"missing keys":       {Subscriber: "mailto:ops@example.com", KeyVersion: "v1"},
		"missing subscriber": {PublicKey: "public", PrivateKey: "private", KeyVersion: "v1"},
		"missing version":    {PublicKey: "public", PrivateKey: "private", Subscriber: "mailto:ops@example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(config); err == nil {
				t.Fatal("expected incomplete VAPID configuration to fail")
			}
		})
	}
}

func TestClassifyStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		code      int
		status    string
		retryable bool
		stale     bool
	}{
		"accepted":      {code: http.StatusCreated, status: "sent"},
		"gone":          {code: http.StatusGone, status: "stale-subscription", stale: true},
		"rate limited":  {code: http.StatusTooManyRequests, status: "push-service-error", retryable: true},
		"provider down": {code: http.StatusBadGateway, status: "push-service-error", retryable: true},
		"bad request":   {code: http.StatusBadRequest, status: "push-service-error"},
	} {
		t.Run(name, func(t *testing.T) {
			got := classifyStatus(tc.code)
			if got.Status != tc.status || got.Retryable != tc.retryable || got.Stale != tc.stale {
				t.Fatalf("classifyStatus(%d) = %+v", tc.code, got)
			}
		})
	}
}

func TestSendBuildsAuthenticatedEncryptedWebPushRequest(t *testing.T) {
	client := &recordingHTTPClient{statusCode: http.StatusCreated}
	service := newTestNotificationService(t, client, "mailto:ops@example.com")
	payload := []byte(`{"notificationId":"notification-1","title":"Voucher received"}`)

	result := service.Send(context.Background(), Message{
		Endpoint: "https://push.example.test/subscriptions/device-1",
		Auth:     testSubscriptionAuth,
		P256dh:   testSubscriptionP256dh,
		Payload:  payload,
		Priority: "high",
		Topic:    "notification1",
	})

	if result.Status != "sent" || result.StatusCode != http.StatusCreated {
		t.Fatalf("Send() = %+v", result)
	}
	if client.request == nil {
		t.Fatal("Send() did not call the push provider")
	}
	if client.request.Method != http.MethodPost {
		t.Fatalf("method = %q, want POST", client.request.Method)
	}
	if client.request.URL.String() != "https://push.example.test/subscriptions/device-1" {
		t.Fatalf("endpoint = %q", client.request.URL)
	}
	for header, want := range map[string]string{
		"Content-Encoding": "aes128gcm",
		"Content-Type":     "application/octet-stream",
		"Topic":            "notification1",
		"TTL":              "3600",
		"Urgency":          "high",
	} {
		if got := client.request.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if len(client.body) == 0 {
		t.Fatal("provider request body is empty")
	}
	if strings.Contains(string(client.body), "Voucher received") {
		t.Fatal("provider request contains the plaintext notification")
	}

	authorization := client.request.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "vapid t=") {
		t.Fatalf("Authorization = %q, want VAPID authorization", authorization)
	}
	tokenText := strings.SplitN(strings.TrimPrefix(authorization, "vapid t="), ",", 2)[0]
	token, _, err := jwt.NewParser().ParseUnverified(tokenText, jwt.MapClaims{})
	if err != nil {
		t.Fatalf("parse VAPID JWT: %v", err)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("unexpected VAPID claims type %T", token.Claims)
	}
	if got := claims["sub"]; got != "mailto:ops@example.com" {
		t.Fatalf("VAPID subscriber = %q, want one mailto prefix", got)
	}
}

func TestSendReturnsProviderFailureDetails(t *testing.T) {
	client := &recordingHTTPClient{
		statusCode:   http.StatusGone,
		responseBody: "subscription expired",
	}
	service := newTestNotificationService(t, client, "ops@example.com")

	result := service.Send(context.Background(), Message{
		Endpoint: "https://push.example.test/subscriptions/stale",
		Auth:     testSubscriptionAuth,
		P256dh:   testSubscriptionP256dh,
		Payload:  []byte(`{"notificationId":"notification-stale"}`),
	})

	if result.Status != "stale-subscription" || !result.Stale || result.Retryable {
		t.Fatalf("Send() = %+v", result)
	}
	if !strings.Contains(result.Error, "subscription expired") {
		t.Fatalf("error = %q, want provider response body", result.Error)
	}
}

func newTestNotificationService(
	t *testing.T,
	client webpush.HTTPClient,
	subscriber string,
) *Service {
	t.Helper()

	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("generate VAPID keys: %v", err)
	}
	service, err := New(Config{
		PublicKey:  publicKey,
		PrivateKey: privateKey,
		Subscriber: subscriber,
		KeyVersion: "v1",
		HTTPClient: client,
	})
	if err != nil {
		t.Fatalf("create notification service: %v", err)
	}
	return service
}
