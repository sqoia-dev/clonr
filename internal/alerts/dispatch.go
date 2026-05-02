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

// Dispatcher routes alert fire/resolve events to the existing webhook and SMTP
// dispatchers.  Both are optional: if either is nil or unconfigured, the event
// is logged and skipped — the engine never fails because of a missing notifier.
type Dispatcher struct {
	Webhook *webhook.Dispatcher        // may be nil
	Mailer  notifications.Mailer       // may be nil
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
func (d *Dispatcher) Fire(ctx context.Context, r *Rule, a *Alert) {
	if d == nil {
		return
	}
	d.dispatch(ctx, "alert.firing", r, a)
}

// Resolve dispatches a resolution notification for alert a.
func (d *Dispatcher) Resolve(ctx context.Context, r *Rule, a *Alert) {
	if d == nil {
		return
	}
	d.dispatch(ctx, "alert.resolved", r, a)
}

func (d *Dispatcher) dispatch(ctx context.Context, event string, r *Rule, a *Alert) {
	// Webhook — if configured and rule requests it.
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

	// SMTP — send to every address listed in notify.email.
	if len(r.Notify.Email) > 0 {
		if d.Mailer == nil || !d.Mailer.IsConfigured() {
			log.Info().
				Str("alert_rule", r.Name).
				Str("node_id", a.NodeID).
				Msg("alerts: SMTP not configured — email notification skipped")
			return
		}
		subject := fmt.Sprintf("[clustr] %s: %s on %s", r.Severity, r.Name, a.NodeID)
		body := d.buildEmailBody(event, r, a)
		if err := d.Mailer.Send(ctx, r.Notify.Email, subject, body); err != nil {
			log.Error().
				Err(err).
				Str("alert_rule", r.Name).
				Str("node_id", a.NodeID).
				Strs("to", r.Notify.Email).
				Msg("alerts: email send failed")
			return
		}
		log.Info().
			Str("alert_rule", r.Name).
			Str("node_id", a.NodeID).
			Strs("to", r.Notify.Email).
			Str("event", event).
			Msg("alerts: email sent")
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
