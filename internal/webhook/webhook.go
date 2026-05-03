// Package webhook implements outbound webhook delivery for clustr events (S4-2).
//
// Supported events:
//   deploy.complete      — a node deploy-complete callback was received
//   deploy.failed        — a node deploy-failed callback was received
//   verify_boot.timeout  — a node did not phone home within CLUSTR_VERIFY_TIMEOUT
//   image.ready          — a base image transitioned to "ready" status
//
// Each delivery is attempted up to 3 times with exponential back-off (1s, 2s, 4s).
// Every attempt (success or failure) is recorded in the webhook_deliveries table.
// The Prometheus counter clustr_webhook_deliveries_total{event,status} is incremented
// on each attempt.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/metrics"
)

// EventType enumerates the events that can trigger webhook deliveries.
type EventType string

const (
	EventDeployComplete    EventType = "deploy.complete"
	EventDeployFailed      EventType = "deploy.failed"
	EventVerifyBootTimeout EventType = "verify_boot.timeout"
	EventImageReady        EventType = "image.ready"
)

// Payload is the JSON body sent to each subscribed webhook URL.
type Payload struct {
	Event     string    `json:"event"`
	NodeID    string    `json:"node_id,omitempty"`
	ImageID   string    `json:"image_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Actor     string    `json:"actor,omitempty"`
}

// maxAttempts is the number of delivery attempts before giving up.
const maxAttempts = 3

// Dispatcher loads active webhook subscriptions from the database and fans
// out delivery goroutines for a given event. Each call to Dispatch is fire-and-
// forget; errors are logged, not propagated. The database write occurs inside
// the goroutine so Dispatch returns immediately.
type Dispatcher struct {
	DB     *db.DB
	Logger zerolog.Logger
	Client *http.Client
}

// New constructs a Dispatcher with a secure HTTP client (TLS 1.2+, 15s timeout).
func New(database *db.DB, logger zerolog.Logger) *Dispatcher {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12, // #nosec G402 — explicitly set, not too low
		},
	}
	return &Dispatcher{
		DB:     database,
		Logger: logger.With().Str("component", "webhook").Logger(),
		Client: &http.Client{
			Timeout:   15 * time.Second,
			Transport: transport,
		},
	}
}

// Dispatch fans out payload delivery to all webhook subscriptions for event.
// Non-blocking: each delivery runs in a background goroutine.
func (d *Dispatcher) Dispatch(ctx context.Context, event EventType, payload Payload) {
	payload.Event = string(event)
	payload.Timestamp = time.Now().UTC()

	subs, err := d.DB.ListWebhookSubscriptions(ctx, string(event))
	if err != nil {
		d.Logger.Warn().Err(err).Str("event", string(event)).Msg("dispatch: list subscriptions failed")
		return
	}
	for _, sub := range subs {
		go d.deliver(context.Background(), sub, string(event), payload)
	}
}

// deliver attempts delivery to a single subscription, retrying up to maxAttempts
// times with exponential back-off. Records each attempt in webhook_deliveries.
func (d *Dispatcher) deliver(ctx context.Context, sub db.WebhookSubscription, event string, payload Payload) {
	body, err := json.Marshal(payload)
	if err != nil {
		d.Logger.Error().Err(err).Str("webhook_id", sub.ID).Msg("deliver: marshal payload failed")
		return
	}

	backoff := time.Second
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		httpStatus, deliveryErr := d.post(ctx, sub, body)

		status := "success"
		errMsg := ""
		if deliveryErr != nil {
			status = "failed"
			errMsg = deliveryErr.Error()
		}

		recID := uuid.New().String()
		_ = d.DB.RecordWebhookDelivery(ctx, db.WebhookDelivery{
			ID:          recID,
			WebhookID:   sub.ID,
			Event:       event,
			PayloadJSON: string(body),
			Status:      status,
			HTTPStatus:  httpStatus,
			Attempt:     attempt,
			ErrorMsg:    errMsg,
			DeliveredAt: time.Now().UTC(),
		})

		metrics.WebhookDeliveries.WithLabelValues(event, status).Inc()

		if deliveryErr == nil {
			d.Logger.Info().
				Str("webhook_id", sub.ID).
				Str("event", event).
				Int("http_status", httpStatus).
				Int("attempt", attempt).
				Msg("webhook delivered successfully")
			return
		}

		d.Logger.Warn().
			Err(deliveryErr).
			Str("webhook_id", sub.ID).
			Str("event", event).
			Int("attempt", attempt).
			Msgf("webhook delivery failed (attempt %d/%d)", attempt, maxAttempts)

		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
		}
	}
}

// post sends the payload to sub.URL and returns the HTTP status code (0 on
// connection error) and any error. A non-2xx response is treated as a failure.
func (d *Dispatcher) post(ctx context.Context, sub db.WebhookSubscription, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "clustr-webhook/1.0")
	req.Header.Set("X-Clustr-Event", sub.ID)

	// Sign the payload with HMAC-SHA256 if a secret is configured.
	if sub.Secret != "" {
		mac := hmac.New(sha256.New, []byte(sub.Secret))
		mac.Write(body)
		sig := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-Clustr-Signature", "sha256="+sig)
	}

	resp, err := d.Client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("non-2xx response: %d %s", resp.StatusCode, resp.Status)
	}
	return resp.StatusCode, nil
}
