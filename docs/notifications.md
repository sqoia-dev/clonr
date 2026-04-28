# Notifications — SMTP Setup and Event Reference

clustr can send email notifications on key cluster events. Email delivery is
best-effort: SMTP failures are logged but never block the primary workflow. If
SMTP is not configured, events are recorded in the audit log as
`notification.skipped`.

---

## Configuration

SMTP settings can be configured via the admin UI (Settings → Notifications) or
environment variables. **Environment variables always take precedence over the
database values.** This lets operators override DB values in container
deployments without running a migration.

### Environment variables

| Variable             | Default      | Description                                    |
|----------------------|--------------|------------------------------------------------|
| `CLUSTR_SMTP_HOST`   | —            | SMTP server hostname (required)                |
| `CLUSTR_SMTP_PORT`   | 587 (or 465) | SMTP port                                      |
| `CLUSTR_SMTP_USER`   | —            | SMTP username                                  |
| `CLUSTR_SMTP_PASS`   | —            | SMTP password (plaintext; encrypted in DB)     |
| `CLUSTR_SMTP_FROM`   | —            | From address, e.g. `clustr <noreply@hpc.edu>`  |
| `CLUSTR_SMTP_USE_TLS`| `false`      | `true` to enable STARTTLS (port 587 typical)   |
| `CLUSTR_SMTP_USE_SSL`| `false`      | `true` for implicit TLS (port 465 typical)     |

The password is stored AES-256-GCM encrypted in the database (via the
`CLUSTR_SECRET_KEY` master key). It is never returned in GET responses.

### Admin UI

Navigate to **Settings → Notifications** as an admin user. Fill in the SMTP
fields and click **Save SMTP settings**. Use **Send test email** to verify
delivery before going live. The test email is sent to the configured From
address.

### Docker Compose example

```yaml
environment:
  CLUSTR_SMTP_HOST: smtp.example.com
  CLUSTR_SMTP_PORT: "587"
  CLUSTR_SMTP_USER: noreply@example.com
  CLUSTR_SMTP_PASS: supersecret
  CLUSTR_SMTP_FROM: "clustr <noreply@example.com>"
  CLUSTR_SMTP_USE_TLS: "true"
```

---

## Event types

| Event                     | Recipient      | Trigger                                            |
|---------------------------|----------------|----------------------------------------------------|
| `ldap_account_created`    | New user       | LDAP account is provisioned by sysaccounts module  |
| `nodegroup_membership_added` | Member      | PI adds a member (auto-approve path) or admin approves a pending request |
| `nodegroup_membership_removed` | Member    | PI removes a member from their NodeGroup           |
| `pi_request_approved`     | PI             | Admin approves a pending member request            |
| `pi_request_denied`       | PI             | Admin denies a pending member request              |
| `annual_review`           | PI             | Admin creates a review cycle (future — not yet auto-sent; PI portal shows the review UI) |
| `annual_review_submitted` | Admins         | PI submits affirm/archive response to a review     |
| `broadcast`               | Group members  | Admin sends a broadcast to a NodeGroup             |

All event recipients use the user's **clustr username** as the email address.
For deployments where usernames are not email addresses (e.g. `jsmith`), set
up a local mail alias or configure LDAP email attribute lookup (planned for v1.4).

---

## Broadcast

Admins can send a message to all approved members of a NodeGroup via the
Notifications settings tab or `POST /api/v1/node-groups/{id}/broadcast`.

**Rate limit:** One broadcast per group per hour (configurable via
`BroadcastRateLimitHours` in the handler, default 1).

```bash
curl -X POST https://clustr.example.com/api/v1/node-groups/GROUP_ID/broadcast \
  -H "Authorization: Bearer clustr-admin-TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"subject":"Maintenance window","body":"The cluster will be down Saturday 2026-05-10 0800–1200."}'
```

---

## Air-gap deployments

clustr is designed for air-gap HPC environments. Notifications are entirely
optional. The DOI lookup feature (CrossRef API) is the only outbound network
call clustr makes, and it is disabled by default:

```
CLUSTR_DOI_LOOKUP_ENABLED=true  # opt-in
```

If this variable is not set, the DOI lookup button in the PI portal will return
`{"found": false}` and the PI can fill in metadata manually.

---

## Troubleshooting

1. **Test email fails** — Check SMTP credentials, firewall rules, and
   TLS/SSL settings. View logs with `journalctl -u clustr-serverd -f`.

2. **`notification.skipped` in audit log** — SMTP is not configured.
   Set `CLUSTR_SMTP_HOST` and `CLUSTR_SMTP_FROM` (minimum required).

3. **Emails deliver but go to spam** — Add SPF/DKIM records for your
   From domain. Use a dedicated notification address.

4. **Member emails bounce** — clustr uses the clustr username as the
   recipient address. Ensure usernames are email addresses or configure
   a mail alias per-user.
