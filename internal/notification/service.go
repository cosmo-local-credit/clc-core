package notification

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

const (
	defaultTTLSeconds = 3600
	maxErrorBodyBytes = 4096
)

type Config struct {
	PublicKey  string
	PrivateKey string
	Subscriber string
	KeyVersion string
	TTLSeconds int
	HTTPClient webpush.HTTPClient
}

type Message struct {
	Endpoint string
	Auth     string
	P256dh   string
	Payload  []byte
	Priority string
	Topic    string
}

type SendResult struct {
	Status     string
	StatusCode int
	Error      string
	Retryable  bool
	Stale      bool
}

type Sender interface {
	Send(context.Context, Message) SendResult
}

type Service struct {
	publicKey  string
	privateKey string
	subscriber string
	keyVersion string
	ttlSeconds int
	httpClient webpush.HTTPClient
}

func New(config Config) (*Service, error) {
	config.PublicKey = strings.TrimSpace(config.PublicKey)
	config.PrivateKey = strings.TrimSpace(config.PrivateKey)
	config.Subscriber = normalizeSubscriber(config.Subscriber)
	config.KeyVersion = strings.TrimSpace(config.KeyVersion)

	if config.PublicKey == "" || config.PrivateKey == "" {
		return nil, errors.New("notification VAPID public and private keys are required")
	}
	if config.Subscriber == "" {
		return nil, errors.New("notification VAPID subscriber is required")
	}
	if config.KeyVersion == "" {
		return nil, errors.New("notification VAPID key version is required")
	}
	if config.TTLSeconds <= 0 {
		config.TTLSeconds = defaultTTLSeconds
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}

	return &Service{
		publicKey:  config.PublicKey,
		privateKey: config.PrivateKey,
		subscriber: config.Subscriber,
		keyVersion: config.KeyVersion,
		ttlSeconds: config.TTLSeconds,
		httpClient: config.HTTPClient,
	}, nil
}

// webpush-go adds the mailto: scheme for email subscribers. Deployment
// configuration uses the RFC-style mailto: value, so remove exactly one prefix
// before handing it to the library; HTTPS contact URLs pass through unchanged.
func normalizeSubscriber(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= len("mailto:") && strings.EqualFold(value[:len("mailto:")], "mailto:") {
		return strings.TrimSpace(value[len("mailto:"):])
	}
	return value
}

func (s *Service) PublicKey() string { return s.publicKey }

func (s *Service) KeyVersion() string { return s.keyVersion }

func (s *Service) Send(ctx context.Context, message Message) SendResult {
	subscription := webpush.Subscription{
		Endpoint: message.Endpoint,
		Keys: webpush.Keys{
			Auth:   message.Auth,
			P256dh: message.P256dh,
		},
	}

	response, err := webpush.SendNotificationWithContext(ctx, message.Payload, &subscription, &webpush.Options{
		AuthScheme:      authSchemeForEndpoint(message.Endpoint),
		HTTPClient:      s.httpClient,
		Subscriber:      s.subscriber,
		TTL:             s.ttlSeconds,
		Topic:           message.Topic,
		Urgency:         urgencyForPriority(message.Priority),
		VAPIDPublicKey:  s.publicKey,
		VAPIDPrivateKey: s.privateKey,
	})
	if err != nil {
		return SendResult{
			Status:    "send-error",
			Error:     err.Error(),
			Retryable: retryableTransportError(err),
		}
	}
	defer response.Body.Close()

	result := classifyStatus(response.StatusCode)
	if response.StatusCode >= http.StatusBadRequest {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
		if readErr != nil {
			result.Error = response.Status
		} else {
			result.Error = strings.TrimSpace(fmt.Sprintf("%s | body=%s", response.Status, body))
		}
	}
	return result
}

func classifyStatus(statusCode int) SendResult {
	result := SendResult{StatusCode: statusCode}
	switch {
	case statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices:
		result.Status = "sent"
	case statusCode == http.StatusNotFound || statusCode == http.StatusGone:
		result.Status = "stale-subscription"
		result.Stale = true
	case statusCode == http.StatusTooManyRequests || statusCode >= http.StatusInternalServerError:
		result.Status = "push-service-error"
		result.Retryable = true
	default:
		result.Status = "push-service-error"
	}
	return result
}

func retryableTransportError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func authSchemeForEndpoint(endpoint string) webpush.AuthScheme {
	endpoint = strings.ToLower(endpoint)
	if strings.Contains(endpoint, "fcm.googleapis.com") || strings.Contains(endpoint, "googleapis.com") {
		return webpush.WebPush
	}
	return webpush.Vapid
}

func urgencyForPriority(priority string) webpush.Urgency {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "very-low":
		return webpush.UrgencyVeryLow
	case "low":
		return webpush.UrgencyLow
	case "normal":
		return webpush.UrgencyNormal
	default:
		return webpush.UrgencyHigh
	}
}
