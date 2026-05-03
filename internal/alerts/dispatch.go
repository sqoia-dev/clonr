package alerts

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/notifications"
	"github.com/sqoia-dev/clustr/internal/webhook"
)

// mailJob is an envelope for a single SMTP delivery task queued by Fire or Resolve.
type mailJob struct {
	to      []string
	subject string
	body    string
	// contextual fields for structured logging only.
	ruleName string
	nodeID   string
	event    string
}

// Dispatcher routes alert fire/resolve events to the existing webhook and SMTP
// dispatchers. Both are optional: if either is nil or unconfigured, the event
// is logged and skipped — the engine never fails because of a missing notifier.
//
// THREAD-SAFETY: Fire and Resolve are non-blocking and safe for concurrent use.
// SMTP delivery is handled by a fixed pool of background worker goroutines owned
// by this Dispatcher. Workers are started via Start(ctx) and exit when ctx is
// cancelled. mailQueue overflow drops the job and emits a Warn log — no SMTP
// send is ever performed on the engine tick goroutine.
type Dispatcher struct {
	Webhook *webhook.Dispatcher  // may be nil
	Mailer  notifications.Mailer // may be nil

	mailQueue chan mailJob
}

const (
	mailQueueCapacity = 256
	mailWorkerCount   = 4
)

// Start launches the SMTP worker pool. Call this once from clustr-serverd's
// startup, after the Dispatcher is constructed but before Engine.Run is called.
// Workers run until ctx is cancelled; they drain any in-flight jobs before
// exiting. Start is not safe to call more than once on the same Dispatcher.
func (d *Dispatcher) Start(ctx context.Context) {
	if d == nil {
		return
	}
	d.mailQueue = make(chan mailJob, mailQueueCapacity)
	for i := 0; i < mailWorkerCount; i++ {
		go d.mailWorker(ctx)
	}
}

// Stop closes the mailQueue channel, signalling workers to drain and exit.
// Optional: ctx cancellation (passed to Start) is the primary shutdown signal.
// Do not call Stop if Start was not called.
func (d *Dispatcher) Stop() {
	if d == nil || d.mailQueue == nil {
		return
	}
	close(d.mailQueue)
}

// mailWorker drains mailQueue until the channel is closed or ctx is cancelled.
func (d *Dispatcher) mailWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-d.mailQueue:
			if !ok {
				return
			}
			// Use a background context for delivery so a single slow MX does not
			// propagate cancellation to unrelated jobs.
			sendCtx := context.Background()
			if err := d.Mailer.Send(sendCtx, job.to, job.subject, job.body); err != nil {
				log.Error().
					Err(err).
					Str("alert_rule", job.ruleName).
					Str("node_id", job.nodeID).
					Strs("to", job.to).
					Msg("alerts: email send failed")
				continue
			}
			log.Info().
				Str("alert_rule", job.ruleName).
				Str("node_id", job.nodeID).
				Strs("to", job.to).
				Str("event", job.event).
				Msg("alerts: email sent")
		}
	}
}

// alertWebhookPayload extends webhook.Payload with alert-specific fields.
// Delivered as the JSON body to webhook subscribers.
type alertWebhookPayload struct {
	Event        string     `json:"event"`
	AlertID      int64      `json:"alert_id"`
	Rule         string     `json:"rule"`
	NodeID       string     `json:"node_id"`
	Sensor       string     `json:"sensor"`
	Severity     string     `json:"severity"`
	Value        float64    `json:"value"`
	ThresholdOp  string     `json:"threshold_op"`
	ThresholdVal float64    `json:"threshold_val"`
	FiredAt      time.Time  `json:"fired_at"`
	ResolvedAt   *time.Time `json:"resolved_at,omitempty"`
	Timestamp    time.Time  `json:"timestamp"`
}

// Fire dispatches a firing notification for alert a using the rule's notify config.
// Non-blocking: SMTP jobs are queued; webhook delivery is fire-and-forget.
func (d *Dispatcher) Fire(ctx context.Context, r *Rule, a *Alert) {
	if d == nil {
		return
	}
	d.dispatch(ctx, "alert.firing", r, a)
}

// Resolve dispatches a resolution notification for alert a.
// Non-blocking: SMTP jobs are queued; webhook delivery is fire-and-forget.
func (d *Dispatcher) Resolve(ctx context.Context, r *Rule, a *Alert) {
	if d == nil {
		return
	}
	d.dispatch(ctx, "alert.resolved", r, a)
}

func (d *Dispatcher) dispatch(ctx context.Context, event string, r *Rule, a *Alert) {
	// Webhook — if configured and rule requests it.
	// Webhook delivery is already asynchronous inside the webhook package.
	if r.Notify.Webhook && d.Webhook != nil {
		wpl := webhook.Payload{
			Event:     event,
			NodeID:    a.NodeID,
			Timestamp: time.Now().UTC(),
		}
		// Dispatch is fire-and-forget; the webhook package handles retries.
		d.Webhook.Dispatch(ctx, webhook.EventType(event), wpl)
		log.Debug().
			Str("alert_rule", r.Name).
			Str("node_id", a.NodeID).
			Str("event", event).
			Msg("alerts: webhook dispatched")
	}

	// SMTP — enqueue to the worker pool; never block the engine tick goroutine.
	if len(r.Notify.Email) > 0 {
		if d.Mailer == nil || !d.Mailer.IsConfigured() {
			log.Info().
				Str("alert_rule", r.Name).
				Str("node_id", a.NodeID).
				Msg("alerts: SMTP not configured — email notification skipped")
			return
		}
		// If Start() was never called (e.g. in tests that don't need delivery),
		// fall back to a direct synchronous send rather than panicking.
		if d.mailQueue == nil {
			subject := fmt.Sprintf("[clustr] %s: %s on %s", r.Severity, r.Name, a.NodeID)
			body := d.buildEmailBody(event, r, a)
			if err := d.Mailer.Send(ctx, r.Notify.Email, subject, body); err != nil {
				log.Error().Err(err).Str("alert_rule", r.Name).Str("node_id", a.NodeID).
					Strs("to", r.Notify.Email).Msg("alerts: email send failed (no worker pool)")
			}
			return
		}

		subject := fmt.Sprintf("[clustr] %s: %s on %s", r.Severity, r.Name, a.NodeID)
		body := d.buildEmailBody(event, r, a)
		job := mailJob{
			to:       r.Notify.Email,
			subject:  subject,
			body:     body,
			ruleName: r.Name,
			nodeID:   a.NodeID,
			event:    event,
		}
		select {
		case d.mailQueue <- job:
		default:
			log.Warn().
				Str("alert_rule", r.Name).
				Str("node_id", a.NodeID).
				Msg("alerts: mail queue full, dropping email notification")
		}
	}
}

// buildEmailBody renders a plain-text email body for a fire/resolve event.
func (d *Dispatcher) buildEmailBody(event string, r *Rule, a *Alert) string {
	var sb strings.Builder

	stateWord := "FIRING"
	if event == "alert.resolved" {
		stateWord = "RESOLVED"
	}

	sb.WriteString(fmt.Sprintf("clustr alert — %s\n", stateWord))
	sb.WriteString(strings.Repeat("-", 40) + "\n\n")
	sb.WriteString(fmt.Sprintf("Rule:      %s\n", r.Name))
	if r.Description != "" {
		sb.WriteString(fmt.Sprintf("           %s\n", r.Description))
	}
	sb.WriteString(fmt.Sprintf("Node:      %s\n", a.NodeID))
	sb.WriteString(fmt.Sprintf("Severity:  %s\n", a.Severity))
	sb.WriteString(fmt.Sprintf("Sensor:    %s\n", a.Sensor))
	sb.WriteString(fmt.Sprintf("Value:     %.4g\n", a.LastValue))
	sb.WriteString(fmt.Sprintf("Threshold: %s %.4g\n", a.ThresholdOp, a.ThresholdVal))
	sb.WriteString(fmt.Sprintf("Fired at:  %s\n", a.FiredAt.Format(time.RFC3339)))
	if a.ResolvedAt != nil {
		sb.WriteString(fmt.Sprintf("Resolved:  %s\n", a.ResolvedAt.Format(time.RFC3339)))
	}
	if len(a.Labels) > 0 {
		sb.WriteString("Labels:    ")
		for k, v := range a.Labels {
			sb.WriteString(fmt.Sprintf("%s=%s ", k, v))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n-- clustr\n")
	return sb.String()
}
