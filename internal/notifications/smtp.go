// Package notifications provides the SMTP email notification infrastructure
// for clustr. All sends are best-effort: if SMTP is not configured, events
// are logged as skipped. No notification failure ever blocks the primary workflow.
//
// Configuration (env vars take precedence over DB values):
//
//	CLUSTR_SMTP_HOST     — SMTP server hostname
//	CLUSTR_SMTP_PORT     — SMTP server port (default 587)
//	CLUSTR_SMTP_USER     — SMTP username
//	CLUSTR_SMTP_PASS     — SMTP password (plaintext env; encrypted in DB)
//	CLUSTR_SMTP_FROM     — From address (e.g. "clustr <noreply@example.com>")
//	CLUSTR_SMTP_USE_TLS  — "true" for STARTTLS (default), "false" for plain
//	CLUSTR_SMTP_USE_SSL  — "true" for implicit TLS on port 465
package notifications

import (
	"bytes"
	"context"
	"crypto/tls"
	"embed"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/rs/zerolog/log"
)

//go:embed templates/*.txt
var templateFS embed.FS

// templateCache caches parsed templates.
var (
	tmplOnce  sync.Once
	tmplCache map[string]*template.Template
)

func loadTemplates() map[string]*template.Template {
	tmplOnce.Do(func() {
		cache := make(map[string]*template.Template)
		entries, err := templateFS.ReadDir("templates")
		if err != nil {
			log.Error().Err(err).Msg("notifications: failed to read template dir")
			return
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
				continue
			}
			data, err := templateFS.ReadFile("templates/" + e.Name())
			if err != nil {
				log.Error().Err(err).Str("file", e.Name()).Msg("notifications: failed to read template")
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".txt")
			t, err := template.New(name).Parse(string(data))
			if err != nil {
				log.Error().Err(err).Str("name", name).Msg("notifications: failed to parse template")
				continue
			}
			cache[name] = t
		}
		tmplCache = cache
	})
	return tmplCache
}

// SMTPConfig holds the resolved SMTP configuration.
// Env vars override DB values.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string // plaintext, never logged
	From     string
	UseTLS   bool // STARTTLS
	UseSSL   bool // implicit TLS (port 465)
}

// IsConfigured reports whether the SMTP config has enough info to send mail.
func (c SMTPConfig) IsConfigured() bool {
	return c.Host != "" && c.From != ""
}

// Mailer is the notification sender interface.
// The real implementation uses SMTP; tests inject a fake.
type Mailer interface {
	// Send sends a plain-text email to one or more recipients.
	// Returns nil on success. Callers should log but not fail on error.
	Send(ctx context.Context, to []string, subject, body string) error
	// IsConfigured reports whether SMTP is configured.
	IsConfigured() bool
}

// SMTPMailer implements Mailer using net/smtp.
type SMTPMailer struct {
	cfg SMTPConfig
}

// NewSMTPMailer constructs a Mailer from env vars + db config.
// The dbCfg values are used as fallback when env vars are not set.
func NewSMTPMailer(dbCfg SMTPConfig) *SMTPMailer {
	cfg := resolveConfig(dbCfg)
	return &SMTPMailer{cfg: cfg}
}

// resolveConfig merges env vars (priority) with db config (fallback).
func resolveConfig(dbCfg SMTPConfig) SMTPConfig {
	cfg := dbCfg
	if h := os.Getenv("CLUSTR_SMTP_HOST"); h != "" {
		cfg.Host = h
	}
	if p := os.Getenv("CLUSTR_SMTP_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			cfg.Port = n
		}
	}
	if u := os.Getenv("CLUSTR_SMTP_USER"); u != "" {
		cfg.Username = u
	}
	if pw := os.Getenv("CLUSTR_SMTP_PASS"); pw != "" {
		cfg.Password = pw
	}
	if f := os.Getenv("CLUSTR_SMTP_FROM"); f != "" {
		cfg.From = f
	}
	if tls := os.Getenv("CLUSTR_SMTP_USE_TLS"); tls != "" {
		cfg.UseTLS = tls == "true" || tls == "1"
	}
	if ssl := os.Getenv("CLUSTR_SMTP_USE_SSL"); ssl != "" {
		cfg.UseSSL = ssl == "true" || ssl == "1"
	}
	if cfg.Port == 0 {
		if cfg.UseSSL {
			cfg.Port = 465
		} else {
			cfg.Port = 587
		}
	}
	return cfg
}

// IsConfigured reports whether SMTP is configured.
func (m *SMTPMailer) IsConfigured() bool {
	return m.cfg.IsConfigured()
}

// Send sends a plain-text email. Returns error on SMTP failure.
// Never logs credentials.
func (m *SMTPMailer) Send(ctx context.Context, to []string, subject, body string) error {
	if !m.cfg.IsConfigured() {
		return fmt.Errorf("smtp not configured")
	}

	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))

	var msg bytes.Buffer
	msg.WriteString("From: " + m.cfg.From + "\r\n")
	msg.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	msg.WriteString("Subject: " + subject + "\r\n")
	msg.WriteString("Date: " + time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700") + "\r\n")
	msg.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(body)

	if m.cfg.UseSSL {
		return m.sendImplicitTLS(addr, to, msg.Bytes())
	}
	return m.sendSMTP(addr, to, msg.Bytes())
}

func (m *SMTPMailer) sendSMTP(addr string, to []string, msg []byte) error {
	var auth smtp.Auth
	if m.cfg.Username != "" {
		auth = smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
	}

	if m.cfg.UseTLS {
		c, err := smtp.Dial(addr)
		if err != nil {
			return fmt.Errorf("smtp: dial: %w", err)
		}
		defer c.Close()
		if err := c.StartTLS(&tls.Config{ServerName: m.cfg.Host}); err != nil { //#nosec G402 -- operator-configured; TLS used
			return fmt.Errorf("smtp: starttls: %w", err)
		}
		if auth != nil {
			if err := c.Auth(auth); err != nil {
				return fmt.Errorf("smtp: auth: %w", err)
			}
		}
		if err := c.Mail(m.cfg.From); err != nil {
			return fmt.Errorf("smtp: mail from: %w", err)
		}
		for _, r := range to {
			if err := c.Rcpt(r); err != nil {
				return fmt.Errorf("smtp: rcpt %s: %w", r, err)
			}
		}
		w, err := c.Data()
		if err != nil {
			return fmt.Errorf("smtp: data: %w", err)
		}
		if _, err := w.Write(msg); err != nil {
			return fmt.Errorf("smtp: write: %w", err)
		}
		return w.Close()
	}

	return smtp.SendMail(addr, auth, m.cfg.From, to, msg)
}

func (m *SMTPMailer) sendImplicitTLS(addr string, to []string, msg []byte) error {
	tlsCfg := &tls.Config{ServerName: m.cfg.Host} //#nosec G402 -- operator-configured host
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("smtp: tls dial: %w", err)
	}

	var auth smtp.Auth
	if m.cfg.Username != "" {
		auth = smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
	}
	c, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp: new client: %w", err)
	}
	defer c.Close()
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp: auth: %w", err)
		}
	}
	if err := c.Mail(m.cfg.From); err != nil {
		return fmt.Errorf("smtp: mail from: %w", err)
	}
	for _, r := range to {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("smtp: rcpt %s: %w", r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp: data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp: write: %w", err)
	}
	return w.Close()
}

// Notifier dispatches named notification events to the Mailer.
// All sends are best-effort: errors are logged but never returned to callers.
// If SMTP is not configured, a skip entry is recorded instead.
type Notifier struct {
	Mailer Mailer
	Audit  AuditRecorder
}

// AuditRecorder writes audit log entries for notification events.
type AuditRecorder interface {
	Record(ctx context.Context, actorID, actorLabel, action, resourceType, resourceID, ipAddr string, oldVal, newVal interface{})
}

// renderTemplate renders the named template with data. Returns the rendered
// string, or a fallback plain message if the template is missing/broken.
func renderTemplate(name string, data interface{}) string {
	cache := loadTemplates()
	t, ok := cache[name]
	if !ok {
		return fmt.Sprintf("[clustr notification: template %q not found]", name)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		log.Warn().Err(err).Str("template", name).Msg("notifications: template render failed")
		return fmt.Sprintf("[clustr notification: template %q render error]", name)
	}
	return buf.String()
}

// send is the internal dispatcher. It renders the template, sends the email,
// and records the result in the audit log.
func (n *Notifier) send(ctx context.Context, eventName string, to []string, subject string, tmplName string, data interface{}) {
	body := renderTemplate(tmplName, data)

	if n.Mailer == nil || !n.Mailer.IsConfigured() {
		log.Info().
			Str("event", eventName).
			Strs("to", to).
			Msg("[email skipped: SMTP not configured]")
		if n.Audit != nil {
			n.Audit.Record(ctx, "system", "clustr", "notification.skipped",
				"notification", eventName, "",
				nil, map[string]interface{}{"event": eventName, "to": to, "reason": "smtp_not_configured"})
		}
		return
	}

	if err := n.Mailer.Send(ctx, to, subject, body); err != nil {
		log.Error().Err(err).Str("event", eventName).Strs("to", to).Msg("notification: send failed")
		if n.Audit != nil {
			n.Audit.Record(ctx, "system", "clustr", "notification.failed",
				"notification", eventName, "",
				nil, map[string]interface{}{"event": eventName, "to": to, "error": err.Error()})
		}
		return
	}

	log.Info().Str("event", eventName).Strs("to", to).Msg("notification: sent")
	if n.Audit != nil {
		n.Audit.Record(ctx, "system", "clustr", "notification.sent",
			"notification", eventName, "",
			nil, map[string]interface{}{"event": eventName, "to": to, "subject": subject})
	}
}

// ─── Event dispatchers ────────────────────────────────────────────────────────

// LDAPAccountCreatedData is the template data for ldap_account_created.
type LDAPAccountCreatedData struct {
	Username    string
	DisplayName string
	ClusterName string
}

// NotifyLDAPAccountCreated sends the ldap_account_created notification.
func (n *Notifier) NotifyLDAPAccountCreated(ctx context.Context, to, username, displayName, clusterName string) {
	n.send(ctx, "ldap_account_created", []string{to},
		"Your HPC cluster account has been created",
		"ldap_account_created",
		LDAPAccountCreatedData{Username: username, DisplayName: displayName, ClusterName: clusterName})
}

// NodeGroupMembershipData is the template data for membership add/remove events.
type NodeGroupMembershipData struct {
	Username  string
	GroupName string
	PIName    string
	Action    string // "added" or "removed"
}

// NotifyMemberAdded sends the nodegroup_membership_added notification.
func (n *Notifier) NotifyMemberAdded(ctx context.Context, to, username, groupName, piName string) {
	n.send(ctx, "nodegroup_membership_added", []string{to},
		"You have been added to "+groupName,
		"nodegroup_membership_added",
		NodeGroupMembershipData{Username: username, GroupName: groupName, PIName: piName, Action: "added"})
}

// NotifyMemberRemoved sends the nodegroup_membership_removed notification.
func (n *Notifier) NotifyMemberRemoved(ctx context.Context, to, username, groupName, piName string) {
	n.send(ctx, "nodegroup_membership_removed", []string{to},
		"You have been removed from "+groupName,
		"nodegroup_membership_removed",
		NodeGroupMembershipData{Username: username, GroupName: groupName, PIName: piName, Action: "removed"})
}

// PIRequestData is the template data for PI request approved/denied events.
type PIRequestData struct {
	Username    string
	GroupName   string
	Action      string // "approved" or "denied"
	AdminName   string
}

// NotifyPIRequestApproved sends the pi_request_approved notification to the PI.
func (n *Notifier) NotifyPIRequestApproved(ctx context.Context, to, username, groupName, adminName string) {
	n.send(ctx, "pi_request_approved", []string{to},
		"Member request approved: "+username+" → "+groupName,
		"pi_request_approved",
		PIRequestData{Username: username, GroupName: groupName, Action: "approved", AdminName: adminName})
}

// NotifyPIRequestDenied sends the pi_request_denied notification to the PI.
func (n *Notifier) NotifyPIRequestDenied(ctx context.Context, to, username, groupName, adminName string) {
	n.send(ctx, "pi_request_denied", []string{to},
		"Member request denied: "+username+" → "+groupName,
		"pi_request_denied",
		PIRequestData{Username: username, GroupName: groupName, Action: "denied", AdminName: adminName})
}

// AnnualReviewData is the template data for the annual review notification.
type AnnualReviewData struct {
	PIName      string
	GroupName   string
	Deadline    string
	ReviewURL   string
}

// NotifyAnnualReview sends the annual_review notification to a PI.
func (n *Notifier) NotifyAnnualReview(ctx context.Context, to, piName, groupName, deadline, reviewURL string) {
	n.send(ctx, "annual_review", []string{to},
		"Annual review required: "+groupName,
		"annual_review",
		AnnualReviewData{PIName: piName, GroupName: groupName, Deadline: deadline, ReviewURL: reviewURL})
}

// AnnualReviewSubmittedData is the template data for admin notification on review submission.
type AnnualReviewSubmittedData struct {
	PIName    string
	GroupName string
	Status    string
	Notes     string
}

// NotifyAnnualReviewSubmitted sends a notification to admins when a PI submits a review.
func (n *Notifier) NotifyAnnualReviewSubmitted(ctx context.Context, to []string, piName, groupName, status, notes string) {
	n.send(ctx, "annual_review_submitted", to,
		"Annual review submitted: "+groupName+" ("+status+")",
		"annual_review_submitted",
		AnnualReviewSubmittedData{PIName: piName, GroupName: groupName, Status: status, Notes: notes})
}

// BroadcastData is used for admin→NodeGroup broadcast messages.
type BroadcastData struct {
	Subject   string
	Body      string
	AdminName string
	GroupName string
}

// SendBroadcast sends a broadcast message to all provided recipients.
func (n *Notifier) SendBroadcast(ctx context.Context, to []string, subject, body, adminName, groupName string) error {
	if n.Mailer == nil || !n.Mailer.IsConfigured() {
		log.Info().Strs("to", to).Msg("[broadcast skipped: SMTP not configured]")
		if n.Audit != nil {
			n.Audit.Record(ctx, "system", "clustr", "broadcast.skipped",
				"node_group", groupName, "",
				nil, map[string]interface{}{"reason": "smtp_not_configured", "to_count": len(to)})
		}
		return fmt.Errorf("SMTP not configured")
	}

	if err := n.Mailer.Send(ctx, to, subject, body); err != nil {
		log.Error().Err(err).Str("group", groupName).Msg("broadcast: send failed")
		return err
	}

	log.Info().Str("group", groupName).Int("recipients", len(to)).Msg("broadcast: sent")
	return nil
}
