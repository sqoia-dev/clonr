# Changelog

All notable changes to clustr are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

---

## [v1.0.1] — 2026-04-27

**Hotfix release — UI privilege escalation + System Accounts crash**

### Fixed

- **System Accounts page crash (B-4):** The badge helper in `sysaccounts.js`
  was named `sysbage` (an accidental shortening); renamed to `sysBadge` to
  follow the camelCase convention used throughout the codebase
  (`dhcpStateBadge`, etc.). The System Accounts page now renders correctly for
  all users. Added three regression tests that guard the response shape of
  `GET /system/accounts` and confirm `system_account` is present in the list
  response (the field sysBadge reads to decide whether to show the "sys"
  indicator).
  Commits: `bb19c05`

- **UI privilege escalation on network error (A-10):** `Auth._role` was
  initialised to `'admin'` and the `/auth/me` fallback also used `'admin'`, so
  any transient network blip during page boot silently granted the full admin UI
  to operator and readonly sessions for the duration of the session. Real
  backend authz was always enforced (operators received 403 on admin endpoints),
  but the UI showed admin affordances it should not have shown.
  Fix: `Auth._role` now defaults to `'readonly'` (lowest privilege). A
  successful `/auth/me` promotes to the real role. Boot retries up to 3 times
  with exponential backoff (500ms, 1s, 2s) before giving up; a 401/403
  redirects to login immediately. On final failure an error banner is shown and
  all role-gated UI stays hidden — no silent privilege grant.
  Commits: `dcfc61f`

### Upgrade

No database migration required. Pull the latest server binary and restart
`clustr-serverd`. The fix takes effect on next page load.

---

## [v1.0.0] — 2026-04-27

Initial release. Self-hosted webhook dev platform for bare-metal HPC clusters.
