# ADR-0001: Authentication and Authorization Model

**Date:** 2026-04-13
**Status:** Accepted

---

## Context

clonr has two distinct client classes with fundamentally different trust and lifecycle properties:

- **Admin clients** (CLI on the operator's workstation, embedded web UI): long-lived sessions, interactive use, need read/write across all resources.
- **Initramfs clients** (clonr binary baked into PXE initramfs on each target node): ephemeral, automated, need only `GET /nodes/by-mac/:mac` and `GET /images/:id/blob`. They authenticate at boot time with no human in the loop.

The current single pre-shared token covers both. This works but has two failure modes: (1) a compromised node gets read/write admin access, and (2) there is no rotation path that does not require re-burning the initramfs on every node.

HPC environments are predominantly air-gapped and operator-administered. There are no end users logging into clonr. RBAC complexity is not justified at current scale. OIDC is the right eventual answer but requires an identity provider that many target sites do not run.

---

## Decision

**v1.0:** API key authentication with two scopes.

clonr-serverd generates and stores API keys (random 32-byte tokens, base64url-encoded, stored as SHA-256 hashes in the `api_keys` table). Two scopes exist:

- `scope=admin` — full read/write access to all endpoints. Used by the admin CLI and UI.
- `scope=node` — read-only access to `/api/v1/nodes/by-mac/:mac` and `/api/v1/images/:id/blob` only. Used by the initramfs client.

A node-scoped key is generated once and burned into the initramfs build. Rotating it requires a new initramfs build, which is an acceptable operational requirement for the target audience. Admin keys can be rotated at any time via `clonr key rotate` without touching the initramfs.

Schema addition:

```sql
CREATE TABLE api_keys (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    scope       TEXT NOT NULL CHECK(scope IN ('admin', 'node')),
    key_hash    TEXT NOT NULL UNIQUE,  -- SHA-256 of the raw token
    key_prefix  TEXT NOT NULL,         -- first 8 chars, for display/audit
    created_at  INTEGER NOT NULL,
    last_used_at INTEGER,
    expires_at  INTEGER               -- NULL = no expiry
);
```

The raw token is shown exactly once at creation and never stored. Audit logs reference `key_prefix`.

All requests carry `Authorization: Bearer <token>`. The middleware hashes the incoming token and does a single-row lookup. No session state on the server.

**v1.1:** OIDC as an optional overlay, not a replacement. Sites that run Keycloak, Dex, or Active Directory with OIDC can configure an `oidc_issuer_url` in server config. When set, the admin endpoints also accept OIDC JWTs. Node-scope remains API-key-only — OIDC is too heavy for initramfs. The `scope=admin` API keys remain functional alongside OIDC; operators choose their path.

---

## Consequences

- Admin key rotation is instant and self-contained. No impact on running nodes.
- Node key compromise means an attacker can read node configs and download image blobs — both already accessible over PXE from the same network segment. The blast radius is acceptable for air-gapped HPC networks.
- The two-scope model is a hard API contract. Future scope additions (e.g., `scope=readonly-admin`) are additive — no existing key is invalidated.
- OIDC in v1.1 adds an external dependency. It is optional and gated on operator configuration — sites that do not run an IdP are unaffected.
- No passwords. No session cookies. No CSRF surface.
