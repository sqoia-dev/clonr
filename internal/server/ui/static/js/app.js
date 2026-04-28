// app.js — clustr-serverd web UI. Hash-based SPA routing, no frameworks.

// ─── Router ───────────────────────────────────────────────────────────────

const Router = {
    _routes: {},
    _current: null,
    _refreshTimer: null,

    register(hash, handler) {
        this._routes[hash] = handler;
    },

    start() {
        window.addEventListener('hashchange', () => this._navigate());
        this._navigate();
    },

    // C3-11: navigation guard. Pages that track dirty state (e.g. node detail)
    // set this to a function that returns a Promise<bool> — true = proceed, false = cancel.
    // Cleared automatically on each completed navigation.
    _navigationGuard: null,

    navigate(hash) {
        if (this._navigationGuard) {
            const guard = this._navigationGuard;
            guard().then(ok => {
                if (ok) {
                    this._navigationGuard = null;
                    window.location.hash = hash;
                }
            });
        } else {
            window.location.hash = hash;
        }
    },

    _navigate() {
        // Clear navigation guard whenever a navigation commits (hashchange fired).
        this._navigationGuard = null;
        const hash = window.location.hash.replace(/^#/, '') || '/';
        // Stop any running auto-refresh from the previous page.
        if (this._refreshTimer) { clearInterval(this._refreshTimer); this._refreshTimer = null; }
        // Disconnect any active log stream.
        if (App._logStream) { App._logStream.disconnect(); App._logStream = null; }
        // Disconnect any active progress stream.
        if (App._progressStream) { App._progressStream.disconnect(); App._progressStream = null; }
        // Close any ISO build SSE stream and elapsed timer.
        if (Pages._isoBuildSSE) { Pages._isoBuildSSE.close(); Pages._isoBuildSSE = null; }
        if (Pages._isoBuildElapsedTimer) { clearInterval(Pages._isoBuildElapsedTimer); Pages._isoBuildElapsedTimer = null; }
        // C3-13: Close node-log SSE stream on navigate-away (was only disconnected on tab switch, not on full nav).
        if (App._nodeLogStream) { App._nodeLogStream.disconnect(); App._nodeLogStream = null; }
        // Remove node detail page click listener for actions dropdown.
        if (Pages._closeActionsDropdownOnOutsideClick) {
            document.removeEventListener('click', Pages._closeActionsDropdownOnOutsideClick);
        }
        // Remove nodes list page click listener for power dropdowns.
        if (Pages._closePowerDropdownsOnOutsideClick) {
            document.removeEventListener('click', Pages._closePowerDropdownsOnOutsideClick);
        }
        // S5-4: Remove bulk action bar if navigating away from nodes.
        const bulkBar = document.getElementById('nodes-bulk-action-bar');
        if (bulkBar) bulkBar.remove();

        // Match exact or prefix.
        let handler = this._routes[hash];
        if (!handler) {
            for (const key of Object.keys(this._routes)) {
                if (hash.startsWith(key + '/')) { handler = this._routes[key + '/*']; break; }
            }
        }
        if (!handler) handler = this._routes['/'];

        // Update sidebar nav active state.
        document.querySelectorAll('.nav-item').forEach(a => {
            const href = a.getAttribute('href').replace(/^#/, '');
            a.classList.toggle('active', hash === href || (href !== '/' && hash.startsWith(href)));
        });

        this._current = hash;
        handler(hash);
    },
};

// ─── App state ────────────────────────────────────────────────────────────

const App = {
    _logStream: null,
    _mainEl: null,

    // Simple short-lived data cache to avoid redundant fetches across refresh cycles.
    // Structure: { key: { data, expiresAt } }
    _cache: {},

    _cacheGet(key) {
        const entry = this._cache[key];
        if (!entry || Date.now() > entry.expiresAt) return null;
        return entry.data;
    },

    _cacheSet(key, data, ttlMs = 2000) {
        this._cache[key] = { data, expiresAt: Date.now() + ttlMs };
    },

    // toast shows a transient notification at the bottom-right of the screen.
    // kind: "success" | "error" | "info" (default info)
    // Auto-dismisses after 4 seconds.
    // C3-12: duplicate messages are deduplicated — a count badge is shown instead of stacking.
    toast(message, kind = 'info') {
        let container = document.getElementById('toast-container');
        if (!container) {
            container = document.createElement('div');
            container.id = 'toast-container';
            container.style.cssText = 'position:fixed;bottom:20px;right:20px;z-index:9999;display:flex;flex-direction:column;gap:8px;pointer-events:none';
            document.body.appendChild(container);
        }
        const colors = {
            success: { bg: '#10b981', icon: '✓' },
            error:   { bg: '#ef4444', icon: '✕' },
            info:    { bg: '#3b82f6', icon: 'ℹ' },
        };
        const c = colors[kind] || colors.info;
        // C3-12: check for existing toast with the same message+kind and bump count.
        const key = `toast-dedup-${kind}-${message}`;
        const existing = container.querySelector(`[data-toast-key="${CSS.escape(key)}"]`);
        if (existing) {
            let badge = existing.querySelector('.toast-count-badge');
            if (!badge) {
                badge = document.createElement('span');
                badge.className = 'toast-count-badge';
                badge.style.cssText = 'background:rgba(0,0,0,0.25);border-radius:10px;padding:1px 7px;font-size:11px;font-weight:700;margin-left:4px;';
                badge.textContent = '2';
                const msgSpan = existing.querySelector('span[style*="flex:1"]');
                if (msgSpan) msgSpan.appendChild(badge);
            } else {
                badge.textContent = String(parseInt(badge.textContent, 10) + 1);
            }
            // Reset dismiss timer by clearing and resetting.
            if (existing._toastTimer) clearTimeout(existing._toastTimer);
            existing._toastTimer = setTimeout(() => {
                existing.style.animation = 'toastOut 0.2s ease-in forwards';
                setTimeout(() => existing.remove(), 200);
            }, 4000);
            return;
        }
        const toast = document.createElement('div');
        toast.setAttribute('data-toast-key', key);
        toast.setAttribute('role', 'alert');
        toast.setAttribute('aria-live', 'assertive');
        toast.setAttribute('aria-atomic', 'true');
        toast.style.cssText = `background:${c.bg};color:white;padding:12px 16px;border-radius:8px;box-shadow:0 4px 12px rgba(0,0,0,0.15);font-size:14px;font-weight:500;min-width:280px;max-width:420px;display:flex;align-items:center;gap:10px;pointer-events:auto;animation:toastIn 0.2s ease-out`;
        toast.innerHTML = `<span style="font-size:18px;font-weight:bold" aria-hidden="true">${c.icon}</span><span style="flex:1">${escHtml(message)}</span><button style="cursor:pointer;opacity:0.7;padding:0 4px;background:none;border:none;color:white;font-size:16px;line-height:1" aria-label="Dismiss notification" onclick="this.parentElement.remove()">×</button>`;
        container.appendChild(toast);
        toast._toastTimer = setTimeout(() => {
            toast.style.animation = 'toastOut 0.2s ease-in forwards';
            setTimeout(() => toast.remove(), 200);
        }, 4000);
    },

    init() {
        this._mainEl = document.getElementById('main-content');
        this._initRoutes();
        this._watchHealth();
        this._updateClusterStrip();
        Router.start();
    },

    async _updateClusterStrip() {
        // Populate the server label once from the browser's origin.
        const nameEl = document.getElementById('cluster-name');
        if (nameEl) nameEl.textContent = location.hostname;

        try {
            const resp = await API.nodes.list().catch(() => ({}));
            // API.nodes.list() returns ListNodesResponse: { nodes: [...], total: N }.
            // Not a plain array — must unpack .nodes before filtering.
            const nodeList = (resp && Array.isArray(resp.nodes)) ? resp.nodes : [];
            const meta = document.getElementById('cluster-meta');
            if (meta) {
                // A node is "live" when clustr-clientd has sent a heartbeat in the
                // last 2 minutes (same threshold used by the node detail page).
                const twoMin = 2 * 60 * 1000;
                const live  = nodeList.filter(n => n.last_seen_at && (Date.now() - new Date(n.last_seen_at).getTime()) < twoMin).length;
                const total = nodeList.length;
                meta.textContent = `${live}/${total} nodes`;
            }
        } catch (_) {}
    },

    _initRoutes() {
        Router.register('/',        ()    => Pages.dashboard());
        Router.register('/images',  (h)   => {
            const parts = h.split('/');
            if (parts.length === 3 && parts[2]) Pages.imageDetail(parts[2]);
            else Pages.images();
        });
        Router.register('/images/*', (h)  => {
            const parts = h.split('/');
            Pages.imageDetail(parts[2]);
        });
        Router.register('/nodes',   (h)   => {
            // Strip query string for path matching but keep it in window.location.hash.
            const path = h.split('?')[0];
            const parts = path.split('/');
            if (parts.length === 3 && parts[2] === 'groups') Pages.nodeGroups();
            else if (parts.length === 4 && parts[2] === 'groups' && parts[3]) Pages.nodeGroupDetail(parts[3]);
            else if (parts.length === 3 && parts[2]) Pages.nodeDetail(parts[2]);
            else Pages.nodes();
        });
        Router.register('/nodes/*', (h)   => {
            const parts = h.split('/');
            if (parts[2] === 'groups' && parts[3]) Pages.nodeGroupDetail(parts[3]);
            else if (parts[2] === 'groups') Pages.nodeGroups();
            else Pages.nodeDetail(parts[2]);
        });
        // S5-10: Direct deployments route — active deployments table reachable in one click.
        Router.register('/deploys',  ()   => Pages.deploys());
        Router.register('/settings', ()   => Pages.settings());
        Router.register('/ldap',     (h)  => {
            const parts = h.split('/');
            if (parts.length === 3 && parts[2] === 'users') LDAPPages.users();
            else if (parts.length === 3 && parts[2] === 'groups') LDAPPages.groups();
            else LDAPPages.settings();
        });
        Router.register('/ldap/*',   (h)  => {
            const parts = h.split('/');
            if (parts[2] === 'users') LDAPPages.users();
            else if (parts[2] === 'groups') LDAPPages.groups();
            else LDAPPages.settings();
        });
        Router.register('/system/accounts', () => {
            if (typeof SysAccountsPages !== 'undefined') SysAccountsPages.accounts();
        });
        Router.register('/system/groups',   () => {
            if (typeof SysAccountsPages !== 'undefined') SysAccountsPages.groups();
        });
        Router.register('/network/allocations', () => Pages.dhcpLeases());
        Router.register('/network/switches', () => {
            if (typeof NetworkPages !== 'undefined') NetworkPages.switches();
        });
        Router.register('/network/profiles', () => {
            if (typeof NetworkPages !== 'undefined') NetworkPages.profiles();
        });
        // B3-3: Audit log route — admin only; non-admins are redirected to dashboard.
        Router.register('/audit',    ()   => Pages.auditLog());

        Router.register('/slurm',    ()   => {
            if (typeof SlurmPages !== 'undefined') SlurmPages.settings();
        });
        Router.register('/slurm/*',  (h)  => {
            if (typeof SlurmPages === 'undefined') return;
            // h = /slurm/configs/slurm.conf/history → parts = ['','slurm','configs','slurm.conf','history']
            const parts = h.split('/');
            if (parts[2] === 'configs') {
                if (parts[3] && parts[4] === 'history') {
                    SlurmPages.configHistory(decodeURIComponent(parts[3]));
                } else if (parts[3]) {
                    SlurmPages.configEditor(decodeURIComponent(parts[3]));
                } else {
                    SlurmPages.configs();
                }
            } else if (parts[2] === 'scripts') {
                if (parts[3] && parts[4] === 'history') {
                    SlurmPages.scriptHistory(decodeURIComponent(parts[3]));
                } else if (parts[3]) {
                    SlurmPages.scriptEditor(decodeURIComponent(parts[3]));
                } else {
                    SlurmPages.scripts();
                }
            } else if (parts[2] === 'sync') {
                SlurmPages.syncStatus();
            } else if (parts[2] === 'push') {
                SlurmPages.push();
            } else if (parts[2] === 'builds') {
                SlurmPages.builds();
            } else if (parts[2] === 'upgrades') {
                if (parts[3]) {
                    SlurmPages.upgradeDetail(parts[3]);
                } else {
                    SlurmPages.upgrades();
                }
            } else {
                SlurmPages.settings();
            }
        });
    },

    render(html) {
        this._mainEl.innerHTML = `<div class="page-enter">${html}</div>`;
    },

    setAutoRefresh(fn, intervalMs = 30000) {
        if (Router._refreshTimer) clearInterval(Router._refreshTimer);
        Router._refreshTimer = setInterval(fn, intervalMs);
    },

    _watchHealth() {
        const dot = document.querySelector('.cluster-strip .live-dot');
        const check = async () => {
            try {
                await API.health.get();
                if (dot) { dot.style.background = '#6ee7b7'; }
            } catch (_) {
                if (dot) { dot.style.background = 'var(--error)'; }
            }
        };

        // C3-10: Session expiry banner with live countdown timer and auto-redirect.
        // Polls /auth/me every 60s. When TTL < 600s, shows banner. When TTL < 300s,
        // switches to second-level countdown. At TTL=0, redirects to /login.
        let _sessionExpiresAt = null;
        let _sessionCountdownTimer = null;

        const _updateSessionBanner = () => {
            const banner = document.getElementById('session-expiry-banner');
            if (!banner || !_sessionExpiresAt) return;
            const ttlSecs = Math.floor((_sessionExpiresAt - Date.now()) / 1000);
            if (ttlSecs <= 0) {
                // Session expired — redirect to login.
                clearInterval(_sessionCountdownTimer);
                window.location.href = '/login?expired=1';
                return;
            }
            if (ttlSecs < 600) {
                banner.style.display = 'block';
                if (ttlSecs < 60) {
                    banner.textContent = `Session expires in ${ttlSecs}s — click to extend`;
                } else {
                    const mins = Math.ceil(ttlSecs / 60);
                    banner.textContent = `Session expires in ${mins} minute${mins === 1 ? '' : 's'} — click to extend`;
                }
            } else {
                banner.style.display = 'none';
            }
        };

        const checkSession = async () => {
            const banner = document.getElementById('session-expiry-banner');
            if (!banner) return;
            try {
                const me = await fetch('/api/v1/auth/me', { credentials: 'same-origin' });
                if (!me.ok) { banner.style.display = 'none'; _sessionExpiresAt = null; return; }
                const data = await me.json();
                if (!data.expires_at) { banner.style.display = 'none'; return; }
                _sessionExpiresAt = new Date(data.expires_at).getTime();
                _updateSessionBanner();
                // Start second-level countdown when TTL < 300s (5 minutes).
                const ttlSecs = Math.floor((_sessionExpiresAt - Date.now()) / 1000);
                if (ttlSecs < 300 && !_sessionCountdownTimer) {
                    _sessionCountdownTimer = setInterval(_updateSessionBanner, 1000);
                }
            } catch (_) {
                if (banner) banner.style.display = 'none';
            }
        };

        check();
        checkSession();
        setInterval(() => { check(); this._updateClusterStrip(); }, 30000);
        setInterval(checkSession, 60000);
    },
};

// ─── Shared UI helpers ────────────────────────────────────────────────────

// trapModalFocus — WCAG 2.1 AA focus trap for modal overlays.
// Traps Tab/Shift+Tab within the overlay and calls onClose on Escape.
function trapModalFocus(overlay, onClose) {
    overlay.addEventListener('keydown', function(e) {
        if (e.key === 'Escape') {
            e.preventDefault();
            onClose();
            return;
        }
        if (e.key === 'Tab') {
            const focusable = overlay.querySelectorAll(
                'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
            );
            if (focusable.length === 0) return;
            const first = focusable[0];
            const last  = focusable[focusable.length - 1];
            if (e.shiftKey && document.activeElement === first) {
                e.preventDefault();
                last.focus();
            } else if (!e.shiftKey && document.activeElement === last) {
                e.preventDefault();
                first.focus();
            }
        }
    });
    setTimeout(() => {
        const btn = overlay.querySelector('.modal-close, .shell-modal-close, button');
        if (btn) btn.focus();
    }, 50);
}

function loading(msg = 'Loading…') {
    return `<div class="loading"><div class="spinner"></div>${escHtml(msg)}</div>`;
}

function alertBox(msg, type = 'error') {
    const role = (type === 'error') ? 'alert' : 'status';
    return `<div class="alert alert-${type}" role="${role}">${escHtml(msg)}</div>`;
}

function badge(status) {
    const cls = {
        ready:    'badge-ready',
        building: 'badge-building',
        error:    'badge-error',
        archived: 'badge-archived',
    }[status] || 'badge-archived';
    return `<span class="badge ${cls}">${escHtml(status)}</span>`;
}

function nodeBadge(node) {
    // ADR-0008: two-phase deploy states. Check the new fields directly in addition
    // to the legacy _deployStatus shorthand computed from last_deploy_succeeded_at.
    if (node.deploy_verified_booted_at)
        return `<span class="badge badge-deployed" title="OS phoned home — bootloader, kernel, and systemd confirmed">Verified</span>`;
    if (node.deploy_verify_timeout_at)
        return `<span class="badge badge-error" title="OS never phoned home within verify timeout. Possible bootloader or kernel failure.">Verify Timeout</span>`;
    if (node.deploy_completed_preboot_at && !node.last_deploy_succeeded_at)
        return `<span class="badge badge-warning" title="clustr-static completed successfully. Waiting for OS boot confirmation.">Deploy Unverified</span>`;
    if (node._deployStatus === 'success') return `<span class="badge badge-deployed">Deployed</span>`;
    if (node._deployStatus === 'error')   return `<span class="badge badge-error">Failed</span>`;
    if (node.base_image_id)               return `<span class="badge badge-info">Configured</span>`;
    if (node.hardware_profile)            return `<span class="badge badge-warning">Registered</span>`;
    return `<span class="badge badge-neutral">Unconfigured</span>`;
}

// dhcpStateBadge maps a node deploy_state string (as returned by the DHCP
// leases endpoint) to a coloured badge. Mirrors the labels from nodeBadge
// but takes a plain string rather than a full node object.
// Retained for any vanilla callers; the Alpine component uses stateBadgeClass/
// stateBadgeLabel instead (see dhcpLeasesComponent below).
function dhcpStateBadge(state) {
    switch (state) {
        case 'deployed_verified':        return `<span class="badge badge-deployed">Verified</span>`;
        case 'deploy_verify_timeout':    return `<span class="badge badge-error">Verify Timeout</span>`;
        case 'deployed_preboot':         return `<span class="badge badge-warning">Unverified</span>`;
        case 'failed':                   return `<span class="badge badge-error">Failed</span>`;
        case 'reimage_pending':          return `<span class="badge badge-info">Reimaging</span>`;
        case 'configured':               return `<span class="badge badge-info">Configured</span>`;
        case 'registered':               return `<span class="badge badge-warning">Registered</span>`;
        default:                         return `<span class="badge badge-neutral">${escHtml(state || 'Unknown')}</span>`;
    }
}

// ─── Alpine.js component: DHCP Leases page (Sprint B.5 pilot) ────────────
//
// dhcpLeasesComponent() is the Alpine data factory for the DHCP Allocations
// page. It is called via x-data="dhcpLeasesComponent()" in the HTML rendered
// by Pages.dhcpLeases(). Alpine looks this up on window automatically.
//
// State shape:
//   loading  — true while the API call is in-flight (shows spinner)
//   error    — non-null string when the fetch fails (shows alert)
//   leases   — array of DHCPLease objects from API.dhcp.leases().leases
//   count    — total count from the API response
//
// Methods:
//   init()            — called by Alpine's x-init; kicks off the first fetch
//   refresh()         — manual refresh (Refresh button @click)
//   stateBadgeClass() — maps deploy_state → CSS class string for :class binding
//   stateBadgeLabel() — maps deploy_state → human label for x-text binding
//   fmtRelative()     — re-exposes the global helper inside Alpine scope
//
// Alpine notes:
//   - x-text and attribute bindings (:href, :title) are safe by default —
//     Alpine never calls innerHTML. No manual escaping needed.
//   - x-show uses display:none; elements are NOT removed from the DOM.
//     This is intentional — it avoids re-rendering the table on refresh.
//   - x-for uses :key="lease.mac" so Alpine diffs only changed rows.
function dhcpLeasesComponent() {
    return {
        loading: true,
        error:   null,
        leases:  [],
        count:   0,

        // init() is the Alpine lifecycle hook called after x-data is mounted.
        // Kick off the first data fetch immediately.
        async init() {
            await this.refresh();
        },

        async refresh() {
            this.loading = true;
            this.error   = null;
            try {
                const data  = await API.dhcp.leases();
                this.leases = (data && Array.isArray(data.leases)) ? data.leases : [];
                this.count  = (data && data.count != null) ? data.count : this.leases.length;
            } catch (err) {
                this.error = 'Failed to load DHCP allocations: ' + err.message;
            } finally {
                this.loading = false;
            }
        },

        // stateBadgeClass returns the CSS class string for a deploy_state value.
        // Used via :class="stateBadgeClass(lease.deploy_state)" in the template.
        stateBadgeClass(state) {
            const map = {
                'deployed_verified':     'badge badge-deployed',
                'deploy_verify_timeout': 'badge badge-error',
                'deployed_preboot':      'badge badge-warning',
                'failed':                'badge badge-error',
                'reimage_pending':       'badge badge-info',
                'configured':            'badge badge-info',
                'registered':            'badge badge-warning',
            };
            return map[state] || 'badge badge-neutral';
        },

        // stateBadgeLabel returns the human-readable label for a deploy_state.
        // Used via x-text="stateBadgeLabel(lease.deploy_state)" in the template.
        stateBadgeLabel(state) {
            const map = {
                'deployed_verified':     'Verified',
                'deploy_verify_timeout': 'Verify Timeout',
                'deployed_preboot':      'Unverified',
                'failed':                'Failed',
                'reimage_pending':       'Reimaging',
                'configured':            'Configured',
                'registered':            'Registered',
            };
            return map[state] || (state || 'Unknown');
        },

        // fmtRelative re-exposes the global helper so Alpine templates can call
        // it as fmtRelative(ts) inside expressions without breaking scope.
        fmtRelative(ts) {
            return fmtRelative(ts);
        },
    };
}

function fmtBytes(bytes) {
    if (!bytes || bytes === 0) return '—';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0, n = bytes;
    while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
    return `${n.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

function fmtDate(ts) {
    if (!ts) return '—';
    return new Date(ts).toLocaleString('en-US', { month: 'short', day: 'numeric', year: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false });
}

function fmtDateShort(ts) {
    if (!ts) return '—';
    return new Date(ts).toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false });
}

function fmtRelative(ts) {
    if (!ts) return '—';
    const diff = Date.now() - new Date(ts).getTime();
    const s = Math.floor(diff / 1000);
    if (s < 60)  return `${s}s ago`;
    const m = Math.floor(s / 60);
    if (m < 60)  return `${m}m ago`;
    const h = Math.floor(m / 60);
    if (h < 24)  return `${h}h ago`;
    return fmtDateShort(ts);
}

function cardWrap(title, body, actions = '') {
    return `
        <div class="card">
            <div class="card-header">
                <h2 class="card-title">${title}</h2>
                <div class="flex gap-8">${actions}</div>
            </div>
            <div class="card-body">${body}</div>
        </div>`;
}

function emptyState(title, sub = '', cta = '') {
    return `<div class="empty-state">
        <div class="empty-state-icon">
            <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24">
                <circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/>
            </svg>
        </div>
        <div class="empty-state-title">${escHtml(title)}</div>
        ${sub ? `<div class="empty-state-text">${escHtml(sub)}</div>` : ''}
        ${cta ? cta : ''}
    </div>`;
}

// Returns HTML for a hostname cell: italic "Unassigned" + MAC below when hostname is
// absent, empty, null, or the literal server placeholder "(none)".
function fmtHostname(hostname, mac) {
    const isUnset = !hostname || hostname === '(none)';
    const macHtml = mac ? `<div class="text-dim text-sm text-mono">${escHtml(mac)}</div>` : '';
    if (isUnset) {
        return `<span class="text-muted" style="font-style:italic">Unassigned</span>${macHtml}`;
    }
    return `<span style="font-weight:600">${escHtml(hostname)}</span>${macHtml}`;
}

// ─── Deployment phase helpers ──────────────────────────────────────────────

const PHASE_BADGE = {
    complete:     'badge-ready',
    downloading:  'badge-info',
    extracting:   'badge-info',
    partitioning: 'badge-info',
    formatting:   'badge-info',
    finalizing:   'badge-info',
    preflight:    'badge-info',
    error:        'badge-error',
    waiting:      'badge-neutral',
};

function phaseBadge(phase) {
    const cls = PHASE_BADGE[phase] || 'badge-info';
    return `<span class="badge ${cls}">${escHtml(phase)}</span>`;
}

// fmtSpeed formats bytes/sec as a human-readable speed string.
function fmtSpeed(bps) {
    if (!bps || bps <= 0) return '—';
    if (bps >= 1024 * 1024) return `${(bps / (1024 * 1024)).toFixed(1)} MB/s`;
    if (bps >= 1024) return `${(bps / 1024).toFixed(1)} KB/s`;
    return `${bps} B/s`;
}

// fmtETA formats remaining seconds as mm:ss.
function fmtETA(secs) {
    if (!secs || secs <= 0) return '—';
    const m = Math.floor(secs / 60);
    const s = secs % 60;
    return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
}

// ─── Pages ────────────────────────────────────────────────────────────────

const Pages = {

    // ── Shared modal utilities ─────────────────────────────────────────────

    // showConfirmModal shows a modal-based confirmation dialog (S2-14).
    // Replaces native confirm()/alert() for testability and consistent UX.
    //
    // opts:
    //   title        {string}   Modal heading (default: "Confirm")
    //   message      {string}   Body text (may contain HTML — caller must escape user data)
    //   confirmText  {string}   Confirm button label (default: "Confirm")
    //   cancelText   {string}   Cancel button label (default: "Cancel"). Omit for alert-only.
    //   danger       {boolean}  Style confirm button as btn-danger
    //   onConfirm    {function} Called when confirmed
    //   onCancel     {function} Optional; called on cancel/close
    showConfirmModal(opts = {}) {
        const id = 'confirm-modal-' + Date.now();
        const title = opts.title || 'Confirm';
        const message = opts.message || '';
        const confirmText = opts.confirmText || 'Confirm';
        const cancelText = opts.cancelText !== undefined ? opts.cancelText : 'Cancel';
        const btnClass = opts.danger ? 'btn btn-danger' : 'btn btn-primary';

        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        overlay.id = id;

        const cancelBtn = cancelText
            ? `<button class="btn btn-secondary" onclick="document.getElementById('${id}').remove()${opts.onCancel ? ';Pages._confirmModalCancel_' + id + '()' : ''}">${escHtml(cancelText)}</button>`
            : '';

        overlay.innerHTML = `
            <div class="modal" style="max-width:440px" aria-labelledby="${id}-title">
                <div class="modal-header">
                    <span class="modal-title" id="${id}-title">${escHtml(title)}</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('${id}').remove()">×</button>
                </div>
                <div class="modal-body">
                    <div style="color:var(--text-secondary);font-size:14px;line-height:1.5;margin-bottom:16px">${message}</div>
                    <div style="display:flex;gap:8px;justify-content:flex-end">
                        ${cancelBtn}
                        <button class="${btnClass}" id="${id}-confirm-btn">${escHtml(confirmText)}</button>
                    </div>
                </div>
            </div>`;

        document.body.appendChild(overlay);
        overlay.addEventListener('click', e => {
            if (e.target === overlay) {
                overlay.remove();
                if (opts.onCancel) opts.onCancel();
            }
        });

        document.getElementById(`${id}-confirm-btn`).addEventListener('click', () => {
            overlay.remove();
            if (opts.onConfirm) opts.onConfirm();
        });

        trapModalFocus(overlay, () => {
            overlay.remove();
            if (opts.onCancel) opts.onCancel();
        });
    },

    // showAlertModal shows a simple non-blocking informational modal (S2-14).
    showAlertModal(title, message, buttonText = 'OK') {
        Pages.showConfirmModal({
            title,
            message,
            confirmText: buttonText,
            cancelText: '',
            danger: false,
        });
    },

    // ── Dashboard ──────────────────────────────────────────────────────────

    // _dashDeployMap persists the deployment map across refresh cycles so the
    // ProgressStream SSE updates are not lost when dashboardRefresh() runs.
    _dashDeployMap: null,

    async dashboard() {
        App.render(loading('Loading dashboard…'));

        try {
            const [imagesResp, nodesResp, progressEntries, initramfsInfo, healthReady] = await Promise.all([
                API.images.list(),
                API.nodes.list(),
                API.progress.list().catch(() => []),
                API.system.initramfs().catch(() => null),
                API.health.ready().catch(() => null),
            ]);

            const images = imagesResp.images || [];
            const nodes  = nodesResp.nodes   || [];

            // Warm the cache so the first auto-refresh is instant.
            App._cacheSet('images', images);
            App._cacheSet('nodes', nodes);

            const ready    = images.filter(i => i.status === 'ready').length;
            const building = images.filter(i => i.status === 'building').length;
            const errored  = images.filter(i => i.status === 'error').length;
            const configured = nodes.filter(n => n.base_image_id).length;

            // Build a MAC → progress map for live updates. Stored on Pages so
            // dashboardRefresh() can reuse and mutate it without losing SSE state.
            const deployMap = new Map();
            (progressEntries || []).forEach(p => deployMap.set(p.node_mac, p));
            Pages._dashDeployMap = deployMap;

            const recentActivity = this._buildRecentActivity(images, nodes);

            // S2-11: Stale initramfs indicator.
            // Show a warning when the newest ready image was created after the current
            // initramfs was built — meaning the initramfs may not include the latest
            // kernel or scripts from that image.
            const staleInitramfsWarning = (() => {
                if (!initramfsInfo || !initramfsInfo.build_time) return '';
                const readyImages = images.filter(i => i.status === 'ready');
                if (readyImages.length === 0) return '';
                const newestImageTs = Math.max(...readyImages.map(i => {
                    const t = i.created_at;
                    return typeof t === 'number' ? t : (t ? new Date(t).getTime() / 1000 : 0);
                }));
                const initramfsTs = typeof initramfsInfo.build_time === 'number'
                    ? initramfsInfo.build_time
                    : new Date(initramfsInfo.build_time).getTime() / 1000;
                if (newestImageTs > initramfsTs) {
                    return `<div class="alert alert-warning" style="margin-bottom:16px;display:flex;align-items:center;justify-content:space-between;gap:12px">
                        <span>
                            <strong>Initramfs may be stale.</strong>
                            A newer image was created after the current initramfs was built. Nodes booting via PXE will use the old initramfs until it is rebuilt.
                        </span>
                        <a href="#/images" class="btn btn-secondary btn-sm" style="white-space:nowrap">Go to Images</a>
                    </div>`;
                }
                return '';
            })();

            App.render(`
                <div class="page-header">
                    <div>
                        <h1 class="page-title">Dashboard</h1>
                        <div class="page-subtitle">System overview and active deployments</div>
                    </div>
                </div>

                ${staleInitramfsWarning}

                ${/* B2-6: show wizard until at least one node has deploy_verified_booted_at set */
                  !nodes.some(n => n.deploy_verified_booted_at) ? `
                <!-- S5-9: First-deploy wizard — shown when there are no images and no nodes. -->
                <div class="card" style="margin-bottom:24px;border:1px solid var(--accent);background:linear-gradient(135deg,var(--surface-secondary) 0%,var(--bg-primary) 100%)">
                    <div class="card-header">
                        <h2 class="card-title" style="display:flex;align-items:center;gap:10px">
                            <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="var(--accent)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24" style="width:20px;height:20px"><circle cx="12" cy="12" r="10"/><polyline points="12 8 12 12 14 14"/></svg>
                            Getting Started
                        </h2>
                        <span class="text-dim text-sm">Complete these steps to deploy your first node</span>
                    </div>
                    <div style="padding:16px 20px 20px;display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:16px">
                        <div style="background:var(--bg-secondary);border:1px solid var(--border);border-radius:8px;padding:16px">
                            <div style="display:flex;align-items:center;gap:8px;margin-bottom:8px">
                                <span style="width:24px;height:24px;border-radius:50%;background:var(--accent);color:#fff;font-size:12px;font-weight:700;display:flex;align-items:center;justify-content:center;flex-shrink:0">1</span>
                                <span style="font-weight:600;font-size:14px">Build an Image</span>
                            </div>
                            <p style="margin:0 0 12px;font-size:12px;color:var(--text-secondary)">Use <em>Build from ISO</em> to create a rootfs image from a standard Linux installer. This is the recommended starting point.</p>
                            <a href="#/images" class="btn btn-primary btn-sm">Go to Images</a>
                        </div>
                        <div style="background:var(--bg-secondary);border:1px solid var(--border);border-radius:8px;padding:16px">
                            <div style="display:flex;align-items:center;gap:8px;margin-bottom:8px">
                                <span style="width:24px;height:24px;border-radius:50%;background:var(--border);color:var(--text-secondary);font-size:12px;font-weight:700;display:flex;align-items:center;justify-content:center;flex-shrink:0">2</span>
                                <span style="font-weight:600;font-size:14px">Register a Node</span>
                            </div>
                            <p style="margin:0 0 12px;font-size:12px;color:var(--text-secondary)">PXE-boot a physical or virtual machine. clustr's iPXE menu will auto-register the node when it phones home.</p>
                            <a href="#/nodes" class="btn btn-secondary btn-sm">Go to Nodes</a>
                        </div>
                        <div style="background:var(--bg-secondary);border:1px solid var(--border);border-radius:8px;padding:16px">
                            <div style="display:flex;align-items:center;gap:8px;margin-bottom:8px">
                                <span style="width:24px;height:24px;border-radius:50%;background:var(--border);color:var(--text-secondary);font-size:12px;font-weight:700;display:flex;align-items:center;justify-content:center;flex-shrink:0">3</span>
                                <span style="font-weight:600;font-size:14px">Deploy</span>
                            </div>
                            <p style="margin:0 0 12px;font-size:12px;color:var(--text-secondary)">Assign the image to the node and trigger a reimage. Live progress appears here on the dashboard.</p>
                            <a href="#/deploys" class="btn btn-secondary btn-sm">View Deployments</a>
                        </div>
                    </div>
                </div>` : ''}

                <div class="stats-grid">
                    <div class="stat-card">
                        <div class="stat-icon stat-icon-blue">
                            <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24">
                                <polygon points="12 2 2 7 12 12 22 7 12 2"/><polyline points="2 17 12 22 22 17"/><polyline points="2 12 12 17 22 12"/>
                            </svg>
                        </div>
                        <div class="stat-label">Total Images</div>
                        <div class="stat-value" id="dash-images-count">${images.length}</div>
                        <div class="stat-sub" id="dash-images-sub">
                            <span class="text-success">${ready} ready</span>
                            ${building > 0 ? ` · <span class="text-accent">${building} building</span>` : ''}
                            ${errored  > 0 ? ` · <span class="text-error">${errored} error</span>` : ''}
                        </div>
                    </div>
                    <div class="stat-card">
                        <div class="stat-icon stat-icon-green">
                            <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24">
                                <rect x="2" y="2" width="20" height="8" rx="2"/><rect x="2" y="14" width="20" height="8" rx="2"/>
                                <line x1="6" y1="6" x2="6.01" y2="6"/><line x1="6" y1="18" x2="6.01" y2="18"/>
                            </svg>
                        </div>
                        <div class="stat-label">Nodes</div>
                        <div class="stat-value" id="dash-nodes-count">${nodes.length}</div>
                        <div class="stat-sub" id="dash-nodes-sub">${configured} configured · ${nodes.length - configured} unconfigured</div>
                    </div>
                    <div class="stat-card">
                        <div class="stat-icon stat-icon-amber">
                            <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24">
                                <polyline points="22 12 18 12 15 21 9 3 6 12 2 12"/>
                            </svg>
                        </div>
                        <div class="stat-label">Active Deployments</div>
                        <div class="stat-value" id="dash-active-count">${Array.from(deployMap.values()).filter(d => d.phase !== 'complete' && d.phase !== 'error').length}</div>
                        <div class="stat-sub" id="dash-complete-count">${Array.from(deployMap.values()).filter(d => d.phase === 'complete').length} completed recently</div>
                    </div>
                    ${(() => {
                        // C3-22: Dynamic System Health card from /healthz/ready.
                        const hr = healthReady;
                        let overallStatus = 'OK';
                        let statusColor = 'var(--success)';
                        let checkSummary = 'All checks passing';
                        if (hr && hr.checks) {
                            const failing = Object.entries(hr.checks).filter(([, v]) => v !== 'ok');
                            if (failing.length > 0) {
                                const hasMissing = failing.some(([, v]) => v.startsWith('missing'));
                                overallStatus = hasMissing ? 'Degraded' : 'Error';
                                statusColor = hasMissing ? 'var(--warning)' : 'var(--error)';
                                checkSummary = failing.map(([k, v]) => `${k}: ${v.split(':')[0]}`).join(' · ');
                            }
                        } else if (!hr) {
                            overallStatus = 'Unknown';
                            statusColor = 'var(--text-secondary)';
                            checkSummary = 'Health check unavailable';
                        }
                        return `<div class="stat-card" id="dash-health-card">
                            <div class="stat-icon stat-icon-purple">
                                <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24">
                                    <path d="M12 2a10 10 0 1 0 10 10"/><polyline points="12 6 12 12 16 14"/>
                                </svg>
                            </div>
                            <div class="stat-label">System Health</div>
                            <div class="stat-value" id="dash-health-status" style="font-size:18px;color:${statusColor}">${overallStatus}</div>
                            <div class="stat-sub" id="dash-health-sub" title="${escHtml(checkSummary)}">${escHtml(checkSummary)}</div>
                        </div>`;
                    })()}
                </div>

                <!-- C2-5: Anomaly card — HTMX polls /api/v1/dashboard/anomalies every 30s.
                     The card is shown/hidden based on data-anomaly-total written by the partial.
                     hx-swap="innerHTML" replaces only the card body; hx-trigger="load, every 30s"
                     fires immediately on insert then refreshes on schedule. -->
                ${this._anomalyCardWrapper()}

                ${cardWrap('Active Deployments',
                    `<div id="deploy-progress-container">${this._deployProgressTable(deployMap)}</div>`)}

                ${Auth._role === 'operator'
                    // B1-3: Operator dashboard — shows groups and recent deploys instead of images/log stream.
                    ? `<div style="display:grid;grid-template-columns:1fr 1fr;gap:20px;margin-bottom:20px">
                        ${cardWrap('Your Groups',
                            `<div id="dash-operator-groups">${this._operatorGroupsPanel(nodes)}</div>`,
                            `<a href="#/nodes/groups" class="btn btn-secondary btn-sm">All groups</a>`)}
                        ${cardWrap('Your Recent Deploys',
                            `<div id="dash-operator-deploys">${this._operatorRecentDeploysPanel(nodes)}</div>`,
                            `<a href="#/deploys" class="btn btn-secondary btn-sm">All deploys</a>`)}
                    </div>`
                    // Admin dashboard — images + nodes + log stream.
                    : `<div style="display:grid;grid-template-columns:1fr 1fr;gap:20px;margin-bottom:20px">
                        ${cardWrap('Recent Images',
                            `<div id="dash-recent-images-wrap">${this._imagesTable(images.slice(0, 6))}</div>`,
                            `<a href="#/images" class="btn btn-secondary btn-sm">View all</a>`)}
                        ${cardWrap('Recent Nodes',
                            `<div id="dash-recent-nodes-wrap">${this._nodesTable(nodes.slice(0, 6))}</div>`,
                            `<a href="#/nodes" class="btn btn-secondary btn-sm">View all</a>`)}
                    </div>
                    ${cardWrap('Live Log Stream',
                        `<div id="dash-log-viewer" class="log-viewer"></div>`,
                        `<span class="follow-indicator" id="dash-follow-ind">
                            <span class="follow-dot"></span>connecting…
                        </span>`)}`}

                ${recentActivity.length > 0 ? cardWrap('Recent Activity',
                    this._activityTimeline(recentActivity),
                    '') : ''}
            `);

            // C2-5: Activate HTMX on the anomaly card body so hx-get fires.
            // App.render uses innerHTML which bypasses HTMX's MutationObserver,
            // so we must call htmx.process() manually on the new element.
            const anomalyBody = document.getElementById('dash-anomaly-card-body');
            if (anomalyBody && typeof htmx !== 'undefined') {
                htmx.process(anomalyBody);
            }
            this._attachAnomalyVisibilityObserver();

            const viewer = document.getElementById('dash-log-viewer');
            if (viewer) {
                const stream = new LogStream(viewer);
                // Pre-load the last 50 log entries so the viewer shows history
                // before any new SSE events arrive.
                try {
                    const resp = await API.logs.query({ limit: 50 });
                    const entries = (resp.logs || []).reverse(); // oldest-first for natural scroll
                    if (entries.length > 0) {
                        stream.loadEntries(entries);
                    } else {
                        viewer.innerHTML = '<div class="empty-state" style="padding:20px"><div class="empty-state-text">Waiting for log events…</div></div>';
                    }
                } catch (_) {
                    viewer.innerHTML = '<div class="empty-state" style="padding:20px"><div class="empty-state-text">Waiting for log events…</div></div>';
                }
                stream.onConnect(() => {
                    const ind = document.getElementById('dash-follow-ind');
                    if (ind) { ind.className = 'follow-indicator live'; ind.innerHTML = '<span class="follow-dot"></span>Live'; }
                    // Clear empty-state placeholder if present
                    const es = viewer.querySelector('.empty-state');
                    if (es) es.remove();
                });
                stream.onDisconnect((permanent) => {
                    const ind = document.getElementById('dash-follow-ind');
                    if (permanent) {
                        if (ind) { ind.className = 'follow-indicator'; ind.innerHTML = '<span class="follow-dot"></span>unavailable'; }
                        const viewer = document.getElementById('dash-log-viewer');
                        if (viewer && !viewer.children.length) {
                            viewer.innerHTML = '<div class="empty-state" style="padding:30px"><div class="empty-state-text">Live log stream unavailable</div></div>';
                        }
                    } else {
                        if (ind) { ind.className = 'follow-indicator'; ind.innerHTML = '<span class="follow-dot"></span>Reconnecting…'; }
                    }
                });
                stream.connect();
                App._logStream = stream;
            }

            // ── Real-time deployment progress via SSE ──────────────────────
            App._progressStream = new ProgressStream(deployMap, () => {
                const container = document.getElementById('deploy-progress-container');
                if (container) container.innerHTML = Pages._deployProgressTable(deployMap);
                // Update stats counters.
                const activeCount = document.getElementById('dash-active-count');
                const completeCount = document.getElementById('dash-complete-count');
                if (activeCount) activeCount.textContent = Array.from(deployMap.values()).filter(d => d.phase !== 'complete' && d.phase !== 'error').length;
                if (completeCount) completeCount.textContent = Array.from(deployMap.values()).filter(d => d.phase === 'complete').length + ' completed recently';
            });
            App._progressStream.connect();

            // Incremental auto-refresh — updates data in-place, no DOM flicker.
            App.setAutoRefresh(() => Pages.dashboardRefresh());

        } catch (e) {
            App.render(alertBox(`Failed to load dashboard: ${e.message}`));
        }
    },

    // dashboardRefresh — called by the auto-refresh timer every 30 seconds.
    // Fetches fresh data and updates existing DOM elements in-place.
    // Never replaces the outer layout, the log stream, or any other stateful widget.
    async dashboardRefresh() {
        // Guard: if the dashboard DOM is gone (navigated away), do nothing.
        if (!document.getElementById('dash-images-count')) return;

        try {
            // Use cached data if still fresh (avoids thundering-herd on rapid refreshes).
            let images = App._cacheGet('images');
            let nodes  = App._cacheGet('nodes');

            const fetches = [];
            if (!images) fetches.push(API.images.list().then(r => { images = r.images || []; App._cacheSet('images', images); }));
            if (!nodes)  fetches.push(API.nodes.list().then(r  => { nodes  = r.nodes   || []; App._cacheSet('nodes',  nodes);  }));
            if (fetches.length) await Promise.all(fetches);

            // ── Stat cards ────────────────────────────────────────────────
            const ready      = images.filter(i => i.status === 'ready').length;
            const building   = images.filter(i => i.status === 'building').length;
            const errored    = images.filter(i => i.status === 'error').length;
            const configured = nodes.filter(n => n.base_image_id).length;

            const imagesCount = document.getElementById('dash-images-count');
            const imagesSub   = document.getElementById('dash-images-sub');
            const nodesCount  = document.getElementById('dash-nodes-count');
            const nodesSub    = document.getElementById('dash-nodes-sub');

            if (imagesCount) imagesCount.textContent = images.length;
            if (imagesSub) {
                let sub = `<span class="text-success">${ready} ready</span>`;
                if (building > 0) sub += ` · <span class="text-accent">${building} building</span>`;
                if (errored  > 0) sub += ` · <span class="text-error">${errored} error</span>`;
                imagesSub.innerHTML = sub;
            }
            if (nodesCount) nodesCount.textContent = nodes.length;
            if (nodesSub)   nodesSub.textContent   = `${configured} configured · ${nodes.length - configured} unconfigured`;

            // ── Recent Images table (diff rows, no full replace) ──────────
            const imagesWrap = document.getElementById('dash-recent-images-wrap');
            if (imagesWrap) {
                this._diffTable(imagesWrap, images.slice(0, 6), 'id', (img) => {
                    const tr = document.createElement('tr');
                    tr.className = 'clickable';
                    tr.dataset.key = img.id;
                    tr.onclick = () => Router.navigate(`/images/${img.id}`);
                    tr.innerHTML = `
                        <td>
                            <span style="font-weight:500">${escHtml(img.name)}</span>
                            ${img.version ? `<span class="text-dim text-sm"> v${escHtml(img.version)}</span>` : ''}
                        </td>
                        <td>
                            ${img.os   ? `<span class="badge badge-neutral badge-sm">${escHtml(img.os)}</span> ` : ''}
                            ${img.arch ? `<span class="badge badge-neutral badge-sm">${escHtml(img.arch)}</span>` : ''}
                            ${!img.os && !img.arch ? '<span class="text-dim">—</span>' : ''}
                        </td>
                        <td>${badge(img.status)}</td>
                        <td class="text-dim text-mono text-sm">${fmtBytes(img.size_bytes)}</td>`;
                    return tr;
                }, (tr, img) => {
                    // Update status badge and size in place — name/os/arch don't change.
                    const cells = tr.querySelectorAll('td');
                    if (cells[2]) cells[2].innerHTML = badge(img.status);
                    if (cells[3]) cells[3].textContent = fmtBytes(img.size_bytes);
                });
            }

            // ── Recent Nodes table (diff rows) ────────────────────────────
            const nodesWrap = document.getElementById('dash-recent-nodes-wrap');
            if (nodesWrap) {
                this._diffTable(nodesWrap, nodes.slice(0, 6), 'id', (n) => {
                    const tr = document.createElement('tr');
                    tr.className = 'clickable';
                    tr.dataset.key = n.id;
                    tr.onclick = () => Router.navigate(`/nodes/${n.id}`);
                    tr.innerHTML = `
                        <td>
                            ${(n.hostname && n.hostname !== '(none)')
                                ? `<span style="font-weight:500">${escHtml(n.hostname)}</span>`
                                : `<span class="text-dim" style="font-style:italic">Unassigned</span>`}
                            <div class="text-dim text-sm text-mono">${escHtml(n.primary_mac || '—')}</div>
                        </td>
                        <td>${nodeBadge(n)}</td>
                        <td class="text-dim text-sm">${fmtRelative(n.updated_at)}</td>`;
                    return tr;
                }, (tr, n) => {
                    const cells = tr.querySelectorAll('td');
                    if (cells[1]) cells[1].innerHTML = nodeBadge(n);
                    if (cells[2]) cells[2].textContent = fmtRelative(n.updated_at);
                });
            }

        } catch (_) {
            // Silently swallow refresh errors — the user is still on a functional page.
            // The next tick will retry automatically.
        }
    },

    // _diffTable reconciles the rows inside a table container (which holds a
    // .table-wrap > table > tbody) against a new data array.
    // - keyField: the property name used as the row identity key (matches data-key)
    // - createRow(item): returns a new <tr> element with data-key set
    // - updateRow(tr, item): mutates an existing <tr> in-place with fresh values
    _diffTable(container, newData, keyField, createRow, updateRow) {
        let tbody = container.querySelector('tbody');
        // C3-2: If the table structure doesn't exist yet (e.g. container shows the
        // empty-state div), bootstrap a minimal table scaffold so the diff can proceed.
        if (!tbody) {
            if (newData.length === 0) return; // leave empty-state as-is; nothing to show
            // Container has empty-state markup — replace with a bare table so rows can be appended.
            container.innerHTML = '<div class="table-wrap"><table><tbody></tbody></table></div>';
            tbody = container.querySelector('tbody');
        }

        const existing = new Map();
        tbody.querySelectorAll('[data-key]').forEach(el => existing.set(el.dataset.key, el));

        // Track insertion order so rows stay sorted consistently.
        for (const item of newData) {
            const key = String(item[keyField]);
            if (existing.has(key)) {
                updateRow(existing.get(key), item);
                existing.delete(key);
            } else {
                tbody.appendChild(createRow(item));
            }
        }

        // Remove rows that are no longer in the data set.
        for (const [, el] of existing) el.remove();
    },

    // C2-5: _anomalyCardWrapper returns the HTMX-driven anomaly card placeholder.
    // On load + every 30s, HTMX swaps the outerHTML of this div with the server
    // response. When there are no anomalies the server returns an empty <div>,
    // effectively removing the card from the layout. When anomalies exist the
    // server returns the full card HTML (including card-header and card-body).
    // hx-headers sets HX-Request: true so the server returns the HTML partial.
    _anomalyCardWrapper() {
        return `<div id="dash-anomaly-htmx"
            hx-get="/api/v1/dashboard/anomalies"
            hx-trigger="load, every 30s"
            hx-swap="outerHTML"
            hx-headers='{"HX-Request": "true"}'></div>`;
    },

    // _attachAnomalyVisibilityObserver — no-op stub kept so call sites do not
    // need updating. The outerHTML swap strategy makes a MutationObserver
    // unnecessary (HTMX replaces the element itself on each poll).
    _attachAnomalyVisibilityObserver() {},

    // B2-4: _buildAnomalyCard renders an "Anomalies" card with clickable node filter CTAs.
    // Shows counts for: failed reimages, verify_timeout, never deployed, stale (>90d).
    // NOTE: kept for backward compatibility; replaced on the dashboard by C2-5 HTMX version.
    _buildAnomalyCard(nodes) {
        if (!nodes || nodes.length === 0) return '';
        const now = Date.now();
        const ninetyDays = 90 * 24 * 3600 * 1000;

        const failed    = nodes.filter(n => n._deployStatus === 'error' || n.deploy_verify_timeout_at).length;
        const neverDeployed = nodes.filter(n => n.base_image_id && !n.last_deploy_succeeded_at && !n.deploy_completed_preboot_at).length;
        const stale     = nodes.filter(n => n.last_deploy_succeeded_at && (now - new Date(n.last_deploy_succeeded_at).getTime()) > ninetyDays).length;

        const total = failed + neverDeployed + stale;
        if (total === 0) return ''; // no anomalies — hide the card

        const items = [
            failed      > 0 ? `<a href="#/nodes?filter=failed" style="display:flex;align-items:center;gap:8px;padding:8px 12px;border-radius:6px;background:var(--error-bg,#fef2f2);border:1px solid #fca5a5;text-decoration:none;color:inherit;">
                <span style="font-size:20px;font-weight:700;color:#dc2626;">${failed}</span>
                <span style="font-size:13px;color:#991b1b;">node${failed !== 1 ? 's' : ''} with failed deploy or verify timeout</span>
            </a>` : '',
            neverDeployed > 0 ? `<a href="#/nodes?filter=never_deployed" style="display:flex;align-items:center;gap:8px;padding:8px 12px;border-radius:6px;background:#fffbeb;border:1px solid #fde68a;text-decoration:none;color:inherit;">
                <span style="font-size:20px;font-weight:700;color:#d97706;">${neverDeployed}</span>
                <span style="font-size:13px;color:#92400e;">configured node${neverDeployed !== 1 ? 's' : ''} never deployed</span>
            </a>` : '',
            stale > 0 ? `<a href="#/nodes?filter=stale" style="display:flex;align-items:center;gap:8px;padding:8px 12px;border-radius:6px;background:var(--bg-secondary);border:1px solid var(--border);text-decoration:none;color:inherit;">
                <span style="font-size:20px;font-weight:700;color:var(--text-secondary);">${stale}</span>
                <span style="font-size:13px;color:var(--text-secondary);">node${stale !== 1 ? 's' : ''} with no successful deploy in >90 days</span>
            </a>` : '',
        ].filter(Boolean).join('');

        return cardWrap('Anomalies',
            `<div style="display:flex;flex-direction:column;gap:8px;">${items}</div>`,
            `<a href="#/nodes" class="btn btn-secondary btn-sm">View Nodes</a>`);
    },

    // B1-3: _operatorGroupsPanel — shows node groups visible to this operator on the dashboard.
    // Groups are derived from the node tags on nodes in the cluster.
    _operatorGroupsPanel(nodes) {
        // Collect all unique tag groups from nodes.
        const groupMap = new Map();
        for (const n of nodes) {
            const tags = n.tags || n.groups || [];
            for (const tag of tags) {
                if (!groupMap.has(tag)) groupMap.set(tag, { name: tag, total: 0, configured: 0 });
                const g = groupMap.get(tag);
                g.total++;
                if (n.base_image_id) g.configured++;
            }
        }
        if (groupMap.size === 0) {
            return `<div class="empty-state"><div class="empty-state-text">No node groups defined yet.</div></div>`;
        }
        const rows = [...groupMap.values()].sort((a, b) => a.name.localeCompare(b.name)).slice(0, 8).map(g => `
            <tr>
                <td><a href="#/nodes/groups/${encodeURIComponent(g.name)}" class="link-accent">${escHtml(g.name)}</a></td>
                <td class="text-dim">${g.total} node${g.total !== 1 ? 's' : ''}</td>
                <td class="text-dim">${g.configured} configured</td>
            </tr>`).join('');
        return `<div class="table-wrap"><table><thead><tr><th>Group</th><th>Nodes</th><th>Configured</th></tr></thead><tbody>${rows}</tbody></table></div>`;
    },

    // B1-3: _operatorRecentDeploysPanel — shows the most recently deployed nodes.
    _operatorRecentDeploysPanel(nodes) {
        const deployed = nodes
            .filter(n => n.last_deploy_succeeded_at || n.deploy_completed_preboot_at)
            .sort((a, b) => {
                const ta = new Date(a.last_deploy_succeeded_at || a.deploy_completed_preboot_at).getTime();
                const tb = new Date(b.last_deploy_succeeded_at || b.deploy_completed_preboot_at).getTime();
                return tb - ta;
            })
            .slice(0, 8);
        if (deployed.length === 0) {
            return `<div class="empty-state"><div class="empty-state-text">No successful deploys yet.</div></div>`;
        }
        const rows = deployed.map(n => {
            const ts = n.last_deploy_succeeded_at || n.deploy_completed_preboot_at;
            return `<tr>
                <td><a href="#/nodes/${n.id}" class="link-accent">${escHtml(n.hostname || n.primary_mac || n.id)}</a></td>
                <td class="text-dim" title="${escHtml(ts)}">${fmtRelative(ts)}</td>
            </tr>`;
        }).join('');
        return `<div class="table-wrap"><table><thead><tr><th>Node</th><th>Deployed</th></tr></thead><tbody>${rows}</tbody></table></div>`;
    },

    // _deployProgressTable renders the active deployments table from a MAC → DeployProgress map.
    _deployProgressTable(deployMap) {
        const all = Array.from(deployMap.values())
            .sort((a, b) => new Date(b.updated_at) - new Date(a.updated_at));
        // C3-1: cap at 20, show "+N more…" indicator when there are additional entries.
        const CAP = 20;
        const entries = all.slice(0, CAP);
        const overflow = all.length - CAP;

        // B2-7: better empty state with CTA.
        if (!entries.length) return emptyState('No deployments in progress', 'No deployments in progress. Trigger a reimage from the Nodes page.', '<a href="#/nodes" class="btn btn-secondary btn-sm" style="margin-top:8px;">Go to Nodes</a>');

        return `<div class="table-wrap"><table aria-label="Active deployments">
            <thead><tr>
                <th>Node</th><th>Phase</th><th>Progress</th><th>Speed</th><th>ETA</th><th>Updated</th>
            </tr></thead>
            <tbody>
            ${entries.map(p => {
                const pct = p.bytes_total > 0 ? Math.min(100, Math.round(p.bytes_done / p.bytes_total * 100)) : 0;
                const displayName = fmtHostname(p.hostname, p.node_mac);
                const barClass = p.phase === 'complete' ? 'complete' : p.phase === 'error' ? 'error' : '';

                let progressCell;
                if (p.phase === 'complete') {
                    progressCell = `<span style="color:var(--success);font-weight:600">&#10003; Done</span>`;
                } else if (p.phase === 'error') {
                    progressCell = `<span style="color:var(--error)" title="${escHtml(p.error || '')}">&#10007; Error${p.error ? ': ' + escHtml(p.error.slice(0, 60)) : ''}</span>`;
                } else if (p.bytes_total > 0) {
                    progressCell = `<div style="display:flex;align-items:center;gap:8px">
                        <div class="progress-bar-wrap" style="min-width:120px">
                            <div class="progress-bar-fill ${barClass}" style="width:${pct}%"></div>
                        </div>
                        <span class="text-dim text-sm" style="white-space:nowrap">${pct}% &nbsp;${fmtBytes(p.bytes_done)} / ${fmtBytes(p.bytes_total)}</span>
                    </div>`;
                } else if (p.message) {
                    progressCell = `<span class="text-dim text-sm">${escHtml(p.message)}</span>`;
                } else {
                    progressCell = `<span class="text-dim text-sm">—</span>`;
                }

                return `<tr data-mac="${escHtml(p.node_mac)}">
                    <td>${displayName}</td>
                    <td>${phaseBadge(p.phase)}</td>
                    <td>${progressCell}</td>
                    <td class="text-dim text-sm">${fmtSpeed(p.speed_bps)}</td>
                    <td class="text-dim text-sm">${p.phase !== 'complete' && p.phase !== 'error' ? fmtETA(p.eta_seconds) : '—'}</td>
                    <td class="text-dim text-sm">${fmtRelative(p.updated_at)}</td>
                </tr>`;
            }).join('')}
            ${overflow > 0 ? `<tr><td colspan="6" class="text-dim text-sm" style="text-align:center;padding:8px 12px">
                + ${overflow} more deployment${overflow !== 1 ? 's' : ''} — <a href="#/deploys" class="link-accent">view all</a>
            </td></tr>` : ''}
            </tbody>
        </table></div>`;
    },

    _buildRecentActivity(images, nodes) {
        const items = [];
        images.slice(0, 4).forEach(img => {
            items.push({ type: 'image', ts: img.created_at, data: img });
        });
        nodes.slice(0, 4).forEach(n => {
            items.push({ type: 'node', ts: n.created_at, data: n });
        });
        return items.sort((a, b) => new Date(b.ts) - new Date(a.ts)).slice(0, 8);
    },

    _activityTimeline(items) {
        return `<div class="timeline">` + items.map(item => {
            if (item.type === 'image') {
                const img = item.data;
                return `<div class="timeline-item">
                    <div class="timeline-icon timeline-icon-blue">
                        <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24">
                            <polygon points="12 2 2 7 12 12 22 7 12 2"/><polyline points="2 17 12 22 22 17"/><polyline points="2 12 12 17 22 12"/>
                        </svg>
                    </div>
                    <div class="timeline-body">
                        <div class="timeline-title">Image <a href="#/images/${img.id}" class="text-accent">${escHtml(img.name)}</a> ${badge(img.status)}</div>
                        <div class="timeline-ts">${fmtRelative(item.ts)}</div>
                    </div>
                </div>`;
            } else {
                const n = item.data;
                const hasHostname = n.hostname && n.hostname !== '(none)';
                const displayName = hasHostname ? n.hostname : n.primary_mac;
                const displayHtml = hasHostname
                    ? escHtml(n.hostname)
                    : `<span class="text-muted" style="font-style:italic">Unassigned</span>`;
                return `<div class="timeline-item">
                    <div class="timeline-icon timeline-icon-green">
                        <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24">
                            <rect x="2" y="2" width="20" height="8" rx="2"/><rect x="2" y="14" width="20" height="8" rx="2"/>
                            <line x1="6" y1="6" x2="6.01" y2="6"/><line x1="6" y1="18" x2="6.01" y2="18"/>
                        </svg>
                    </div>
                    <div class="timeline-body">
                        <div class="timeline-title">Node <a href="#/nodes/${n.id}" class="text-accent">${displayHtml}</a> registered ${nodeBadge(n)}</div>
                        <div class="timeline-ts">${fmtRelative(item.ts)}</div>
                    </div>
                </div>`;
            }
        }).join('') + `</div>`;
    },

    _imagesTable(images) {
        if (!images.length) return emptyState('No images yet', 'Pull an image from the Images page');
        return `<div class="table-wrap"><table aria-label="Images">
            <thead><tr>
                <th>Name</th><th>OS / Arch</th><th>Status</th><th>Size</th>
            </tr></thead>
            <tbody>
            ${images.map(img => `
                <tr class="clickable" data-key="${escHtml(img.id)}" onclick="Router.navigate('/images/${img.id}')">
                    <td>
                        <span style="font-weight:500">${escHtml(img.name)}</span>
                        ${img.version ? `<span class="text-dim text-sm"> v${escHtml(img.version)}</span>` : ''}
                    </td>
                    <td>
                        ${img.os   ? `<span class="badge badge-neutral badge-sm">${escHtml(img.os)}</span> ` : ''}
                        ${img.arch ? `<span class="badge badge-neutral badge-sm">${escHtml(img.arch)}</span>` : ''}
                        ${!img.os && !img.arch ? '<span class="text-dim">—</span>' : ''}
                    </td>
                    <td>${badge(img.status)}</td>
                    <td class="text-dim text-mono text-sm">${fmtBytes(img.size_bytes)}</td>
                </tr>`).join('')}
            </tbody>
        </table></div>`;
    },

    _nodesTable(nodes) {
        if (!nodes.length) return emptyState('No nodes configured', 'Add a node from the Nodes page');
        return `<div class="table-wrap"><table aria-label="Nodes">
            <thead><tr>
                <th>Host</th><th>Status</th><th>Updated</th>
            </tr></thead>
            <tbody>
            ${nodes.map(n => `
                <tr class="clickable" data-key="${escHtml(n.id)}" onclick="Router.navigate('/nodes/${n.id}')">
                    <td>
                        ${(n.hostname && n.hostname !== '(none)')
                            ? `<span style="font-weight:500">${escHtml(n.hostname)}</span>`
                            : `<span class="text-dim" style="font-style:italic">Unassigned</span>`}
                        <div class="text-dim text-sm text-mono">${escHtml(n.primary_mac || '—')}</div>
                    </td>
                    <td>${nodeBadge(n)}</td>
                    <td class="text-dim text-sm">${fmtRelative(n.updated_at)}</td>
                </tr>`).join('')}
            </tbody>
        </table></div>`;
    },

    // ── Images ─────────────────────────────────────────────────────────────

    async images(filterTag = '') {
        App.render(loading('Loading images…'));
        try {
            // C3-23: Initramfs card moved to Settings → System tab.
            const [resp] = await Promise.all([
                // Always load all images for tag collection; server-side ?tag= used when filtering.
                filterTag ? API.images.list('', filterTag) : API.images.list(),
            ]);
            const images = resp.images || [];

            // Collect all unique tags across all images for the filter dropdown.
            // When filtered we still want full tag list so re-load all in background.
            // For simplicity, tags are collected from the currently displayed set — good
            // enough since the dropdown is informational; all filtering is server-side.
            const allTagsSet = new Set();
            images.forEach(img => (img.tags || []).forEach(t => allTagsSet.add(t)));
            const allTags = Array.from(allTagsSet).sort();
            const tagFilterHtml = allTags.length > 0 || filterTag ? `
                <select id="img-tag-filter" onchange="Pages.images(this.value)"
                    style="padding:6px 10px;font-size:13px;border:1px solid var(--border-color);border-radius:4px;background:var(--bg-secondary);color:var(--text-primary)">
                    <option value="">All tags</option>
                    ${allTags.map(t => `<option value="${escHtml(t)}" ${t === filterTag ? 'selected' : ''}>${escHtml(t)}</option>`).join('')}
                    ${filterTag && !allTags.includes(filterTag) ? `<option value="${escHtml(filterTag)}" selected>${escHtml(filterTag)}</option>` : ''}
                </select>` : '';

            App.render(`
                <div class="page-header">
                    <div>
                        <h1 class="page-title">Images</h1>
                        <div class="page-subtitle">${images.length} image${images.length !== 1 ? 's' : ''} total${filterTag ? ` (filtered by tag: <strong>${escHtml(filterTag)}</strong>)` : ''}</div>
                    </div>
                    <div class="flex gap-8" style="align-items:center">
                        ${tagFilterHtml}
                        <!-- S5-8: secondary actions grouped together, Build from ISO is primary -->
                        <span title="Pull a pre-built image tarball from a URL or registry">
                            <button class="btn btn-secondary" onclick="Pages.showPullModal()">Pull</button>
                        </span>
                        <span title="Capture the running OS of an SSH-reachable host into a clustr image">
                            <button class="btn btn-secondary" onclick="Pages.showCaptureModal()">Capture from Host</button>
                        </span>
                        <span title="Import a raw ISO file already present on the server filesystem">
                            <button class="btn btn-secondary" onclick="Pages.showImportISOModal()">Import ISO</button>
                        </span>
                        <!-- Build from ISO is first-choice and visually highlighted -->
                        <button class="btn btn-primary" onclick="Pages.showBuildFromISOModal()">
                            <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24">
                                <rect x="2" y="3" width="20" height="14" rx="2"/><polyline points="8 21 12 17 16 21"/>
                            </svg>
                            Build from ISO
                        </button>
                    </div>
                </div>

                ${images.length === 0 && !filterTag ? `
                <!-- S5-8: Getting-started callout for new operators (Persona D) -->
                <div class="alert alert-info" style="margin-bottom:16px;display:flex;align-items:flex-start;gap:14px">
                    <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24" style="width:20px;height:20px;flex-shrink:0;margin-top:2px">
                        <circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/>
                    </svg>
                    <div>
                        <div style="font-weight:600;margin-bottom:4px">New here? Build from ISO is the recommended starting point.</div>
                        <div style="font-size:13px;color:var(--text-secondary)">
                            Provide a standard Linux ISO (Rocky, Ubuntu, Debian, RHEL) and clustr will perform an unattended install into a deployable image.
                            Use <strong>Pull</strong> if you have an existing clustr image tarball, <strong>Capture from Host</strong> to clone a running OS, or <strong>Import ISO</strong> to stage a raw ISO file.
                        </div>
                    </div>
                </div>` : ''}

                ${images.length === 0
                    ? `<div class="card"><div class="card-body">${emptyState(
                        filterTag ? `No images with tag "${filterTag}"` : 'No images yet',
                        filterTag ? 'Try a different tag filter or clear the filter.' : 'Use "Build from ISO" to create your first image.',
                        filterTag
                            ? `<button class="btn btn-secondary" onclick="Pages.images()">Clear Filter</button>`
                            : `<button class="btn btn-primary" onclick="Pages.showBuildFromISOModal()">Build from ISO</button>`
                    )}</div></div>`
                    : `<div class="image-grid">${images.map(img => this._imageCard(img)).join('')}</div>`
                }
            `);

            const hasBuilding = images.some(i => i.status === 'building' || i.status === 'interrupted');
            App.setAutoRefresh(() => Pages.images(filterTag), hasBuilding ? 5000 : 30000);

        } catch (e) {
            App.render(alertBox(`Failed to load images: ${e.message}`));
        }
    },

    // ── System initramfs card ───────────────────────────────────────────────

    _initramfsCard(info) {
        const sha = info && info.sha256 ? info.sha256.slice(0, 16) + '…' : 'not built';
        const size = info && info.size_bytes ? fmtBytes(info.size_bytes) : '—';
        const builtAt = info && info.build_time ? fmtRelative(info.build_time) : '—';
        const kernel = info && info.kernel_version ? escHtml(info.kernel_version) : '—';

        // Live sha256 — the sha256 of the file currently on disk, returned by GET
        // /system/initramfs.  Any history entry whose sha256 matches this is the
        // live image and must not be deleted.
        const liveSHA256 = (info && info.sha256) ? info.sha256 : null;

        const historyRows = (info && info.history && info.history.length > 0)
            ? info.history.map(h => {
                // Attribution: use label if present, fall back to key prefix, then "session".
                const by = h.triggered_by_label ? escHtml(h.triggered_by_label)
                         : h.triggered_by_prefix && h.triggered_by_prefix !== 'session' ? escHtml(h.triggered_by_prefix)
                         : 'session';
                // Show delete button for every entry that is not the currently-live
                // initramfs.  The live entry is identified by matching sha256 against
                // the on-disk file hash returned by GET /system/initramfs.  Pending
                // entries have no sha256 yet — never show delete for them.
                const isLive = h.outcome === 'success' && liveSHA256 && h.sha256 === liveSHA256;
                const isDeletable = !isLive && h.outcome !== 'pending';
                const delBtn = isDeletable
                    ? `<button class="btn btn-xs" style="padding:2px 6px;font-size:11px;color:var(--error);background:transparent;border:1px solid var(--error);border-radius:4px;cursor:pointer;line-height:1.2"
                         onclick="Pages.deleteInitramfsHistory('${escHtml(h.id)}')" title="Delete this entry">×</button>`
                    : '';
                return `<tr>
                <td class="text-mono text-sm">${escHtml(h.sha256 ? h.sha256.slice(0,16)+'…' : '—')}</td>
                <td class="text-sm">${escHtml(h.kernel_version || '—')}</td>
                <td class="text-sm">${fmtBytes(h.size_bytes)}</td>
                <td class="text-sm"><span class="badge ${h.outcome === 'success' ? 'badge-ready' : h.outcome === 'pending' ? 'badge-building' : 'badge-error'}">${escHtml(h.outcome)}</span></td>
                <td class="text-dim text-sm">${fmtRelative(h.started_at)}</td>
                <td class="text-dim text-sm">${by}</td>
                <td style="width:40px;text-align:center">${delBtn}</td>
            </tr>`;
            }).join('')
            : `<tr><td colspan="7" class="text-dim text-sm" style="text-align:center;padding:12px">No rebuild history</td></tr>`;

        return `<div class="card" style="margin-bottom:20px;border-left:3px solid var(--accent)">
            <div class="card-header">
                <h2 class="card-title">System Initramfs</h2>
                <div class="flex gap-8">
                    <button class="btn btn-secondary btn-sm" style="color:var(--error);border-color:var(--error)" onclick="Pages.cancelAllActiveDeploys()" title="Cancel all pending/running/triggered reimage requests — unblocks initramfs rebuild">
                        Cancel All Active Deploys
                    </button>
                    <button class="btn btn-secondary btn-sm" onclick="Pages.showRebuildInitramfsModal()">
                        Rebuild
                    </button>
                </div>
            </div>
            <div style="padding:0 20px 12px">
                <div style="display:grid;grid-template-columns:repeat(4,1fr);gap:12px;margin-bottom:16px">
                    <div>
                        <div class="text-dim text-sm">SHA256</div>
                        <div class="text-mono text-sm" style="margin-top:2px">${escHtml(sha)}</div>
                    </div>
                    <div>
                        <div class="text-dim text-sm">Size</div>
                        <div style="margin-top:2px">${size}</div>
                    </div>
                    <div>
                        <div class="text-dim text-sm">Built</div>
                        <div style="margin-top:2px">${builtAt}</div>
                    </div>
                    <div>
                        <div class="text-dim text-sm">Kernel</div>
                        <div style="margin-top:2px">${kernel}</div>
                    </div>
                </div>
                <div class="table-wrap">
                    <table id="initramfs-history-table" style="font-size:13px">
                        <thead><tr>
                            <th>SHA256</th><th>Kernel</th><th>Size</th><th>Outcome</th><th>When</th><th>By</th><th></th>
                        </tr></thead>
                        <tbody>${historyRows}</tbody>
                    </table>
                </div>
            </div>
        </div>`;
    },

    showRebuildInitramfsModal() {
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        overlay.id = 'rebuild-initramfs-modal';
        overlay.innerHTML = `
            <div class="modal" style="max-width:480px" aria-labelledby="modal-title-1">
                <div class="modal-header">
                    <span class="modal-title" id="modal-title-1">Rebuild System Initramfs</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('rebuild-initramfs-modal').remove()">×</button>
                </div>
                <div class="modal-body">
                    <p style="color:var(--text-secondary);margin-bottom:16px">
                        This will shell out to <code>scripts/build-initramfs.sh</code>, build a new initramfs image,
                        verify its sha256, and atomically replace the current one. The build takes 1–5 minutes.
                    </p>
                    <p style="color:var(--warning,#f59e0b);font-size:13px">
                        Rejected if any node has an active deployment in progress.
                    </p>
                    <div id="rebuild-log-pane" style="display:none;margin-top:16px;max-height:300px;overflow-y:auto;
                         background:var(--bg-tertiary,#0f1923);border-radius:6px;padding:12px;
                         font-family:monospace;font-size:12px;line-height:1.6;color:var(--text-secondary)">
                    </div>
                    <div id="rebuild-result" style="margin-top:12px;display:none"></div>
                </div>
                <div class="modal-footer" style="display:flex;gap:8px;justify-content:flex-end">
                    <button class="btn btn-secondary" onclick="document.getElementById('rebuild-initramfs-modal').remove()">Cancel</button>
                    <button class="btn btn-primary" id="rebuild-confirm-btn" onclick="Pages.confirmRebuildInitramfs()">Rebuild</button>
                </div>
            </div>`;
        document.body.appendChild(overlay);
        trapModalFocus(overlay, () => overlay.remove());
    },

    async confirmRebuildInitramfs() {
        const btn = document.getElementById('rebuild-confirm-btn');
        const logPane = document.getElementById('rebuild-log-pane');
        const resultDiv = document.getElementById('rebuild-result');
        if (btn) { btn.disabled = true; btn.textContent = 'Building…'; }
        if (logPane) { logPane.style.display = 'block'; logPane.textContent = 'Starting rebuild…\n'; }

        try {
            const result = await API.system.rebuildInitramfs();
            if (logPane && result && result.log_lines) {
                logPane.textContent = result.log_lines.join('\n');
                logPane.scrollTop = logPane.scrollHeight;
            }
            if (resultDiv) {
                resultDiv.style.display = 'block';
                resultDiv.innerHTML = `<div class="alert alert-success" role="status" style="background:rgba(16,185,129,0.1);border:1px solid var(--success);border-radius:6px;padding:12px;color:var(--success)">
                    Rebuild complete. New sha256: <code>${escHtml((result && result.sha256 || '').slice(0,16))}…</code>
                </div>`;
            }
            if (btn) { btn.textContent = 'Done'; }
            // Refresh the page after a moment to show new hash.
            setTimeout(() => {
                const modal = document.getElementById('rebuild-initramfs-modal');
                if (modal) modal.remove();
                Pages.images();
            }, 2000);
        } catch (e) {
            if (resultDiv) {
                resultDiv.style.display = 'block';
                // deploy_active: surface a cancel-all button so the operator can unblock.
                const isDeployActive = e.message && (
                    e.message.includes('active deployment') || e.message.includes('deploy_active')
                );
                if (isDeployActive) {
                    resultDiv.innerHTML = `<div class="alert alert-error" style="background:rgba(239,68,68,0.1);border:1px solid var(--error);border-radius:6px;padding:12px;color:var(--error)">
                        <div style="margin-bottom:8px"><strong>Blocked:</strong> ${escHtml(e.message)}</div>
                        <button class="btn btn-secondary btn-sm" style="color:var(--error);border-color:var(--error)"
                            onclick="Pages.cancelAllActiveDeploys().then(() => { document.getElementById('rebuild-initramfs-modal')?.remove(); Pages.images(); })">
                            Cancel All Active Deploys
                        </button>
                    </div>`;
                } else {
                    resultDiv.innerHTML = `<div class="alert alert-error" style="background:rgba(239,68,68,0.1);border:1px solid var(--error);border-radius:6px;padding:12px;color:var(--error)">Rebuild failed: ${escHtml(e.message)}</div>`;
                }
            }
            if (btn) { btn.disabled = false; btn.textContent = 'Retry'; }
        }
    },

    // ── Delete a single initramfs history entry ────────────────────────────

    async deleteInitramfsHistory(id) {
        Pages.showConfirmModal({
            title: 'Delete History Entry',
            message: 'Remove this initramfs history entry? This cannot be undone.',
            confirmText: 'Delete',
            danger: true,
            onConfirm: async () => {
                try {
                    await API.system.deleteInitramfsHistory(id);
                    const rows = document.querySelectorAll('#initramfs-history-table tbody tr');
                    rows.forEach(row => {
                        const btn = row.querySelector('button');
                        if (btn && btn.getAttribute('onclick') && btn.getAttribute('onclick').includes(id)) {
                            row.remove();
                        }
                    });
                    Pages.images();
                } catch (e) {
                    Pages.showAlertModal('Delete Failed', escHtml(e.message));
                }
            },
        });
    },

    // ── Image card with resume button ──────────────────────────────────────

    _imageCard(img) {
        const statusClass = {
            ready:       'badge-ready',
            building:    'badge-building',
            error:       'badge-error',
            archived:    'badge-archived',
            interrupted: 'badge-error',
        }[img.status] || 'badge-archived';

        const isResumable = img.status === 'interrupted' || img.status === 'error';
        const resumeBtn = isResumable
            ? `<button class="btn btn-secondary btn-sm" style="margin-top:8px;font-size:11px"
                onclick="event.stopPropagation();Pages.resumeImageBuild('${escHtml(img.id)}')">
                &#9654; Resume
               </button>`
            : '';

        return `<div class="image-card" onclick="Router.navigate('/images/${img.id}')">
            <div class="image-card-name" title="${escHtml(img.name)}">${escHtml(img.name)}</div>
            <div class="image-card-meta">
                <span class="badge ${statusClass}">${escHtml(img.status)}</span>
                ${img.os   ? `<span class="badge badge-neutral badge-sm">${escHtml(img.os)}</span>` : ''}
                ${img.arch ? `<span class="badge badge-neutral badge-sm">${escHtml(img.arch)}</span>` : ''}
                ${img.format ? `<span class="badge badge-neutral badge-sm">${escHtml(img.format)}</span>` : ''}
            </div>
            <div class="image-card-footer">
                <span class="text-mono">${fmtBytes(img.size_bytes)}</span>
                <span>${fmtRelative(img.created_at)}</span>
            </div>
            ${resumeBtn}
        </div>`;
    },

    async resumeImageBuild(imageId) {
        try {
            await API.resume.image(imageId);
            App.toast('Build resumed — polling for progress…', 'success');
            Pages.images();
        } catch (e) {
            App.toast(`Resume failed: ${e.message}`, 'error');
        }
    },

    showPullModal() {
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        overlay.id = 'pull-modal';
        overlay.innerHTML = `
            <div class="modal" style="max-width:600px" aria-labelledby="modal-title-2">
                <div class="modal-header">
                    <span class="modal-title" id="modal-title-2">Pull Image</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('pull-modal').remove()">×</button>
                </div>
                <div class="modal-body">
                    <form id="pull-form" onsubmit="Pages.submitPull(event)">
                        <div class="form-grid">
                            <div class="form-group" style="grid-column:1/-1">
                                <label>Image URL *</label>
                                <input type="url" name="url" id="pull-url"
                                    placeholder="https://example.com/image.tar.gz  or  https://…/Rocky-10.1-x86_64-dvd1.iso"
                                    required>
                                <div id="pull-iso-hint" style="display:none;margin-top:6px;padding:8px 10px;
                                    background:var(--bg-tertiary,#1e2a3a);border-radius:6px;font-size:12px;
                                    color:var(--text-secondary)">
                                    Installer ISO detected — clustr will run the installer in a temporary
                                    QEMU VM and capture the result as a base image (5-30 min).
                                    KVM acceleration is used when available.
                                </div>
                            </div>
                            <div class="form-group">
                                <label>Name *</label>
                                <input type="text" name="name" placeholder="rocky-10-base" required>
                            </div>
                            <div class="form-group">
                                <label>Version</label>
                                <input type="text" name="version" placeholder="10.1">
                            </div>
                            <div class="form-group">
                                <label>OS</label>
                                <input type="text" name="os" placeholder="rocky">
                            </div>
                            <div class="form-group">
                                <label>Arch</label>
                                <input type="text" name="arch" placeholder="x86_64">
                            </div>
                            <!-- Standard (non-ISO) fields -->
                            <div class="form-group" id="pull-format-group">
                                <label>Format</label>
                                <select name="format">
                                    <option value="filesystem">filesystem (tar)</option>
                                    <option value="block">block (raw/partclone)</option>
                                </select>
                            </div>
                            <!-- ISO-installer fields (shown only for .iso URLs) -->
                            <div class="form-group" id="pull-disk-group" style="display:none">
                                <label>Disk Size (GB)</label>
                                <input type="number" name="disk_size_gb" value="20" min="10" max="500">
                            </div>
                            <div class="form-group" id="pull-mem-group" style="display:none">
                                <label>VM Memory (MB)</label>
                                <input type="number" name="memory_mb" value="2048" min="512" max="32768">
                            </div>
                            <div class="form-group" id="pull-cpu-group" style="display:none">
                                <label>VM CPUs</label>
                                <input type="number" name="cpus" value="2" min="1" max="16">
                            </div>
                            <!-- Role presets (ISO mode only) -->
                            <div style="grid-column:1/-1" id="pull-roles-group" style="display:none">
                                <div style="font-size:13px;font-weight:600;margin-bottom:8px;color:var(--text-primary)">Node Roles <span style="font-weight:400;font-size:12px;color:var(--text-secondary)">(select all that apply)</span></div>
                                <div id="pull-roles-list" style="display:grid;gap:6px;margin-bottom:10px">
                                    <div style="font-size:12px;color:var(--text-secondary);font-style:italic">Loading roles…</div>
                                </div>
                                <div id="pull-roles-preview" style="font-size:12px;color:var(--text-secondary);margin-bottom:10px"></div>
                                <label style="display:flex;align-items:flex-start;gap:8px;cursor:pointer;font-size:13px">
                                    <input type="checkbox" name="install_updates" id="pull-install-updates" style="margin-top:2px;flex-shrink:0">
                                    <span>
                                        <strong>Install OS updates during build</strong><br>
                                        <span style="font-size:12px;color:var(--text-secondary)">Adds ~5-10 min. The resulting image will not need immediate patching on deploy.</span>
                                    </span>
                                </label>
                            </div>
                            <div class="form-group" style="grid-column:1/-1" id="pull-ks-group" style="display:none">
                                <label>Custom Kickstart / Autoinstall</label>
                                <textarea name="custom_kickstart" rows="3"
                                    placeholder="Paste a custom kickstart or autoinstall config here (optional — leave blank to use the auto-generated template)"
                                    style="width:100%;font-family:monospace;font-size:12px;resize:vertical"></textarea>
                            </div>
                            <div class="form-group" style="grid-column:1/-1">
                                <label>Notes</label>
                                <input type="text" name="notes" placeholder="Optional description">
                            </div>
                        </div>
                        <div id="pull-progress" style="display:none;margin-top:12px">
                            <div style="font-size:12px;color:var(--text-secondary);margin-bottom:6px" id="pull-progress-label">Submitting…</div>
                            <div class="progress-bar-wrap" style="width:100%">
                                <div class="progress-bar-fill" style="width:60%;animation:indeterminate 1.5s ease infinite"></div>
                            </div>
                        </div>
                        <div id="pull-result"></div>
                        <div class="form-actions">
                            <button type="button" class="btn btn-secondary" onclick="document.getElementById('pull-modal').remove()">Cancel</button>
                            <button type="submit" class="btn btn-primary" id="pull-btn">Pull Image</button>
                        </div>
                    </form>
                </div>
            </div>`;
        document.body.appendChild(overlay);
        overlay.addEventListener('click', e => { if (e.target === overlay) overlay.remove(); });
        trapModalFocus(overlay, () => overlay.remove());

        // Detect ISO URL and toggle ISO-specific fields.
        const urlInput    = document.getElementById('pull-url');
        const isoHint     = document.getElementById('pull-iso-hint');
        const fmtGroup    = document.getElementById('pull-format-group');
        const diskGroup   = document.getElementById('pull-disk-group');
        const memGroup    = document.getElementById('pull-mem-group');
        const cpuGroup    = document.getElementById('pull-cpu-group');
        const rolesGroup  = document.getElementById('pull-roles-group');
        const ksGroup     = document.getElementById('pull-ks-group');
        const pullBtn     = document.getElementById('pull-btn');
        let rolesLoaded   = false;

        // _loadRoles fetches the role list from the server and renders the picker.
        // Called once on first ISO URL detection; subsequent toggles reuse the DOM.
        const _loadRoles = async () => {
            if (rolesLoaded) return;
            rolesLoaded = true;
            const list    = document.getElementById('pull-roles-list');
            const preview = document.getElementById('pull-roles-preview');
            try {
                const resp   = await API.imageRoles.list();
                const roles  = resp.roles || [];
                list.innerHTML = roles.map(r => `
                    <label style="display:flex;align-items:flex-start;gap:8px;cursor:pointer;
                                  padding:6px 8px;border-radius:6px;
                                  background:var(--bg-tertiary,#1e2a3a);font-size:13px"
                           title="${escHtml(r.notes || '')}">
                        <input type="checkbox" name="role_ids" value="${escHtml(r.id)}"
                               style="margin-top:2px;flex-shrink:0"
                               onchange="Pages._updateRolePreview()">
                        <span>
                            <strong>${escHtml(r.name)}</strong>
                            <span style="color:var(--text-secondary);font-size:12px;display:block">${escHtml(r.description)}</span>
                            ${r.notes ? `<span style="color:var(--text-secondary);font-size:11px;font-style:italic;display:block">${escHtml(r.notes)}</span>` : ''}
                        </span>
                    </label>`).join('');
                Pages._updateRolePreview();
            } catch (ex) {
                list.innerHTML = `<div style="font-size:12px;color:var(--text-secondary)">Could not load role presets: ${escHtml(ex.message)}</div>`;
            }
        };

        const applyISOMode = (isISO) => {
            isoHint.style.display   = isISO ? 'block' : 'none';
            fmtGroup.style.display  = isISO ? 'none'  : '';
            diskGroup.style.display = isISO ? ''      : 'none';
            memGroup.style.display  = isISO ? ''      : 'none';
            cpuGroup.style.display  = isISO ? ''      : 'none';
            if (rolesGroup) rolesGroup.style.display = isISO ? '' : 'none';
            if (ksGroup) ksGroup.style.display = isISO ? '' : 'none';
            pullBtn.textContent = isISO ? 'Build from ISO' : 'Pull Image';
            if (isISO) _loadRoles();
        };

        urlInput.addEventListener('input', () => {
            const val = urlInput.value.trim().toLowerCase().split('?')[0];
            applyISOMode(val.endsWith('.iso'));
            // Auto-fill name/os from filename when URL looks like an ISO.
            if (val.endsWith('.iso')) {
                Pages._autoFillFromISOUrl(urlInput.value);
            }
        });

        urlInput.focus();
    },

    // _autoFillFromISOUrl populates Name/Version/OS inputs from an ISO filename.
    _autoFillFromISOUrl(isoURL) {
        const form    = document.getElementById('pull-form');
        if (!form) return;
        const nameEl  = form.elements['name'];
        const verEl   = form.elements['version'];
        const osEl    = form.elements['os'];
        if (!nameEl || !verEl || !osEl) return;

        const base = isoURL.split('/').pop().split('?')[0].replace(/\.iso$/i, '');
        if (!nameEl.value) {
            nameEl.value = base.toLowerCase().replace(/[^a-z0-9-]/g, '-').replace(/-+/g, '-').replace(/^-|-$/g, '');
        }
        if (!verEl.value) {
            const m = base.match(/(\d+\.\d+)/);
            if (m) verEl.value = m[1];
        }
        if (!osEl.value) {
            const lower = base.toLowerCase();
            if (lower.includes('rocky'))     osEl.value = 'rocky';
            else if (lower.includes('alma')) osEl.value = 'almalinux';
            else if (lower.includes('centos')) osEl.value = 'centos';
            else if (lower.includes('ubuntu')) osEl.value = 'ubuntu';
            else if (lower.includes('debian')) osEl.value = 'debian';
            else if (lower.includes('opensuse') || lower.includes('suse')) osEl.value = 'suse';
            else if (lower.includes('alpine')) osEl.value = 'alpine';
        }
    },

    // _updateRolePreview updates the "Selected: ..." line below the role picker.
    _updateRolePreview() {
        const form    = document.getElementById('pull-form');
        const preview = document.getElementById('pull-roles-preview');
        if (!form || !preview) return;
        const checked = [...form.querySelectorAll('input[name="role_ids"]:checked')];
        if (checked.length === 0) {
            preview.textContent = '';
            return;
        }
        const names = checked.map(cb => {
            const label = cb.closest('label');
            return label ? (label.querySelector('strong') || {}).textContent || cb.value : cb.value;
        });
        preview.textContent = 'Selected: ' + names.join(', ');
    },

    showImportISOModal() {
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        overlay.id = 'iso-modal';
        overlay.innerHTML = `
            <div class="modal" aria-labelledby="modal-title-3">
                <div class="modal-header">
                    <span class="modal-title" id="modal-title-3">Upload Image File</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('iso-modal').remove()">×</button>
                </div>
                <div class="modal-body">
                    <form id="iso-form" onsubmit="Pages.submitImportISO(event)">
                        <div class="form-group" style="margin-bottom:16px">
                            <label>Image File</label>
                            <div class="upload-zone" id="iso-drop-zone">
                                <input type="file" id="iso-file-input" accept=".iso,.img,.qcow2,.tar.gz,.tgz,.raw">
                                <div class="upload-zone-icon">&#8686;</div>
                                <div class="upload-zone-label">
                                    Drop an ISO, qcow2, img, or tarball here<br>
                                    <span style="font-size:12px">or click to browse</span>
                                </div>
                                <div class="upload-zone-filename" id="iso-filename"></div>
                            </div>
                        </div>
                        <div class="form-grid">
                            <div class="form-group">
                                <label>Name *</label>
                                <input type="text" name="name" id="iso-name" placeholder="rocky-9-hpc" required>
                            </div>
                            <div class="form-group">
                                <label>Version</label>
                                <input type="text" name="version" id="iso-version" placeholder="9.3">
                            </div>
                        </div>
                        <div class="upload-progress-wrap" id="iso-progress-wrap" style="display:none">
                            <div class="upload-progress-bar-outer">
                                <div class="upload-progress-bar-inner" id="iso-progress-bar"></div>
                            </div>
                            <div class="upload-progress-meta">
                                <span id="iso-progress-pct">0%</span>
                                <span id="iso-progress-speed"></span>
                                <span id="iso-progress-eta"></span>
                            </div>
                        </div>
                        <div id="iso-result"></div>
                        <div class="form-actions">
                            <button type="button" class="btn btn-secondary" onclick="document.getElementById('iso-modal').remove()">Cancel</button>
                            <button type="submit" class="btn btn-primary" id="iso-btn">Upload &amp; Import</button>
                        </div>
                    </form>
                </div>
            </div>`;
        document.body.appendChild(overlay);
        overlay.addEventListener('click', e => { if (e.target === overlay) overlay.remove(); });
        trapModalFocus(overlay, () => overlay.remove());

        // Wire up file picker and drag-and-drop after the DOM is appended.
        const zone    = document.getElementById('iso-drop-zone');
        const input   = document.getElementById('iso-file-input');
        const fnLabel = document.getElementById('iso-filename');
        const nameEl  = document.getElementById('iso-name');
        const verEl   = document.getElementById('iso-version');

        const applyFile = (file) => {
            if (!file) return;
            fnLabel.textContent = `${file.name} (${fmtBytes(file.size)})`;
            // Auto-populate name/version from filename when the fields are still empty.
            if (!nameEl.value) {
                const base = file.name.replace(/\.(iso|img|qcow2|tar\.gz|tgz|raw)$/i, '');
                nameEl.value = base.toLowerCase().replace(/[^a-z0-9-]/g, '-').replace(/-+/g, '-').replace(/^-|-$/g, '');
            }
            if (!verEl.value) {
                const m = file.name.match(/(\d+\.\d+)/);
                if (m) verEl.value = m[1];
            }
        };

        input.addEventListener('change', () => applyFile(input.files[0]));

        zone.addEventListener('dragover',  e => { e.preventDefault(); zone.classList.add('drag-over'); });
        zone.addEventListener('dragleave', () => zone.classList.remove('drag-over'));
        zone.addEventListener('drop', e => {
            e.preventDefault();
            zone.classList.remove('drag-over');
            const file = e.dataTransfer.files[0];
            if (file) {
                const dt = new DataTransfer();
                dt.items.add(file);
                input.files = dt.files;
                applyFile(file);
            }
        });
    },

    async submitPull(e) {
        e.preventDefault();
        const form  = e.target;
        const btn   = document.getElementById('pull-btn');
        const res   = document.getElementById('pull-result');
        const prog  = document.getElementById('pull-progress');
        const label = document.getElementById('pull-progress-label');
        const data  = new FormData(form);
        const url   = (data.get('url') || '').trim();
        const isISO = url.toLowerCase().split('?')[0].endsWith('.iso');

        btn.disabled = true;
        btn.textContent = 'Submitting…';
        if (prog) prog.style.display = 'block';
        if (label) label.textContent = isISO
            ? 'Starting ISO build (this can take 5-30 min — check the image status for progress)…'
            : 'Submitting pull request…';
        res.innerHTML = '';

        try {
            let img;
            if (isISO) {
                const roleIds = [...form.querySelectorAll('input[name="role_ids"]:checked')].map(cb => cb.value);
                const updatesEl = form.querySelector('input[name="install_updates"]');
                const body = {
                    url:              url,
                    name:             data.get('name'),
                    version:          data.get('version') || undefined,
                    os:               data.get('os')      || undefined,
                    arch:             data.get('arch')    || undefined,
                    disk_size_gb:     parseInt(data.get('disk_size_gb'), 10) || 0,
                    memory_mb:        parseInt(data.get('memory_mb'),    10) || 0,
                    cpus:             parseInt(data.get('cpus'),         10) || 0,
                    role_ids:         roleIds.length > 0 ? roleIds : undefined,
                    install_updates:  updatesEl ? updatesEl.checked : false,
                    custom_kickstart: data.get('custom_kickstart') || undefined,
                    notes:            data.get('notes')  || undefined,
                    tags:             [],
                };
                img = await API.factory.buildFromISO(body);
            } else {
                const body = {
                    url:     url,
                    name:    data.get('name'),
                    version: data.get('version'),
                    os:      data.get('os'),
                    arch:    data.get('arch'),
                    format:  data.get('format'),
                    notes:   data.get('notes'),
                    tags:    [],
                };
                img = await API.factory.pull(body);
            }
            if (prog) prog.style.display = 'none';
            const verb = isISO ? 'ISO build started' : 'Pull started';
            res.innerHTML = alertBox(`${verb}: ${escHtml(img.name)} (${img.id}) — status: ${img.status}`, 'success');
            form.reset();
            App.setAutoRefresh(() => Pages.images(), 5000);
            setTimeout(() => {
                const modal = document.getElementById('pull-modal');
                if (modal) modal.remove();
                Pages.images();
            }, 1500);
        } catch (ex) {
            if (prog) prog.style.display = 'none';
            res.innerHTML = alertBox(`${isISO ? 'ISO build' : 'Pull'} failed: ${ex.message}`);
            btn.disabled = false;
            btn.textContent = isISO ? 'Build from ISO' : 'Pull Image';
        }
    },

    async submitImportISO(e) {
        e.preventDefault();
        const form     = e.target;
        const btn      = document.getElementById('iso-btn');
        const res      = document.getElementById('iso-result');
        const input    = document.getElementById('iso-file-input');
        const progWrap = document.getElementById('iso-progress-wrap');
        const progBar  = document.getElementById('iso-progress-bar');
        const progPct  = document.getElementById('iso-progress-pct');
        const progSpd  = document.getElementById('iso-progress-speed');
        const progEta  = document.getElementById('iso-progress-eta');

        const file = input && input.files[0];
        if (!file) {
            res.innerHTML = alertBox('Please select a file to upload.');
            return;
        }

        const data = new FormData(form);
        btn.disabled = true;
        btn.textContent = 'Uploading…';
        res.innerHTML = '';
        if (progWrap) progWrap.style.display = 'block';

        const onProgress = (pct, loaded, total, bps, eta) => {
            if (!progBar) return;
            progBar.style.width  = `${pct}%`;
            progPct.textContent  = `${pct}%`;
            progSpd.textContent  = bps > 0 ? `${fmtBytes(Math.round(bps))}/s` : '';
            progEta.textContent  = eta > 0 ? `ETA ${Math.ceil(eta)}s` : '';
        };

        try {
            const img = await API.factory.uploadISO(file, {
                name:    data.get('name'),
                version: data.get('version'),
            }, onProgress);

            if (progBar) { progBar.style.width = '100%'; progBar.classList.add('complete'); }
            if (progPct) progPct.textContent = '100%';
            if (progEta) progEta.textContent = 'Done';

            res.innerHTML = alertBox(`Upload complete: ${img.name} (${img.id}) — processing in background`, 'success');
            App.setAutoRefresh(() => Pages.images(), 5000);
            setTimeout(() => {
                const modal = document.getElementById('iso-modal');
                if (modal) modal.remove();
                Pages.images();
            }, 2000);
        } catch (ex) {
            if (progBar) progBar.classList.add('error');
            res.innerHTML = alertBox(`Upload failed: ${ex.message}`);
            btn.disabled = false;
            btn.textContent = 'Upload & Import';
        }
    },

    async archiveImage(id, name) {
        Pages.showConfirmModal({
            title: 'Archive Image',
            message: `Archive image <strong>${escHtml(name)}</strong>? This cannot be undone.`,
            confirmText: 'Archive',
            danger: true,
            onConfirm: async () => {
                try {
                    await API.images.archive(id);
                    Pages.images();
                } catch (e) {
                    Pages.showAlertModal('Archive Failed', escHtml(e.message));
                }
            },
        });
    },

    // showDeleteImageModal — opens a confirmation modal for real image deletion.
    // When the image is in-use by nodes, shows them and offers a force-delete checkbox.
    async showDeleteImageModal(id, name) {
        // Pre-fetch to see if any nodes are using the image.
        // B4-6: use API.nodes.list() instead of direct API.get('/nodes').
        let nodes = [];
        try {
            const resp = await API.nodes.list({ base_image_id: id });
            nodes = (resp && resp.nodes) || [];
        } catch (_) {}

        const nodesHtml = nodes.length
            ? `<div style="margin:12px 0 8px;padding:10px 12px;background:var(--bg-secondary);border-radius:6px;border:1px solid var(--border)">
                <div style="font-size:12px;font-weight:600;color:var(--text-secondary);margin-bottom:6px">Nodes using this image:</div>
                ${nodes.map(n => `<div style="font-size:13px;font-family:var(--font-mono);padding:2px 0">${escHtml(n.hostname || n.primary_mac)}</div>`).join('')}
               </div>
               <label style="display:flex;align-items:center;gap:8px;cursor:pointer;margin:4px 0 12px">
                   <input type="checkbox" id="delete-image-force">
                   <span style="font-size:13px">Force delete — unassign ${nodes.length} node${nodes.length !== 1 ? 's' : ''} and delete anyway</span>
               </label>`
            : `<p style="margin:8px 0 16px;color:var(--text-secondary);font-size:13px">This will permanently remove the image and all associated files. This action cannot be undone.</p>`;

        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        overlay.id = 'delete-image-modal';
        overlay.innerHTML = `
            <div class="modal" style="max-width:480px" aria-labelledby="modal-title-4">
                <div class="modal-header">
                    <span class="modal-title" id="modal-title-4">Delete Image</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('delete-image-modal').remove()">×</button>
                </div>
                <div class="modal-body">
                    <p style="margin:0 0 4px;font-weight:600">${escHtml(name)}</p>
                    ${nodesHtml}
                    <div id="delete-image-error" style="display:none" class="form-error"></div>
                    <div class="form-actions" style="margin-top:0">
                        <button class="btn btn-secondary" onclick="document.getElementById('delete-image-modal').remove()">Cancel</button>
                        <button class="btn btn-danger" id="delete-image-confirm-btn" onclick="Pages._confirmDeleteImage('${id}')">Delete Permanently</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(overlay);
        overlay.addEventListener('click', e => { if (e.target === overlay) overlay.remove(); });
        trapModalFocus(overlay, () => overlay.remove());
    },

    async _confirmDeleteImage(id) {
        const force = !!(document.getElementById('delete-image-force') || {}).checked;
        const btn = document.getElementById('delete-image-confirm-btn');
        const errEl = document.getElementById('delete-image-error');
        if (btn) { btn.disabled = true; btn.textContent = 'Deleting…'; }
        if (errEl) errEl.style.display = 'none';
        try {
            await API.images.delete(id, { force });
            const modal = document.getElementById('delete-image-modal');
            if (modal) modal.remove();
            Router.navigate('/images');
        } catch (e) {
            if (errEl) { errEl.textContent = e.message; errEl.style.display = 'block'; }
            if (btn) { btn.disabled = false; btn.textContent = 'Delete Permanently'; }
        }
    },

    // ── Capture from Host ──────────────────────────────────────────────────

    showCaptureModal(prefillHost = '', prefillName = '') {
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        overlay.id = 'capture-modal';
        overlay.innerHTML = `
            <div class="modal" style="max-width:560px" aria-labelledby="modal-title-5">
                <div class="modal-header">
                    <span class="modal-title" id="modal-title-5">Capture from Host</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('capture-modal').remove()">×</button>
                </div>
                <div class="modal-body">
                    <div class="alert alert-info" role="note" style="margin-bottom:16px;font-size:12px">
                        The server will SSH to the source host and rsync its filesystem into a new image.
                        SSH host key verification is disabled — only use this on trusted golden nodes.
                    </div>
                    <form id="capture-form" onsubmit="Pages.submitCapture(event)">
                        <div class="form-group" style="margin-bottom:14px">
                            <label>Source Host * <span style="font-size:11px;color:var(--text-secondary)">(user@host or host)</span></label>
                            <input type="text" name="source_host" placeholder="root@192.168.1.10" value="${escHtml(prefillHost)}" required>
                        </div>
                        <div class="form-grid" style="margin-bottom:14px">
                            <div class="form-group">
                                <label>Name *</label>
                                <input type="text" name="name" placeholder="rocky9-golden" value="${escHtml(prefillName)}" required>
                            </div>
                            <div class="form-group">
                                <label>Version</label>
                                <input type="text" name="version" placeholder="1.0.0" value="1.0.0">
                            </div>
                            <div class="form-group">
                                <label>OS</label>
                                <input type="text" name="os" placeholder="Rocky Linux 9">
                            </div>
                            <div class="form-group">
                                <label>Arch</label>
                                <input type="text" name="arch" placeholder="x86_64" value="x86_64">
                            </div>
                            <div class="form-group">
                                <label>SSH Port</label>
                                <input type="number" name="ssh_port" value="22" min="1" max="65535">
                            </div>
                        </div>
                        <div style="margin-bottom:14px">
                            <label style="font-size:12px;font-weight:600;color:var(--text-secondary);margin-bottom:6px;display:block">SSH Authentication</label>
                            <div class="tab-bar" style="margin-bottom:10px">
                                <div class="tab active" id="ssh-tab-key" onclick="Pages._switchCaptureAuth('key')">Private Key (server path)</div>
                                <div class="tab" id="ssh-tab-pwd" onclick="Pages._switchCaptureAuth('pwd')">Password</div>
                            </div>
                            <div id="ssh-auth-key">
                                <div class="form-group">
                                    <label>Key Path <span style="font-size:11px;color:var(--text-secondary)">(absolute path on the server)</span></label>
                                    <input type="text" name="ssh_key_path" placeholder="/etc/clustr/keys/golden_key">
                                </div>
                            </div>
                            <div id="ssh-auth-pwd" style="display:none">
                                <div class="form-group">
                                    <label>Password <span style="font-size:11px;color:var(--text-secondary)">(requires sshpass on server)</span></label>
                                    <input type="password" name="ssh_password" autocomplete="off">
                                </div>
                            </div>
                        </div>
                        <div class="form-group" style="margin-bottom:14px">
                            <label>Extra Exclude Paths <span style="font-size:11px;color:var(--text-secondary)">(one per line, beyond defaults)</span></label>
                            <textarea name="exclude_paths" rows="3" placeholder="/opt/scratch&#10;/data/volatile"></textarea>
                        </div>
                        <div class="form-group" style="margin-bottom:14px">
                            <label>Notes</label>
                            <input type="text" name="notes" placeholder="Optional description">
                        </div>
                        <div id="capture-progress" style="display:none;margin-top:12px">
                            <div style="font-size:12px;color:var(--text-secondary);margin-bottom:6px">Submitting capture request…</div>
                            <div class="progress-bar-wrap" style="width:100%">
                                <div class="progress-bar-fill" style="width:60%;animation:indeterminate 1.5s ease infinite"></div>
                            </div>
                        </div>
                        <div id="capture-result"></div>
                        <div class="form-actions">
                            <button type="button" class="btn btn-secondary" onclick="document.getElementById('capture-modal').remove()">Cancel</button>
                            <button type="submit" class="btn btn-primary" id="capture-btn">Start Capture</button>
                        </div>
                    </form>
                </div>
            </div>`;
        document.body.appendChild(overlay);
        overlay.addEventListener('click', e => { if (e.target === overlay) overlay.remove(); });
        trapModalFocus(overlay, () => overlay.remove());
        const firstInput = overlay.querySelector('input[name="source_host"]');
        if (firstInput && !prefillHost) firstInput.focus();
    },

    _switchCaptureAuth(tab) {
        const keyDiv = document.getElementById('ssh-auth-key');
        const pwdDiv = document.getElementById('ssh-auth-pwd');
        const keyTab = document.getElementById('ssh-tab-key');
        const pwdTab = document.getElementById('ssh-tab-pwd');
        if (tab === 'key') {
            if (keyDiv) keyDiv.style.display = '';
            if (pwdDiv) pwdDiv.style.display = 'none';
            if (keyTab) keyTab.classList.add('active');
            if (pwdTab) pwdTab.classList.remove('active');
        } else {
            if (keyDiv) keyDiv.style.display = 'none';
            if (pwdDiv) pwdDiv.style.display = '';
            if (keyTab) keyTab.classList.remove('active');
            if (pwdTab) pwdTab.classList.add('active');
        }
    },

    async submitCapture(e) {
        e.preventDefault();
        const form = e.target;
        const btn  = document.getElementById('capture-btn');
        const res  = document.getElementById('capture-result');
        const prog = document.getElementById('capture-progress');
        const data = new FormData(form);

        btn.disabled = true;
        btn.textContent = 'Submitting…';
        if (prog) prog.style.display = 'block';
        res.innerHTML = '';

        const excludeRaw = (data.get('exclude_paths') || '').split('\n').map(s => s.trim()).filter(Boolean);

        try {
            const body = {
                source_host:  data.get('source_host'),
                ssh_user:     '',  // embedded in source_host if user@host form
                ssh_key_path: data.get('ssh_key_path') || '',
                ssh_password: data.get('ssh_password') || '',
                ssh_port:     parseInt(data.get('ssh_port') || '22', 10),
                name:         data.get('name'),
                version:      data.get('version') || '1.0.0',
                os:           data.get('os') || '',
                arch:         data.get('arch') || 'x86_64',
                exclude_paths: excludeRaw,
                notes:        data.get('notes') || '',
                tags:         [],
            };
            const img = await API.factory.capture(body);
            if (prog) prog.style.display = 'none';
            res.innerHTML = alertBox(
                `Capture started: ${img.name} (${img.id.substring(0, 8)}) — status: ${img.status}. ` +
                `The server is rsyncing from ${body.source_host} — this may take several minutes.`,
                'success'
            );
            App.setAutoRefresh(() => Pages.images(), 5000);
            setTimeout(() => {
                const modal = document.getElementById('capture-modal');
                if (modal) modal.remove();
                Pages.images();
            }, 2500);
        } catch (ex) {
            if (prog) prog.style.display = 'none';
            res.innerHTML = alertBox(`Capture failed: ${ex.message}`);
            btn.disabled = false;
            btn.textContent = 'Start Capture';
        }
    },

    // ── Image Detail ───────────────────────────────────────────────────────

    async imageDetail(id) {
        App.render(loading('Loading image…'));
        try {
            // Fetch image and metadata in parallel; metadata may not exist (404 is fine).
            const [img, meta] = await Promise.all([
                API.images.get(id),
                API.images.metadata(id).catch(() => null),
            ]);

            App.render(`
                <div class="breadcrumb">
                    <a href="#/images">Images</a>
                    <span class="breadcrumb-sep">/</span>
                    <span>${escHtml(img.name)}</span>
                </div>
                <div class="page-header">
                    <div style="display:flex;align-items:center;gap:12px">
                        <button class="detail-back-btn" onclick="Router.navigate('/images')">
                            <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24">
                                <polyline points="15 18 9 12 15 6"/>
                            </svg>
                            Back
                        </button>
                        <div>
                            <h1 class="page-title">${escHtml(img.name)}</h1>
                            <div class="page-subtitle">${escHtml(img.id)}</div>
                        </div>
                        ${badge(img.status)}
                    </div>
                    <div class="flex gap-8">
                        ${img.status === 'ready'
                            ? `<button class="btn btn-secondary" onclick="Pages.openShellTerminal('${escHtml(img.id)}')">Shell Access</button>`
                            : ''}
                        ${img.status === 'ready'
                            // C3-6: blob download via fetch() so non-2xx responses show an error toast.
                            ? `<button class="btn btn-secondary btn-sm" onclick="Pages._downloadImageBlob('${escHtml(img.id)}', '${escHtml(img.name)}')">Download Image</button>`
                            : ''}
                        ${Auth._role === 'admin' ? `<button class="btn btn-danger btn-sm" onclick="Pages.showDeleteImageModal('${img.id}', '${escHtml(img.name)}')">Delete Image</button>` : ''}
                    </div>
                </div>

                ${img.error_message ? alertBox(`Error: ${img.error_message}`) : ''}
                ${img.status === 'building' ? Pages._isoBuildInProgress(img) : ''}

                ${cardWrap('Image Details', `
                    <div class="kv-grid">
                        <div class="kv-item"><div class="kv-key">ID</div><div class="kv-value">${escHtml(img.id)}</div></div>
                        <div class="kv-item"><div class="kv-key">Name</div><div class="kv-value">${escHtml(img.name)}</div></div>
                        <div class="kv-item"><div class="kv-key">Version</div><div class="kv-value">${escHtml(img.version || '—')}</div></div>
                        <div class="kv-item"><div class="kv-key">OS</div><div class="kv-value">${escHtml(img.os || '—')}</div></div>
                        <div class="kv-item"><div class="kv-key">Arch</div><div class="kv-value">${escHtml(img.arch || '—')}</div></div>
                        <div class="kv-item"><div class="kv-key">Format</div><div class="kv-value">${escHtml(img.format || '—')}</div></div>
                        <div class="kv-item"><div class="kv-key">Firmware</div><div class="kv-value">${img.firmware === 'bios' ? '<span class="badge badge-warning badge-sm">BIOS (legacy)</span>' : '<span class="badge badge-neutral badge-sm">UEFI</span>'}</div></div>
                        <div class="kv-item"><div class="kv-key">Size</div><div class="kv-value">${fmtBytes(img.size_bytes)}</div></div>
                        <div class="kv-item"><div class="kv-key">Checksum (sha256)</div><div class="kv-value" style="font-size:11px">${escHtml(img.checksum || '—')}</div></div>
                        <div class="kv-item" style="grid-column:1/-1"><div class="kv-key">Source URL</div>
                            <div class="kv-value" style="font-size:12px">${img.source_url
                                ? `<a href="${escHtml(img.source_url)}" target="_blank" rel="noreferrer">${escHtml(img.source_url)}</a>`
                                : '—'}</div>
                        </div>
                        <div class="kv-item" style="grid-column:1/-1">
                            <div class="kv-key">Tags</div>
                            <div class="kv-value" id="img-tags-display">
                                ${Pages._renderImageTagEditor(img.id, img.tags || [])}
                            </div>
                        </div>
                        <div class="kv-item"><div class="kv-key">Notes</div><div class="kv-value">${escHtml(img.notes || '—')}</div></div>
                        <div class="kv-item"><div class="kv-key">Created</div><div class="kv-value">${fmtDate(img.created_at)}</div></div>
                        <div class="kv-item"><div class="kv-key">Finalized</div><div class="kv-value">${fmtDate(img.finalized_at)}</div></div>
                    </div>`)}

                ${meta && !meta.error ? cardWrap('Build Metadata', `
                    <div class="kv-grid">
                        ${meta.kernel_version ? `<div class="kv-item"><div class="kv-key">Kernel</div><div class="kv-value"><code>${escHtml(meta.kernel_version)}</code></div></div>` : ''}
                        ${meta.cuda_version ? `<div class="kv-item"><div class="kv-key">CUDA</div><div class="kv-value"><code>${escHtml(meta.cuda_version)}</code></div></div>` : ''}
                        ${meta.build_method ? `<div class="kv-item"><div class="kv-key">Build Method</div><div class="kv-value">${escHtml(meta.build_method)}</div></div>` : ''}
                        ${meta.build_timestamp ? `<div class="kv-item"><div class="kv-key">Build Timestamp</div><div class="kv-value">${escHtml(meta.build_timestamp)}</div></div>` : ''}
                        ${meta.distro ? `<div class="kv-item"><div class="kv-key">Distro</div><div class="kv-value">${escHtml(meta.distro)}</div></div>` : ''}
                        ${meta.firmware ? `<div class="kv-item"><div class="kv-key">Firmware (detected)</div><div class="kv-value">${escHtml(meta.firmware)}</div></div>` : ''}
                        ${(meta.installed_packages_summary && meta.installed_packages_summary.length > 0) ? `
                        <div class="kv-item" style="grid-column:1/-1">
                            <div class="kv-key">Installed Packages (summary)</div>
                            <div class="kv-value" style="font-size:12px">${meta.installed_packages_summary.slice(0, 20).map(p => escHtml(p)).join(', ')}${meta.installed_packages_summary.length > 20 ? ` <span class="text-dim">+${meta.installed_packages_summary.length - 20} more</span>` : ''}</div>
                        </div>` : ''}
                    </div>`) : ''}

                ${img.disk_layout ? cardWrap('Disk Layout', `
                    ${this._renderDiskLayout(img.disk_layout)}
                `) : ''}

                <div id="shell-hint-area"></div>
            `);

            if (img.status === 'building') {
                if (img.build_method === 'iso') {
                    // ISO build: subscribe to the SSE build progress stream for
                    // live phase, serial console, and byte progress updates.
                    Pages._startIsoBuildSSE(id);
                } else {
                    // Non-ISO build (pull, capture, import): fall back to polling.
                    App.setAutoRefresh(async () => {
                        try {
                            const fresh = await API.images.get(id);
                            if (fresh.status !== 'building') {
                                Pages.imageDetail(id);
                            }
                        } catch (_) {}
                    }, 5000);
                }
            }
        } catch (e) {
            App.render(alertBox(`Failed to load image: ${e.message}`));
        }
    },

    // C3-6: _downloadImageBlob downloads the image blob via fetch() so that
    // non-2xx responses (e.g. 404 blob not found, 503 too many streams) are
    // surfaced as an error toast rather than a silent browser failure.
    async _downloadImageBlob(imageId, imageName) {
        const btn = event && event.target;
        if (btn) { btn.disabled = true; btn.textContent = 'Preparing…'; }
        try {
            const url = `/api/v1/images/${encodeURIComponent(imageId)}/blob`;
            // Build auth headers without Content-Type (this is a GET, not a JSON post).
            const headers = {};
            const tok = API._token();
            if (tok) headers['Authorization'] = `Bearer ${tok}`;
            const resp = await fetch(url, {
                headers,
                credentials: 'same-origin',
            });
            if (!resp.ok) {
                let errMsg = `Download failed (${resp.status})`;
                try { const j = await resp.json(); errMsg = j.error || errMsg; } catch (_) {}
                App.toast(errMsg, 'error');
                return;
            }
            // Stream the response into a Blob, then trigger browser download.
            const blob = await resp.blob();
            const objUrl = URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = objUrl;
            // Use imageName as filename, stripping unsafe characters.
            a.download = (imageName || imageId).replace(/[/\\?%*:|"<>]/g, '_') + '.tar';
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
            setTimeout(() => URL.revokeObjectURL(objUrl), 10000);
        } catch (e) {
            App.toast(`Download error: ${e.message}`, 'error');
        } finally {
            if (btn) { btn.disabled = false; btn.textContent = 'Download Image'; }
        }
    },

    // _renderImageTagEditor renders the interactive tag editor for the image detail page (S2-3).
    // Tags are displayed as removable chips with an inline add-tag input.
    _renderImageTagEditor(imageId, tags) {
        const chips = tags.map(t => `
            <span class="badge badge-neutral" style="display:inline-flex;align-items:center;gap:4px">
                ${escHtml(t)}
                <button type="button" class="tag-remove-btn" title="Remove tag"
                    onclick="Pages._removeImageTag('${escHtml(imageId)}', '${escHtml(t)}')"
                    style="background:none;border:none;cursor:pointer;padding:0;line-height:1;color:inherit;opacity:0.7;font-size:12px">&times;</button>
            </span>`).join(' ');
        return `
            <div style="display:flex;flex-wrap:wrap;gap:6px;align-items:center" id="img-tag-chips">
                ${chips || '<span class="text-dim" style="font-size:12px">No tags</span>'}
            </div>
            <div style="display:flex;gap:6px;margin-top:8px;align-items:center">
                <input type="text" id="img-tag-input" placeholder="Add tag…"
                    style="width:160px;padding:4px 8px;font-size:13px;border:1px solid var(--border-color);border-radius:4px;background:var(--bg-secondary);color:var(--text-primary)"
                    onkeydown="if(event.key==='Enter'){event.preventDefault();Pages._addImageTag('${escHtml(imageId)}');}">
                <button type="button" class="btn btn-secondary btn-sm" onclick="Pages._addImageTag('${escHtml(imageId)}')">Add</button>
                <span id="img-tag-error" style="color:var(--color-danger);font-size:12px"></span>
            </div>`;
    },

    // _addImageTag adds a tag to the image and refreshes the tag display (S2-3).
    async _addImageTag(imageId) {
        const input = document.getElementById('img-tag-input');
        const errEl = document.getElementById('img-tag-error');
        if (!input) return;
        const newTag = input.value.trim();
        if (!newTag) return;

        // Gather current tags from existing chips.
        const chips = document.querySelectorAll('#img-tag-chips .badge');
        const current = Array.from(chips).map(c => c.textContent.trim().replace(/×$/, '').trim()).filter(Boolean);
        if (current.includes(newTag)) {
            if (errEl) errEl.textContent = 'Tag already exists';
            return;
        }
        const updated = [...current, newTag];
        try {
            const img = await API.images.updateTags(imageId, updated);
            const container = document.getElementById('img-tags-display');
            if (container) container.innerHTML = Pages._renderImageTagEditor(imageId, img.tags || []);
            if (errEl) errEl.textContent = '';
        } catch (e) {
            if (errEl) errEl.textContent = `Failed: ${e.message}`;
        }
    },

    // _removeImageTag removes a tag from the image and refreshes the tag display (S2-3).
    // B4-3: DOM removal moved to success path; errors surfaced via App.toast.
    async _removeImageTag(imageId, tagToRemove) {
        const chips = document.querySelectorAll('#img-tag-chips .badge');
        const current = Array.from(chips).map(c => c.textContent.trim().replace(/×$/, '').trim()).filter(Boolean);
        const updated = current.filter(t => t !== tagToRemove);
        try {
            const img = await API.images.updateTags(imageId, updated);
            // Only update DOM after server confirms the change.
            const container = document.getElementById('img-tags-display');
            if (container) container.innerHTML = Pages._renderImageTagEditor(imageId, img.tags || []);
        } catch (e) {
            App.toast(`Failed to remove tag: ${e.message}`, 'error');
        }
    },

    _renderDiskLayout(layout) {
        if (!layout) return '<pre class="json-block">null</pre>';
        if (typeof layout === 'object') {
            const disks = layout.disks || layout.Disks || [];
            if (disks.length > 0) {
                return disks.map(disk => {
                    const parts = disk.partitions || disk.Partitions || [];
                    const totalBytes = parts.reduce((s, p) => s + (p.size_bytes || p.Size || 0), 0) || 1;
                    const segColors = ['seg-boot', 'seg-root', 'seg-swap', 'seg-data', 'seg-other'];
                    const barHtml = parts.map((p, i) => {
                        const sz = p.size_bytes || p.Size || 0;
                        const pct = Math.max(3, Math.round((sz / totalBytes) * 100));
                        const label = p.name || p.Name || p.mountpoint || p.MountPoint || `p${i+1}`;
                        const cls = segColors[i % segColors.length];
                        return `<div class="${cls} disk-segment" style="flex:${pct}" title="${escHtml(label)}: ${fmtBytes(sz)}">
                            ${pct > 8 ? escHtml(label.replace('/dev/', '').replace('sda', '').replace('nvme0n1', '')) : ''}
                        </div>`;
                    }).join('');
                    return `<div style="margin-bottom:16px">
                        <div style="font-size:12px;font-family:var(--font-mono);font-weight:600;color:var(--text-secondary);margin-bottom:6px">
                            ${escHtml(disk.name || disk.Name || 'disk')} — ${fmtBytes(disk.size_bytes || disk.Size || 0)}
                        </div>
                        <div class="disk-bar">${barHtml || '<div class="seg-other disk-segment" style="flex:1">unpartitioned</div>'}</div>
                        <div style="display:flex;flex-wrap:wrap;gap:8px;margin-top:6px">
                            ${parts.map((p, i) => `<span style="display:inline-flex;align-items:center;gap:5px;font-size:11px;color:var(--text-secondary)">
                                <span style="width:10px;height:10px;border-radius:2px;display:inline-block" class="${segColors[i % segColors.length]}"></span>
                                ${escHtml(p.name || p.Name || p.mountpoint || p.MountPoint || `p${i+1}`)} (${fmtBytes(p.size_bytes || p.Size || 0)})
                            </span>`).join('')}
                        </div>
                    </div>`;
                }).join('') + `<details style="margin-top:12px">
                    <summary>Raw JSON</summary>
                    <pre class="json-block" style="margin:12px">${escHtml(JSON.stringify(layout, null, 2))}</pre>
                </details>`;
            }
        }
        return `<pre class="json-block">${escHtml(JSON.stringify(layout, null, 2))}</pre>`;
    },

    showShellHint(id) {
        // Legacy fallback — replaced by openShellTerminal.
        this.openShellTerminal(id);
    },

    // ── Browser Shell Terminal ─────────────────────────────────────────────

    _shellTerm: null,          // active xterm.js Terminal instance
    _shellWs: null,            // active WebSocket
    _shellSessionId: null,     // active session ID for cleanup
    _shellImageId: null,       // active image ID for cleanup

    async openShellTerminal(imageId) {
        // Create session on server first.
        let sess;
        try {
            sess = await API.images.openShellSession(imageId);
        } catch (e) {
            Pages.showAlertModal('Shell Session Failed', escHtml(e.message));
            return;
        }

        this._shellSessionId = sess.session_id;
        this._shellImageId = imageId;

        // Check for active deploys (for the warning banner).
        let activeCount = 0;
        try {
            const ad = await API.images.activeDeploys(imageId);
            activeCount = ad.active_count || 0;
        } catch (_) {}

        // Build modal HTML.
        const warnHtml = activeCount > 0
            ? `<div class="shell-modal-warn">
                &#9888; This image is currently being deployed to ${activeCount} node${activeCount !== 1 ? 's' : ''}.
                Shell access is safe (read-only race) but changes won\'t affect in-progress deployments.
               </div>`
            : '';

        const overlay = document.createElement('div');
        overlay.className = 'shell-modal-overlay';
        overlay.id = 'shell-modal-overlay';
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        overlay.setAttribute('aria-labelledby', 'shell-modal-title');
        overlay.innerHTML = `
            <div class="shell-modal">
                <div class="shell-modal-header">
                    <div style="display:flex;align-items:center;gap:12px">
                        <div class="shell-modal-dots">
                            <div class="shell-modal-dot red"></div>
                            <div class="shell-modal-dot yellow"></div>
                            <div class="shell-modal-dot green"></div>
                        </div>
                        <span class="shell-modal-title" id="shell-modal-title">shell &mdash; ${escHtml(imageId)}</span>
                    </div>
                    <button class="shell-modal-close" onclick="Pages.closeShellTerminal()" title="Close terminal">&times;</button>
                </div>
                ${warnHtml}
                <div class="shell-modal-body">
                    <div id="shell-terminal-container"></div>
                </div>
                <div class="shell-status-bar">
                    <span class="shell-status-indicator connecting" id="shell-status-dot"></span>
                    <span id="shell-status-text">Connecting…</span>
                </div>
            </div>
        `;
        document.body.appendChild(overlay);

        // Close on overlay click (outside the modal box).
        overlay.addEventListener('click', (e) => {
            if (e.target === overlay) Pages.closeShellTerminal();
        });

        // Escape key closes the shell terminal; Tab is intentionally NOT trapped
        // here because xterm.js needs to receive Tab keystrokes for the shell.
        this._shellEscHandler = (e) => { if (e.key === 'Escape') Pages.closeShellTerminal(); };
        document.addEventListener('keydown', this._shellEscHandler);

        // Initialise xterm.js.
        const term = new Terminal({
            cursorBlink: true,
            theme: {
                background: '#0d1117',
                foreground: '#c9d1d9',
                cursor:     '#58a6ff',
                selectionBackground: 'rgba(88,166,255,0.3)',
            },
            fontFamily: 'JetBrains Mono, Fira Code, Cascadia Code, Consolas, monospace',
            fontSize: 13,
            lineHeight: 1.4,
            scrollback: 3000,
        });

        const fitAddon = new FitAddon.FitAddon();
        term.loadAddon(fitAddon);
        term.open(document.getElementById('shell-terminal-container'));
        fitAddon.fit();
        this._shellTerm = term;

        // Resize observer — fit terminal to container size.
        const ro = new ResizeObserver(() => {
            try { fitAddon.fit(); } catch (_) {}
            if (this._shellWs && this._shellWs.readyState === WebSocket.OPEN && term.cols && term.rows) {
                this._shellWs.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
            }
        });
        ro.observe(document.getElementById('shell-terminal-container'));
        this._shellRo = ro;

        // Open WebSocket.
        const wsUrl = API.images.shellWsUrl(imageId, sess.session_id);
        const ws = new WebSocket(wsUrl);
        this._shellWs = ws;

        ws.onopen = () => {
            this._setShellStatus('connected', 'Connected');
            // Send initial resize.
            if (term.cols && term.rows) {
                ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
            }
            term.focus();
        };

        ws.onmessage = (evt) => {
            try {
                const msg = JSON.parse(evt.data);
                if (msg.type === 'data') term.write(msg.data);
            } catch (_) {
                // Raw string fallback (shouldn't happen with our server).
                term.write(evt.data);
            }
        };

        ws.onerror = () => {
            this._setShellStatus('disconnected', 'Connection error');
            term.writeln('\r\n\x1b[31m[clustr] WebSocket error\x1b[0m');
        };

        ws.onclose = () => {
            this._setShellStatus('disconnected', 'Disconnected');
            term.writeln('\r\n\x1b[90m[clustr] Session closed\x1b[0m');
        };

        // Pipe keystrokes to WebSocket.
        term.onData((data) => {
            if (ws.readyState === WebSocket.OPEN) {
                ws.send(JSON.stringify({ type: 'data', data }));
            }
        });
    },

    closeShellTerminal() {
        // Kill WebSocket.
        if (this._shellWs) {
            this._shellWs.onclose = null; // suppress the "Disconnected" write
            this._shellWs.close();
            this._shellWs = null;
        }
        // Dispose terminal.
        if (this._shellTerm) {
            this._shellTerm.dispose();
            this._shellTerm = null;
        }
        // Stop resize observer.
        if (this._shellRo) {
            this._shellRo.disconnect();
            this._shellRo = null;
        }
        // Remove Escape listener.
        if (this._shellEscHandler) {
            document.removeEventListener('keydown', this._shellEscHandler);
            this._shellEscHandler = null;
        }
        // Remove modal.
        const overlay = document.getElementById('shell-modal-overlay');
        if (overlay) overlay.remove();

        // Close server-side session.
        if (this._shellImageId && this._shellSessionId) {
            const imgId = this._shellImageId;
            const sid   = this._shellSessionId;
            this._shellImageId = null;
            this._shellSessionId = null;
            API.images.closeShellSession(imgId, sid).catch(() => {});
        }
    },

    _setShellStatus(state, text) {
        const dot  = document.getElementById('shell-status-dot');
        const span = document.getElementById('shell-status-text');
        if (dot)  { dot.className = `shell-status-indicator ${state}`; }
        if (span) { span.textContent = text; }
    },

    _copyText(text, btn) {
        if (navigator.clipboard) {
            navigator.clipboard.writeText(text).then(() => {
                const orig = btn.textContent;
                btn.textContent = 'Copied!';
                btn.style.color = 'var(--success-text)';
                setTimeout(() => { btn.textContent = orig; btn.style.color = ''; }, 1500);
            }).catch(() => {});
        }
    },

    // ── Nodes ──────────────────────────────────────────────────────────────

    // _nodesImages caches the images list used for modal data across refresh cycles.
    _nodesImages: null,

    // _nodesRoleOrder defines the display order and labels for role-based sections.
    _nodesRoleOrder: [
        { role: 'admin',   label: 'Admin Nodes' },
        { role: 'login',   label: 'Login Nodes' },
        { role: 'storage', label: 'Storage Nodes' },
        { role: 'compute', label: 'Compute Nodes' },
        { role: 'gpu',     label: 'GPU Compute Nodes' },
        { role: '',        label: 'Unassigned' },
    ],

    // _nodesRoleSections renders role-bucketed collapsible sections for the nodes list.
    _nodesRoleSections(nodes, imgMap, images, groupMap) {
        // Bucket nodes by their group's role (or '' for unassigned/no role).
        const buckets = {};
        for (const def of Pages._nodesRoleOrder) buckets[def.role] = [];

        for (const n of nodes) {
            const grp = n.group_id ? groupMap[n.group_id] : null;
            const role = (grp && grp.role) ? grp.role : '';
            // Fall back to unassigned bucket if role is unrecognised.
            if (role in buckets) {
                buckets[role].push(n);
            } else {
                buckets[''].push(n);
            }
        }

        // S5-3: Sortable column headers. Current sort state persisted on Pages.
        const sortCol = Pages._nodesSortCol || '';
        const sortDir = Pages._nodesSortDir || 'asc';
        const sortIcon = (col) => {
            if (sortCol !== col) return ' <span style="opacity:0.3;font-size:10px">&#8597;</span>';
            return sortDir === 'asc' ? ' <span style="font-size:10px">&#8593;</span>' : ' <span style="font-size:10px">&#8595;</span>';
        };
        const sortClick = (col) => `onclick="Pages._nodesSortBy('${col}')"`;
        // S5-4: Select-all checkbox.
        const selectAllCb = (Auth._role === 'admin' || Auth._role === 'operator')
            ? `<th style="width:32px"><input type="checkbox" id="nodes-select-all" title="Select all" onchange="Pages._nodesSelectAll(this.checked)"></th>`
            : '';
        const tableHeader = `<thead><tr>
            ${selectAllCb}
            <th style="cursor:pointer" ${sortClick('hostname')}>Host${sortIcon('hostname')}</th>
            <th>Image</th>
            <th style="cursor:pointer" ${sortClick('status')}>Status${sortIcon('status')}</th>
            <th>Hardware</th>
            <th style="cursor:pointer" ${sortClick('group')}>Group${sortIcon('group')}</th>
            <th style="cursor:pointer" ${sortClick('last_deploy')}>Updated${sortIcon('last_deploy')}</th>
            <th style="min-width:80px">Power</th>
            <th>Actions</th>
        </tr></thead>`;

        return Pages._nodesRoleOrder.map(({ role, label }) => {
            const roleNodes = buckets[role] || [];
            if (!roleNodes.length) return ''; // hide empty sections

            const safeRole = role || 'unassigned';
            return `
                <div class="card" style="margin-bottom:16px" id="nodes-section-${safeRole}">
                    <div class="card-header" style="cursor:pointer;user-select:none"
                         onclick="Pages._toggleNodesSection('${safeRole}')">
                        <h2 class="card-title">${escHtml(label)}
                            <span class="badge badge-neutral badge-sm" style="margin-left:8px;font-size:11px">${roleNodes.length}</span>
                        </h2>
                        <span id="nodes-section-chevron-${safeRole}" style="font-size:12px;color:var(--text-secondary)">&#9650;</span>
                    </div>
                    <div id="nodes-section-body-${safeRole}">
                        <div class="table-wrap"><table aria-label="Nodes — ${escHtml(label)}">
                            ${tableHeader}
                            <tbody id="nodes-tbody-${safeRole}">
                                ${roleNodes.map(n => Pages._nodeRow(n, imgMap, images)).join('')}
                            </tbody>
                        </table></div>
                    </div>
                </div>`;
        }).join('');
    },

    // _toggleNodesSection collapses/expands a role section.
    _toggleNodesSection(safeRole) {
        const body    = document.getElementById(`nodes-section-body-${safeRole}`);
        const chevron = document.getElementById(`nodes-section-chevron-${safeRole}`);
        if (!body) return;
        const collapsed = body.style.display === 'none';
        body.style.display    = collapsed ? '' : 'none';
        if (chevron) chevron.innerHTML = collapsed ? '&#9650;' : '&#9660;';
    },

    // S5-3: Sort state — persisted on Pages across refresh cycles.
    _nodesSortCol: '',
    _nodesSortDir: 'asc',

    // S5-3: _nodesSortBy toggles or sets the sort column and re-fetches.
    _nodesSortBy(col) {
        if (Pages._nodesSortCol === col) {
            Pages._nodesSortDir = Pages._nodesSortDir === 'asc' ? 'desc' : 'asc';
        } else {
            Pages._nodesSortCol = col;
            Pages._nodesSortDir = 'asc';
        }
        Pages.nodes();
    },

    // S5-4: Bulk reimage state.
    _nodesCheckedIds: new Set(),

    // S5-4: _nodesSelectAll selects or deselects all visible node checkboxes.
    _nodesSelectAll(checked) {
        document.querySelectorAll('.node-select-cb').forEach(cb => {
            cb.checked = checked;
            const id = cb.dataset.id;
            if (checked) Pages._nodesCheckedIds.add(id);
            else         Pages._nodesCheckedIds.delete(id);
        });
        Pages._nodesUpdateActionBar();
    },

    // S5-4: _nodesOnCheckboxChange updates _nodesCheckedIds and the action bar.
    _nodesOnCheckboxChange() {
        Pages._nodesCheckedIds = new Set();
        document.querySelectorAll('.node-select-cb:checked').forEach(cb => {
            Pages._nodesCheckedIds.add(cb.dataset.id);
        });
        Pages._nodesUpdateActionBar();
    },

    // S5-4: _nodesUpdateActionBar shows/hides the bulk action bar.
    _nodesUpdateActionBar() {
        const count = Pages._nodesCheckedIds.size;
        let bar = document.getElementById('nodes-bulk-action-bar');
        if (!bar) {
            bar = document.createElement('div');
            bar.id = 'nodes-bulk-action-bar';
            bar.style.cssText = 'position:fixed;bottom:20px;left:50%;transform:translateX(-50%);background:var(--bg-secondary);border:1px solid var(--border);border-radius:8px;padding:10px 20px;display:flex;align-items:center;gap:12px;box-shadow:0 4px 20px rgba(0,0,0,0.3);z-index:500;min-width:360px';
            document.body.appendChild(bar);
        }
        if (count === 0) {
            bar.style.display = 'none';
            return;
        }
        bar.style.display = 'flex';
        bar.innerHTML = `
            <span style="font-size:13px;font-weight:600">${count} node${count !== 1 ? 's' : ''} selected</span>
            <button class="btn btn-primary btn-sm" onclick="Pages._nodesBulkReimage()">Reimage Selected</button>
            <div style="width:1px;background:var(--border);align-self:stretch"></div>
            <button class="btn btn-secondary btn-sm" title="Select all nodes in failed state"
                onclick="Pages._nodesSelectByStatus('failed')">+ Failed</button>
            <button class="btn btn-secondary btn-sm" title="Select all nodes with verify-boot timeout"
                onclick="Pages._nodesSelectByStatus('verify_timeout')">+ Timed Out</button>
            <button class="btn btn-secondary btn-sm" title="Select all nodes that have never deployed (image assigned but no deploy attempt)"
                onclick="Pages._nodesSelectByStatus('never_deployed')">+ Never Deployed</button>
            <div style="width:1px;background:var(--border);align-self:stretch"></div>
            <button class="btn btn-secondary btn-sm" onclick="Pages._nodesSelectAll(false);document.getElementById('nodes-select-all')&&(document.getElementById('nodes-select-all').checked=false)">Clear</button>`;
    },

    // C3-21: _nodesSelectByStatus selects all visible checkboxes for nodes matching status.
    _nodesSelectByStatus(status) {
        document.querySelectorAll('.node-select-cb').forEach(cb => {
            const id = cb.dataset.id;
            const nodeStatus = cb.dataset.status || '';
            let match = false;
            if (status === 'failed')         match = nodeStatus === 'error' || nodeStatus === 'failed';
            if (status === 'verify_timeout') match = nodeStatus === 'deploy_verify_timeout';
            if (status === 'never_deployed') match = nodeStatus === 'never_deployed';
            if (match) {
                cb.checked = true;
                Pages._nodesCheckedIds.add(id);
            }
        });
        Pages._nodesUpdateActionBar();
    },

    // S5-4: _nodesBulkReimage opens a modal to reimage all checked nodes.
    async _nodesBulkReimage() {
        const ids = Array.from(Pages._nodesCheckedIds);
        if (!ids.length) return;

        const images = Pages._nodesImages || [];
        const readyImages = images.filter(i => i.status === 'ready');

        const modal = document.createElement('div');
        modal.id = 'bulk-reimage-modal';
        modal.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.5);display:flex;align-items:center;justify-content:center;z-index:1000;';
        modal.innerHTML = `
            <div class="card" style="width:460px;max-width:95vw;">
                <div class="card-header">
                    <h2 class="card-title">Bulk Reimage (${ids.length} nodes)</h2>
                    <button class="btn btn-ghost btn-sm" onclick="document.getElementById('bulk-reimage-modal').remove()">×</button>
                </div>
                <div style="padding:16px;display:flex;flex-direction:column;gap:12px;">
                    <div class="alert alert-warning">
                        Each node will be reimaged individually. This cannot be undone.
                    </div>
                    <label class="form-label">Image
                        <select id="brm-image" class="form-input" style="margin-top:4px;">
                            <option value="">— use each node's assigned image —</option>
                            ${readyImages.map(i => `<option value="${escHtml(i.id)}">${escHtml(i.name)}${i.version ? ' v' + escHtml(i.version) : ''}</option>`).join('')}
                        </select>
                    </label>
                    <div class="form-group">
                        <label style="display:flex;align-items:center;gap:8px;cursor:pointer;font-weight:400">
                            <input type="checkbox" id="brm-dry-run">
                            <span>Dry run — PXE boot but skip disk write</span>
                        </label>
                    </div>
                    <div id="brm-progress" style="display:none;font-size:13px;color:var(--text-secondary)">Submitting…</div>
                    <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:8px;">
                        <button class="btn btn-secondary" onclick="document.getElementById('bulk-reimage-modal').remove()">Cancel</button>
                        <button class="btn btn-danger" onclick="Pages._nodesBulkReimageSubmit(${JSON.stringify(ids)})">Reimage ${ids.length} Nodes</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(modal);
    },

    // S5-4: Submit individual reimage requests for each selected node.
    async _nodesBulkReimageSubmit(ids) {
        const imageId = document.getElementById('brm-image')?.value || '';
        const dryRun  = !!(document.getElementById('brm-dry-run')?.checked);
        const progEl  = document.getElementById('brm-progress');
        if (progEl) progEl.style.display = '';

        let ok = 0, fail = 0;
        for (const nodeId of ids) {
            try {
                const body = { dry_run: dryRun };
                if (imageId) body.base_image_id = imageId;
                await API.request('POST', `/nodes/${nodeId}/reimage`, body);
                ok++;
            } catch (_) {
                fail++;
            }
            if (progEl) progEl.textContent = `Submitted ${ok + fail} / ${ids.length}…`;
        }

        document.getElementById('bulk-reimage-modal')?.remove();
        App.toast(`Bulk reimage: ${ok} queued${fail ? ', ' + fail + ' failed' : ''}`, ok > 0 ? 'success' : 'error');
        Pages._nodesSelectAll(false);
        Pages.nodesRefresh();
    },

    // S5-1: _nodesFetchPower fetches power state for nodes that have BMC/power_provider.
    // Runs up to concurrencyLimit concurrent requests to avoid overwhelming IPMI/Proxmox.
    async _nodesFetchPower(nodes, concurrencyLimit = 10) {
        const powerNodes = nodes.filter(n =>
            (n.bmc && n.bmc.ip_address) || (n.power_provider && n.power_provider.type)
        );
        if (!powerNodes.length) return;

        const queue = [...powerNodes];
        const workers = Array.from({ length: Math.min(concurrencyLimit, queue.length) }, async () => {
            while (queue.length) {
                const n = queue.shift();
                if (!n) break;
                try {
                    const data = await API.nodes.power.status(n.id);
                    const status = (data && data.status) || 'unknown';
                    const cell = document.getElementById(`pwr-cell-${n.id}`);
                    if (cell) {
                        const cls = { on: 'var(--success)', off: 'var(--text-dim)', unknown: 'var(--text-dim)', error: 'var(--error)' }[status] || 'var(--text-dim)';
                        cell.innerHTML = `<span style="color:${cls};font-weight:${status === 'on' ? 600 : 400}">${escHtml(status)}</span>`;
                    }
                } catch (_) {
                    // silently skip — power status is best-effort
                }
            }
        });
        await Promise.all(workers);
    },

    // _nodesSearchTimer is used to debounce the search input.
    _nodesSearchTimer: null,

    // _nodesSearchQuery tracks the current search query so the auto-refresh
    // can re-use it.
    _nodesSearchQuery: '',

    async nodes() {
        App.render(loading('Loading nodes…'));
        try {
            // Parse optional ?group= query param for group filter.
            const urlParams = new URLSearchParams(window.location.hash.includes('?') ? window.location.hash.split('?')[1] : '');
            const groupFilter = urlParams.get('group') || '';

            // Reset search state on page load.
            Pages._nodesSearchQuery = '';
            // Reset bulk selection state on navigation.
            Pages._nodesCheckedIds = new Set();
            const existingBar = document.getElementById('nodes-bulk-action-bar');
            if (existingBar) existingBar.remove();

            // S5-3: Build sort params from persisted state.
            const sortParams = {};
            if (Pages._nodesSortCol) {
                sortParams.sort = Pages._nodesSortCol;
                sortParams.dir  = Pages._nodesSortDir || 'asc';
            }

            const [nodesResp, imagesResp, groupsResp] = await Promise.all([
                API.nodes.list(sortParams),
                API.images.list(),
                API.nodeGroups.list().catch(() => ({ groups: [] })),
            ]);
            const nodes  = nodesResp.nodes  || [];
            const images = imagesResp.images || [];
            const groups = (groupsResp && (groupsResp.groups || groupsResp.node_groups)) || [];

            // Build group lookup map for the Groups column.
            const groupMap = Object.fromEntries(groups.map(g => [g.id, g]));
            Pages._nodesGroupMap = groupMap;

            // Apply group filter if set.
            const filteredNodes = groupFilter
                ? nodes.filter(n => n.group_id === groupFilter)
                : nodes;

            const activeGroup = groupFilter ? groupMap[groupFilter] : null;

            // Cache for incremental refresh.
            App._cacheSet('nodes',  nodes);
            App._cacheSet('images', images);
            Pages._nodesImages = images;
            Pages._nodesGroupFilter = groupFilter;

            const imgMap = Object.fromEntries(images.map(i => [i.id, i]));

            const filterBanner = activeGroup
                ? `<div style="display:flex;align-items:center;gap:10px;padding:8px 14px;background:var(--surface-secondary);border-radius:6px;margin-bottom:12px;font-size:13px">
                    <span>Filtered by group: <strong>${escHtml(activeGroup.name)}</strong></span>
                    <a href="#/nodes" style="margin-left:auto;color:var(--text-secondary);text-decoration:none;font-size:12px">Clear filter ×</a>
                   </div>`
                : '';

            const sectionsHtml = filteredNodes.length
                ? Pages._nodesRoleSections(filteredNodes, imgMap, images, groupMap)
                : `<div class="card">${emptyState('No nodes', groupFilter ? 'No nodes in this group.' : 'Add your first node using the button above',
                    groupFilter ? `<a href="#/nodes" class="btn btn-secondary">Clear filter</a>` : `<button class="btn btn-primary" onclick='Pages.showNodeModal(null, ${JSON.stringify(JSON.stringify(images))}, ${JSON.stringify(JSON.stringify(groups))})'>Add Node</button>`)}</div>`;

            const canAdd = Auth._role === 'admin';

            App.render(`
                <div class="page-header">
                    <div>
                        <h1 class="page-title">Nodes</h1>
                        <div class="page-subtitle" id="nodes-subtitle">${filteredNodes.length}${groupFilter ? ' (filtered)' : ''} of ${nodes.length} node${nodes.length !== 1 ? 's' : ''}</div>
                    </div>
                    <div class="flex gap-8">
                        <input id="nodes-search" type="search" placeholder="Search hostname, MAC, status…"
                            style="width:220px;padding:6px 10px;border:1px solid var(--border);border-radius:var(--radius);background:var(--bg-secondary);color:var(--text-primary);font-size:13px"
                            oninput="Pages._nodesSearchDebounced(this.value)">
                        ${canAdd ? `<button class="btn btn-primary" onclick='Pages.showNodeModal(null, ${JSON.stringify(JSON.stringify(images))}, ${JSON.stringify(JSON.stringify(groups))})'>
                            <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24">
                                <line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/>
                            </svg>
                            Add Node
                        </button>` : ''}
                    </div>
                </div>

                <div class="tab-bar" style="margin-bottom:20px">
                    <div class="tab active" onclick="Router.navigate('/nodes')">All Nodes</div>
                    <div class="tab" onclick="Router.navigate('/nodes/groups')">Groups</div>
                </div>

                ${filterBanner}

                <div id="nodes-sections-container">
                    ${sectionsHtml}
                </div>
            `);

            // Incremental auto-refresh — updates rows in-place without blowing away the DOM.
            App.setAutoRefresh(() => Pages.nodesRefresh());

            // Close power dropdowns when clicking outside any of them.
            document.addEventListener('click', Pages._closePowerDropdownsOnOutsideClick);

            // S5-1: Batch-fetch power states for all nodes with BMC/power_provider configured.
            // Non-blocking — runs after the page is rendered.
            Pages._nodesFetchPower(filteredNodes, 10);

        } catch (e) {
            App.render(alertBox(`Failed to load nodes: ${e.message}`));
        }
    },

    // _nodesSearchDebounced debounces the search input and fires _nodesSearch
    // after 300ms of idle typing.
    _nodesSearchDebounced(value) {
        Pages._nodesSearchQuery = value.trim();
        if (Pages._nodesSearchTimer) clearTimeout(Pages._nodesSearchTimer);
        Pages._nodesSearchTimer = setTimeout(() => Pages._nodesSearch(), 300);
    },

    // _nodesSearch fires a ?search= request and re-renders the sections container.
    async _nodesSearch() {
        const q = Pages._nodesSearchQuery;
        const container = document.getElementById('nodes-sections-container');
        const subtitle  = document.getElementById('nodes-subtitle');
        if (!container) return;

        try {
            const [nodesResp] = await Promise.all([
                API.nodes.list({ search: q }),
            ]);
            const nodes  = nodesResp.nodes || [];
            const images = Pages._nodesImages || [];
            const groupMap = Pages._nodesGroupMap || {};
            const imgMap = Object.fromEntries(images.map(i => [i.id, i]));

            if (subtitle) subtitle.textContent = q
                ? `${nodes.length} result${nodes.length !== 1 ? 's' : ''} for "${q}"`
                : `${nodes.length} node${nodes.length !== 1 ? 's' : ''}`;

            if (!nodes.length) {
                container.innerHTML = `<div class="card">${emptyState('No nodes found', q ? `No nodes match "${escHtml(q)}"` : 'No nodes registered yet.')}</div>`;
                return;
            }
            container.innerHTML = Pages._nodesRoleSections(nodes, imgMap, images, groupMap);
        } catch (e) {
            if (container) container.innerHTML = alertBox(`Search failed: ${e.message}`);
        }
    },

    // _nodeRow renders a single <tr> string for the nodes table.
    _nodeRow(n, imgMap, images) {
        const img = imgMap[n.base_image_id];
        let hwChips = '';
        try {
            const hw = n.hardware_profile
                ? (typeof n.hardware_profile === 'string' ? JSON.parse(n.hardware_profile) : n.hardware_profile)
                : null;
            if (hw) {
                const chips = [];
                if (hw.CPUCount || hw.cpu_count) chips.push(`${hw.CPUCount || hw.cpu_count} CPU`);
                if (hw.MemoryBytes || hw.memory_bytes) chips.push(fmtBytes(hw.MemoryBytes || hw.memory_bytes) + ' RAM');
                if (hw.Disks && hw.Disks.length) chips.push(`${hw.Disks.length} disk${hw.Disks.length > 1 ? 's' : ''}`);
                if (hw.NICs && hw.NICs.length) chips.push(`${hw.NICs.length} NIC${hw.NICs.length > 1 ? 's' : ''}`);
                hwChips = `<div class="hw-chips">${chips.map(c => `<span class="hw-chip">${escHtml(c)}</span>`).join('')}</div>`;
            }
        } catch (_) {}
        const hostnameHtml = (n.hostname && n.hostname !== '(none)')
            ? `${escHtml(n.hostname)}${n.hostname_auto ? ' <span class="badge badge-neutral badge-sm" title="Auto-generated hostname">auto</span>' : ''}`
            : `<span class="text-dim" style="font-style:italic">Unassigned</span>`;
        // Show a green "Live" badge when clustr-clientd has reported a heartbeat
        // within the last 2 minutes (last_seen_at is updated on every heartbeat).
        const liveBadge = (() => {
            if (!n.last_seen_at) return '';
            const seenMs = new Date(n.last_seen_at).getTime();
            const ageMs = Date.now() - seenMs;
            return ageMs < 2 * 60 * 1000
                ? ' <span class="badge badge-success badge-sm" title="clustr-clientd is connected and sending heartbeats">Live</span>'
                : '';
        })();
        // S5-4: Checkbox cell (admin/operator only).
        // C3-21: data-status for quick-select by status.
        const canMutate = Auth._role === 'admin' || Auth._role === 'operator';
        const nodeStatusKey = (() => {
            if (n.deploy_verify_timeout_at) return 'deploy_verify_timeout';
            if (n._deployStatus === 'error') return 'error';
            if (n.base_image_id && !n.deploy_completed_preboot_at && !n._deployStatus) return 'never_deployed';
            return n._deployStatus || 'unknown';
        })();
        const checkboxCell = canMutate
            ? `<td style="width:32px"><input type="checkbox" class="node-select-cb" data-id="${escHtml(n.id)}" data-status="${nodeStatusKey}" onchange="Pages._nodesOnCheckboxChange()"></td>`
            : '';
        // S5-1: Power state cell. Initially shows — ; populated async by Pages._nodesFetchPower().
        const powerCell = `<td id="pwr-cell-${escHtml(n.id)}" class="text-dim text-sm">—</td>`;

        return `<tr data-key="${escHtml(n.id)}">
            ${checkboxCell}
            <td>
                <a href="#/nodes/${n.id}" style="font-weight:500;color:var(--text-primary)">
                    ${hostnameHtml}${liveBadge}
                </a>
                <div class="text-dim text-sm text-mono">${escHtml(n.primary_mac || '—')}</div>
            </td>
            <td class="text-sm">
                ${img
                    ? `<a href="#/images/${img.id}">${escHtml(img.name)}</a>`
                    : (n.base_image_id ? `<span class="text-dim text-mono">${n.base_image_id.substring(0, 8)}…</span>` : '<span class="text-dim">—</span>')}
            </td>
            <td>${nodeBadge(n)}</td>
            <td>${hwChips || '<span class="text-dim text-sm">—</span>'}</td>
            <td>
                ${(() => {
                    const gm = Pages._nodesGroupMap || {};
                    if (n.group_id && gm[n.group_id]) {
                        const grp = gm[n.group_id];
                        return `<a href="#/nodes?group=${encodeURIComponent(n.group_id)}" style="text-decoration:none" title="Filter by this group">
                                    <span class="badge badge-info badge-sm" style="cursor:pointer">${escHtml(grp.name)}</span>
                                </a>`;
                    } else if (n.group_id) {
                        return `<span class="badge badge-neutral badge-sm text-mono" style="font-size:10px">${n.group_id.substring(0,8)}</span>`;
                    }
                    return '<span class="text-dim">—</span>';
                })()}
            </td>
            <td class="text-dim text-sm">${fmtRelative(n.updated_at)}</td>
            ${powerCell}
            <td>
                ${Pages._nodeRowActions(n)}
            </td>
        </tr>`;
    },

    // _nodeRowActions renders the actions cell for a node row.
    // Power actions and deploy actions shown only to admin/operator; readonly sees View only.
    // S5-5: Adds "Re-deploy last image" and "Retry" quick-action buttons.
    // B2-1: "Configure and Deploy" CTA for Registered nodes (has hardware_profile, no base_image_id).
    _nodeRowActions(n) {
        const canMutate = Auth._role === 'admin' || Auth._role === 'operator';
        // B2-1: show "Configure and Deploy" CTA for nodes registered but not yet assigned an image.
        const isRegistered = n.hardware_profile && !n.base_image_id;
        const configureBtn = (canMutate && isRegistered)
            ? `<button class="btn btn-primary btn-sm" onclick="Pages._configureAndDeployModal('${n.id}','${escHtml(n.hostname||n.primary_mac)}')" title="Assign an image and deploy this node">Configure &amp; Deploy</button>`
            : '';
        // S5-5: "Retry" appears for nodes in Failed state.
        const isFailed = n._deployStatus === 'error' || (n.last_deploy_failed_at && (!n.last_deploy_succeeded_at || n.last_deploy_failed_at > n.last_deploy_succeeded_at));
        const retryBtn = (canMutate && isFailed)
            ? `<button class="btn btn-secondary btn-sm" onclick="Pages._listRetryLastReimage('${n.id}')" title="Retry the last failed deploy">Retry</button>`
            : '';
        // S5-5: "Re-deploy last image" pre-populates reimage modal with last image.
        const redeployBtn = (canMutate && n.base_image_id && !isFailed)
            ? `<button class="btn btn-secondary btn-sm" onclick="Pages._listRedeploy('${n.id}','${escHtml(n.hostname||n.primary_mac)}')" title="Re-deploy the currently assigned image">Re-deploy</button>`
            : '';
        const pwrDropdown = canMutate ? `
            <div class="actions-dropdown" id="pwr-dd-${n.id}">
                <button class="btn btn-secondary btn-sm" onclick="Pages._togglePowerDropdown('${n.id}',event)" title="Power actions">
                    &#9889;
                    <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24" style="width:11px;height:11px;margin-left:2px"><polyline points="6 9 12 15 18 9"/></svg>
                </button>
                <div class="actions-dropdown-menu" id="pwr-menu-${n.id}">
                    <button class="actions-dropdown-item" onclick="Pages._listPowerAction('${n.id}','on');Pages._togglePowerDropdown('${n.id}',null)">Power On</button>
                    <button class="actions-dropdown-item" onclick="Pages._listPowerAction('${n.id}','status');Pages._togglePowerDropdown('${n.id}',null)">Check Status</button>
                    <div class="actions-dropdown-sep"></div>
                    <button class="actions-dropdown-item danger" onclick="Pages._listConfirmPowerAction('${n.id}','off','Power Off','This will immediately cut power to the node.');Pages._togglePowerDropdown('${n.id}',null)">Power Off</button>
                    <button class="actions-dropdown-item danger" onclick="Pages._listConfirmPowerAction('${n.id}','reset','Reset','This will issue a hard reset. The node will reboot immediately.');Pages._togglePowerDropdown('${n.id}',null)">Reset</button>
                    <button class="actions-dropdown-item danger" onclick="Pages._listConfirmPowerAction('${n.id}','cycle','Power Cycle','This will hard-cycle the node (power off then on).');Pages._togglePowerDropdown('${n.id}',null)">Power Cycle</button>
                </div>
            </div>` : '';
        return `<div class="flex gap-6" style="align-items:center">
            <a class="btn btn-secondary btn-sm" href="#/nodes/${n.id}">View</a>
            ${configureBtn}${retryBtn}${redeployBtn}
            ${pwrDropdown}
        </div>`;
    },

    // S5-5: _listRetryLastReimage retries the most recent failed reimage for a node.
    async _listRetryLastReimage(nodeId) {
        try {
            const resp = await API.reimages.listForNode(nodeId);
            const requests = (resp && resp.requests) || [];
            const failed = requests.find(r => r.status === 'failed');
            if (!failed) {
                App.toast('No failed reimage found to retry', 'info');
                return;
            }
            await API.request('POST', `/reimage/${failed.id}/retry`, {});
            App.toast('Reimage retried', 'success');
            Pages.nodesRefresh();
        } catch (e) {
            App.toast(`Retry failed: ${e.message}`, 'error');
        }
    },

    // S5-5: _listRedeploy opens the reimage modal pre-populated with the node's current image.
    _listRedeploy(nodeId, displayName) {
        Pages._nodeActionsTriggerReimage(nodeId, displayName);
    },

    // B2-1: _configureAndDeployModal — 3-step guided modal for Registered nodes.
    // Step 1: image select. Step 2: SSH keys confirm. Step 3: reimage trigger.
    // Assigns the image to the node then immediately queues a reimage.
    async _configureAndDeployModal(nodeId, displayName) {
        const MID = 'configure-deploy-modal';
        // Remove any stale instance.
        const stale = document.getElementById(MID);
        if (stale) stale.remove();

        // Fetch images for the picker.
        let images = [];
        try {
            const resp = await API.images.list();
            images = (resp && resp.images) || [];
        } catch (_) {}

        // Fetch current node to pre-fill SSH keys.
        let node = null;
        try {
            node = await API.nodes.get(nodeId);
        } catch (_) {}
        const existingKeys = (node && node.ssh_keys) ? node.ssh_keys.join('\n') : '';

        if (images.length === 0) {
            Pages.showAlertModal(
                'No Images Available',
                'There are no images to assign. <a href="#/images">Upload an image</a> first, then return to configure this node.'
            );
            return;
        }

        const imageOptions = images
            .map(img => `<option value="${escHtml(img.id)}">${escHtml(img.name)}${img.version ? ' — ' + escHtml(img.version) : ''}</option>`)
            .join('');

        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = MID;
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        overlay.innerHTML = `
            <div class="modal" style="max-width:480px" aria-labelledby="${MID}-title">
                <div class="modal-header">
                    <span class="modal-title" id="${MID}-title">Configure &amp; Deploy — ${escHtml(displayName)}</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('${MID}').remove()">&#215;</button>
                </div>
                <div class="modal-body" style="display:flex;flex-direction:column;gap:16px;">
                    <!-- Step indicator -->
                    <div style="display:flex;gap:0;border-radius:6px;overflow:hidden;border:1px solid var(--border);font-size:12px;font-weight:600;">
                        <div id="${MID}-step1-ind" style="flex:1;text-align:center;padding:6px 0;background:var(--accent);color:#fff;">1. Image</div>
                        <div id="${MID}-step2-ind" style="flex:1;text-align:center;padding:6px 0;background:var(--bg-secondary);color:var(--text-secondary);">2. SSH Keys</div>
                        <div id="${MID}-step3-ind" style="flex:1;text-align:center;padding:6px 0;background:var(--bg-secondary);color:var(--text-secondary);">3. Deploy</div>
                    </div>

                    <!-- Step 1: image select -->
                    <div id="${MID}-step1">
                        <label class="form-label" style="margin-bottom:6px;">Select image to assign to this node</label>
                        <select id="${MID}-image" class="form-input" style="margin-top:4px;">
                            ${imageOptions}
                        </select>
                        <div style="font-size:11px;color:var(--text-secondary);margin-top:6px;">
                            This image will be set as the node's base image and immediately queued for deploy.
                        </div>
                    </div>

                    <!-- Step 2: SSH keys -->
                    <div id="${MID}-step2" style="display:none;">
                        <label class="form-label" style="margin-bottom:6px;">SSH authorized keys (one per line)</label>
                        <textarea id="${MID}-keys" class="form-input" rows="4"
                            placeholder="ssh-ed25519 AAAA…&#10;ssh-rsa AAAA…"
                            style="margin-top:4px;font-family:var(--font-mono);font-size:12px;resize:vertical;">${escHtml(existingKeys)}</textarea>
                        <div style="font-size:11px;color:var(--text-secondary);margin-top:6px;">
                            Leave blank to keep existing keys or deploy without key injection.
                        </div>
                    </div>

                    <!-- Step 3: confirm -->
                    <div id="${MID}-step3" style="display:none;">
                        <div style="background:rgba(234,179,8,0.08);border:1px solid rgba(234,179,8,0.35);border-radius:6px;padding:12px 14px;font-size:13px;">
                            <strong>Ready to deploy</strong><br>
                            <span id="${MID}-summary" style="color:var(--text-secondary);font-size:12px;"></span>
                        </div>
                        <div style="font-size:12px;color:var(--text-secondary);margin-top:10px;">
                            The node will be reimaged on its next PXE boot. Make sure it is set to PXE boot first.
                        </div>
                    </div>

                    <div id="${MID}-err" style="color:var(--error);font-size:13px;display:none;"></div>
                </div>
                <div class="modal-footer">
                    <button class="btn btn-secondary" id="${MID}-back" style="display:none;" onclick="Pages._cdStep(${JSON.stringify(MID)}, 'back')">Back</button>
                    <button class="btn btn-secondary" id="${MID}-cancel" onclick="document.getElementById('${JSON.stringify(MID)}').remove()">Cancel</button>
                    <button class="btn btn-primary" id="${MID}-next" onclick="Pages._cdStep('${MID}', 'next', '${nodeId}')">Next</button>
                </div>
            </div>`;

        // Fix the cancel button — JSON.stringify added quotes; just use MID directly.
        overlay.querySelector('#' + MID + '-cancel').onclick = () => overlay.remove();

        document.body.appendChild(overlay);
        overlay.addEventListener('click', e => { if (e.target === overlay) overlay.remove(); });
        trapModalFocus(overlay, () => overlay.remove());

        // Track current step in a dataset on the overlay.
        overlay.dataset.step = '1';
    },

    // B2-1: _cdStep — advance or retreat the configure-and-deploy modal steps.
    async _cdStep(mid, direction, nodeId) {
        const overlay = document.getElementById(mid);
        if (!overlay) return;
        let step = parseInt(overlay.dataset.step, 10);

        const errEl  = document.getElementById(mid + '-err');
        const nextBtn = document.getElementById(mid + '-next');
        const backBtn = document.getElementById(mid + '-back');
        function showErr(msg) { errEl.textContent = msg; errEl.style.display = ''; }
        function clearErr()   { errEl.textContent = ''; errEl.style.display = 'none'; }
        clearErr();

        if (direction === 'next') {
            if (step === 1) {
                // Validate image selected.
                const imageId = document.getElementById(mid + '-image')?.value;
                if (!imageId) { showErr('Please select an image.'); return; }
                step = 2;
            } else if (step === 2) {
                // Move to confirm step — populate summary.
                const imageId  = document.getElementById(mid + '-image')?.value;
                const imageEl  = document.getElementById(mid + '-image');
                const imageName = imageEl ? imageEl.options[imageEl.selectedIndex]?.text : imageId;
                const summaryEl = document.getElementById(mid + '-summary');
                if (summaryEl) summaryEl.textContent = `Image: ${imageName}`;
                step = 3;
                nextBtn.textContent = 'Deploy';
                nextBtn.classList.remove('btn-primary');
                nextBtn.classList.add('btn-danger');
            } else if (step === 3) {
                // Submit: assign image + SSH keys, then trigger reimage.
                const imageId = document.getElementById(mid + '-image')?.value;
                const keysRaw = document.getElementById(mid + '-keys')?.value || '';
                const sshKeys = keysRaw.split('\n').map(k => k.trim()).filter(Boolean);

                nextBtn.disabled = true;
                nextBtn.textContent = 'Deploying…';

                try {
                    // Step A: update node with image and SSH keys.
                    await API.nodes.update(nodeId, { base_image_id: imageId, ssh_keys: sshKeys });
                    // Step B: queue reimage.
                    await API.request('POST', `/nodes/${nodeId}/reimage`, {});
                    overlay.remove();
                    App.toast('Image assigned and reimage queued — node will deploy on next PXE boot', 'success');
                    // Bust the node cache so the list reloads with the updated base_image_id.
                    App._cacheSet('nodes', null, 0);
                    Pages.nodes();
                } catch (e) {
                    nextBtn.disabled = false;
                    nextBtn.textContent = 'Deploy';
                    showErr(e.message || 'Deploy failed — check server logs.');
                }
                return;
            }
        } else {
            // back
            step = Math.max(1, step - 1);
            if (step < 3) {
                nextBtn.textContent = 'Next';
                nextBtn.classList.add('btn-primary');
                nextBtn.classList.remove('btn-danger');
            }
        }

        overlay.dataset.step = step;

        // Show/hide step panels.
        [1, 2, 3].forEach(s => {
            const panel = document.getElementById(mid + '-step' + s);
            const ind   = document.getElementById(mid + '-step' + s + '-ind');
            if (panel) panel.style.display = (s === step) ? '' : 'none';
            if (ind) {
                ind.style.background = (s === step) ? 'var(--accent)' : (s < step ? 'var(--success-bg, #d1fae5)' : 'var(--bg-secondary)');
                ind.style.color      = (s === step) ? '#fff' : (s < step ? 'var(--success-text, #065f46)' : 'var(--text-secondary)');
            }
        });

        // Back button visibility.
        if (backBtn) backBtn.style.display = (step > 1) ? '' : 'none';

        // Focus first focusable in active step.
        const activePanel = document.getElementById(mid + '-step' + step);
        if (activePanel) {
            const first = activePanel.querySelector('select, input, textarea, button');
            if (first) first.focus();
        }
    },

    // nodesRefresh — called by the auto-refresh timer. Updates the nodes table
    // in-place without replacing the full page layout or showing a loading state.
    async nodesRefresh() {
        // Check that at least one role-section tbody exists (navigated away if not).
        const anyTbody = document.querySelector('[id^="nodes-tbody-"]');
        if (!anyTbody) return;

        try {
            let nodes  = App._cacheGet('nodes');
            let images = App._cacheGet('images') || Pages._nodesImages || [];

            const fetches = [];
            if (!nodes)  fetches.push(API.nodes.list().then(r  => { nodes  = r.nodes   || []; App._cacheSet('nodes',  nodes);  }));
            // Only re-fetch images if cache is cold — they change infrequently.
            if (!App._cacheGet('images')) fetches.push(API.images.list().then(r => { images = r.images || []; App._cacheSet('images', images); Pages._nodesImages = images; }));
            if (fetches.length) await Promise.all(fetches);

            const imgMap   = Object.fromEntries(images.map(i => [i.id, i]));
            const groupMap = Pages._nodesGroupMap || {};

            // Update subtitle.
            const subtitle = document.getElementById('nodes-subtitle');
            if (subtitle) subtitle.textContent = `${nodes.length} node${nodes.length !== 1 ? 's' : ''} total`;

            // Build a unified map of all existing rows across all role tbodies.
            const existing = new Map();
            document.querySelectorAll('[id^="nodes-tbody-"] [data-key]').forEach(el => existing.set(el.dataset.key, el));

            for (const n of nodes) {
                const key = n.id;
                if (existing.has(key)) {
                    // Update only the columns that can change between refreshes.
                    const tr   = existing.get(key);
                    const cells = tr.querySelectorAll('td');
                    if (cells[2]) cells[2].innerHTML = nodeBadge(n);
                    if (cells[5]) cells[5].textContent = fmtRelative(n.updated_at);
                    // Refresh action buttons using the shared helper (role-aware).
                    if (cells[6]) cells[6].innerHTML = Pages._nodeRowActions(n);
                    existing.delete(key);
                } else {
                    // New node appeared — insert into its role-appropriate tbody.
                    const grp      = n.group_id ? groupMap[n.group_id] : null;
                    const role     = (grp && grp.role) ? grp.role : '';
                    const safeRole = role || 'unassigned';
                    const tbody    = document.getElementById(`nodes-tbody-${safeRole}`);
                    if (tbody) {
                        tbody.insertAdjacentHTML('beforeend', Pages._nodeRow(n, imgMap, images));
                    }
                }
            }

            // Remove rows for nodes that were deleted.
            for (const [, el] of existing) el.remove();

        } catch (_) {
            // Silently ignore refresh errors — next tick will retry.
        }
    },

    showNodeModal(nodeJson, imagesJson, nodeGroupsJson) {
        const node       = nodeJson       ? JSON.parse(nodeJson)       : null;
        const images     = imagesJson     ? JSON.parse(imagesJson)     : [];
        const nodeGroups = nodeGroupsJson ? JSON.parse(nodeGroupsJson) : [];
        const isEdit = !!node;

        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        overlay.id = 'node-modal';

        const imgOptions = images
            .filter(i => i.status === 'ready')
            .map(i => `<option value="${escHtml(i.id)}" ${node && node.base_image_id === i.id ? 'selected' : ''}>${escHtml(i.name)}${i.version ? ' (' + i.version + ')' : ''}</option>`)
            .join('');

        // S2-6: Node Group dropdown — populated from nodeGroups list.
        const groupOptions = nodeGroups
            .map(g => `<option value="${escHtml(g.id)}" ${node && node.group_id === g.id ? 'selected' : ''}>${escHtml(g.name)}</option>`)
            .join('');

        overlay.innerHTML = `
            <div class="modal" aria-labelledby="modal-title-6">
                <div class="modal-header">
                    <span class="modal-title" id="modal-title-6">${isEdit ? 'Edit Node' : 'Add Node'}</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('node-modal').remove()">×</button>
                </div>
                <div class="modal-body">
                    <form id="node-form" onsubmit="Pages.submitNode(event, ${isEdit ? `'${node.id}'` : 'null'})">
                        <div class="form-grid">
                            <div class="form-group">
                                <label>Hostname *
                                    ${isEdit && node.hostname_auto
                                        ? ' <span class="badge badge-neutral badge-sm" title="Auto-generated hostname">auto</span>'
                                        : ''}
                                </label>
                                <div style="display:flex;gap:6px;align-items:center">
                                    <input type="text" name="hostname" id="node-hostname-input"
                                        value="${isEdit ? escHtml(node.hostname) : ''}"
                                        placeholder="${isEdit && node.hostname_auto ? escHtml(node.hostname) + ' (auto-generated)' : ''}"
                                        style="flex:1" required>
                                    ${isEdit && node.hostname_auto
                                        ? `<button type="button" class="btn btn-secondary btn-sm"
                                               onclick="Pages._regenerateHostname('${escHtml(node.primary_mac)}')"
                                               title="Pick a new auto-generated hostname">Regenerate</button>`
                                        : ''}
                                </div>
                            </div>
                            <div class="form-group">
                                <label>FQDN</label>
                                <input type="text" name="fqdn" value="${isEdit ? escHtml(node.fqdn || '') : ''}">
                            </div>
                            <div class="form-group">
                                <label>Primary MAC *</label>
                                <input type="text" name="primary_mac" value="${isEdit ? escHtml(node.primary_mac) : ''}" placeholder="aa:bb:cc:dd:ee:ff" required>
                            </div>
                            <div class="form-group">
                                <label>Base Image</label>
                                <select name="base_image_id">
                                    <option value="">Select image…</option>
                                    ${imgOptions}
                                    ${!imgOptions ? `<option disabled>No ready images available</option>` : ''}
                                </select>
                                <div id="role-mismatch-warning" class="alert alert-warning" style="display:none;margin-top:8px;font-size:12px"></div>
                            </div>
                            <div class="form-group">
                                <!-- S2-7: Tags = freeform, was "Groups"; S2-6: Node Group dropdown -->
                                <label>Tags <span style="font-size:11px;color:var(--text-secondary)">(comma-separated)</span>
                                    <span style="font-size:11px;color:var(--text-secondary);display:block;font-weight:400">Unstructured labels used for filtering and Slurm role assignment.</span>
                                </label>
                                <input type="text" name="tags" value="${isEdit ? escHtml((node.tags || node.groups || []).join(', ')) : ''}" placeholder="compute, gpu, infiniband">
                            </div>
                            <div class="form-group">
                                <label>Node Group
                                    <span style="font-size:11px;color:var(--text-secondary);display:block;font-weight:400">Primary operational group — controls disk layout inheritance, network profile, and group reimages.</span>
                                </label>
                                <select name="group_id">
                                    <option value="">None</option>
                                    ${groupOptions || '<option disabled>No groups defined</option>'}
                                </select>
                            </div>
                            <div class="form-group" style="grid-column:1/-1">
                                <label>Kernel Args</label>
                                <input type="text" name="kernel_args" value="${isEdit ? escHtml(node.kernel_args || '') : ''}" placeholder="quiet splash">
                            </div>
                            <div class="form-group" style="grid-column:1/-1">
                                <label>SSH Public Keys (one per line)</label>
                                <textarea name="ssh_keys" rows="3" placeholder="ssh-ed25519 AAAA…">${isEdit ? escHtml((node.ssh_keys || []).join('\n')) : ''}</textarea>
                            </div>
                        </div>

                        <!-- Power Provider section -->
                        <div style="margin-top:20px;padding-top:16px;border-top:1px solid var(--border)">
                            <div style="font-weight:600;font-size:13px;margin-bottom:12px;color:var(--text-secondary)">Power Provider</div>
                            <div class="form-grid">
                                <div class="form-group" style="grid-column:1/-1">
                                    <label>Provider Type</label>
                                    <select name="power_provider_type" id="pp-type-select"
                                        onchange="Pages._onPowerProviderTypeChange(this.value)"
                                        value="${isEdit && node.power_provider ? escHtml(node.power_provider.type || '') : ''}">
                                        <option value="">None — no power management</option>
                                        <option value="ipmi" ${isEdit && node.power_provider && node.power_provider.type === 'ipmi' ? 'selected' : ''}>IPMI (uses BMC config)</option>
                                        <option value="proxmox" ${isEdit && node.power_provider && node.power_provider.type === 'proxmox' ? 'selected' : ''}>Proxmox VE</option>
                                    </select>
                                </div>
                            </div>
                            <!-- Proxmox fields — shown/hidden by JS -->
                            <div id="pp-proxmox-fields" style="display:${isEdit && node.power_provider && node.power_provider.type === 'proxmox' ? '' : 'none'}">
                                <div class="form-grid">
                                    <div class="form-group">
                                        <label>API URL</label>
                                        <input type="text" name="pp_api_url"
                                            value="${isEdit && node.power_provider && node.power_provider.fields ? escHtml(node.power_provider.fields.api_url || '') : ''}"
                                            placeholder="https://proxmox.example.com:8006">
                                    </div>
                                    <div class="form-group">
                                        <label>Node Name</label>
                                        <input type="text" name="pp_node"
                                            value="${isEdit && node.power_provider && node.power_provider.fields ? escHtml(node.power_provider.fields.node || '') : ''}"
                                            placeholder="pve">
                                    </div>
                                    <div class="form-group">
                                        <label>VM ID</label>
                                        <input type="text" name="pp_vmid"
                                            value="${isEdit && node.power_provider && node.power_provider.fields ? escHtml(node.power_provider.fields.vmid || '') : ''}"
                                            placeholder="202">
                                    </div>
                                    <div class="form-group">
                                        <label>Username</label>
                                        <input type="text" name="pp_username"
                                            value="${isEdit && node.power_provider && node.power_provider.fields ? escHtml(node.power_provider.fields.username || '') : ''}"
                                            placeholder="root@pam">
                                    </div>
                                    <div class="form-group">
                                        <label>Password</label>
                                        <input type="password" name="pp_password"
                                            placeholder="${isEdit && node.power_provider && node.power_provider.fields && node.power_provider.fields.password === '****' ? '(saved — leave blank to keep)' : 'Enter password'}">
                                    </div>
                                    <div class="form-group">
                                        <label>TLS CA Cert Path <span style="font-size:11px;color:var(--text-secondary)">(optional — overrides insecure)</span></label>
                                        <input type="text" name="pp_tls_ca_cert_path"
                                            value="${isEdit && node.power_provider && node.power_provider.fields ? escHtml(node.power_provider.fields.tls_ca_cert_path || '') : ''}"
                                            placeholder="/etc/clustr/pki/proxmox-ca.pem">
                                    </div>
                                    <div class="form-group" style="display:flex;align-items:center;gap:8px;padding-top:22px">
                                        <input type="checkbox" name="pp_insecure" id="pp-insecure"
                                            ${isEdit && node.power_provider && node.power_provider.fields && node.power_provider.fields.insecure === 'true' ? 'checked' : ''}>
                                        <label for="pp-insecure" style="margin:0;font-weight:400">Skip TLS verification (self-signed certs)</label>
                                    </div>
                                </div>
                            </div>
                        </div>
                        <!-- End Power Provider section -->

                        <!-- Shared Mounts section -->
                        <div style="margin-top:20px;padding-top:16px;border-top:1px solid var(--border)">
                            <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:12px">
                                <div style="font-weight:600;font-size:13px;color:var(--text-secondary)">Additional Mounts (fstab)</div>
                                <div style="display:flex;gap:6px;align-items:center">
                                    <select id="mount-preset-select" onchange="Pages._applyMountPreset()" style="font-size:12px;padding:4px 6px">
                                        <option value="">Insert preset…</option>
                                        <option value="nfs-home">NFS home directory</option>
                                        <option value="lustre">Lustre scratch</option>
                                        <option value="beegfs">BeeGFS data</option>
                                        <option value="cifs">CIFS share (Windows)</option>
                                        <option value="bind">Bind mount</option>
                                        <option value="tmpfs">tmpfs for /tmp</option>
                                    </select>
                                    <button type="button" class="btn btn-secondary btn-sm" onclick="Pages._addMountRow()">+ Add Mount</button>
                                </div>
                            </div>
                            <div id="mounts-table-wrap">
                                <table id="mounts-table" style="width:100%;font-size:12px;border-collapse:collapse">
                                    <thead>
                                        <tr style="border-bottom:1px solid var(--border)">
                                            <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">Source</th>
                                            <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">Mount Point</th>
                                            <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">FS Type</th>
                                            <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">Options</th>
                                            <th style="text-align:center;padding:4px 6px;font-weight:500;color:var(--text-secondary)" title="Auto-create the mount point directory">mkd</th>
                                            <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">Comment</th>
                                            <th style="padding:4px"></th>
                                        </tr>
                                    </thead>
                                    <tbody id="mounts-tbody">
                                        ${(() => {
                                            const mounts = (isEdit && node.extra_mounts) ? node.extra_mounts : [];
                                            return mounts.map((m, i) => Pages._mountRowHTML(i, m)).join('');
                                        })()}
                                    </tbody>
                                </table>
                                ${(() => {
                                    const mounts = (isEdit && node.extra_mounts) ? node.extra_mounts : [];
                                    return mounts.length === 0 ? '<div id="mounts-empty" style="text-align:center;padding:12px;color:var(--text-dim);font-size:12px">No additional mounts configured</div>' : '';
                                })()}
                            </div>
                        </div>
                        <!-- End Shared Mounts section -->

                        <div id="node-form-result"></div>
                        <div class="form-actions">
                            <button type="button" class="btn btn-secondary" onclick="document.getElementById('node-modal').remove()">Cancel</button>
                            <button type="submit" class="btn btn-primary" id="node-submit-btn">${isEdit ? 'Save Changes' : 'Create Node'}</button>
                        </div>
                    </form>
                </div>
            </div>`;

        document.body.appendChild(overlay);
        overlay.addEventListener('click', e => { if (e.target === overlay) overlay.remove(); });
        trapModalFocus(overlay, () => overlay.remove());

        // Wire up role-mismatch warning when admin changes the base image selection.
        const imageSelect = overlay.querySelector('select[name="base_image_id"]');
        if (imageSelect && node) {
            const checkMismatch = () => Pages._checkRoleMismatch(imageSelect.value, node, images);
            imageSelect.addEventListener('change', checkMismatch);
            // Run once on open so pre-selected images are validated immediately.
            if (node.base_image_id) checkMismatch();
        }

        // Attach live-validation listeners after DOM is ready.
        const tbody = document.getElementById('mounts-tbody');
        if (tbody) {
            tbody.addEventListener('input', () => Pages._validateMountRows());
            tbody.addEventListener('change', (e) => {
                // When fs_type changes, suggest _netdev for network filesystems.
                if (e.target && e.target.name === 'mount_fs_type') {
                    Pages._onFSTypeChange(e.target);
                }
            });
        }
    },

    // _mountRowHTML builds the HTML for a single mount row in the fstab editor.
    _mountRowHTML(idx, m) {
        m = m || {};
        const networkFSTypes = ['nfs','nfs4','cifs','smbfs','beegfs','lustre','gpfs','9p'];
        const fsTypes = ['nfs','nfs4','cifs','beegfs','lustre','gpfs','xfs','ext4','bind','9p','tmpfs','vfat','ext3','smbfs'];
        const fsSelect = fsTypes.map(t =>
            `<option value="${t}"${m.fs_type === t ? ' selected' : ''}>${t}</option>`
        ).join('');
        return `<tr data-mount-idx="${idx}" style="border-bottom:1px solid var(--border)">
            <td style="padding:4px 3px">
                <input type="text" name="mount_source" value="${escHtml(m.source||'')}"
                    placeholder="nfs-server:/export/home" style="width:100%;min-width:120px;font-size:12px"
                    required>
            </td>
            <td style="padding:4px 3px">
                <input type="text" name="mount_point" value="${escHtml(m.mount_point||'')}"
                    placeholder="/home/shared" style="width:100%;min-width:100px;font-size:12px"
                    required pattern="/.+">
            </td>
            <td style="padding:4px 3px">
                <select name="mount_fs_type" style="font-size:12px;padding:2px 4px">
                    ${fsSelect}
                </select>
            </td>
            <td style="padding:4px 3px">
                <input type="text" name="mount_options" value="${escHtml(m.options||'')}"
                    placeholder="defaults,_netdev" style="width:100%;min-width:120px;font-size:12px">
            </td>
            <td style="padding:4px 3px;text-align:center">
                <input type="checkbox" name="mount_auto_mkdir" ${m.auto_mkdir !== false ? 'checked' : ''}>
            </td>
            <td style="padding:4px 3px">
                <input type="text" name="mount_comment" value="${escHtml(m.comment||'')}"
                    placeholder="optional note" style="width:100%;min-width:80px;font-size:12px">
            </td>
            <td style="padding:4px 3px">
                <button type="button" class="btn btn-danger btn-sm"
                    onclick="Pages._removeMountRow(this)" style="padding:2px 6px;font-size:11px">✕</button>
            </td>
        </tr>`;
    },

    // _addMountRow appends a blank mount row to the table.
    _addMountRow(preset) {
        const tbody = document.getElementById('mounts-tbody');
        const empty = document.getElementById('mounts-empty');
        if (!tbody) return;
        if (empty) empty.remove();
        const idx = tbody.querySelectorAll('tr').length;
        tbody.insertAdjacentHTML('beforeend', Pages._mountRowHTML(idx, preset || {}));
        Pages._validateMountRows();
    },

    // _removeMountRow removes the row containing the given button.
    _removeMountRow(btn) {
        const row = btn.closest('tr');
        if (row) row.remove();
        const tbody = document.getElementById('mounts-tbody');
        if (tbody && tbody.querySelectorAll('tr').length === 0) {
            const wrap = document.getElementById('mounts-table-wrap');
            if (wrap && !document.getElementById('mounts-empty')) {
                wrap.insertAdjacentHTML('beforeend',
                    '<div id="mounts-empty" style="text-align:center;padding:12px;color:var(--text-dim);font-size:12px">No additional mounts configured</div>');
            }
        }
        Pages._validateMountRows();
    },

    // _validateMountRows highlights rows with missing required fields.
    _validateMountRows() {
        const tbody = document.getElementById('mounts-tbody');
        if (!tbody) return;
        let hasErrors = false;
        tbody.querySelectorAll('tr').forEach(row => {
            const src = row.querySelector('[name="mount_source"]');
            const mp  = row.querySelector('[name="mount_point"]');
            let rowOk = true;
            if (src && !src.value.trim()) { src.style.border = '1px solid var(--error)'; rowOk = false; }
            else if (src) src.style.border = '';
            if (mp && (!mp.value.trim() || mp.value[0] !== '/')) {
                mp.style.border = '1px solid var(--error)'; rowOk = false;
            } else if (mp) mp.style.border = '';
            if (!rowOk) hasErrors = true;
        });
        const btn = document.getElementById('node-submit-btn');
        if (btn) btn.disabled = hasErrors;
    },

    // _onFSTypeChange auto-suggests _netdev for network filesystems when the
    // options field is empty.
    _onFSTypeChange(select) {
        const networkFS = ['nfs','nfs4','cifs','smbfs','beegfs','lustre','gpfs','9p'];
        const row = select.closest('tr');
        if (!row) return;
        const optsInput = row.querySelector('[name="mount_options"]');
        if (!optsInput || optsInput.value.trim()) return; // don't overwrite existing
        if (networkFS.includes(select.value)) {
            optsInput.value = 'defaults,_netdev';
        }
    },

    // _applyMountPreset inserts a preset row based on the dropdown selection.
    _applyMountPreset() {
        const sel = document.getElementById('mount-preset-select');
        if (!sel || !sel.value) return;
        const presets = {
            'nfs-home': { source: 'nfs-server:/export/home', mount_point: '/home/shared', fs_type: 'nfs4', options: 'defaults,_netdev,vers=4', auto_mkdir: true, comment: 'NFS home directory' },
            'lustre':   { source: 'mgs@tcp:/scratch',        mount_point: '/scratch',     fs_type: 'lustre', options: 'defaults,_netdev,flock', auto_mkdir: true, comment: 'Lustre scratch' },
            'beegfs':   { source: 'beegfs',                  mount_point: '/mnt/beegfs',  fs_type: 'beegfs', options: 'defaults,_netdev',       auto_mkdir: true, comment: 'BeeGFS data' },
            'cifs':     { source: '//winserver/share',       mount_point: '/mnt/share',   fs_type: 'cifs',   options: 'defaults,_netdev,vers=3.0,sec=ntlmssp', auto_mkdir: true, comment: 'CIFS (Windows) share' },
            'bind':     { source: '/data/src',               mount_point: '/data/dst',    fs_type: 'bind',   options: 'defaults,bind',           auto_mkdir: true, comment: 'Bind mount' },
            'tmpfs':    { source: 'tmpfs',                   mount_point: '/tmp',         fs_type: 'tmpfs',  options: 'defaults,size=4G,mode=1777', auto_mkdir: false, comment: 'tmpfs /tmp' },
        };
        const p = presets[sel.value];
        if (p) Pages._addMountRow(p);
        sel.value = ''; // reset dropdown
    },

    // _collectMounts reads all mount rows from the form and returns an array
    // of FstabEntry objects ready for the API body.
    _collectMounts() {
        const tbody = document.getElementById('mounts-tbody');
        if (!tbody) return [];
        const rows = tbody.querySelectorAll('tr');
        const mounts = [];
        rows.forEach(row => {
            const source    = (row.querySelector('[name="mount_source"]')?.value || '').trim();
            const mountPoint = (row.querySelector('[name="mount_point"]')?.value || '').trim();
            const fsType    = row.querySelector('[name="mount_fs_type"]')?.value || 'nfs';
            const options   = (row.querySelector('[name="mount_options"]')?.value || '').trim();
            const autoMkdir = row.querySelector('[name="mount_auto_mkdir"]')?.checked !== false;
            const comment   = (row.querySelector('[name="mount_comment"]')?.value || '').trim();
            if (!source || !mountPoint) return; // skip incomplete rows
            mounts.push({ source, mount_point: mountPoint, fs_type: fsType, options, auto_mkdir: autoMkdir, comment, dump: 0, pass: 0 });
        });
        return mounts;
    },

    // _onPowerProviderTypeChange shows/hides the Proxmox fields when the provider
    // type dropdown changes. Called by the onchange handler in the node edit modal.
    _onPowerProviderTypeChange(type) {
        const proxmoxFields = document.getElementById('pp-proxmox-fields');
        if (proxmoxFields) proxmoxFields.style.display = (type === 'proxmox') ? '' : 'none';
    },

    async submitNode(e, nodeId) {
        e.preventDefault();
        const form = e.target;
        const btn  = document.getElementById('node-submit-btn');
        const res  = document.getElementById('node-form-result');
        const data = new FormData(form);

        btn.disabled = true;
        btn.textContent = 'Saving…';
        res.innerHTML = '';

        const tags    = data.get('tags').split(',').map(g => g.trim()).filter(Boolean);
        const sshKeys = data.get('ssh_keys').split('\n').map(k => k.trim()).filter(Boolean);
        const groupId = data.get('group_id') || null;

        // Build power_provider from form fields.
        const ppType = data.get('power_provider_type') || '';
        let powerProvider = null;
        if (ppType === 'proxmox') {
            const fields = {
                api_url:           data.get('pp_api_url') || '',
                node:              data.get('pp_node') || '',
                vmid:              data.get('pp_vmid') || '',
                username:          data.get('pp_username') || '',
                tls_ca_cert_path:  (data.get('pp_tls_ca_cert_path') || '').trim(),
                insecure:          document.getElementById('pp-insecure') && document.getElementById('pp-insecure').checked ? 'true' : 'false',
            };
            // Only include password if the user typed something; blank means keep existing.
            const pw = data.get('pp_password');
            if (pw) fields.password = pw;
            powerProvider = { type: 'proxmox', fields };
        } else if (ppType === 'ipmi') {
            powerProvider = { type: 'ipmi', fields: {} };
        }

        const body = {
            hostname:       data.get('hostname'),
            fqdn:           data.get('fqdn'),
            primary_mac:    data.get('primary_mac'),
            base_image_id:  data.get('base_image_id'),
            tags,
            groups:         tags, // backward-compat alias
            group_id:       groupId,
            ssh_keys:       sshKeys,
            kernel_args:    data.get('kernel_args'),
            interfaces:     [],
            custom_vars:    {},
            power_provider: powerProvider,
            extra_mounts:   Pages._collectMounts(),
        };

        try {
            if (nodeId) {
                await API.nodes.update(nodeId, body);
            } else {
                await API.nodes.create(body);
            }
            document.getElementById('node-modal').remove();
            Pages.nodes();
        } catch (ex) {
            res.innerHTML = `<div class="form-error">${escHtml(ex.message)}</div>`;
            btn.disabled = false;
            btn.textContent = nodeId ? 'Save Changes' : 'Create Node';
        }
    },

    async deleteNode(id, name) {
        Pages.showConfirmModal({
            title: 'Delete Node',
            message: `Delete node <strong>${escHtml(name)}</strong>? This cannot be undone.`,
            confirmText: 'Delete',
            danger: true,
            onConfirm: async () => {
                try {
                    await API.nodes.del(id);
                    Pages.nodes();
                } catch (e) {
                    Pages.showAlertModal('Delete Failed', escHtml(e.message));
                }
            },
        });
    },

    // _togglePowerDropdown opens/closes the per-row power dropdown on the nodes list.
    // Uses position:fixed to escape overflow:hidden/auto ancestors (card, table-wrap).
    _togglePowerDropdown(nodeId, event) {
        if (event) event.stopPropagation();
        // Close every other open power menu.
        document.querySelectorAll('.actions-dropdown-menu[id^="pwr-menu-"]').forEach(m => {
            if (m.id !== `pwr-menu-${nodeId}`) {
                m.classList.remove('open');
                m.style.cssText = '';
            }
        });
        const menu = document.getElementById(`pwr-menu-${nodeId}`);
        if (!menu) return;
        const isOpen = menu.classList.contains('open');
        if (isOpen) {
            menu.classList.remove('open');
            menu.style.cssText = '';
            return;
        }
        // Position the menu using fixed coords so it escapes any overflow container.
        const btn = document.getElementById(`pwr-dd-${nodeId}`);
        if (btn) {
            const rect = btn.getBoundingClientRect();
            menu.style.position = 'fixed';
            menu.style.top = (rect.bottom + 4) + 'px';
            menu.style.right = (window.innerWidth - rect.right) + 'px';
            menu.style.left = 'auto';
            menu.style.zIndex = '1000';
        }
        menu.classList.add('open');
    },

    // _closePowerDropdownsOnOutsideClick is bound as a document listener on the nodes
    // list page and removed on navigation. It closes any open power dropdown when the
    // user clicks outside of it.
    _closePowerDropdownsOnOutsideClick(e) {
        if (!e.target.closest('[id^="pwr-dd-"]')) {
            document.querySelectorAll('.actions-dropdown-menu[id^="pwr-menu-"]')
                .forEach(m => { m.classList.remove('open'); m.style.cssText = ''; });
        }
    },

    // _listPowerAction executes a non-destructive power action from the nodes list.
    // Handles: on (power on) and status (check and report via toast).
    async _listPowerAction(nodeId, action) {
        if (action === 'status') {
            try {
                App.toast(`Checking power status for ${nodeId}…`, 'info');
                const data = await API.nodes.power.status(nodeId);
                const state = (data && data.state) ? data.state.toUpperCase() : 'UNKNOWN';
                App.toast(`${nodeId}: power is ${state}`, 'info');
            } catch (e) {
                App.toast(`Status check failed: ${e.message}`, 'error');
            }
            return;
        }
        if (action === 'on') {
            try {
                await API.nodes.power.on(nodeId);
                App.toast(`Power on sent to ${nodeId}`, 'success');
            } catch (e) {
                App.toast(`Power on failed: ${e.message}`, 'error');
            }
        }
    },

    // _listConfirmPowerAction shows a confirmation dialog before executing a destructive
    // power action from the nodes list (off, reset, cycle).
    async _listConfirmPowerAction(nodeId, action, title, description) {
        Pages.showConfirmModal({
            title,
            message: `${escHtml(description)}<br><br><span class="text-dim">Node: ${escHtml(nodeId)}</span>`,
            confirmText: title,
            danger: true,
            onConfirm: async () => {
                const actionFns = {
                    off:   () => API.nodes.power.off(nodeId),
                    reset: () => API.nodes.power.reset(nodeId),
                    cycle: () => API.nodes.power.cycle(nodeId),
                };
                const fn = actionFns[action];
                if (!fn) return;
                try {
                    await fn();
                    App.toast(`${title} sent to ${nodeId}`, 'success');
                } catch (e) {
                    App.toast(`${title} failed: ${e.message}`, 'error');
                }
            },
        });
    },

    // _regenerateHostname picks a new random 4-hex suffix and fills the hostname field.
    // Called by the Regenerate button in the node edit modal.
    _regenerateHostname(mac) {
        const input = document.getElementById('node-hostname-input');
        if (!input) return;
        // Generate a random 4-character hex suffix (not MAC-derived, so it's clearly new).
        const suffix = Math.floor(Math.random() * 0xffff).toString(16).padStart(4, '0');
        input.value = 'clustr-' + suffix;
        input.focus();
    },

    // ── Node Detail ────────────────────────────────────────────────────────

    // _nodeEditorState tracks per-tab dirty state for inline editing.
    // Structure: { tabId: { dirty: bool, original: {}, current: {} } }
    _nodeEditorState: {},

    // _nodeEditorNodeId is the node ID currently loaded in nodeDetail.
    _nodeEditorNodeId: null,

    async nodeDetail(id) {
        App.render(loading('Loading node…'));
        // Reset per-tab dirty state on page load.
        Pages._nodeEditorState = {};
        Pages._nodeEditorNodeId = id;

        try {
            const [node, imagesResp, nodeGroupsResp, reimagesResp, heartbeatResp] = await Promise.all([
                API.nodes.get(id),
                API.images.list(),
                API.nodeGroups.list().catch(() => ({ groups: [] })),
                API.reimages.listForNode(id).catch(() => ({ requests: [] })),
                API.request('GET', `/nodes/${id}/heartbeat`).catch(() => null),
            ]);
            const images     = imagesResp.images || [];
            const nodeGroups = (nodeGroupsResp && (nodeGroupsResp.node_groups || nodeGroupsResp.groups)) || [];
            const img        = images.find(i => i.id === node.base_image_id);
            const reimageHistory = (reimagesResp && reimagesResp.requests) || [];

            let hw = null;
            try {
                if (node.hardware_profile) {
                    hw = typeof node.hardware_profile === 'string'
                        ? JSON.parse(node.hardware_profile)
                        : node.hardware_profile;
                }
            } catch (_) {}

            const displayName = node.hostname || node.primary_mac;

            // Build capture-this-node button HTML if node has a configured IP.
            let captureBtn = '';
            const iface = (node.interfaces || []).find(i => i.ip_address);
            if (iface) {
                const ip = iface.ip_address.split('/')[0];
                const prefillHost = 'root@' + ip;
                const prefillName = (node.hostname && node.hostname !== '(none)')
                    ? node.hostname.toLowerCase().replace(/[^a-z0-9-]/g, '-') + '-capture'
                    : '';
                captureBtn = '<button class="btn btn-secondary" onclick="Pages.showCaptureModal(' +
                    JSON.stringify(prefillHost) + ',' + JSON.stringify(prefillName) + ')">Capture this node</button>';
            }

            // Ready image options for Overview tab image selector.
            const imgOptions = images
                .filter(i => i.status === 'ready')
                .map(i => `<option value="${escHtml(i.id)}" ${node.base_image_id === i.id ? 'selected' : ''}>${escHtml(i.name)}${i.version ? ' (' + i.version + ')' : ''}</option>`)
                .join('');

            // Node group options for Overview tab.
            const groupOptions = nodeGroups
                .map(g => `<option value="${escHtml(g.id)}" ${node.group_id === g.id ? 'selected' : ''}>${escHtml(g.name)}</option>`)
                .join('');

            // Discovered NIC MACs for Network tab (for interface editor MAC dropdowns).
            const discoveredMACs = hw && hw.NICs ? hw.NICs.map(n => n.MAC || n.MACAddress).filter(Boolean) : [];
            const discoveredMACsJSON = JSON.stringify(discoveredMACs);

            App.render(`
                <div class="breadcrumb">
                    <a href="#/nodes">Nodes</a>
                    <span class="breadcrumb-sep">/</span>
                    <span>${escHtml(displayName)}</span>
                </div>
                <div class="page-header">
                    <div style="display:flex;align-items:center;gap:12px">
                        <button class="detail-back-btn" onclick="Pages._nodeDetailBack()">
                            <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24">
                                <polyline points="15 18 9 12 15 6"/>
                            </svg>
                            Back
                        </button>
                        <div>
                            <h1 class="page-title" style="display:flex;align-items:center;gap:8px">
                                ${(node.hostname && node.hostname !== '(none)')
                                    ? escHtml(node.hostname)
                                    : `<span class="text-dim" style="font-style:italic">Unassigned</span>`}
                                ${node.hostname_auto ? `<span class="badge badge-neutral badge-sm" title="Auto-generated hostname">auto</span>` : ''}
                            </h1>
                            <div class="page-subtitle text-mono">${escHtml(node.primary_mac)}</div>
                        </div>
                        ${nodeBadge(node)}
                    </div>
                    <div class="flex gap-8">
                        ${captureBtn}
                        ${Auth._role === 'readonly' ? '' : `
                        <div class="actions-dropdown" id="node-actions-dropdown">
                            <button class="btn btn-secondary" onclick="Pages._toggleActionsDropdown()">
                                Actions
                                <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24" style="width:13px;height:13px"><polyline points="6 9 12 15 18 9"/></svg>
                            </button>
                            <div class="actions-dropdown-menu" id="node-actions-menu">
                                <button class="actions-dropdown-item" onclick="Pages._nodeActionsRediscover('${node.id}');Pages._toggleActionsDropdown()">Queue Reimage</button>
                                <button class="actions-dropdown-item" onclick="Pages._nodeActionsTriggerReimage('${node.id}','${escHtml(displayName)}');Pages._toggleActionsDropdown()">Trigger reimage</button>
                                ${iface ? `<button class="actions-dropdown-item" onclick="Pages.showCaptureModal(${JSON.stringify('root@' + iface.ip_address.split('/')[0])},${JSON.stringify((node.hostname && node.hostname !== '(none)') ? node.hostname.toLowerCase().replace(/[^a-z0-9-]/g, '-') + '-capture' : '')});Pages._toggleActionsDropdown()">Capture as image</button>` : ''}
                                ${Auth._role === 'admin' ? `<div class="actions-dropdown-sep"></div>
                                <button class="actions-dropdown-item danger" onclick="Pages.deleteNodeAndGoBack('${node.id}', '${escHtml(displayName)}');Pages._toggleActionsDropdown()">Delete node</button>` : ''}
                            </div>
                        </div>
                        `}
                    </div>
                </div>

                <!-- C3-20: Last failure summary banner — shown when last deploy failed and hasn't been superseded. -->
                ${(() => {
                    const failed = node.last_deploy_failed_at &&
                        (!node.deploy_completed_preboot_at || new Date(node.last_deploy_failed_at) > new Date(node.deploy_completed_preboot_at)) &&
                        !node.deploy_verified_booted_at &&
                        !node.reimage_pending;
                    if (!failed) return '';
                    return `<div class="alert alert-error" style="margin:12px 0;display:flex;align-items:flex-start;gap:12px">
                        <div style="flex:1">
                            <strong>Last deployment failed</strong>
                            <span style="font-weight:400;margin-left:6px;font-size:13px">${fmtDate(node.last_deploy_failed_at)}</span>
                            <div style="margin-top:4px;font-size:13px;color:inherit;opacity:0.85">
                                The most recent deploy did not complete. Check deploy logs for details.
                                ${Auth._role !== 'readonly' ? `Reimage when ready to retry.` : ''}
                            </div>
                        </div>
                        <div style="display:flex;gap:8px;flex-shrink:0">
                            <button class="btn btn-secondary btn-sm"
                                onclick="Pages._switchNodeTab(document.getElementById('node-tab-btn-logs'), 'tab-logs', 'logs');Pages.loadNodeLogs('${escHtml(node.primary_mac)}')">
                                View Logs
                            </button>
                            ${Auth._role !== 'readonly' ? `<button class="btn btn-secondary btn-sm"
                                onclick="Pages._nodeActionsTriggerReimage('${node.id}', '${escHtml(displayName)}')">
                                Re-deploy
                            </button>` : ''}
                        </div>
                    </div>`;
                })()}

                <div class="tab-bar" id="node-tab-bar">
                    <div class="tab active" id="node-tab-btn-overview" onclick="Pages._switchNodeTab(this, 'tab-overview', 'overview')">Overview</div>
                    <div class="tab" id="node-tab-btn-hardware" onclick="Pages._switchNodeTab(this, 'tab-hardware', 'hardware')">Hardware</div>
                    <div class="tab" id="node-tab-btn-network" onclick="Pages._switchNodeTab(this, 'tab-network', 'network')">Network</div>
                    <div class="tab" id="node-tab-btn-bmc" onclick="Pages._switchNodeTab(this, 'tab-bmc', 'bmc');Pages._onBMCTabOpen('${node.id}', ${!!(node.bmc || node.power_provider)})">Power / IPMI</div>
                    <div class="tab" id="node-tab-btn-disklayout" onclick="Pages._switchNodeTab(this, 'tab-disklayout', 'disklayout');Pages._onDiskLayoutTabOpen('${node.id}')">Disk Layout</div>
                    <div class="tab" id="node-tab-btn-mounts" onclick="Pages._switchNodeTab(this, 'tab-mounts', 'mounts');Pages._onMountsTabOpen('${node.id}')">Mounts</div>
                    <div class="tab" id="node-tab-btn-config" onclick="Pages._switchNodeTab(this, 'tab-config', 'config')">Configuration</div>
                    <div class="tab" id="node-tab-btn-logs" onclick="Pages._switchNodeTab(this, 'tab-logs', 'logs');Pages.loadNodeLogs('${escHtml(node.primary_mac)}')">Logs</div>
                    <div class="tab" id="node-tab-btn-configpush" onclick="Pages._switchNodeTab(this, 'tab-configpush', 'configpush')">Config Push</div>
                    <div class="tab" id="node-tab-btn-slurm" onclick="Pages._switchNodeTab(this, 'tab-slurm', 'slurm');Pages._onSlurmTabOpen('${escHtml(node.id)}')">Slurm</div>
                    <div class="tab" id="node-tab-btn-confighistory" onclick="Pages._switchNodeTab(this, 'tab-confighistory', 'confighistory');Pages._onConfigHistoryTabOpen('${escHtml(node.id)}')">Config History</div>
                    ${(node.last_seen_at && (Date.now() - new Date(node.last_seen_at).getTime()) < 2 * 60 * 1000) ? `<div class="tab" id="node-tab-btn-diagnostics" onclick="Pages._switchNodeTab(this, 'tab-diagnostics', 'diagnostics')">Diagnostics</div>` : ''}
                </div>

                <!-- Overview tab — inline editable -->
                <div id="tab-overview" class="tab-panel active">
                    ${node.deploy_verify_timeout_at ? `
                    <div class="alert alert-error" style="margin-bottom:12px">
                        <strong>Verification timeout.</strong>
                        Deploy succeeded pre-boot but the OS never phoned home
                        (timeout at ${fmtDate(node.deploy_verify_timeout_at)}).
                        Node may not be bootable — possible causes: bootloader failure, kernel panic,
                        network misconfiguration, or <code>/etc/clustr/node-token</code> not written correctly.
                        Attach serial console to investigate or ${Auth._role !== 'readonly' ? `<button class="btn btn-secondary btn-sm" style="margin-left:4px" onclick="Pages._nodeActionsTriggerReimage('${node.id}', '${escHtml(displayName)}')">Re-deploy</button>` : ''}
                    </div>` : ''}
                    ${(node.deploy_completed_preboot_at && !node.deploy_verified_booted_at && !node.deploy_verify_timeout_at) ? `
                    <div class="alert alert-warning" style="margin-bottom:12px">
                        <strong>Awaiting boot confirmation.</strong>
                        This node has not confirmed boot. clustr-static completed successfully, but the OS has not phoned home yet.
                        Attach serial console to verify if the deploy is stalled.
                    </div>` : ''}
                    <div id="tab-save-bar-overview" class="tab-save-bar" style="display:none">
                        <span class="save-status modified" id="tab-save-status-overview">Unsaved changes</span>
                        <button class="btn btn-secondary btn-sm" onclick="Pages._tabRevert('overview')" id="tab-revert-overview">Revert</button>
                        <button class="btn btn-primary btn-sm" onclick="Pages._tabSaveOverview('${node.id}')" id="tab-save-overview">Save</button>
                    </div>
                    ${cardWrap('Node Details', `
                        <div class="form-grid" style="margin-bottom:0">
                                <div class="form-group">
                                    <label>Hostname</label>
                                    <input type="text" id="ov-hostname" value="${escHtml(node.hostname || '')}"
                                        placeholder="clustr-node" pattern="^[a-zA-Z0-9][a-zA-Z0-9.-]*$"
                                        oninput="Pages._tabMarkDirty('overview')">
                                </div>
                                <div class="form-group">
                                    <label>FQDN</label>
                                    <input type="text" id="ov-fqdn" value="${escHtml(node.fqdn || '')}"
                                        placeholder="node.example.com"
                                        oninput="Pages._tabMarkDirty('overview')">
                                </div>
                                <div class="form-group">
                                    <label>Base Image</label>
                                    <select id="ov-base-image" onchange="Pages._tabMarkDirty('overview');Pages._checkRoleMismatchInline(this.value, ${JSON.stringify(node)}, ${JSON.stringify(images)})">
                                        <option value="">No image assigned</option>
                                        ${imgOptions}
                                    </select>
                                    <div id="ov-role-mismatch-warning" class="alert alert-warning" style="display:none;margin-top:6px;font-size:12px"></div>
                                </div>
                                <div class="form-group">
                                    <label>Node Group</label>
                                    <select id="ov-group-id" onchange="Pages._tabMarkDirty('overview')">
                                        <option value="">None</option>
                                        ${groupOptions}
                                    </select>
                                </div>
                                <div class="form-group" style="grid-column:1/-1">
                                    <label>Tags <span class="tooltip-icon" title="Unstructured labels used for filtering and Slurm role assignment." style="cursor:help;font-size:11px;color:var(--text-secondary)">(?)</span> <span style="font-size:11px;color:var(--text-secondary)">(comma-separated)</span></label>
                                    <input type="text" id="ov-tags" value="${escHtml((node.tags || node.groups || []).join(', '))}"
                                        placeholder="compute, gpu, infiniband"
                                        oninput="Pages._tabMarkDirty('overview')">
                                </div>
                                <div class="form-group" style="grid-column:1/-1">
                                    <label>Reimage Status</label>
                                    <div style="display:flex;align-items:center;gap:12px;padding:8px 0">
                                        ${node.reimage_pending
                                            ? `<span class="badge badge-warning">Reimage pending</span>
                                               <span class="text-dim" style="font-size:12px">Node will re-deploy on next PXE boot</span>`
                                            : `<span class="badge badge-neutral">Normal</span>
                                               ${Auth._role !== 'readonly' ? `<button type="button" class="btn btn-secondary btn-sm" onclick="Pages._nodeActionsTriggerReimage('${node.id}', '${escHtml(displayName)}')">Request Reimage</button>` : ''}`}
                                    </div>
                                </div>
                            </div>`)}

                    ${cardWrap('Node Info', `
                        <div class="kv-grid">
                                <div class="kv-item"><div class="kv-key">ID</div><div class="kv-value">${escHtml(node.id)}</div></div>
                                <div class="kv-item"><div class="kv-key">Primary MAC</div><div class="kv-value text-mono">${escHtml(node.primary_mac)}</div></div>
                                <div class="kv-item"><div class="kv-key">Status</div><div class="kv-value">${nodeBadge(node)}</div></div>
                                <div class="kv-item"><div class="kv-key">Current Image</div><div class="kv-value">
                                    ${img ? `<a href="#/images/${img.id}">${escHtml(img.name)}</a> ${badge(img.status)}` : (node.base_image_id ? escHtml(node.base_image_id) : '—')}
                                </div></div>
                                <div class="kv-item"><div class="kv-key">Node Group</div><div class="kv-value">
                                    ${node.group_id
                                        ? (() => { const g = nodeGroups.find(x => x.id === node.group_id); return g ? `<a href="#/nodes/groups/${g.id}">${escHtml(g.name)}</a>` : `<span class="text-mono text-dim text-sm">${escHtml(node.group_id)}</span>`; })()
                                        : '<span class="text-dim">—</span>'}
                                </div></div>
                                <div class="kv-item"><div class="kv-key">Last Deploy OK</div><div class="kv-value">${node.last_deploy_succeeded_at ? fmtDate(node.last_deploy_succeeded_at) : '—'}</div></div>
                                <div class="kv-item"><div class="kv-key">Last Deploy Failed</div><div class="kv-value">${node.last_deploy_failed_at ? fmtDate(node.last_deploy_failed_at) : '—'}</div></div>
                                <div class="kv-item"><div class="kv-key">Deploy Complete (pre-boot)</div><div class="kv-value" title="Set when clustr-static finishes in the PXE initramfs. Proves rootfs written; not that OS boots.">${node.deploy_completed_preboot_at ? fmtRelative(node.deploy_completed_preboot_at) : '—'}</div></div>
                                <div class="kv-item"><div class="kv-key">Boot Verified</div><div class="kv-value" title="Set when the deployed OS phones home post-boot. Proves bootloader, kernel, and systemd all started.">${node.deploy_verified_booted_at ? fmtRelative(node.deploy_verified_booted_at) : (node.deploy_completed_preboot_at ? '<span class="badge badge-warning">Pending</span>' : '—')}</div></div>
                                ${node.deploy_verify_timeout_at ? `<div class="kv-item" style="grid-column:1/-1"><div class="kv-key">Verify Timeout</div><div class="kv-value text-danger">${fmtDate(node.deploy_verify_timeout_at)}</div></div>` : ''}
                                ${node.last_seen_at ? `<div class="kv-item"><div class="kv-key">Last Seen</div><div class="kv-value">${fmtRelative(node.last_seen_at)}</div></div>` : ''}
                                <div class="kv-item"><div class="kv-key">Created</div><div class="kv-value">${fmtDate(node.created_at)}</div></div>
                                <div class="kv-item"><div class="kv-key">Updated</div><div class="kv-value">${fmtDate(node.updated_at)}</div></div>
                            </div>`)}

                    ${cardWrap('Heartbeat', (() => {
                        if (!heartbeatResp) {
                            return emptyState('No heartbeat received', 'clustr-clientd is not connected or has not sent a heartbeat yet.');
                        }
                        const hb = heartbeatResp;
                        const receivedAt = hb.ReceivedAt || hb.received_at;
                        const load1 = hb.Load1 ?? hb.load_1 ?? null;
                        const load5 = hb.Load5 ?? hb.load_5 ?? null;
                        const load15 = hb.Load15 ?? hb.load_15 ?? null;
                        const memTotal = hb.MemTotalKB ?? hb.mem_total_kb ?? null;
                        const memAvail = hb.MemAvailKB ?? hb.mem_avail_kb ?? null;
                        const uptime = hb.UptimeSec ?? hb.uptime_sec ?? null;
                        const kernel = hb.Kernel || hb.kernel || '';
                        const clientdVer = hb.ClientdVer || hb.clientd_ver || '';
                        const disk = hb.DiskUsage || hb.disk_usage || [];
                        const services = hb.Services || hb.services || [];

                        const fmtUptime = (s) => {
                            if (s === null) return '—';
                            const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600),
                                  m = Math.floor((s % 3600) / 60);
                            return d > 0 ? `${d}d ${h}h ${m}m` : (h > 0 ? `${h}h ${m}m` : `${m}m`);
                        };
                        const fmtMem = (kb) => kb !== null ? (kb / 1024 / 1024).toFixed(1) + ' GiB' : '—';
                        const fmtPct = (used, total) => total ? Math.round(used / total * 100) + '%' : '—';
                        // B4-1: removed inner fmtBytes shadow — use the outer binary fmtBytes consistently.

                        const diskRows = disk.map(d =>
                            `<tr>
                                <td class="text-mono text-sm">${escHtml(d.mount_point || d.MountPoint || '?')}</td>
                                <td class="text-sm">${fmtBytes(d.total_bytes ?? d.TotalBytes ?? 0)}</td>
                                <td class="text-sm">${fmtBytes(d.used_bytes ?? d.UsedBytes ?? 0)}</td>
                                <td class="text-sm">${fmtPct(d.used_bytes ?? d.UsedBytes ?? 0, d.total_bytes ?? d.TotalBytes ?? 0)}</td>
                            </tr>`
                        ).join('');

                        const svcBadges = services.map(s => {
                            const name = s.name || s.Name || '?';
                            const active = s.active || s.Active;
                            const state = s.state || s.State || '';
                            const cls = active ? 'badge-success' : 'badge-neutral';
                            return `<span class="badge ${cls} badge-sm" title="${escHtml(state)}">${escHtml(name)}</span>`;
                        }).join(' ');

                        return `
                            <div class="kv-grid" style="margin-bottom:12px">
                                <div class="kv-item"><div class="kv-key">Received</div><div class="kv-value">${receivedAt ? fmtRelative(receivedAt) : '—'}</div></div>
                                <div class="kv-item"><div class="kv-key">Uptime</div><div class="kv-value">${fmtUptime(uptime)}</div></div>
                                <div class="kv-item"><div class="kv-key">Load (1/5/15)</div><div class="kv-value text-mono">${load1 !== null ? load1.toFixed(2) : '—'} / ${load5 !== null ? load5.toFixed(2) : '—'} / ${load15 !== null ? load15.toFixed(2) : '—'}</div></div>
                                <div class="kv-item"><div class="kv-key">Memory (total / free)</div><div class="kv-value">${fmtMem(memTotal)} / ${fmtMem(memAvail)}</div></div>
                                ${kernel ? `<div class="kv-item"><div class="kv-key">Kernel</div><div class="kv-value text-mono text-sm">${escHtml(kernel)}</div></div>` : ''}
                                ${clientdVer ? `<div class="kv-item"><div class="kv-key">clientd version</div><div class="kv-value text-mono text-sm">${escHtml(clientdVer)}</div></div>` : ''}
                            </div>
                            ${disk.length > 0 ? `
                                <div class="text-dim text-sm" style="margin-bottom:6px;font-weight:500">Disk Usage</div>
                                <table style="width:100%;border-collapse:collapse;font-size:13px;margin-bottom:12px">
                                    <thead><tr style="border-bottom:1px solid var(--border)">
                                        <th style="padding:4px 8px;text-align:left;color:var(--text-secondary);font-weight:500">Mount</th>
                                        <th style="padding:4px 8px;text-align:left;color:var(--text-secondary);font-weight:500">Total</th>
                                        <th style="padding:4px 8px;text-align:left;color:var(--text-secondary);font-weight:500">Used</th>
                                        <th style="padding:4px 8px;text-align:left;color:var(--text-secondary);font-weight:500">%</th>
                                    </tr></thead>
                                    <tbody>${diskRows}</tbody>
                                </table>` : ''}
                            ${services.length > 0 ? `
                                <div class="text-dim text-sm" style="margin-bottom:6px;font-weight:500">Services</div>
                                <div style="display:flex;flex-wrap:wrap;gap:6px">${svcBadges}</div>` : ''}`;
                    })())}

                    ${cardWrap('Reimage History', (() => {
                        const recent = reimageHistory.slice(0, 10);
                        if (recent.length === 0) {
                            return emptyState('No reimage history', 'Reimage requests appear here after the first deploy.');
                        }
                        const TERMINAL = new Set(['complete', 'succeeded', 'failed', 'canceled']);
                        const statusBadge = (s) => {
                            const cls = {
                                complete:    'badge-success',
                                succeeded:   'badge-success',
                                failed:      'badge-danger',
                                in_progress: 'badge-warning',
                                triggered:   'badge-warning',
                                pending:     'badge-neutral',
                                canceled:    'badge-neutral',
                            }[s] || 'badge-neutral';
                            return `<span class="badge ${cls}">${escHtml(s)}</span>`;
                        };
                        const rows = recent.map(r => {
                            const failDetail = r.status === 'failed' && (r.exit_code != null || r.phase)
                                ? `<div class="text-dim text-sm" style="margin-top:2px">exit&nbsp;${r.exit_code ?? '?'}&nbsp;(${escHtml(r.exit_name || r.phase || 'unknown')})</div>`
                                : '';
                            const errTip = r.error_message
                                ? `title="${escHtml(r.error_message)}"`
                                : '';
                            const cancelBtn = !TERMINAL.has(r.status)
                                ? `<button class="btn btn-xs" style="padding:2px 6px;font-size:11px;color:var(--error);background:transparent;border:1px solid var(--error);border-radius:4px;cursor:pointer;line-height:1.2"
                                       onclick="Pages._cancelReimage('${escHtml(r.id)}','${escHtml(node.id)}')" title="Cancel this reimage">Cancel</button>`
                                : '';
                            return `<tr>
                                <td class="text-mono text-sm" ${errTip}>${escHtml(r.id.slice(0,8))}</td>
                                <td>${statusBadge(r.status)}${failDetail}</td>
                                <td class="text-sm">${r.completed_at ? fmtDate(r.completed_at) : (r.created_at ? fmtDate(r.created_at) : '—')}</td>
                                <td class="text-sm text-dim">${escHtml(r.phase || '—')}</td>
                                <td style="width:70px;text-align:center">${cancelBtn}</td>
                            </tr>`;
                        }).join('');
                        return `<table class="card-table" style="width:100%;border-collapse:collapse;font-size:13px">
                            <thead><tr style="border-bottom:1px solid var(--border)">
                                <th style="padding:8px 16px;text-align:left;font-weight:500;color:var(--text-secondary)">ID</th>
                                <th style="padding:8px 16px;text-align:left;font-weight:500;color:var(--text-secondary)">Status</th>
                                <th style="padding:8px 16px;text-align:left;font-weight:500;color:var(--text-secondary)">When</th>
                                <th style="padding:8px 16px;text-align:left;font-weight:500;color:var(--text-secondary)">Phase</th>
                                <th style="padding:8px 16px;text-align:left;font-weight:500;color:var(--text-secondary)"></th>
                            </tr></thead>
                            <tbody>${rows}</tbody>
                        </table>`;
                    })())}
                </div>

                <!-- Hardware tab — read-only display + re-discover action -->
                <div id="tab-hardware" class="tab-panel">
                    <div style="display:flex;align-items:center;gap:12px;margin-bottom:16px">
                        ${hw && hw.discovered_at ? `<span class="text-dim" style="font-size:12px">Last discovered: ${fmtRelative(hw.discovered_at)}</span>` : ''}
                        <button class="btn btn-secondary btn-sm" style="margin-left:auto" onclick="Pages._nodeActionsRediscover('${node.id}')">
                            <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24" style="width:13px;height:13px"><polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/></svg>
                            Queue Reimage
                        </button>
                    </div>
                    ${hw ? this._hardwareProfile(hw) : `<div class="card"><div class="card-body">${emptyState('No hardware profile', 'Hardware is discovered when a node registers via PXE boot.')}</div></div>`}
                </div>

                <!-- Network tab — inline editable interface configs -->
                <div id="tab-network" class="tab-panel">
                    <div id="tab-save-bar-network" class="tab-save-bar" style="display:none">
                        <span class="save-status modified" id="tab-save-status-network">Unsaved changes</span>
                        <button class="btn btn-secondary btn-sm" onclick="Pages._tabRevert('network')" id="tab-revert-network">Revert</button>
                        <button class="btn btn-primary btn-sm" onclick="Pages._tabSaveNetwork('${node.id}')" id="tab-save-network">Save</button>
                    </div>
                    ${cardWrap('Network Interfaces', `
                        <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:12px">
                            <span class="text-dim" style="font-size:12px">Configure logical interfaces. Discovered interfaces are shown read-only on the Hardware tab.</span>
                            <button type="button" class="btn btn-secondary btn-sm" data-macs="${escHtml(discoveredMACsJSON)}" onclick="Pages._netAddInterface(JSON.parse(this.dataset.macs))">+ Add Interface</button>
                        </div>
                        <div id="net-interfaces-list">
                            ${(node.interfaces || []).length === 0
                                ? `<div id="net-empty" style="text-align:center;padding:16px;color:var(--text-dim);font-size:12px">No interfaces configured</div>`
                                : (node.interfaces || []).map((iface, i) => Pages._netInterfaceRowHTML(i, iface, discoveredMACs)).join('')}
                        </div>`)}
                </div>

                <!-- Power / IPMI tab — power controls + inline provider editor -->
                <div id="tab-bmc" class="tab-panel">
                    ${(node.bmc && node.bmc.ip_address) || (node.power_provider && node.power_provider.type) ? `
                    ${node.bmc && node.bmc.ip_address ? cardWrap('Power Status',
                        `<div id="power-status-panel" style="display:flex;align-items:center;gap:16px;margin-bottom:16px">
                            <div id="power-indicator" style="width:18px;height:18px;border-radius:50%;background:var(--border);flex-shrink:0"></div>
                            <div>
                                <div id="power-label" style="font-weight:600;font-size:15px">Checking…</div>
                                <div id="power-last-checked" class="text-dim text-sm"></div>
                            </div>
                            <button class="btn btn-secondary btn-sm" style="margin-left:auto" onclick="Pages._refreshPowerStatus('${node.id}')">Refresh</button>
                        </div>
                        <div id="power-error-msg" style="display:none" class="alert alert-error"></div>`,
                        ''
                    ) : ''}

                    ${node.bmc && node.bmc.ip_address ? cardWrap('Power Controls',
                        `<div class="flex gap-8" style="flex-wrap:wrap;margin-bottom:8px">
                            <button id="btn-power-on"    class="btn btn-secondary btn-sm" onclick="Pages._doPowerAction('${node.id}', 'on')">Power On</button>
                            <button id="btn-power-off"   class="btn btn-danger btn-sm"    onclick="Pages._confirmPowerAction('${node.id}', 'off',   'Power Off', 'This will immediately cut power to the node.')">Power Off</button>
                            <button id="btn-power-cycle" class="btn btn-danger btn-sm"    onclick="Pages._confirmPowerAction('${node.id}', 'cycle', 'Power Cycle', 'This will hard-cycle the node (power off then on).')">Power Cycle</button>
                            <button id="btn-power-reset" class="btn btn-danger btn-sm"    onclick="Pages._confirmPowerAction('${node.id}', 'reset', 'Reset', 'This will issue a hard reset. The node will reboot immediately.')">Reset</button>
                        </div>
                        <div class="flex gap-8" style="flex-wrap:wrap">
                            <button class="btn btn-secondary btn-sm" onclick="Pages._confirmPowerAction('${node.id}', 'pxe',  'Boot to PXE', 'Sets next boot to PXE and power-cycles the node.')">
                                <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24" style="width:13px;height:13px"><path d="M4 15s1-1 4-1 5 2 8 2 4-1 4-1V3s-1 1-4 1-5-2-8-2-4 1-4 1z"/><line x1="4" y1="22" x2="4" y2="15"/></svg>
                                PXE Boot
                            </button>
                            <button class="btn btn-secondary btn-sm" onclick="Pages._doPowerAction('${node.id}', 'disk')">
                                <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24" style="width:13px;height:13px"><ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3"/><path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5"/></svg>
                                Boot to Disk
                            </button>
                            <button class="btn btn-secondary btn-sm" onclick="Pages._doFlipToDisk('${node.id}')">
                                <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24" style="width:13px;height:13px"><ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3"/><path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5"/></svg>
                                Flip Next Boot → Disk
                            </button>
                            <button class="btn btn-danger btn-sm" onclick="Pages._doFlipToDisk('${node.id}', true)">
                                <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24" style="width:13px;height:13px"><polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/></svg>
                                Flip → Disk + Reboot
                            </button>
                        </div>
                        <div id="power-action-feedback" style="display:none;margin-top:10px" class="alert alert-info" role="status" aria-live="polite"></div>`,
                        ''
                    ) : (node.power_provider && node.power_provider.type ? cardWrap('Power Actions',
                        `<div class="flex gap-8">
                            <button class="btn btn-secondary btn-sm" onclick="Pages._doFlipToDisk('${node.id}')">Flip Next Boot → Disk</button>
                            <button class="btn btn-danger btn-sm" onclick="Pages._doFlipToDisk('${node.id}', true)">Flip → Disk + Reboot</button>
                        </div>
                        <div id="power-action-feedback" style="display:none;margin-top:10px" class="alert alert-info" role="status" aria-live="polite"></div>`, ''
                    ) : '')}

                    ${node.bmc && node.bmc.ip_address ? cardWrap('BMC Information',
                        `<div class="kv-grid">
                            <div class="kv-item"><div class="kv-key">IP Address</div><div class="kv-value text-mono">${escHtml(node.bmc.ip_address || '—')}</div></div>
                            <div class="kv-item"><div class="kv-key">Netmask</div><div class="kv-value text-mono">${escHtml(node.bmc.netmask || '—')}</div></div>
                            <div class="kv-item"><div class="kv-key">Gateway</div><div class="kv-value text-mono">${escHtml(node.bmc.gateway || '—')}</div></div>
                            <div class="kv-item"><div class="kv-key">Username</div><div class="kv-value text-mono">${escHtml(node.bmc.username || '—')}</div></div>
                        </div>`,
                        ''
                    ) : ''}

                    ${node.bmc && node.bmc.ip_address ? cardWrap('Sensor Readings',
                        `<div id="sensor-table-wrap"><div class="loading"><div class="spinner"></div>Loading sensors…</div></div>`,
                        `<button class="btn btn-secondary btn-sm" onclick="Pages._refreshSensors('${node.id}')">Refresh</button>`
                    ) : ''}
                    ` : ''}

                    <!-- Power Provider Configuration — always shown, inline editable -->
                    <div id="tab-save-bar-bmc" class="tab-save-bar" style="display:none">
                        <span class="save-status modified" id="tab-save-status-bmc">Unsaved changes</span>
                        <button class="btn btn-secondary btn-sm" onclick="Pages._tabRevert('bmc')" id="tab-revert-bmc">Revert</button>
                        <button class="btn btn-primary btn-sm" onclick="Pages._tabSavePower('${node.id}')" id="tab-save-bmc">Save</button>
                    </div>
                    ${cardWrap('Power Provider Configuration', `
                        <div class="form-grid">
                                <div class="form-group" style="grid-column:1/-1">
                                    <label>Provider Type</label>
                                    <select id="pp-type" onchange="Pages._onPowerProviderInlineTypeChange(this.value);Pages._tabMarkDirty('bmc')">
                                        <option value="" ${!node.power_provider || !node.power_provider.type ? 'selected' : ''}>None — no power management</option>
                                        <option value="ipmi" ${node.power_provider && node.power_provider.type === 'ipmi' ? 'selected' : ''}>IPMI (uses BMC config)</option>
                                        <option value="proxmox" ${node.power_provider && node.power_provider.type === 'proxmox' ? 'selected' : ''}>Proxmox VE</option>
                                    </select>
                                </div>
                            </div>
                            <div id="pp-inline-ipmi-fields" style="display:${node.power_provider && node.power_provider.type === 'ipmi' ? '' : 'none'}">
                                <div class="form-grid">
                                    <div class="form-group">
                                        <label>BMC IP Address</label>
                                        <input type="text" id="pp-ipmi-ip"
                                            value="${escHtml(node.power_provider && node.power_provider.fields ? node.power_provider.fields.ip || '' : '')}"
                                            placeholder="192.168.1.100" oninput="Pages._tabMarkDirty('bmc')">
                                    </div>
                                    <div class="form-group">
                                        <label>Username</label>
                                        <input type="text" id="pp-ipmi-username"
                                            value="${escHtml(node.power_provider && node.power_provider.fields ? node.power_provider.fields.username || '' : '')}"
                                            placeholder="admin" oninput="Pages._tabMarkDirty('bmc')">
                                    </div>
                                    <div class="form-group">
                                        <label>Password <span style="font-size:11px;color:var(--text-secondary)">(blank = keep existing)</span></label>
                                        <input type="password" id="pp-ipmi-password" placeholder="••••••••" oninput="Pages._tabMarkDirty('bmc')" autocomplete="new-password">
                                    </div>
                                    <div class="form-group">
                                        <label>Channel</label>
                                        <input type="number" id="pp-ipmi-channel"
                                            value="${escHtml(node.power_provider && node.power_provider.fields ? node.power_provider.fields.channel || '1' : '1')}"
                                            min="1" max="15" oninput="Pages._tabMarkDirty('bmc')">
                                    </div>
                                </div>
                            </div>
                            <div id="pp-inline-proxmox-fields" style="display:${node.power_provider && node.power_provider.type === 'proxmox' ? '' : 'none'}">
                                <div class="form-grid">
                                    <div class="form-group">
                                        <label>API URL</label>
                                        <input type="text" id="pp-pve-url"
                                            value="${escHtml(node.power_provider && node.power_provider.fields ? node.power_provider.fields.api_url || '' : '')}"
                                            placeholder="https://proxmox.example.com:8006" oninput="Pages._tabMarkDirty('bmc')">
                                    </div>
                                    <div class="form-group">
                                        <label>PVE Node Name</label>
                                        <input type="text" id="pp-pve-node"
                                            value="${escHtml(node.power_provider && node.power_provider.fields ? node.power_provider.fields.node || '' : '')}"
                                            placeholder="pve" oninput="Pages._tabMarkDirty('bmc')">
                                    </div>
                                    <div class="form-group">
                                        <label>VM ID</label>
                                        <input type="text" id="pp-pve-vmid"
                                            value="${escHtml(node.power_provider && node.power_provider.fields ? node.power_provider.fields.vmid || '' : '')}"
                                            placeholder="202" oninput="Pages._tabMarkDirty('bmc')">
                                    </div>
                                    <div class="form-group">
                                        <label>Username</label>
                                        <input type="text" id="pp-pve-username"
                                            value="${escHtml(node.power_provider && node.power_provider.fields ? node.power_provider.fields.username || '' : '')}"
                                            placeholder="root@pam" oninput="Pages._tabMarkDirty('bmc')">
                                    </div>
                                    <div class="form-group">
                                        <label>Password <span style="font-size:11px;color:var(--text-secondary)">(blank = keep existing)</span></label>
                                        <input type="password" id="pp-pve-password" placeholder="••••••••" oninput="Pages._tabMarkDirty('bmc')" autocomplete="new-password">
                                    </div>
                                    <div class="form-group">
                                        <label>TLS CA Cert Path <span style="font-size:11px;color:var(--text-secondary)">(optional)</span></label>
                                        <input type="text" id="pp-pve-ca-cert-path"
                                            value="${escHtml(node.power_provider && node.power_provider.fields ? node.power_provider.fields.tls_ca_cert_path || '' : '')}"
                                            placeholder="/etc/clustr/pki/proxmox-ca.pem" oninput="Pages._tabMarkDirty('bmc')">
                                    </div>
                                    <div class="form-group" style="display:flex;align-items:center;gap:8px;padding-top:22px">
                                        <input type="checkbox" id="pp-pve-insecure" ${node.power_provider && node.power_provider.fields && node.power_provider.fields.insecure === 'true' ? 'checked' : ''}
                                            onchange="Pages._tabMarkDirty('bmc')">
                                        <label for="pp-pve-insecure" style="margin:0;font-weight:400;cursor:pointer">Skip TLS verification (self-signed certs)</label>
                                    </div>
                                </div>
                            </div>`)}
                </div>

                <!-- Disk Layout tab — Richard's existing inline editor, untouched -->
                <div id="tab-disklayout" class="tab-panel">
                    <div id="disklayout-content">
                        <div class="loading"><div class="spinner"></div>Loading layout…</div>
                    </div>
                </div>

                <!-- Mounts tab — inline editable node-level mounts -->
                <div id="tab-mounts" class="tab-panel">
                    <div id="tab-save-bar-mounts" class="tab-save-bar" style="display:none">
                        <span class="save-status modified" id="tab-save-status-mounts">Unsaved changes</span>
                        <button class="btn btn-secondary btn-sm" onclick="Pages._tabRevert('mounts')" id="tab-revert-mounts">Revert</button>
                        <button class="btn btn-primary btn-sm" onclick="Pages._tabSaveMounts('${node.id}')" id="tab-save-mounts">Save</button>
                    </div>
                    <div id="mounts-content">
                        <div class="loading"><div class="spinner"></div>Loading mounts…</div>
                    </div>
                </div>

                <!-- Configuration tab — inline editable SSH keys, kernel args, custom vars -->
                <div id="tab-config" class="tab-panel">
                    <div id="tab-save-bar-config" class="tab-save-bar" style="display:none">
                        <span class="save-status modified" id="tab-save-status-config">Unsaved changes</span>
                        <button class="btn btn-secondary btn-sm" onclick="Pages._tabRevert('config')" id="tab-revert-config">Revert</button>
                        <button class="btn btn-primary btn-sm" onclick="Pages._tabSaveConfig('${node.id}')" id="tab-save-config">Save</button>
                    </div>
                    ${cardWrap('SSH Authorized Keys', `
                        <div class="form-group" style="margin-bottom:0">
                            <label>One key per line</label>
                            <textarea id="cfg-ssh-keys" rows="6"
                                placeholder="ssh-ed25519 AAAA…&#10;ssh-rsa AAAA…"
                                oninput="Pages._tabMarkDirty('config')"
                                style="font-family:var(--font-mono);font-size:12px">${escHtml((node.ssh_keys || []).join('\n'))}</textarea>
                            <div id="cfg-ssh-keys-error" style="display:none;color:var(--error);font-size:12px;margin-top:4px"></div>
                        </div>`)}

                    ${cardWrap('Kernel Arguments', `
                        <div class="form-group" style="margin-bottom:0">
                            <label>Extra kernel cmdline args appended at boot</label>
                            <input type="text" id="cfg-kernel-args" value="${escHtml(node.kernel_args || '')}"
                                placeholder="quiet splash"
                                oninput="Pages._tabMarkDirty('config')">
                            <div id="cfg-kernel-args-error" style="display:none;color:var(--error);font-size:12px;margin-top:4px"></div>
                        </div>`)}

                    ${cardWrap('Custom Variables', `
                        <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:10px">
                            <span class="text-dim" style="font-size:12px">
                                Key/value pairs available as template variables during deployment.
                                <!-- C3-15: "Supported variables" link -->
                                <a href="https://github.com/sqoia-dev/clustr/blob/main/docs/install.md#custom-variables" target="_blank" rel="noopener" style="color:var(--accent);margin-left:4px;">Supported variables</a>
                            </span>
                            <button type="button" class="btn btn-secondary btn-sm" onclick="Pages._cfgAddVar()">+ Add Variable</button>
                        </div>
                        <div id="cfg-vars-list">
                            ${Object.keys(node.custom_vars || {}).length === 0
                                ? `<div id="cfg-vars-empty" style="text-align:center;padding:12px;color:var(--text-dim);font-size:12px">No custom variables</div>`
                                : Object.entries(node.custom_vars || {}).map(([k, v], i) => Pages._cfgVarRowHTML(i, k, v)).join('')}
                        </div>`)}

                    ${cardWrap('Deployment Timing', `
                        <div class="form-group" style="margin-bottom:0">
                            <label>Verify-Boot Timeout Override
                                <span style="font-weight:400;font-size:12px;color:var(--text-secondary);margin-left:6px">(seconds; leave blank to use global default)</span>
                            </label>
                            <div style="display:flex;align-items:center;gap:10px">
                                <input type="number" id="cfg-verify-timeout" min="0" max="86400"
                                    value="${node.verify_timeout_override != null ? node.verify_timeout_override : ''}"
                                    placeholder="e.g. 600"
                                    oninput="Pages._tabMarkDirty('config')"
                                    style="width:140px">
                                <button type="button" class="btn btn-secondary btn-sm" id="cfg-verify-timeout-clear"
                                    style="${node.verify_timeout_override != null ? '' : 'display:none'}"
                                    onclick="Pages._cfgClearVerifyTimeout()">Use Global Default</button>
                            </div>
                            <div class="form-hint" style="margin-top:4px">
                                Overrides <code>CLUSTR_VERIFY_TIMEOUT</code> for this node only.
                                Set to <code>0</code> to disable verify-boot timeout entirely for this node.
                            </div>
                        </div>`)}

                    ${cardWrap('Raw JSON', `<pre class="json-block">${escHtml(JSON.stringify(node, null, 2))}</pre>`)}
                </div>

                <!-- Logs tab -->
                <div id="tab-logs" class="tab-panel">
                    ${cardWrap(`Logs — ${escHtml(node.hostname || node.primary_mac)}`, `
                        <div class="log-filter-bar" style="border:none;box-shadow:none;border-bottom:1px solid var(--border);border-radius:0;padding:12px 16px;display:flex;align-items:center;gap:16px;flex-wrap:wrap">
                            <div style="display:flex;align-items:center;gap:8px">
                                <span class="text-dim" style="font-size:12px;white-space:nowrap">Source:</span>
                                <select id="node-log-source" class="select" style="font-size:12px;padding:2px 6px;height:28px"
                                    onchange="Pages.onNodeLogSourceChange(this.value, '${escHtml(node.primary_mac)}')">
                                    <option value="deploy">Deploy Logs</option>
                                    <option value="journal">Live Journal</option>
                                </select>
                            </div>
                            <label class="toggle" id="node-follow-label">
                                <input type="checkbox" id="node-follow-toggle" onchange="Pages.toggleNodeLogs(this.checked, '${escHtml(node.primary_mac)}')">
                                Live
                            </label>
                            <span class="follow-indicator" id="node-follow-ind">
                                <span class="follow-dot"></span>static
                            </span>
                        </div>
                        <div id="node-log-viewer" class="log-viewer tall"></div>`,
                        `<button class="btn btn-secondary btn-sm" id="node-log-refresh" onclick="Pages.loadNodeLogs('${escHtml(node.primary_mac)}')">Refresh</button>`)}
                </div>

                <!-- Config Push tab -->
                ${(() => {
                    const nodeIsLive = node.last_seen_at && (Date.now() - new Date(node.last_seen_at).getTime()) < 2 * 60 * 1000;
                    return `<div id="tab-configpush" class="tab-panel">
                    ${cardWrap('Config Push', (() => {
                        if (!nodeIsLive) {
                            return emptyState('Node offline', 'Config push is only available when clustr-clientd is connected (Live).');
                        }
                        return `<p style="margin:0 0 12px;font-size:13px;color:var(--text-secondary)">
                                Push a whitelisted config file to this node atomically. The node validates the checksum,
                                backs up the existing file, writes the new content, and restarts the associated service if needed.
                            </p>
                            <div class="form-group" style="margin-bottom:12px">
                                <label class="form-label">Target</label>
                                <select id="configpush-target" class="select" style="width:200px">
                                    <option value="hosts">/etc/hosts</option>
                                    <option value="sssd">/etc/sssd/sssd.conf</option>
                                    <option value="chrony">/etc/chrony.conf</option>
                                    <option value="ntp">/etc/ntp.conf</option>
                                    <option value="resolv">/etc/resolv.conf</option>
                                </select>
                            </div>
                            <div class="form-group" style="margin-bottom:12px">
                                <label class="form-label">Content</label>
                                <textarea id="configpush-content" class="form-input" rows="12"
                                    style="font-family:monospace;font-size:12px;resize:vertical"
                                    placeholder="Paste file content here…"></textarea>
                            </div>
                            <div id="configpush-result" style="display:none;margin-bottom:12px" role="status" aria-live="polite"></div>`;
                    })(), `${nodeIsLive ? `<button class="btn btn-primary btn-sm" id="configpush-submit" onclick="Pages._doConfigPush('${escHtml(node.id)}')">Push</button>` : ''}`)}
                </div>`;
                })()}

                <!-- Slurm Role tab -->
                <div id="tab-slurm" class="tab-panel">
                    ${cardWrap('Slurm Role', `
                        <p style="margin:0 0 12px;font-size:13px;color:var(--text-secondary)">
                            Assign Slurm roles to this node. The role determines which config files
                            are deployed and which systemd services are enabled during imaging.
                        </p>
                        <div id="slurm-role-current" style="margin-bottom:12px;font-size:13px;color:var(--text-secondary)">
                            Loading current role&hellip;
                        </div>
                        <div style="display:flex;gap:16px;flex-wrap:wrap;margin-bottom:16px">
                            <label style="display:flex;align-items:center;gap:6px;font-size:13px;cursor:pointer">
                                <input type="checkbox" id="slurm-role-controller" value="controller"> Controller
                            </label>
                            <label style="display:flex;align-items:center;gap:6px;font-size:13px;cursor:pointer">
                                <input type="checkbox" id="slurm-role-compute" value="compute"> Compute
                            </label>
                            <label style="display:flex;align-items:center;gap:6px;font-size:13px;cursor:pointer">
                                <input type="checkbox" id="slurm-role-login" value="login"> Login
                            </label>
                            <label style="display:flex;align-items:center;gap:6px;font-size:13px;cursor:pointer">
                                <input type="checkbox" id="slurm-role-dbd" value="dbd"> DBD
                            </label>
                        </div>
                        <div id="slurm-role-save-row" style="display:flex;align-items:center;gap:10px">
                            <button class="btn btn-primary btn-sm" id="slurm-role-save-btn"
                                onclick="Pages._saveSlurmRole('${escHtml(node.id)}')">Save Role</button>
                            <span id="slurm-role-save-status" style="font-size:12px;color:var(--text-secondary)"></span>
                        </div>
                    `)}
                    ${cardWrap('Sync Status', `
                        <div id="slurm-node-sync-status">Loading…</div>
                        <div style="margin-top:12px;">
                            <button class="btn btn-primary btn-sm" onclick="Pages._pushToSingleNode('${escHtml(node.id)}')">Push to This Node</button>
                            <a href="#/slurm/sync" style="font-size:13px;color:var(--accent);margin-left:12px;">View all sync status</a>
                        </div>
                    `)}
                    ${cardWrap('Node Overrides', `
                        <p style="margin:0 0 12px;font-size:13px;color:var(--text-secondary)">
                            Override Slurm node parameters for this specific node. These values are injected
                            into the config template during rendering, overriding cluster defaults.
                        </p>
                        <div id="slurm-node-overrides">Loading…</div>
                    `)}
                </div>

                <!-- Diagnostics tab — only rendered when node is Live -->
                ${(() => {
                    const nodeIsLive = node.last_seen_at && (Date.now() - new Date(node.last_seen_at).getTime()) < 2 * 60 * 1000;
                    if (!nodeIsLive) return '';
                    const quickCmds = [
                        { label: 'System Info',   cmd: 'uname',      args: ['-a'] },
                        { label: 'Disk Usage',    cmd: 'df',         args: ['-h'] },
                        { label: 'Memory',        cmd: 'free',       args: ['-m'] },
                        { label: 'CPU Info',      cmd: 'lscpu',      args: [] },
                        { label: 'IP Config',     cmd: 'ip',         args: ['addr', 'show'] },
                        { label: 'Processes',     cmd: 'ps',         args: ['aux'] },
                        { label: 'Slurm Status',  cmd: 'sinfo',      args: ['-N', '-l'] },
                        { label: 'Job Queue',     cmd: 'squeue',     args: ['-l'] },
                        { label: 'SSSD Status',   cmd: 'systemctl',  args: ['status', 'sssd'] },
                        { label: 'Slurm Conf',    cmd: 'cat',        args: ['/etc/slurm/slurm.conf'] },
                    ];
                    const whitelistedCmds = [
                        'journalctl','systemctl','df','free','uptime','ip','cat',
                        'ping','sinfo','squeue','scontrol','hostname','uname',
                        'whoami','id','ps','top','lscpu','lsblk','mount',
                    ];
                    return `<div id="tab-diagnostics" class="tab-panel">
                    ${cardWrap('Diagnostics', `
                        <p style="margin:0 0 16px;font-size:13px;color:var(--text-secondary)">
                            Run read-only diagnostic commands on this node via clustr-clientd.
                            Only whitelisted commands are permitted — no shell, no pipes.
                        </p>

                            <div style="margin-bottom:20px">
                                <div style="font-size:12px;font-weight:600;text-transform:uppercase;letter-spacing:.05em;color:var(--text-dim);margin-bottom:8px">Quick Commands</div>
                                <div style="display:flex;flex-wrap:wrap;gap:6px">
                                    ${quickCmds.map(q =>
                                        `<button class="btn btn-secondary btn-sm"
                                            onclick="Pages._runDiagnosticCmd('${escHtml(node.id)}', '${escHtml(q.cmd)}', ${JSON.stringify(q.args)})"
                                        >${escHtml(q.label)}</button>`
                                    ).join('')}
                                </div>
                            </div>

                            <div style="margin-bottom:16px">
                                <div style="font-size:12px;font-weight:600;text-transform:uppercase;letter-spacing:.05em;color:var(--text-dim);margin-bottom:8px">Custom Command</div>
                                <div style="display:flex;gap:8px;flex-wrap:wrap;align-items:flex-end">
                                    <div class="form-group" style="margin:0;flex:0 0 160px">
                                        <label class="form-label" style="font-size:12px">Command</label>
                                        <select id="diag-cmd-select" class="select" style="font-size:12px">
                                            ${whitelistedCmds.map(c => `<option value="${escHtml(c)}">${escHtml(c)}</option>`).join('')}
                                        </select>
                                    </div>
                                    <div class="form-group" style="margin:0;flex:1 1 200px">
                                        <label class="form-label" style="font-size:12px">Arguments <span style="font-weight:400;color:var(--text-dim)">(space-separated)</span></label>
                                        <input type="text" id="diag-args-input" class="form-input" style="font-size:12px;font-family:monospace"
                                            placeholder="-h" onkeydown="if(event.key==='Enter')Pages._runDiagnosticCmdCustom('${escHtml(node.id)}')">
                                    </div>
                                    <div style="padding-bottom:0">
                                        <button class="btn btn-primary btn-sm" id="diag-run-btn"
                                            onclick="Pages._runDiagnosticCmdCustom('${escHtml(node.id)}')">Run</button>
                                    </div>
                                </div>
                            </div>

                            <div id="diag-result-wrap" style="display:none">
                                <div style="display:flex;align-items:center;gap:12px;margin-bottom:6px;font-size:12px;color:var(--text-secondary)">
                                    <span id="diag-result-cmd" style="font-family:monospace;font-weight:600"></span>
                                    <span id="diag-exit-badge"></span>
                                    <span id="diag-truncated-badge" style="display:none" class="badge badge-warning">Output truncated at 64 KB</span>
                                </div>
                                <div id="diag-stdout-wrap" style="margin-bottom:8px">
                                    <div style="font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:.05em;color:var(--text-dim);margin-bottom:4px">stdout</div>
                                    <pre id="diag-stdout" style="background:var(--bg-secondary);border:1px solid var(--border);border-radius:4px;padding:10px;font-size:12px;overflow:auto;max-height:400px;white-space:pre-wrap;word-break:break-all;margin:0"></pre>
                                </div>
                                <div id="diag-stderr-wrap" style="display:none">
                                    <div style="font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:.05em;color:var(--text-dim);margin-bottom:4px">stderr</div>
                                    <pre id="diag-stderr" style="background:#fef9ec;border:1px solid #f0c040;border-radius:4px;padding:10px;font-size:12px;overflow:auto;max-height:200px;white-space:pre-wrap;word-break:break-all;margin:0"></pre>
                                </div>
                                <div id="diag-error-wrap" style="display:none" class="alert alert-error" style="margin-top:8px"></div>
                            </div>
                            <div id="diag-running" style="display:none;font-size:13px;color:var(--text-secondary);padding:8px 0">
                                <span class="spinner" style="width:14px;height:14px;display:inline-block;margin-right:6px"></span>Running&hellip; (up to 30s)
                            </div>
                    `)}
                </div>`;
                })()}

                <!-- S5-12: Config History tab — append-only audit log of node config field changes -->
                <div id="tab-confighistory" class="tab-panel">
                    ${cardWrap('Configuration Change History',
                        `<div id="confighistory-wrap">${loading('Loading history…')}</div>`,
                        '')}
                </div>
            `);

            // Store original values for revert on each editable tab.
            Pages._nodeEditorState['overview'] = {
                dirty: false,
                original: {
                    hostname:      node.hostname || '',
                    fqdn:          node.fqdn || '',
                    base_image_id: node.base_image_id || '',
                    group_id:      node.group_id || '',
                    tags:          (node.tags || node.groups || []).join(', '),
                },
            };
            Pages._nodeEditorState['bmc'] = {
                dirty: false,
                original: {
                    pp_type:          (node.power_provider && node.power_provider.type) || '',
                    pp_ipmi_ip:       (node.power_provider && node.power_provider.fields && node.power_provider.fields.ip) || '',
                    pp_ipmi_username: (node.power_provider && node.power_provider.fields && node.power_provider.fields.username) || '',
                    pp_ipmi_channel:  (node.power_provider && node.power_provider.fields && node.power_provider.fields.channel) || '1',
                    pp_pve_url:       (node.power_provider && node.power_provider.fields && node.power_provider.fields.api_url) || '',
                    pp_pve_node:      (node.power_provider && node.power_provider.fields && node.power_provider.fields.node) || '',
                    pp_pve_vmid:      (node.power_provider && node.power_provider.fields && node.power_provider.fields.vmid) || '',
                    pp_pve_username:  (node.power_provider && node.power_provider.fields && node.power_provider.fields.username) || '',
                    pp_pve_insecure:  !!(node.power_provider && node.power_provider.fields && node.power_provider.fields.insecure === 'true'),
                },
            };
            Pages._nodeEditorState['config'] = {
                dirty: false,
                original: {
                    ssh_keys:    (node.ssh_keys || []).join('\n'),
                    kernel_args: node.kernel_args || '',
                    custom_vars: Object.assign({}, node.custom_vars || {}),
                },
            };
            Pages._nodeEditorState['network'] = {
                dirty: false,
                original: {
                    interfaces: JSON.parse(JSON.stringify(node.interfaces || [])),
                },
            };
            Pages._nodeEditorState['mounts'] = {
                dirty: false,
                original: {
                    extra_mounts: JSON.parse(JSON.stringify(node.extra_mounts || [])),
                },
            };

            // Kick off initial power status fetch if any power management is configured.
            if ((node.bmc && node.bmc.ip_address) || (node.power_provider && node.power_provider.type)) {
                Pages._refreshPowerStatus(node.id);
            }

            // Close actions dropdown when clicking outside.
            document.addEventListener('click', Pages._closeActionsDropdownOnOutsideClick);

            // C3-11: navigation guard — warn on hash-change if any tab is dirty.
            Router._navigationGuard = () => {
                const dirtyTabs = Object.entries(Pages._nodeEditorState)
                    .filter(([, s]) => s.dirty)
                    .map(([tab]) => tab);
                if (dirtyTabs.length === 0) return Promise.resolve(true);
                return new Promise(resolve => {
                    Pages.showConfirmModal({
                        title: 'Unsaved Changes',
                        message: `You have unsaved changes on the <strong>${escHtml(dirtyTabs.join(', '))}</strong> tab(s). Leave without saving?`,
                        confirmText: 'Leave',
                        cancelText: 'Stay',
                        danger: false,
                        onConfirm: () => resolve(true),
                        onCancel:  () => resolve(false),
                    });
                });
            };

        } catch (e) {
            App.render(alertBox(`Failed to load node: ${e.message}`));
        }
    },

    // _closeActionsDropdownOnOutsideClick closes the actions dropdown when the user
    // clicks anywhere outside it. Bound as a document listener, removed on navigate.
    _closeActionsDropdownOnOutsideClick(e) {
        const dropdown = document.getElementById('node-actions-dropdown');
        if (dropdown && !dropdown.contains(e.target)) {
            const menu = document.getElementById('node-actions-menu');
            if (menu) menu.classList.remove('open');
        }
    },

    _toggleActionsDropdown() {
        const menu = document.getElementById('node-actions-menu');
        if (!menu) return;
        const isOpen = menu.classList.toggle('open');
        if (isOpen) {
            // Move focus to the first item when opened via keyboard.
            const first = menu.querySelector('.actions-dropdown-item:not([disabled])');
            if (first) setTimeout(() => first.focus(), 10);
            // Arrow key navigation within the dropdown.
            menu._dropdownKeyHandler = (e) => {
                const items = Array.from(menu.querySelectorAll('.actions-dropdown-item:not([disabled])'));
                const idx = items.indexOf(document.activeElement);
                if (e.key === 'ArrowDown') {
                    e.preventDefault();
                    const next = items[idx + 1] || items[0];
                    if (next) next.focus();
                } else if (e.key === 'ArrowUp') {
                    e.preventDefault();
                    const prev = items[idx - 1] || items[items.length - 1];
                    if (prev) prev.focus();
                } else if (e.key === 'Escape') {
                    e.preventDefault();
                    menu.classList.remove('open');
                    const btn = document.querySelector('#node-actions-dropdown > button');
                    if (btn) btn.focus();
                }
            };
            menu.addEventListener('keydown', menu._dropdownKeyHandler);
        } else if (menu._dropdownKeyHandler) {
            menu.removeEventListener('keydown', menu._dropdownKeyHandler);
            menu._dropdownKeyHandler = null;
        }
    },

    // _nodeDetailBack navigates back to /nodes, prompting if there are unsaved changes.
    _nodeDetailBack() {
        const dirtyTabs = Object.entries(Pages._nodeEditorState)
            .filter(([, s]) => s.dirty)
            .map(([tab]) => tab);
        if (dirtyTabs.length === 0) {
            Router.navigate('/nodes');
            return;
        }
        Pages.showConfirmModal({
            title: 'Unsaved Changes',
            message: `You have unsaved changes on the <strong>${escHtml(dirtyTabs.join(', '))}</strong> tab(s). Leave without saving?`,
            confirmText: 'Leave',
            cancelText: 'Stay',
            danger: false,
            onConfirm: () => Router.navigate('/nodes'),
        });
    },

    // _switchNodeTab handles tab switching with unsaved-changes protection.
    _switchNodeTab(tabEl, panelId, tabKey) {
        const currentTabKey = Pages._nodeCurrentTab || 'overview';

        // Check if current tab is dirty.
        const currentState = Pages._nodeEditorState[currentTabKey];
        if (currentState && currentState.dirty) {
            // Show unsaved-changes dialog.
            Pages._showUnsavedChangesDialog(currentTabKey, () => {
                // Discard and continue.
                Pages._tabRevert(currentTabKey);
                Pages._doSwitchNodeTab(tabEl, panelId, tabKey);
            }, async () => {
                // Save and continue.
                const saved = await Pages._tabSaveByKey(currentTabKey, Pages._nodeEditorNodeId);
                if (saved) Pages._doSwitchNodeTab(tabEl, panelId, tabKey);
            });
            return;
        }

        Pages._doSwitchNodeTab(tabEl, panelId, tabKey);
    },

    _doSwitchNodeTab(tabEl, panelId, tabKey) {
        document.querySelectorAll('#node-tab-bar .tab').forEach(t => t.classList.remove('active'));
        document.querySelectorAll('.tab-panel').forEach(p => p.classList.remove('active'));
        tabEl.classList.add('active');
        const panel = document.getElementById(panelId);
        if (panel) panel.classList.add('active');
        Pages._nodeCurrentTab = tabKey;
    },

    // _switchTab is kept for non-node pages (image detail uses it via tab-bar).
    _switchTab(tabEl, panelId) {
        tabEl.closest('.tab-bar').querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
        document.querySelectorAll('.tab-panel').forEach(p => p.classList.remove('active'));
        tabEl.classList.add('active');
        const panel = document.getElementById(panelId);
        if (panel) panel.classList.add('active');
    },

    // _showUnsavedChangesDialog shows a confirm dialog for unsaved changes protection.
    // onDiscard — called when user clicks "Discard and continue"
    // onSaveAndContinue — called when user clicks "Save and continue"
    _showUnsavedChangesDialog(tabName, onDiscard, onSaveAndContinue) {
        const existing = document.getElementById('unsaved-changes-modal');
        if (existing) existing.remove();

        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        overlay.id = 'unsaved-changes-modal';
        overlay.innerHTML = `
            <div class="modal" style="max-width:440px" aria-labelledby="modal-title-7">
                <div class="modal-header">
                    <span class="modal-title" id="modal-title-7">Unsaved Changes</span>
                </div>
                <div class="modal-body">
                    <p style="margin:0 0 16px;color:var(--text-secondary);font-size:13px">
                        You have unsaved changes on the <strong>${escHtml(tabName)}</strong> tab.
                    </p>
                    <div class="form-actions" style="margin-top:0">
                        <button class="btn btn-secondary" id="ucd-cancel">Cancel</button>
                        <button class="btn btn-secondary" id="ucd-discard">Discard and continue</button>
                        <button class="btn btn-primary" id="ucd-save">Save and continue</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(overlay);
        trapModalFocus(overlay, () => overlay.remove());

        overlay.querySelector('#ucd-cancel').onclick  = () => overlay.remove();
        overlay.querySelector('#ucd-discard').onclick = () => { overlay.remove(); onDiscard(); };
        overlay.querySelector('#ucd-save').onclick    = () => { overlay.remove(); onSaveAndContinue(); };
    },

    // ── Tab dirty state tracking ───────────────────────────────────────────────

    _tabMarkDirty(tabKey) {
        const state = Pages._nodeEditorState[tabKey];
        if (!state) return;
        state.dirty = true;

        const saveBar    = document.getElementById(`tab-save-bar-${tabKey}`);
        const tabBtnEl   = document.getElementById(`node-tab-btn-${tabKey}`);
        if (saveBar)  saveBar.style.display = '';
        if (tabBtnEl) tabBtnEl.classList.add('tab-dirty');
    },

    _tabMarkClean(tabKey) {
        const state = Pages._nodeEditorState[tabKey];
        if (state) state.dirty = false;

        const saveBar    = document.getElementById(`tab-save-bar-${tabKey}`);
        const statusEl   = document.getElementById(`tab-save-status-${tabKey}`);
        const tabBtnEl   = document.getElementById(`node-tab-btn-${tabKey}`);
        if (saveBar)  saveBar.style.display = 'none';
        if (statusEl) { statusEl.textContent = 'Saved'; statusEl.className = 'save-status saved'; }
        if (tabBtnEl) tabBtnEl.classList.remove('tab-dirty');
        // B4-2: re-enable all save buttons after any save completes successfully.
        document.querySelectorAll('[id^="tab-save-"]').forEach(btn => { btn.disabled = false; });
    },

    _tabMarkSaving(tabKey) {
        const statusEl = document.getElementById(`tab-save-status-${tabKey}`);
        const saveBtn  = document.getElementById(`tab-save-${tabKey}`);
        if (statusEl) { statusEl.textContent = 'Saving…'; statusEl.className = 'save-status'; }
        // B4-2: disable ALL save buttons while any save is in flight to prevent GET-then-PUT race.
        document.querySelectorAll('[id^="tab-save-"]').forEach(btn => { btn.disabled = true; });
    },

    _tabMarkError(tabKey, msg) {
        const statusEl = document.getElementById(`tab-save-status-${tabKey}`);
        const saveBtn  = document.getElementById(`tab-save-${tabKey}`);
        if (statusEl) { statusEl.textContent = msg; statusEl.className = 'save-status error'; }
        // B4-2: re-enable all save buttons when save fails.
        document.querySelectorAll('[id^="tab-save-"]').forEach(btn => { btn.disabled = false; });
    },

    // _tabRevert resets the tab inputs back to original values without saving.
    _tabRevert(tabKey) {
        const state = Pages._nodeEditorState[tabKey];
        if (!state) return;
        const orig = state.original;

        if (tabKey === 'overview') {
            const h   = document.getElementById('ov-hostname');
            const f   = document.getElementById('ov-fqdn');
            const img = document.getElementById('ov-base-image');
            const grp = document.getElementById('ov-group-id');
            const gs  = document.getElementById('ov-tags');
            if (h)   h.value   = orig.hostname;
            if (f)   f.value   = orig.fqdn;
            if (img) img.value = orig.base_image_id;
            if (grp) grp.value = orig.group_id;
            if (gs)  gs.value  = orig.tags;
        } else if (tabKey === 'bmc') {
            const pt = document.getElementById('pp-type');
            if (pt) { pt.value = orig.pp_type; Pages._onPowerProviderInlineTypeChange(orig.pp_type); }
            const ipIp  = document.getElementById('pp-ipmi-ip');
            const ipUsr = document.getElementById('pp-ipmi-username');
            const ipCh  = document.getElementById('pp-ipmi-channel');
            if (ipIp)  ipIp.value  = orig.pp_ipmi_ip;
            if (ipUsr) ipUsr.value = orig.pp_ipmi_username;
            if (ipCh)  ipCh.value  = orig.pp_ipmi_channel;
            const pveUrl  = document.getElementById('pp-pve-url');
            const pveNode = document.getElementById('pp-pve-node');
            const pveVmid = document.getElementById('pp-pve-vmid');
            const pveUsr  = document.getElementById('pp-pve-username');
            const pveIns  = document.getElementById('pp-pve-insecure');
            if (pveUrl)  pveUrl.value   = orig.pp_pve_url;
            if (pveNode) pveNode.value  = orig.pp_pve_node;
            if (pveVmid) pveVmid.value  = orig.pp_pve_vmid;
            if (pveUsr)  pveUsr.value   = orig.pp_pve_username;
            if (pveIns)  pveIns.checked = orig.pp_pve_insecure;
        } else if (tabKey === 'config') {
            const keys = document.getElementById('cfg-ssh-keys');
            const krnl = document.getElementById('cfg-kernel-args');
            if (keys) keys.value = orig.ssh_keys;
            if (krnl) krnl.value = orig.kernel_args;
            // Re-render the custom vars list.
            const list = document.getElementById('cfg-vars-list');
            if (list) {
                if (Object.keys(orig.custom_vars).length === 0) {
                    list.innerHTML = `<div id="cfg-vars-empty" style="text-align:center;padding:12px;color:var(--text-dim);font-size:12px">No custom variables</div>`;
                } else {
                    list.innerHTML = Object.entries(orig.custom_vars).map(([k, v], i) => Pages._cfgVarRowHTML(i, k, v)).join('');
                }
            }
        } else if (tabKey === 'network') {
            const list = document.getElementById('net-interfaces-list');
            if (list) {
                if (orig.interfaces.length === 0) {
                    list.innerHTML = `<div id="net-empty" style="text-align:center;padding:16px;color:var(--text-dim);font-size:12px">No interfaces configured</div>`;
                } else {
                    list.innerHTML = orig.interfaces.map((iface, i) => Pages._netInterfaceRowHTML(i, iface, [])).join('');
                }
            }
        } else if (tabKey === 'mounts') {
            const tbody = document.getElementById('mounts-node-tbody');
            if (tbody) {
                tbody.innerHTML = orig.extra_mounts.map((m, i) => Pages._mountsNodeRowHTML(i, m)).join('');
                Pages._mountsUpdateEmpty();
            }
        }

        Pages._tabMarkClean(tabKey);
    },

    // _tabSaveByKey is a dispatch helper used by the unsaved-changes dialog.
    // Returns true on success, false on failure.
    async _tabSaveByKey(tabKey, nodeId) {
        try {
            if (tabKey === 'overview') await Pages._tabSaveOverview(nodeId);
            else if (tabKey === 'bmc')    await Pages._tabSavePower(nodeId);
            else if (tabKey === 'config') await Pages._tabSaveConfig(nodeId);
            else if (tabKey === 'network') await Pages._tabSaveNetwork(nodeId);
            else if (tabKey === 'mounts') await Pages._tabSaveMounts(nodeId);
            return true;
        } catch (_) {
            return false;
        }
    },

    // ── Per-tab save handlers ──────────────────────────────────────────────────

    async _tabSaveOverview(nodeId) {
        Pages._tabMarkSaving('overview');
        const saveBtn = document.getElementById('tab-save-overview');

        const hostname    = (document.getElementById('ov-hostname')?.value || '').trim();
        const fqdn        = (document.getElementById('ov-fqdn')?.value || '').trim();
        const baseImageId = document.getElementById('ov-base-image')?.value || '';
        const groupId     = document.getElementById('ov-group-id')?.value || '';
        const tagsRaw     = (document.getElementById('ov-tags')?.value || '');

        // Validate hostname.
        if (hostname && !/^[a-zA-Z0-9][a-zA-Z0-9.-]*$/.test(hostname)) {
            Pages._tabMarkError('overview', 'Invalid hostname format');
            if (saveBtn) saveBtn.disabled = false;
            return;
        }

        const tags = tagsRaw.split(',').map(g => g.trim()).filter(Boolean);

        try {
            // Fetch current node to get all fields we're not changing (the API requires full body).
            const existing = await API.nodes.get(nodeId);
            const body = {
                hostname:        hostname || existing.hostname,
                fqdn,
                primary_mac:     existing.primary_mac,
                base_image_id:   baseImageId,
                group_id:        groupId,
                tags,
                groups:          tags, // backward-compat alias
                ssh_keys:        existing.ssh_keys || [],
                kernel_args:     existing.kernel_args || '',
                custom_vars:     existing.custom_vars || {},
                interfaces:      existing.interfaces || [],
                power_provider:  existing.power_provider || null,
                extra_mounts:    existing.extra_mounts || [],
                disk_layout_override: existing.disk_layout_override || null,
            };
            await API.nodes.update(nodeId, body);

            // Update original state so subsequent reverts work correctly.
            Pages._nodeEditorState['overview'].original = {
                hostname, fqdn, base_image_id: baseImageId, group_id: groupId, tags: tagsRaw,
            };
            Pages._tabMarkClean('overview');

            // Update the page title if hostname changed.
            const titleEl = document.querySelector('.page-title');
            if (titleEl && hostname) titleEl.textContent = hostname;
        } catch (e) {
            Pages._tabMarkError('overview', `Save failed: ${e.message}`);
            if (saveBtn) saveBtn.disabled = false;
        }
    },

    async _tabSavePower(nodeId) {
        Pages._tabMarkSaving('bmc');
        const saveBtn = document.getElementById('tab-save-bmc');

        const ppType = document.getElementById('pp-type')?.value || '';
        let powerProvider = null;

        if (ppType === 'ipmi') {
            const fields = {
                ip:       (document.getElementById('pp-ipmi-ip')?.value || '').trim(),
                username: (document.getElementById('pp-ipmi-username')?.value || '').trim(),
                channel:  (document.getElementById('pp-ipmi-channel')?.value || '1').trim(),
            };
            const pw = document.getElementById('pp-ipmi-password')?.value || '';
            if (pw) fields.password = pw;
            powerProvider = { type: 'ipmi', fields };
        } else if (ppType === 'proxmox') {
            const insecureEl = document.getElementById('pp-pve-insecure');
            const fields = {
                api_url:          (document.getElementById('pp-pve-url')?.value || '').trim(),
                node:             (document.getElementById('pp-pve-node')?.value || '').trim(),
                vmid:             (document.getElementById('pp-pve-vmid')?.value || '').trim(),
                username:         (document.getElementById('pp-pve-username')?.value || '').trim(),
                tls_ca_cert_path: (document.getElementById('pp-pve-ca-cert-path')?.value || '').trim(),
                insecure:         (insecureEl && insecureEl.checked) ? 'true' : 'false',
            };
            const pw = document.getElementById('pp-pve-password')?.value || '';
            if (pw) fields.password = pw;
            powerProvider = { type: 'proxmox', fields };
        }

        try {
            const existing = await API.nodes.get(nodeId);
            const body = {
                hostname:        existing.hostname,
                primary_mac:     existing.primary_mac,
                fqdn:            existing.fqdn || '',
                base_image_id:   existing.base_image_id || '',
                group_id:        existing.group_id || '',
                groups:          existing.groups || [],
                ssh_keys:        existing.ssh_keys || [],
                kernel_args:     existing.kernel_args || '',
                custom_vars:     existing.custom_vars || {},
                interfaces:      existing.interfaces || [],
                power_provider:  powerProvider,
                extra_mounts:    existing.extra_mounts || [],
                disk_layout_override: existing.disk_layout_override || null,
            };
            await API.nodes.update(nodeId, body);

            // Clear the password inputs so they don't re-submit old values.
            const pwEl1 = document.getElementById('pp-ipmi-password');
            const pwEl2 = document.getElementById('pp-pve-password');
            if (pwEl1) pwEl1.value = '';
            if (pwEl2) pwEl2.value = '';

            Pages._nodeEditorState['bmc'].original.pp_type = ppType;
            Pages._tabMarkClean('bmc');

            // Re-fetch power status after provider change.
            if (powerProvider) setTimeout(() => Pages._refreshPowerStatus(nodeId), 500);
        } catch (e) {
            Pages._tabMarkError('bmc', `Save failed: ${e.message}`);
            if (saveBtn) saveBtn.disabled = false;
        }
    },

    async _tabSaveConfig(nodeId) {
        Pages._tabMarkSaving('config');
        const saveBtn = document.getElementById('tab-save-config');

        const sshKeysRaw  = document.getElementById('cfg-ssh-keys')?.value || '';
        const kernelArgs  = (document.getElementById('cfg-kernel-args')?.value || '').trim();
        const keysErrEl   = document.getElementById('cfg-ssh-keys-error');
        const krnlErrEl   = document.getElementById('cfg-kernel-args-error');

        if (keysErrEl) keysErrEl.style.display = 'none';
        if (krnlErrEl) krnlErrEl.style.display = 'none';

        // Validate SSH keys.
        const sshKeys = sshKeysRaw.split('\n').map(k => k.trim()).filter(Boolean);
        const invalidKeys = sshKeys.filter(k => !/^(ssh-rsa|ssh-ed25519|ecdsa-sha2-)/.test(k));
        if (invalidKeys.length) {
            if (keysErrEl) { keysErrEl.textContent = `Invalid key format: ${invalidKeys[0].substring(0, 40)}…`; keysErrEl.style.display = ''; }
            Pages._tabMarkError('config', 'Validation error');
            if (saveBtn) saveBtn.disabled = false;
            return;
        }

        // Validate kernel args — no shell metacharacters.
        if (/[;|`$()]/.test(kernelArgs)) {
            if (krnlErrEl) { krnlErrEl.textContent = 'Kernel args must not contain shell metacharacters (; | ` $ ())'; krnlErrEl.style.display = ''; }
            Pages._tabMarkError('config', 'Validation error');
            if (saveBtn) saveBtn.disabled = false;
            return;
        }

        // Collect custom vars from the editor rows.
        const customVars = {};
        document.querySelectorAll('#cfg-vars-list .cfg-var-row').forEach(row => {
            const k = (row.querySelector('.cfg-var-key')?.value || '').trim();
            const v = (row.querySelector('.cfg-var-val')?.value || '').trim();
            if (k) customVars[k] = v;
        });

        // C3-18: collect verify timeout override (null = use global default).
        const vtoEl = document.getElementById('cfg-verify-timeout');
        const vtoRaw = vtoEl ? vtoEl.value.trim() : '';
        let verifyTimeoutOverride = null;
        let clearVerifyTimeoutOverride = false;
        if (vtoRaw === '') {
            // If the input is blank and there WAS an override before, clear it.
            // We signal this via clear_verify_timeout_override.
            clearVerifyTimeoutOverride = true;
        } else {
            const parsed = parseInt(vtoRaw, 10);
            if (!isNaN(parsed) && parsed >= 0) {
                verifyTimeoutOverride = parsed;
            }
        }

        try {
            const existing = await API.nodes.get(nodeId);
            const body = {
                hostname:        existing.hostname,
                primary_mac:     existing.primary_mac,
                fqdn:            existing.fqdn || '',
                base_image_id:   existing.base_image_id || '',
                group_id:        existing.group_id || '',
                groups:          existing.groups || [],
                ssh_keys:        sshKeys,
                kernel_args:     kernelArgs,
                custom_vars:     customVars,
                interfaces:      existing.interfaces || [],
                power_provider:  existing.power_provider || null,
                extra_mounts:    existing.extra_mounts || [],
                disk_layout_override: existing.disk_layout_override || null,
                verify_timeout_override: verifyTimeoutOverride,
                clear_verify_timeout_override: clearVerifyTimeoutOverride,
            };
            await API.nodes.update(nodeId, body);

            Pages._nodeEditorState['config'].original = {
                ssh_keys:    sshKeys.join('\n'),
                kernel_args: kernelArgs,
                custom_vars: Object.assign({}, customVars),
                verify_timeout_override: verifyTimeoutOverride,
            };
            Pages._tabMarkClean('config');
        } catch (e) {
            Pages._tabMarkError('config', `Save failed: ${e.message}`);
            if (saveBtn) saveBtn.disabled = false;
        }
    },

    async _tabSaveNetwork(nodeId) {
        Pages._tabMarkSaving('network');
        const saveBtn = document.getElementById('tab-save-network');

        // C3-16: CIDR validation helper — accepts "a.b.c.d/prefix" or empty.
        const validCIDR = (v) => !v || /^(\d{1,3}\.){3}\d{1,3}\/\d{1,2}$/.test(v);
        const validIP   = (v) => !v || /^(\d{1,3}\.){3}\d{1,3}$/.test(v);

        // Collect interface rows.
        const interfaces = [];
        let validationError = '';
        document.querySelectorAll('#net-interfaces-list .net-iface-row').forEach((row, i) => {
            const mac  = row.querySelector('.net-iface-mac')?.value.trim() || '';
            const name = row.querySelector('.net-iface-name')?.value.trim() || '';
            const ip   = row.querySelector('.net-iface-ip')?.value.trim() || '';
            const gw   = row.querySelector('.net-iface-gw')?.value.trim() || '';
            const dns  = (row.querySelector('.net-iface-dns')?.value || '').split(',').map(s => s.trim()).filter(Boolean);
            const mtu  = parseInt(row.querySelector('.net-iface-mtu')?.value || '0', 10) || 0;
            const bond = row.querySelector('.net-iface-bond')?.value.trim() || '';
            // C3-16: validate IP/CIDR format before saving.
            if (ip && !validCIDR(ip)) {
                validationError = `Interface ${i + 1}: IP Address must be in CIDR notation (e.g. 192.168.1.50/24), got: ${ip}`;
            }
            if (gw && !validIP(gw)) {
                validationError = validationError || `Interface ${i + 1}: Gateway must be a bare IP address (e.g. 192.168.1.1), got: ${gw}`;
            }
            if (mac || name || ip) {
                interfaces.push({ mac_address: mac, name, ip_address: ip, gateway: gw, dns, mtu: mtu || undefined, bond: bond || undefined });
            }
        });

        if (validationError) {
            Pages._tabMarkError('network', validationError);
            if (saveBtn) saveBtn.disabled = false;
            return;
        }

        try {
            const existing = await API.nodes.get(nodeId);
            const body = {
                hostname:        existing.hostname,
                primary_mac:     existing.primary_mac,
                fqdn:            existing.fqdn || '',
                base_image_id:   existing.base_image_id || '',
                group_id:        existing.group_id || '',
                groups:          existing.groups || [],
                ssh_keys:        existing.ssh_keys || [],
                kernel_args:     existing.kernel_args || '',
                custom_vars:     existing.custom_vars || {},
                interfaces,
                power_provider:  existing.power_provider || null,
                extra_mounts:    existing.extra_mounts || [],
                disk_layout_override: existing.disk_layout_override || null,
            };
            await API.nodes.update(nodeId, body);

            Pages._nodeEditorState['network'].original = {
                interfaces: JSON.parse(JSON.stringify(interfaces)),
            };
            Pages._tabMarkClean('network');
        } catch (e) {
            Pages._tabMarkError('network', `Save failed: ${e.message}`);
            if (saveBtn) saveBtn.disabled = false;
        }
    },

    // ── Network tab helpers ───────────────────────────────────────────────────

    _netInterfaceRowHTML(idx, iface, discoveredMACs) {
        iface = iface || {};
        const macOptions = (discoveredMACs || [])
            .map(m => `<option value="${escHtml(m)}" ${iface.mac_address === m ? 'selected' : ''}>${escHtml(m)}</option>`)
            .join('');
        return `<div class="net-iface-row" data-idx="${idx}" style="border:1px solid var(--border);border-radius:6px;padding:12px;margin-bottom:10px">
            <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:10px">
                <span style="font-size:12px;font-weight:600;color:var(--text-secondary)">Interface ${idx + 1}</span>
                <button type="button" class="btn btn-danger btn-sm" style="padding:2px 8px;font-size:11px"
                    onclick="Pages._netRemoveInterface(this);Pages._tabMarkDirty('network')">Remove</button>
            </div>
            <div class="form-grid" style="margin-bottom:0">
                <div class="form-group">
                    <label>MAC Address</label>
                    ${discoveredMACs && discoveredMACs.length
                        ? `<select class="net-iface-mac" onchange="Pages._tabMarkDirty('network')">
                                <option value="">Custom / none</option>
                                ${macOptions}
                            </select>`
                        : `<input type="text" class="net-iface-mac" value="${escHtml(iface.mac_address || '')}"
                                placeholder="aa:bb:cc:dd:ee:ff" oninput="Pages._tabMarkDirty('network')">`
                    }
                </div>
                <div class="form-group">
                    <label>Interface Name</label>
                    <input type="text" class="net-iface-name" value="${escHtml(iface.name || '')}"
                        placeholder="eth0" oninput="Pages._tabMarkDirty('network')">
                </div>
                <div class="form-group">
                    <label>IP Address (CIDR)</label>
                    <input type="text" class="net-iface-ip" value="${escHtml(iface.ip_address || '')}"
                        placeholder="192.168.1.50/24" oninput="Pages._tabMarkDirty('network')">
                </div>
                <div class="form-group">
                    <label>Gateway</label>
                    <input type="text" class="net-iface-gw" value="${escHtml(iface.gateway || '')}"
                        placeholder="192.168.1.1" oninput="Pages._tabMarkDirty('network')">
                </div>
                <div class="form-group">
                    <label>DNS <span style="font-size:11px;color:var(--text-secondary)">(comma-separated)</span></label>
                    <input type="text" class="net-iface-dns" value="${escHtml((iface.dns || []).join(', '))}"
                        placeholder="8.8.8.8, 8.8.4.4" oninput="Pages._tabMarkDirty('network')">
                </div>
                <div class="form-group">
                    <label>MTU</label>
                    <input type="number" class="net-iface-mtu" value="${iface.mtu || ''}"
                        placeholder="1500" oninput="Pages._tabMarkDirty('network')">
                </div>
                <div class="form-group">
                    <label>Bond</label>
                    <input type="text" class="net-iface-bond" value="${escHtml(iface.bond || '')}"
                        placeholder="bond0" oninput="Pages._tabMarkDirty('network')">
                </div>
            </div>
        </div>`;
    },

    _netAddInterface(discoveredMACs) {
        const list = document.getElementById('net-interfaces-list');
        if (!list) return;
        const emptyEl = document.getElementById('net-empty');
        if (emptyEl) emptyEl.remove();

        const macs = Array.isArray(discoveredMACs) ? discoveredMACs : [];
        const idx = list.querySelectorAll('.net-iface-row').length;
        list.insertAdjacentHTML('beforeend', Pages._netInterfaceRowHTML(idx, {}, macs));
        Pages._tabMarkDirty('network');
    },

    _netRemoveInterface(btn) {
        const row = btn.closest('.net-iface-row');
        if (row) row.remove();
        const list = document.getElementById('net-interfaces-list');
        if (list && list.querySelectorAll('.net-iface-row').length === 0) {
            list.innerHTML = `<div id="net-empty" style="text-align:center;padding:16px;color:var(--text-dim);font-size:12px">No interfaces configured</div>`;
        }
    },

    // ── Configuration tab helpers ─────────────────────────────────────────────

    _cfgVarRowHTML(idx, key, value) {
        // C3-15: warn when the key contains characters that may not be valid in template expansion.
        // Valid keys use [A-Za-z0-9_-] only. Spaces or special chars cause substitution failures.
        const keyInvalid = key && !/^[A-Za-z0-9_-]+$/.test(key);
        const warnIcon = keyInvalid
            ? `<span title="Variable name contains invalid characters — use letters, digits, underscores, or hyphens only" style="color:#f59e0b;font-size:14px;cursor:help;">&#9888;</span>`
            : '';
        return `<div class="cfg-var-row" data-idx="${idx}" style="display:flex;gap:8px;align-items:center;margin-bottom:6px">
            ${warnIcon}
            <input type="text" class="cfg-var-key" value="${escHtml(key)}"
                placeholder="variable_name" style="flex:1${keyInvalid ? ';border-color:#f59e0b;' : ''}" oninput="Pages._tabMarkDirty('config')">
            <input type="text" class="cfg-var-val" value="${escHtml(value)}"
                placeholder="value" style="flex:2" oninput="Pages._tabMarkDirty('config')">
            <button type="button" class="btn btn-danger btn-sm" style="padding:2px 8px;font-size:11px;flex-shrink:0"
                onclick="Pages._cfgRemoveVar(this);Pages._tabMarkDirty('config')">✕</button>
        </div>`;
    },

    _cfgAddVar() {
        const list = document.getElementById('cfg-vars-list');
        if (!list) return;
        const emptyEl = document.getElementById('cfg-vars-empty');
        if (emptyEl) emptyEl.remove();
        const idx = list.querySelectorAll('.cfg-var-row').length;
        list.insertAdjacentHTML('beforeend', Pages._cfgVarRowHTML(idx, '', ''));
        Pages._tabMarkDirty('config');
    },

    _cfgRemoveVar(btn) {
        const row = btn.closest('.cfg-var-row');
        if (row) row.remove();
        const list = document.getElementById('cfg-vars-list');
        if (list && list.querySelectorAll('.cfg-var-row').length === 0) {
            list.innerHTML = `<div id="cfg-vars-empty" style="text-align:center;padding:12px;color:var(--text-dim);font-size:12px">No custom variables</div>`;
        }
    },

    // C3-18: _cfgClearVerifyTimeout clears the verify-timeout input and hides the "Use Global Default" button.
    _cfgClearVerifyTimeout() {
        const input = document.getElementById('cfg-verify-timeout');
        const clearBtn = document.getElementById('cfg-verify-timeout-clear');
        if (input) input.value = '';
        if (clearBtn) clearBtn.style.display = 'none';
        Pages._tabMarkDirty('config');
    },

    // ── Power Provider inline type change ─────────────────────────────────────

    _onPowerProviderInlineTypeChange(type) {
        const ipmiFields   = document.getElementById('pp-inline-ipmi-fields');
        const proxmoxFields = document.getElementById('pp-inline-proxmox-fields');
        if (ipmiFields)    ipmiFields.style.display    = (type === 'ipmi')    ? '' : 'none';
        if (proxmoxFields) proxmoxFields.style.display = (type === 'proxmox') ? '' : 'none';
    },

    // _checkRoleMismatchInline is the inline-editing version of _checkRoleMismatch.
    // Uses the #ov-role-mismatch-warning element in the overview tab form.
    _checkRoleMismatchInline(imageId, node, images) {
        const warnEl = document.getElementById('ov-role-mismatch-warning');
        if (!warnEl) return;
        if (!imageId) { warnEl.style.display = 'none'; return; }
        const img = (images || []).find(i => i.id === imageId);
        if (!img) { warnEl.style.display = 'none'; return; }
        const builtFor = img.built_for_roles || [];
        if (!builtFor.length) { warnEl.style.display = 'none'; return; }
        const roleKeywords = ['compute', 'gpu-compute', 'gpu', 'storage', 'head-node', 'management', 'minimal'];
        const nodeRoles = (node.tags || node.groups || []).filter(g => roleKeywords.some(k => g.toLowerCase().includes(k)));
        const mismatched = nodeRoles.filter(g => !builtFor.some(r => g.toLowerCase().includes(r) || r.toLowerCase().includes(g)));
        if (mismatched.length) {
            warnEl.innerHTML = `Role mismatch: node has <strong>${escHtml(mismatched.join(', '))}</strong> but image built for <strong>${escHtml(builtFor.join(', '))}</strong>`;
            warnEl.style.display = '';
        } else {
            warnEl.style.display = 'none';
        }
    },

    // ── Node Actions dropdown ─────────────────────────────────────────────────

    // B4-7: renamed from _nodeActionsRediscover; updated label and warning text.
    async _nodeActionsRediscover(nodeId) {
        Pages.showConfirmModal({
            title: 'Queue Reimage',
            message: '<strong>Warning:</strong> Queuing a reimage will wipe the disk on the next PXE boot.<br><br>The node must PXE boot to proceed. The disk will be formatted and the assigned image re-deployed.',
            confirmText: 'Queue Reimage',
            danger: true,
            onConfirm: async () => {
                try {
                    await API.request('POST', `/nodes/${nodeId}/reimage`, {});
                    App.toast('Reimage queued — PXE-boot the node to proceed.', 'success');
                } catch (e) {
                    App.toast('Queue reimage failed: ' + e.message, 'error');
                }
            },
        });
    },

    _nodeActionsTriggerReimage(nodeId, displayName) {
        // Open the full reimage modal with optional scheduled_at.
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = 'reimage-trigger-modal';
        overlay.innerHTML = `
            <div class="modal" style="max-width:440px" role="dialog" aria-modal="true">
                <div class="modal-header">
                    <span class="modal-title">Trigger Reimage</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('reimage-trigger-modal').remove()">&#215;</button>
                </div>
                <div class="modal-body">
                    <p style="margin:0 0 14px;font-size:13px">
                        Trigger reimage of <strong>${escHtml(displayName)}</strong>. The node will
                        re-deploy on its next PXE boot.
                    </p>
                    <div class="form-group">
                        <label for="reimage-scheduled-at" style="font-size:13px">
                            Schedule for (optional)
                            <span class="tooltip-icon" title="Leave empty to trigger immediately. Set a future time to schedule the reimage." style="cursor:help;font-size:11px;color:var(--text-secondary)">(?)</span>
                        </label>
                        <input id="reimage-scheduled-at" type="datetime-local" class="form-input"
                            style="margin-top:4px"
                            min="${new Date().toISOString().slice(0,16)}">
                        <div style="font-size:11px;color:var(--text-secondary);margin-top:4px">
                            Leave empty to trigger immediately. Times are in your local timezone.
                        </div>
                    </div>
                    <div id="reimage-trigger-error" style="color:var(--error);font-size:13px;margin-top:8px;display:none"></div>
                </div>
                <div class="modal-footer">
                    <button class="btn btn-secondary" onclick="document.getElementById('reimage-trigger-modal').remove()">Cancel</button>
                    <button class="btn btn-danger" id="reimage-trigger-btn" onclick="Pages._nodeActionsTriggerReimageSubmit('${nodeId}')">Trigger Reimage</button>
                </div>
            </div>`;
        document.body.appendChild(overlay);
    },

    async _nodeActionsTriggerReimageSubmit(nodeId) {
        const overlay = document.getElementById('reimage-trigger-modal');
        const btn     = document.getElementById('reimage-trigger-btn');
        const errEl   = document.getElementById('reimage-trigger-error');
        if (!overlay) return;

        const scheduledInput = document.getElementById('reimage-scheduled-at');
        const body = {};
        if (scheduledInput && scheduledInput.value) {
            // Convert local datetime to UTC ISO string.
            body.scheduled_at = new Date(scheduledInput.value).toISOString();
        }

        btn.disabled = true;
        btn.textContent = 'Submitting…';
        if (errEl) { errEl.style.display = 'none'; errEl.textContent = ''; }
        try {
            await API.request('POST', `/nodes/${nodeId}/reimage`, body);
            overlay.remove();
            const msg = body.scheduled_at
                ? `Reimage scheduled for ${new Date(body.scheduled_at).toLocaleString()}`
                : 'Reimage queued — node will re-deploy on next PXE boot';
            App.toast(msg, 'success');
            Pages.nodeDetail(nodeId);
        } catch (e) {
            btn.disabled = false;
            btn.textContent = 'Trigger Reimage';
            if (errEl) { errEl.textContent = e.message; errEl.style.display = ''; }
        }
    },

    // ── Cancel a single reimage request ──────────────────────────────────────

    async _cancelReimage(reimageId, nodeId) {
        Pages.showConfirmModal({
            title: 'Cancel Reimage',
            message: "Cancel this reimage request?<br><br>The node's <code>reimage_pending</code> flag will be cleared so PXE boots route to disk.",
            confirmText: 'Cancel Reimage',
            danger: true,
            onConfirm: async () => {
                try {
                    await API.reimages.cancel(reimageId);
                    App.toast('Reimage request canceled', 'success');
                    Pages.nodeDetail(nodeId);
                } catch (e) {
                    Pages.showAlertModal('Cancel Failed', escHtml(e.message));
                }
            },
        });
    },

    // ── Cancel all non-terminal reimage requests ──────────────────────────────

    async cancelAllActiveDeploys() {
        Pages.showConfirmModal({
            title: 'Cancel All Active Deploys',
            message: 'Cancel ALL active reimage requests (pending, triggered, in_progress)?<br><br>This will clear <code>reimage_pending</code> on all affected nodes.',
            confirmText: 'Cancel All',
            danger: true,
            onConfirm: async () => {
                try {
                    const result = await API.reimages.cancelAllActive();
                    const n = result && result.canceled != null ? result.canceled : '?';
                    App.toast(`${n} reimage request${n === 1 ? '' : 's'} canceled`, 'success');
                    Pages.images();
                } catch (e) {
                    Pages.showAlertModal('Cancel All Failed', escHtml(e.message));
                }
            },
        });
    },

    async loadNodeLogs(mac) {
        const viewer = document.getElementById('node-log-viewer');
        if (!viewer) return;
        try {
            const resp = await API.logs.query({ mac, limit: '300' });
            const entries = resp.logs || [];
            if (!App._nodeLogStream) {
                App._nodeLogStream = new LogStream(viewer);
            }
            App._nodeLogStream.loadEntries(entries);
            if (!entries.length) {
                viewer.innerHTML = `<div class="empty-state" style="padding:30px">
                    <div class="empty-state-text">No log entries for this node</div>
                </div>`;
            }
        } catch (e) {
            viewer.innerHTML = `<div style="padding:12px;color:var(--error);font-size:12px">Error: ${escHtml(e.message)}</div>`;
        }
    },

    toggleNodeLogs(enabled, mac) {
        const viewer = document.getElementById('node-log-viewer');
        const ind    = document.getElementById('node-follow-ind');
        if (!viewer) return;

        if (enabled) {
            if (!App._nodeLogStream) App._nodeLogStream = new LogStream(viewer);
            App._nodeLogStream.setFilters({ mac });
            App._nodeLogStream.setAutoScroll(true);
            App._nodeLogStream.onConnect(() => {
                if (ind) { ind.className = 'follow-indicator live'; ind.innerHTML = '<span class="follow-dot"></span>Live'; }
            });
            App._nodeLogStream.onDisconnect((permanent) => {
                if (ind) {
                    ind.className = 'follow-indicator';
                    ind.innerHTML = permanent
                        ? '<span class="follow-dot"></span>unavailable'
                        : '<span class="follow-dot"></span>Reconnecting…';
                }
            });
            App._nodeLogStream.connect();
        } else {
            if (App._nodeLogStream) {
                App._nodeLogStream.disconnect();
                if (ind) { ind.className = 'follow-indicator'; ind.innerHTML = '<span class="follow-dot"></span>static'; }
            }
        }
    },

    // onNodeLogSourceChange handles switching between "Deploy Logs" and "Live Journal".
    // "Live Journal" uses component=node-journal which triggers log_pull_start on the node.
    onNodeLogSourceChange(source, mac) {
        const viewer     = document.getElementById('node-log-viewer');
        const toggle     = document.getElementById('node-follow-toggle');
        const ind        = document.getElementById('node-follow-ind');
        const refreshBtn = document.getElementById('node-log-refresh');
        if (!viewer) return;

        // Stop any active stream when switching sources.
        if (App._nodeLogStream) {
            App._nodeLogStream.disconnect();
            App._nodeLogStream = null;
        }
        if (toggle) toggle.checked = false;
        if (ind) { ind.className = 'follow-indicator'; ind.innerHTML = '<span class="follow-dot"></span>static'; }
        viewer.innerHTML = '';

        if (source === 'journal') {
            // Live Journal: hide Refresh button (journal is always live), show Live toggle.
            if (refreshBtn) refreshBtn.style.display = 'none';

            // Auto-connect with component filter so the server drives log_pull_start.
            const stream = new LogStream(viewer);
            stream.setFilters({ mac, component: 'node-journal' });
            stream.setAutoScroll(true);
            stream.onConnect(() => {
                if (ind) { ind.className = 'follow-indicator live'; ind.innerHTML = '<span class="follow-dot"></span>Live'; }
                if (toggle) toggle.checked = true;
            });
            stream.onDisconnect((permanent) => {
                if (ind) {
                    ind.className = 'follow-indicator';
                    ind.innerHTML = permanent
                        ? '<span class="follow-dot"></span>unavailable'
                        : '<span class="follow-dot"></span>Reconnecting…';
                }
                if (toggle) toggle.checked = false;
            });
            // Override toggle: reconnect/disconnect the journal stream.
            if (toggle) {
                toggle.onchange = (e) => {
                    if (e.target.checked) {
                        stream.connect();
                    } else {
                        stream.disconnect();
                        if (ind) { ind.className = 'follow-indicator'; ind.innerHTML = '<span class="follow-dot"></span>static'; }
                    }
                };
            }
            App._nodeLogStream = stream;
            stream.connect();
        } else {
            // Deploy Logs: restore the default toggle and refresh button behaviour.
            if (refreshBtn) refreshBtn.style.display = '';
            if (toggle) {
                toggle.onchange = (e) => Pages.toggleNodeLogs(e.target.checked, mac);
            }
            Pages.loadNodeLogs(mac);
        }
    },

    // ── Config Push ───────────────────────────────────────────────────────────

    // _doConfigPush sends the config push request for nodeId and shows the result.
    async _doConfigPush(nodeId) {
        const targetEl  = document.getElementById('configpush-target');
        const contentEl = document.getElementById('configpush-content');
        const resultEl  = document.getElementById('configpush-result');
        const submitBtn = document.getElementById('configpush-submit');
        if (!targetEl || !contentEl || !resultEl || !submitBtn) return;

        const target  = targetEl.value;
        const content = contentEl.value;
        if (!content.trim()) {
            resultEl.style.display = '';
            resultEl.className = 'alert alert-error';
            resultEl.textContent = 'Content cannot be empty.';
            return;
        }

        resultEl.style.display = '';
        resultEl.className = 'alert alert-info';
        resultEl.innerHTML = '<span class="spinner" style="width:14px;height:14px;display:inline-block;margin-right:6px"></span>Pushing config… (waiting for node ack, up to 30s)';
        submitBtn.disabled = true;

        try {
            await API.request('PUT', `/nodes/${nodeId}/config-push`, { target, content });
            resultEl.className = 'alert alert-success';
            resultEl.textContent = `Config "${target}" pushed successfully.`;
        } catch (e) {
            resultEl.className = 'alert alert-error';
            resultEl.textContent = `Push failed: ${e.message}`;
        } finally {
            submitBtn.disabled = false;
        }
    },

    // ── Diagnostics tab ───────────────────────────────────────────────────────

    // _runDiagnosticCmd runs a specific command+args on nodeId and renders the result.
    async _runDiagnosticCmd(nodeId, command, args) {
        const resultWrap  = document.getElementById('diag-result-wrap');
        const runningEl   = document.getElementById('diag-running');
        const cmdEl       = document.getElementById('diag-result-cmd');
        const exitBadge   = document.getElementById('diag-exit-badge');
        const truncBadge  = document.getElementById('diag-truncated-badge');
        const stdoutEl    = document.getElementById('diag-stdout');
        const stdoutWrap  = document.getElementById('diag-stdout-wrap');
        const stderrEl    = document.getElementById('diag-stderr');
        const stderrWrap  = document.getElementById('diag-stderr-wrap');
        const errWrap     = document.getElementById('diag-error-wrap');
        const runBtn      = document.getElementById('diag-run-btn');

        if (!resultWrap || !runningEl) return;

        // Reset display.
        resultWrap.style.display = 'none';
        runningEl.style.display = '';
        if (runBtn) runBtn.disabled = true;

        // Disable all quick-command buttons during the run.
        const quickBtns = document.querySelectorAll('#tab-diagnostics .btn-secondary');
        quickBtns.forEach(b => { b.disabled = true; });

        try {
            const result = await API.nodes.exec(nodeId, command, args);

            // Render the command line.
            const cmdLine = [command, ...(args || [])].join(' ');
            if (cmdEl) cmdEl.textContent = '$ ' + cmdLine;

            // Exit code badge.
            if (exitBadge) {
                exitBadge.innerHTML = result.exit_code === 0
                    ? `<span class="badge badge-success">exit 0</span>`
                    : `<span class="badge badge-error">exit ${result.exit_code}</span>`;
            }

            // Truncation warning.
            if (truncBadge) truncBadge.style.display = result.truncated ? '' : 'none';

            // Stdout.
            if (stdoutEl) stdoutEl.textContent = result.stdout || '';
            if (stdoutWrap) stdoutWrap.style.display = result.stdout ? '' : 'none';

            // Stderr.
            if (stderrEl) stderrEl.textContent = result.stderr || '';
            if (stderrWrap) stderrWrap.style.display = result.stderr ? '' : 'none';

            // Error message (from whitelist rejection or exec failure).
            if (errWrap) {
                errWrap.style.display = result.error ? '' : 'none';
                errWrap.textContent = result.error || '';
            }

            resultWrap.style.display = '';
        } catch (e) {
            if (errWrap) {
                errWrap.style.display = '';
                errWrap.textContent = 'Request failed: ' + e.message;
            }
            if (resultWrap) resultWrap.style.display = '';
        } finally {
            runningEl.style.display = 'none';
            if (runBtn) runBtn.disabled = false;
            quickBtns.forEach(b => { b.disabled = false; });
        }
    },

    // _runDiagnosticCmdCustom reads the command/args inputs and runs the command.
    _runDiagnosticCmdCustom(nodeId) {
        const cmdSelect = document.getElementById('diag-cmd-select');
        const argsInput = document.getElementById('diag-args-input');
        if (!cmdSelect) return;

        const command = cmdSelect.value;
        // Split args on whitespace, filtering empty tokens.
        const argsRaw = (argsInput ? argsInput.value : '').trim();
        const args = argsRaw ? argsRaw.split(/\s+/).filter(Boolean) : [];

        Pages._runDiagnosticCmd(nodeId, command, args);
    },

    // ── Slurm Role tab ────────────────────────────────────────────────────────

    // _onSlurmTabOpen fetches the current roles for nodeId and checks the
    // appropriate checkboxes. Called once when the Slurm tab is first opened.
    async _onSlurmTabOpen(nodeId) {
        const currentEl = document.getElementById('slurm-role-current');
        // Load roles.
        try {
            const data = await API.slurm.getNodeRole(nodeId);
            const roles = data.roles || [];
            ['controller', 'compute', 'login', 'dbd'].forEach(r => {
                const cb = document.getElementById(`slurm-role-${r}`);
                if (cb) cb.checked = roles.includes(r);
            });
            if (currentEl) {
                if (roles.length === 0) {
                    currentEl.innerHTML = '<span class="badge badge-neutral">No role assigned</span>';
                } else {
                    currentEl.innerHTML = roles.map(r =>
                        `<span class="badge badge-info" style="margin-right:4px">${escHtml(r)}</span>`
                    ).join('');
                }
            }
        } catch (err) {
            if (currentEl) currentEl.textContent = 'Failed to load role: ' + err.message;
        }
        // Load overrides and sync status in parallel (via SlurmPages helpers).
        if (typeof SlurmPages !== 'undefined') {
            const syncEl     = document.getElementById('slurm-node-sync-status');
            const overrideEl = document.getElementById('slurm-node-overrides');
            if (syncEl)     SlurmPages.renderNodeSyncStatus(nodeId, syncEl);
            if (overrideEl) SlurmPages.renderNodeOverrideEditor(nodeId, overrideEl);
        }
    },

    // _pushToSingleNode initiates a push to a single node and shows toast feedback.
    async _pushToSingleNode(nodeId) {
        Pages.showConfirmModal({
            title: 'Push Slurm Configs',
            message: `Push all Slurm configs to node <strong>${escHtml(nodeId)}</strong>?`,
            confirmText: 'Push',
            onConfirm: async () => {
                try {
                    const op = await API.slurm.push({ filenames: [], node_ids: [nodeId], apply_action: 'reconfigure' });
                    App.toast('Push started (op: ' + (op.id || '?').slice(0,8) + ')', 'info');
                } catch (err) {
                    App.toast('Push failed: ' + err.message, 'error');
                }
            },
        });
    },

    // _saveSlurmRole reads the checked role checkboxes and calls PUT /nodes/:id/slurm/role.
    async _saveSlurmRole(nodeId) {
        const btn = document.getElementById('slurm-role-save-btn');
        const statusEl = document.getElementById('slurm-role-save-status');
        const roles = ['controller', 'compute', 'login', 'dbd']
            .filter(r => { const cb = document.getElementById(`slurm-role-${r}`); return cb && cb.checked; });
        if (btn) { btn.disabled = true; btn.textContent = 'Saving…'; }
        if (statusEl) statusEl.textContent = '';
        try {
            await API.slurm.setNodeRole(nodeId, { roles });
            if (statusEl) { statusEl.textContent = 'Saved.'; statusEl.style.color = 'var(--success, green)'; }
            // Refresh displayed badges.
            const currentEl = document.getElementById('slurm-role-current');
            if (currentEl) {
                if (roles.length === 0) {
                    currentEl.innerHTML = '<span class="badge badge-neutral">No role assigned</span>';
                } else {
                    currentEl.innerHTML = roles.map(r =>
                        `<span class="badge badge-info" style="margin-right:4px">${escHtml(r)}</span>`
                    ).join('');
                }
            }
        } catch (err) {
            if (statusEl) { statusEl.textContent = 'Error: ' + err.message; statusEl.style.color = 'var(--error, red)'; }
        } finally {
            if (btn) { btn.disabled = false; btn.textContent = 'Save Role'; }
        }
    },

    // ── Power management ──────────────────────────────────────────────────────

    // _doFlipToDisk calls POST /nodes/:id/power/flip-to-disk via the provider.
    // When cycle=true the server also power-cycles the node after flipping.
    async _doFlipToDisk(nodeId, cycle) {
        const feedback = document.getElementById('power-action-feedback');
        const label = cycle ? 'Flip to Disk + Reboot' : 'Flip to Disk';
        if (feedback) { feedback.textContent = `${label}…`; feedback.style.display = ''; feedback.className = 'alert alert-info'; }
        try {
            await API.nodes.power.flipToDisk(nodeId, !!cycle);
            if (feedback) { feedback.textContent = `${label} command sent.`; feedback.className = 'alert alert-info'; }
            if (!cycle) setTimeout(() => Pages._refreshPowerStatus(nodeId), 2000);
        } catch (e) {
            if (feedback) { feedback.textContent = `Error: ${e.message}`; feedback.className = 'alert alert-error'; }
        }
    },

    // ── Disk Layout tab ───────────────────────────────────────────────────────

    // _onDiskLayoutTabOpen loads the effective layout and recommendation for the
    // disk layout editor tab. Called once when the tab is first opened.
    async _onDiskLayoutTabOpen(nodeId) {
        const container = document.getElementById('disklayout-content');
        if (!container) return;
        container.innerHTML = `<div class="loading"><div class="spinner"></div>Loading…</div>`;
        try {
            const [effectiveResp, recResp] = await Promise.allSettled([
                API.request('GET', `/nodes/${nodeId}/effective-layout`),
                API.request('GET', `/nodes/${nodeId}/layout-recommendation`),
            ]);
            const effective = effectiveResp.status === 'fulfilled' ? effectiveResp.value : null;
            const rec = recResp.status === 'fulfilled' ? recResp.value : null;
            container.innerHTML = Pages._renderDiskLayoutTab(nodeId, effective, rec);
        } catch (e) {
            container.innerHTML = alertBox(`Failed to load disk layout: ${e.message}`);
        }
    },

    // _onMountsTabOpen fetches both effective-mounts and node data, then renders the two-section editor.
    async _onMountsTabOpen(nodeId) {
        const container = document.getElementById('mounts-content');
        if (!container) return;
        // Don't reload if already populated and not dirty.
        const state = Pages._nodeEditorState['mounts'];
        if (container.dataset.loaded === nodeId && !(state && state.dirty)) return;
        container.innerHTML = `<div class="loading"><div class="spinner"></div>Loading mounts…</div>`;
        try {
            const [effectiveResp, node] = await Promise.all([
                API.request('GET', `/nodes/${nodeId}/effective-mounts`),
                API.nodes.get(nodeId),
            ]);
            // Sync editor state with fresh node data (in case another tab saved something).
            if (state && !state.dirty) {
                state.original = { extra_mounts: JSON.parse(JSON.stringify(node.extra_mounts || [])) };
            }
            container.innerHTML = Pages._renderEffectiveMountsTab(nodeId, effectiveResp, node);
            container.dataset.loaded = nodeId;
            // Wire up live validation on the editable tbody.
            const tbody = document.getElementById('mounts-node-tbody');
            if (tbody) {
                tbody.addEventListener('input', () => Pages._tabMarkDirty('mounts'));
                tbody.addEventListener('change', (e) => {
                    if (e.target && e.target.name === 'mount_fs_type') {
                        Pages._onFSTypeChange(e.target);
                    }
                    Pages._tabMarkDirty('mounts');
                });
            }
        } catch (e) {
            container.innerHTML = alertBox(`Failed to load mounts: ${e.message}`);
        }
    },

    // _renderEffectiveMountsTab renders a two-section layout:
    //   Section 1 — inherited group mounts (read-only)
    //   Section 2 — node-level mounts (inline editable)
    _renderEffectiveMountsTab(nodeId, resp, node) {
        const allMounts  = (resp && resp.mounts) || [];
        const groupId    = (resp && resp.group_id) || '';
        const groupMounts = allMounts.filter(m => m.source === 'group');
        const nodeMounts  = (node && node.extra_mounts) || [];

        // ── Section 1: Inherited from group ──────────────────────────────────
        const groupSection = (() => {
            if (groupMounts.length === 0) {
                const noGroupMsg = groupId
                    ? `<div class="text-dim" style="padding:12px;font-size:13px">No mounts defined on the assigned group.</div>`
                    : `<div class="text-dim" style="padding:12px;font-size:13px">Node is not assigned to a group.</div>`;
                return cardWrap('Inherited from Group', noGroupMsg);
            }
            const rows = groupMounts.map(m => `<tr>
                <td class="mono">${escHtml(m.source_device||m.source||'—')}</td>
                <td class="mono">${escHtml(m.mount_point||'—')}</td>
                <td><span class="badge badge-neutral badge-sm">${escHtml(m.fs_type||'—')}</span></td>
                <td class="mono dim" style="font-size:11px">${escHtml(m.options||'defaults')}</td>
                <td style="text-align:center">${m.auto_mkdir ? '✓' : '—'}</td>
                <td class="dim" style="font-size:11px">${escHtml(m.comment||'—')}</td>
            </tr>`).join('');
            return cardWrap('Inherited from Group',
                `<p style="margin:0 0 10px;color:var(--text-secondary);font-size:12px">
                    These mounts come from the node's group and cannot be edited here.
                    Node-level entries with the same mount point will override the group entry.
                </p>
                <div class="table-wrap"><table>
                    <thead><tr>
                        <th>Source</th><th>Mount Point</th><th>FS Type</th>
                        <th>Options</th><th>Auto-mkdir</th><th>Comment</th>
                    </tr></thead>
                    <tbody>${rows}</tbody>
                </table></div>`);
        })();

        // ── Section 2: Node-level mounts (editable) ──────────────────────────
        const nodeRows = nodeMounts.map((m, i) => Pages._mountsNodeRowHTML(i, m)).join('');
        const emptyRow = nodeMounts.length === 0
            ? `<div id="mounts-node-empty" style="text-align:center;padding:16px;color:var(--text-dim);font-size:12px">No node-level mounts configured</div>`
            : '';

        const nodeSection = cardWrap('Node-Level Mounts',
            `<p style="margin:0 0 10px;color:var(--text-secondary);font-size:12px">
                Added to <code>/etc/fstab</code> during deployment.
                These entries override group entries when the mount point matches.
            </p>
            <div style="display:flex;align-items:center;gap:8px;margin-bottom:10px">
                <select id="mounts-preset-select" class="form-select" style="font-size:12px;padding:4px 8px;width:auto">
                    <option value="">— Apply preset —</option>
                    <option value="nfs-home">NFS home</option>
                    <option value="lustre">Lustre scratch</option>
                    <option value="beegfs">BeeGFS data</option>
                    <option value="cifs">CIFS / Samba</option>
                    <option value="bind">Bind mount</option>
                    <option value="tmpfs">tmpfs</option>
                </select>
                <button type="button" class="btn btn-secondary btn-sm"
                    onclick="Pages._mountsApplyPreset()">Apply</button>
                <button type="button" class="btn btn-secondary btn-sm"
                    onclick="Pages._mountsAddRow()">+ Add Mount</button>
            </div>
            <div id="mounts-node-table-wrap">
                <div class="table-wrap" style="overflow-x:auto${nodeMounts.length === 0 ? ';display:none' : ''}">
                    <table id="mounts-node-table" style="width:100%;font-size:12px;border-collapse:collapse">
                        <thead><tr style="border-bottom:1px solid var(--border)">
                            <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">Source</th>
                            <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">Mount Point</th>
                            <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">FS Type</th>
                            <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">Options</th>
                            <th style="text-align:center;padding:4px 6px;font-weight:500;color:var(--text-secondary)" title="Auto-create mount point directory">mkd</th>
                            <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">Comment</th>
                            <th style="padding:4px"></th>
                        </tr></thead>
                        <tbody id="mounts-node-tbody">${nodeRows}</tbody>
                    </table>
                </div>
                ${emptyRow}
            </div>`);

        return `${groupSection}${nodeSection}`;
    },

    // _mountsNodeRowHTML builds one editable row for a node-level mount entry.
    _mountsNodeRowHTML(idx, m) {
        m = m || {};
        const fsTypes = ['nfs','nfs4','cifs','beegfs','lustre','gpfs','xfs','ext4','bind','9p','tmpfs','vfat','ext3','smbfs'];
        const fsSelect = fsTypes.map(t =>
            `<option value="${t}"${m.fs_type === t ? ' selected' : ''}>${t}</option>`
        ).join('');
        return `<tr data-mount-idx="${idx}" style="border-bottom:1px solid var(--border)">
            <td style="padding:4px 3px">
                <input type="text" name="mount_source" value="${escHtml(m.source||'')}"
                    placeholder="nfs-server:/export/home" style="width:100%;min-width:130px;font-size:12px" required>
            </td>
            <td style="padding:4px 3px">
                <input type="text" name="mount_point" value="${escHtml(m.mount_point||'')}"
                    placeholder="/mnt/share" style="width:100%;min-width:110px;font-size:12px" required pattern="/.+">
            </td>
            <td style="padding:4px 3px">
                <select name="mount_fs_type" style="font-size:12px;padding:2px 4px">${fsSelect}</select>
            </td>
            <td style="padding:4px 3px">
                <input type="text" name="mount_options" value="${escHtml(m.options||'')}"
                    placeholder="defaults,_netdev" style="width:100%;min-width:130px;font-size:12px">
            </td>
            <td style="padding:4px 3px;text-align:center">
                <input type="checkbox" name="mount_auto_mkdir" ${m.auto_mkdir !== false ? 'checked' : ''}>
            </td>
            <td style="padding:4px 3px">
                <input type="text" name="mount_comment" value="${escHtml(m.comment||'')}"
                    placeholder="optional note" style="width:100%;min-width:80px;font-size:12px">
            </td>
            <td style="padding:4px 3px">
                <button type="button" class="btn btn-danger btn-sm"
                    onclick="Pages._mountsRemoveRow(this)" style="padding:2px 6px;font-size:11px">✕</button>
            </td>
        </tr>`;
    },

    // _mountsAddRow appends a blank (or preset) row to the node mounts table.
    _mountsAddRow(preset) {
        const tbody = document.getElementById('mounts-node-tbody');
        if (!tbody) return;
        const idx = tbody.querySelectorAll('tr').length;
        tbody.insertAdjacentHTML('beforeend', Pages._mountsNodeRowHTML(idx, preset || {}));
        Pages._mountsShowTable();
        Pages._tabMarkDirty('mounts');
    },

    // _mountsRemoveRow removes the row containing the given button.
    _mountsRemoveRow(btn) {
        const row = btn.closest('tr');
        if (row) row.remove();
        Pages._mountsUpdateEmpty();
        Pages._tabMarkDirty('mounts');
    },

    // _mountsApplyPreset reads the preset dropdown and inserts the preset row.
    _mountsApplyPreset() {
        const sel = document.getElementById('mounts-preset-select');
        if (!sel || !sel.value) return;
        const presets = {
            'nfs-home': { source: 'nfs-server:/export/home', mount_point: '/home/shared',  fs_type: 'nfs4',   options: 'defaults,_netdev,vers=4',              auto_mkdir: true,  comment: 'NFS home directory' },
            'lustre':   { source: 'mgs@tcp:/scratch',        mount_point: '/scratch',       fs_type: 'lustre', options: 'defaults,_netdev,flock',               auto_mkdir: true,  comment: 'Lustre scratch' },
            'beegfs':   { source: 'beegfs',                  mount_point: '/mnt/beegfs',    fs_type: 'beegfs', options: 'defaults,_netdev',                     auto_mkdir: true,  comment: 'BeeGFS data' },
            'cifs':     { source: '//winserver/share',        mount_point: '/mnt/share',     fs_type: 'cifs',   options: 'defaults,_netdev,vers=3.0,sec=ntlmssp',auto_mkdir: true,  comment: 'CIFS (Windows) share' },
            'bind':     { source: '/data/src',               mount_point: '/data/dst',      fs_type: 'bind',   options: 'defaults,bind',                        auto_mkdir: true,  comment: 'Bind mount' },
            'tmpfs':    { source: 'tmpfs',                   mount_point: '/tmp',           fs_type: 'tmpfs',  options: 'defaults,size=4G,mode=1777',           auto_mkdir: false, comment: 'tmpfs /tmp' },
        };
        const p = presets[sel.value];
        if (p) Pages._mountsAddRow(p);
        sel.value = '';
    },

    // _mountsShowTable reveals the table wrapper (used after first row is added).
    _mountsShowTable() {
        const wrap = document.getElementById('mounts-node-table-wrap');
        if (!wrap) return;
        const tableWrap = wrap.querySelector('.table-wrap');
        if (tableWrap) tableWrap.style.display = '';
        const empty = document.getElementById('mounts-node-empty');
        if (empty) empty.remove();
    },

    // _mountsUpdateEmpty shows the empty-state message when the tbody is empty,
    // and hides the table wrapper.
    _mountsUpdateEmpty() {
        const tbody = document.getElementById('mounts-node-tbody');
        const wrap  = document.getElementById('mounts-node-table-wrap');
        if (!tbody || !wrap) return;
        const hasRows = tbody.querySelectorAll('tr').length > 0;
        const tableWrap = wrap.querySelector('.table-wrap');
        if (tableWrap) tableWrap.style.display = hasRows ? '' : 'none';
        const existing = document.getElementById('mounts-node-empty');
        if (!hasRows && !existing) {
            wrap.insertAdjacentHTML('beforeend',
                `<div id="mounts-node-empty" style="text-align:center;padding:16px;color:var(--text-dim);font-size:12px">No node-level mounts configured</div>`);
        } else if (hasRows && existing) {
            existing.remove();
        }
    },

    // _mountsCollect reads all editable rows and returns an array of FstabEntry objects.
    // Returns null (with inline validation errors shown) if any row is invalid.
    _mountsCollect() {
        const tbody = document.getElementById('mounts-node-tbody');
        if (!tbody) return [];
        const fsTypeWhitelist = new Set(['nfs','nfs4','cifs','beegfs','lustre','gpfs','xfs','ext4','bind','9p','tmpfs','vfat','ext3','smbfs']);
        const mounts = [];
        let valid = true;
        tbody.querySelectorAll('tr').forEach(row => {
            const srcEl  = row.querySelector('[name="mount_source"]');
            const mpEl   = row.querySelector('[name="mount_point"]');
            const fsEl   = row.querySelector('[name="mount_fs_type"]');
            const optEl  = row.querySelector('[name="mount_options"]');
            const mkdEl  = row.querySelector('[name="mount_auto_mkdir"]');
            const cmtEl  = row.querySelector('[name="mount_comment"]');
            const source     = (srcEl?.value || '').trim();
            const mountPoint = (mpEl?.value || '').trim();
            const fsType     = fsEl?.value || 'nfs';
            const options    = (optEl?.value || '').trim();
            const autoMkdir  = mkdEl ? mkdEl.checked : true;
            const comment    = (cmtEl?.value || '').trim();

            // Validate required fields.
            if (!source) { if (srcEl) srcEl.style.border = '1px solid var(--error)'; valid = false; }
            else if (srcEl) srcEl.style.border = '';
            if (!mountPoint || mountPoint[0] !== '/') { if (mpEl) mpEl.style.border = '1px solid var(--error)'; valid = false; }
            else if (mpEl) mpEl.style.border = '';
            if (!fsTypeWhitelist.has(fsType)) { valid = false; return; }

            mounts.push({ source, mount_point: mountPoint, fs_type: fsType, options, auto_mkdir: autoMkdir, comment, dump: 0, pass: 0 });
        });
        return valid ? mounts : null;
    },

    // _tabSaveMounts saves the node-level mounts via PUT /nodes/:id.
    async _tabSaveMounts(nodeId) {
        Pages._tabMarkSaving('mounts');
        const saveBtn = document.getElementById('tab-save-mounts');

        const mounts = Pages._mountsCollect();
        if (mounts === null) {
            Pages._tabMarkError('mounts', 'Fix validation errors before saving');
            if (saveBtn) saveBtn.disabled = false;
            return;
        }

        try {
            const existing = await API.nodes.get(nodeId);
            const body = Object.assign({}, existing, { extra_mounts: mounts });
            await API.nodes.update(nodeId, body);

            // Update editor state so revert has the new baseline.
            const state = Pages._nodeEditorState['mounts'];
            if (state) state.original = { extra_mounts: JSON.parse(JSON.stringify(mounts)) };

            Pages._tabMarkClean('mounts');
            App.toast('Mounts saved', 'success');

            // Force tab to re-render on next open so effective view is fresh.
            const container = document.getElementById('mounts-content');
            if (container) delete container.dataset.loaded;
        } catch (e) {
            Pages._tabMarkError('mounts', `Save failed: ${e.message}`);
            if (saveBtn) saveBtn.disabled = false;
        }
    },

    _renderDiskLayoutTab(nodeId, effective, rec) {
        const sourceLabel = {
            node:  '<span class="badge badge-info">Node Override</span>',
            group: '<span class="badge badge-neutral">Group Override</span>',
            image: '<span class="badge badge-archived">Image Default</span>',
        };

        const layoutToTable = (layout) => {
            if (!layout || !layout.partitions || layout.partitions.length === 0) {
                return `<div class="text-dim" style="padding:12px">No partitions defined.</div>`;
            }
            const totalFixed = layout.partitions.reduce((s, p) => s + (p.size_bytes || 0), 0);
            // Visual bar: compute each partition's width as a % of total fixed space (or uniform if fill).
            const hasFill = layout.partitions.some(p => !p.size_bytes);
            const barParts = layout.partitions.map(p => {
                const pct = (hasFill || totalFixed === 0)
                    ? (p.size_bytes ? Math.round(p.size_bytes / (totalFixed || 1) * 80) : 20)
                    : Math.max(2, Math.round(p.size_bytes / totalFixed * 100));
                const colors = {
                    xfs: '#3b82f6', ext4: '#8b5cf6', vfat: '#10b981', swap: '#f59e0b',
                    biosboot: '#6b7280', bios_grub: '#6b7280',
                };
                const bg = colors[p.filesystem] || '#94a3b8';
                return `<div style="flex:${pct};background:${bg};min-width:24px;display:flex;align-items:center;justify-content:center;font-size:10px;color:#fff;overflow:hidden;white-space:nowrap;padding:0 4px" title="${escHtml(p.label||p.mountpoint||p.filesystem)}">${escHtml(p.label||p.mountpoint||'')}</div>`;
            }).join('');
            const bar = `<div style="display:flex;height:32px;border-radius:6px;overflow:hidden;margin-bottom:12px;border:1px solid var(--border)">${barParts}</div>`;

            const rows = layout.partitions.map((p, i) => {
                const sizeStr = p.size_bytes
                    ? fmtBytes(p.size_bytes)
                    : '<span class="badge badge-neutral" style="font-size:10px">fill</span>';
                return `<tr>
                    <td>${escHtml(p.label || '—')}</td>
                    <td>${sizeStr}</td>
                    <td><span class="badge badge-neutral" style="font-size:10px">${escHtml(p.filesystem || '—')}</span></td>
                    <td class="text-mono">${escHtml(p.mountpoint || '—')}</td>
                    <td class="text-dim">${(p.flags||[]).join(', ') || '—'}</td>
                    <td class="text-dim">${escHtml(p.device||'(auto)')}</td>
                </tr>`;
            }).join('');

            const bootloader = layout.bootloader
                ? `<div style="margin-top:8px;font-size:12px;color:var(--text-secondary)">Bootloader: <strong>${escHtml(layout.bootloader.type||'')} (${escHtml(layout.bootloader.target||'')})</strong></div>`
                : '';
            if (layout.target_device) {
                // show target device hint
            }

            return `
                ${bar}
                <div class="table-wrap"><table>
                    <thead><tr><th>Label</th><th>Size</th><th>Filesystem</th><th>Mountpoint</th><th>Flags</th><th>Device</th></tr></thead>
                    <tbody>${rows}</tbody>
                </table></div>
                ${bootloader}`;
        };

        let effectiveSection = '';
        if (effective) {
            const src = sourceLabel[effective.source] || `<span class="badge badge-neutral">${escHtml(effective.source)}</span>`;
            // C3-17: "Customize Layout" is collapsed by default unless the node already has its own override.
            const customizeOpen = effective.source === 'node' ? ' open' : '';
            const customizeSummaryHint = effective.source === 'node'
                ? '<span style="font-size:11px;color:var(--text-secondary);margin-left:8px;font-weight:400">node override active</span>'
                : '<span style="font-size:11px;color:var(--text-secondary);margin-left:8px;font-weight:400">inheriting — click to override</span>';
            const customizeDetails = `
                <details${customizeOpen} style="margin-top:12px;border:1px solid var(--border);border-radius:6px">
                    <summary style="padding:8px 12px;cursor:pointer;font-size:13px;font-weight:600;user-select:none">
                        Customize Layout${customizeSummaryHint}
                    </summary>
                    <div style="padding:12px;border-top:1px solid var(--border);display:flex;gap:8px;align-items:center">
                        <button class="btn btn-secondary btn-sm" onclick='Pages._showLayoutOverrideEditor(${JSON.stringify(nodeId)}, ${JSON.stringify(JSON.stringify(effective.layout))})'>
                            Edit Override
                        </button>
                        ${effective.source !== 'image' ? `<button class="btn btn-secondary btn-sm" onclick='Pages._clearLayoutOverride(${JSON.stringify(nodeId)})'>Clear Override</button>` : ''}
                        <span style="font-size:12px;color:var(--text-secondary)">Override replaces the ${escHtml(effective.source)} default for this node only.</span>
                    </div>
                </details>`;
            effectiveSection = cardWrap(
                `Current Effective Layout &nbsp;${src}`,
                `${layoutToTable(effective.layout)}${customizeDetails}`
            );
        }

        let recSection = '';
        if (rec) {
            const warnings = (rec.warnings || []).map(w =>
                `<div class="alert alert-warning" style="margin:4px 0;font-size:12px">${escHtml(w)}</div>`).join('');
            recSection = cardWrap(
                'Recommended Layout',
                `${layoutToTable(rec.layout)}
                ${warnings}
                ${rec.reasoning ? `<details style="margin-top:12px"><summary style="cursor:pointer;font-size:12px;color:var(--text-secondary)">Reasoning</summary><pre style="font-size:11px;margin-top:8px;white-space:pre-wrap;color:var(--text-secondary)">${escHtml(rec.reasoning)}</pre></details>` : ''}`,
                `<button class="btn btn-primary btn-sm" onclick='Pages._applyRecommendedLayout(${JSON.stringify(nodeId)}, ${JSON.stringify(JSON.stringify(rec.layout))})'>Apply Recommended Layout</button>`
            );
        } else if (rec === null) {
            recSection = cardWrap('Recommended Layout',
                emptyState('No recommendation available', 'Hardware profile not yet discovered (node must PXE-boot to register hardware).'));
        }

        return effectiveSection + recSection;
    },

    async _applyRecommendedLayout(nodeId, layoutJSON) {
        const layout = JSON.parse(layoutJSON);
        Pages.showConfirmModal({
            title: 'Apply Recommended Layout',
            message: 'Apply the recommended disk layout as a node-level override?<br><br>This will override the image/group default for this node only.',
            confirmText: 'Apply',
            onConfirm: async () => {
                try {
                    await API.request('PUT', `/nodes/${nodeId}/layout-override`, { layout });
                    App.toast('Recommended layout applied as node override', 'success');
                    Pages._onDiskLayoutTabOpen(nodeId);
                } catch (e) {
                    App.toast(`Failed to apply layout: ${e.message}`, 'error');
                }
            },
        });
    },

    async _clearLayoutOverride(nodeId) {
        Pages.showConfirmModal({
            title: 'Clear Layout Override',
            message: 'Clear the node-level disk layout override?<br><br>The group or image default will be used instead.',
            confirmText: 'Clear Override',
            danger: true,
            onConfirm: async () => {
                try {
                    await API.request('PUT', `/nodes/${nodeId}/layout-override`, { clear_layout_override: true });
                    App.toast('Node layout override cleared — using group or image default', 'success');
                    Pages._onDiskLayoutTabOpen(nodeId);
                } catch (e) {
                    App.toast(`Failed to clear override: ${e.message}`, 'error');
                }
            },
        });
    },

    _showLayoutOverrideEditor(nodeId, layoutJSON) {
        const layout = JSON.parse(layoutJSON);
        // Build an editable partition table in a modal.
        const rows = (layout.partitions || []).map((p, i) => `
            <tr>
                <td><input type="text" value="${escHtml(p.label||'')}" onchange="Pages._layoutEditorUpdate(${i},'label',this.value)" style="width:90px"></td>
                <td>
                    <input type="text" value="${p.size_bytes ? fmtBytes(p.size_bytes) : 'fill'}" onchange="Pages._layoutEditorParseSizeInput(${i},this.value)" style="width:80px" placeholder="e.g. 100GB or fill">
                </td>
                <td>
                    <select onchange="Pages._layoutEditorUpdate(${i},'filesystem',this.value)">
                        ${[['xfs','xfs'],['ext4','ext4'],['vfat','vfat (ESP/FAT)'],['swap','swap'],['biosboot','biosboot'],['','none (raw/LVM PV)']].map(([val,lbl]) =>
                            `<option value="${val}" ${(p.filesystem||'')===val?'selected':''}>${lbl}</option>`).join('')}
                    </select>
                </td>
                <td><input type="text" value="${escHtml(p.mountpoint||'')}" onchange="Pages._layoutEditorUpdate(${i},'mountpoint',this.value)" style="width:90px"></td>
                <td>
                    <button class="btn btn-danger btn-sm" onclick="Pages._layoutEditorRemoveRow(${i})" style="padding:2px 8px">✕</button>
                </td>
            </tr>`).join('');

        const overlay = document.createElement('div');
        overlay.id = 'layout-editor-modal';
        overlay.className = 'modal-overlay';
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        overlay.innerHTML = `
            <div class="modal" style="max-width:720px;width:95vw">
                <div class="modal-header"><h2>Edit Disk Layout Override</h2></div>
                <div class="modal-body" style="padding:20px">
                    <div id="layout-editor-warnings" style="margin-bottom:10px"></div>
                    <div class="table-wrap">
                        <table id="layout-editor-table">
                            <thead><tr><th>Label</th><th>Size</th><th>Filesystem</th><th>Mountpoint</th><th></th></tr></thead>
                            <tbody id="layout-editor-tbody">${rows}</tbody>
                        </table>
                    </div>
                    <div style="margin-top:10px;display:flex;gap:8px">
                        <button class="btn btn-secondary btn-sm" onclick="Pages._layoutEditorAddRow()">Add Partition</button>
                        <button class="btn btn-secondary btn-sm" onclick="Pages._layoutEditorFillLast()">Set Last → Fill</button>
                    </div>
                    <div id="layout-editor-result" style="margin-top:10px"></div>
                    <div class="form-actions" style="margin-top:16px">
                        <button class="btn btn-secondary" onclick="document.getElementById('layout-editor-modal').remove()">Cancel</button>
                        <button class="btn btn-primary" id="layout-save-btn" onclick="Pages._layoutEditorSave('${nodeId}')">Save Override</button>
                    </div>
                </div>
            </div>`;
        // Store current layout state on the element.
        overlay._layoutState = JSON.parse(JSON.stringify(layout));
        overlay._nodeId = nodeId;
        document.body.appendChild(overlay);
        overlay.addEventListener('click', e => { if (e.target === overlay) overlay.remove(); });
        trapModalFocus(overlay, () => overlay.remove());
    },

    _getLayoutEditorModal() {
        return document.getElementById('layout-editor-modal');
    },

    _layoutEditorUpdate(idx, field, value) {
        const modal = this._getLayoutEditorModal();
        if (!modal) return;
        modal._layoutState.partitions[idx][field] = value;
        this._layoutEditorValidate(modal);
    },

    _layoutEditorParseSizeInput(idx, value) {
        const modal = this._getLayoutEditorModal();
        if (!modal) return;
        const trimmed = value.trim().toLowerCase();
        let bytes = 0;
        if (trimmed === 'fill' || trimmed === '0' || trimmed === '') {
            bytes = 0;
        } else {
            const match = trimmed.match(/^([\d.]+)\s*(mb|gb|tb|kb|b)?$/);
            if (match) {
                const n = parseFloat(match[1]);
                const unit = match[2] || 'b';
                const mult = {b:1, kb:1024, mb:1024**2, gb:1024**3, tb:1024**4};
                bytes = Math.round(n * (mult[unit]||1));
            }
        }
        modal._layoutState.partitions[idx].size_bytes = bytes;
        this._layoutEditorValidate(modal);
    },

    _layoutEditorRemoveRow(idx) {
        const modal = this._getLayoutEditorModal();
        if (!modal) return;
        modal._layoutState.partitions.splice(idx, 1);
        // Re-render the table body.
        this._layoutEditorRebuildRows(modal);
        this._layoutEditorValidate(modal);
    },

    _layoutEditorAddRow() {
        const modal = this._getLayoutEditorModal();
        if (!modal) return;
        modal._layoutState.partitions.push({ label: '', size_bytes: 0, filesystem: 'xfs', mountpoint: '' });
        this._layoutEditorRebuildRows(modal);
    },

    _layoutEditorFillLast() {
        const modal = this._getLayoutEditorModal();
        if (!modal || !modal._layoutState.partitions.length) return;
        const last = modal._layoutState.partitions[modal._layoutState.partitions.length - 1];
        last.size_bytes = 0;
        this._layoutEditorRebuildRows(modal);
    },

    _layoutEditorRebuildRows(modal) {
        const tbody = document.getElementById('layout-editor-tbody');
        if (!tbody) return;
        const parts = modal._layoutState.partitions;
        tbody.innerHTML = parts.map((p, i) => `
            <tr>
                <td><input type="text" value="${escHtml(p.label||'')}" onchange="Pages._layoutEditorUpdate(${i},'label',this.value)" style="width:90px"></td>
                <td><input type="text" value="${p.size_bytes ? fmtBytes(p.size_bytes) : 'fill'}" onchange="Pages._layoutEditorParseSizeInput(${i},this.value)" style="width:80px"></td>
                <td><select onchange="Pages._layoutEditorUpdate(${i},'filesystem',this.value)">${
                    [['xfs','xfs'],['ext4','ext4'],['vfat','vfat (ESP/FAT)'],['swap','swap'],['biosboot','biosboot'],['','none (raw/LVM PV)']].map(([val,lbl]) =>
                        `<option value="${val}" ${(p.filesystem||'')===val?'selected':''}>${lbl}</option>`).join('')
                }</select></td>
                <td><input type="text" value="${escHtml(p.mountpoint||'')}" onchange="Pages._layoutEditorUpdate(${i},'mountpoint',this.value)" style="width:90px"></td>
                <td><button class="btn btn-danger btn-sm" onclick="Pages._layoutEditorRemoveRow(${i})" style="padding:2px 8px">✕</button></td>
            </tr>`).join('');
        this._layoutEditorValidate(modal);
    },

    _layoutEditorValidate(modal) {
        const warningsEl = document.getElementById('layout-editor-warnings');
        const saveBtn = document.getElementById('layout-save-btn');
        if (!warningsEl || !modal) return;
        const parts = modal._layoutState.partitions;
        const errs = [];
        const hasRoot = parts.some(p => p.mountpoint === '/');
        if (!hasRoot) errs.push('Must have a / (root) partition');
        const fillCount = parts.filter(p => !p.size_bytes).length;
        if (fillCount > 1) errs.push('Only one partition may use "fill" (size_bytes = 0)');
        // ESP must be vfat — UEFI firmware cannot read other filesystem types.
        for (const p of parts) {
            const isESP = p.mountpoint === '/boot/efi' || (p.flags || []).includes('esp');
            const fs = (p.filesystem || '').toLowerCase();
            if (isESP && fs !== '' && fs !== 'vfat' && fs !== 'fat32' && fs !== 'fat') {
                errs.push(`ESP partition (${p.mountpoint || p.label || '/boot/efi'}) must use vfat — UEFI firmware cannot read "${p.filesystem}"`);
            }
            // Swap mountpoint must pair with swap filesystem.
            if (p.mountpoint === 'swap' && fs !== '' && fs !== 'swap') {
                errs.push(`Partition with mountpoint "swap" must use the swap filesystem, not "${p.filesystem}"`);
            }
        }
        warningsEl.innerHTML = errs.map(e => `<div class="alert alert-error" style="margin:2px 0;font-size:12px">${escHtml(e)}</div>`).join('');
        if (saveBtn) saveBtn.disabled = errs.length > 0;
    },

    async _layoutEditorSave(nodeId) {
        const modal = this._getLayoutEditorModal();
        if (!modal) return;
        const saveBtn = document.getElementById('layout-save-btn');
        const resultEl = document.getElementById('layout-editor-result');
        if (saveBtn) { saveBtn.disabled = true; saveBtn.textContent = 'Saving…'; }
        try {
            await API.request('PUT', `/nodes/${nodeId}/layout-override`, { layout: modal._layoutState });
            modal.remove();
            Pages._onDiskLayoutTabOpen(nodeId);
        } catch (e) {
            if (resultEl) resultEl.innerHTML = `<div class="alert alert-error">${escHtml(e.message)}</div>`;
            if (saveBtn) { saveBtn.disabled = false; saveBtn.textContent = 'Save Override'; }
        }
    },

    // _onBMCTabOpen is called when the user clicks the Power / IPMI tab.
    // Starts a 20-second auto-refresh for power status and a 30-second refresh
    // for sensors. Clears both when the user navigates away (Router stop/start).
    _onBMCTabOpen(nodeId, hasBMC) {
        if (!hasBMC) return;
        // Clear any existing IPMI timers.
        if (Pages._powerTimer)  { clearInterval(Pages._powerTimer);  Pages._powerTimer  = null; }
        if (Pages._sensorTimer) { clearInterval(Pages._sensorTimer); Pages._sensorTimer = null; }
        // Load sensors immediately, then every 30s.
        Pages._refreshSensors(nodeId);
        Pages._sensorTimer = setInterval(() => Pages._refreshSensors(nodeId), 30000);
        // Power status is already being fetched; start auto-refresh every 20s.
        Pages._powerTimer = setInterval(() => Pages._refreshPowerStatus(nodeId), 20000);
    },

    // S5-12: _onConfigHistoryTabOpen loads config change history for the given node.
    // Paginates server-side via GET /api/v1/nodes/:id/config-history?page=&per_page=.
    async _onConfigHistoryTabOpen(nodeId, page = 1) {
        // C3-7: limit=50, "Load more" append pattern instead of Previous/Next.
        const PER_PAGE = 50;
        const wrap = document.getElementById('confighistory-wrap');
        if (!wrap) return;

        // On first load (page 1) replace wrap. On subsequent loads append rows.
        if (page === 1) {
            wrap.innerHTML = loading('Loading history…');
        } else {
            // Remove "Load more" button while fetching.
            const prev = wrap.querySelector('#confighistory-load-more');
            if (prev) { prev.textContent = 'Loading…'; prev.disabled = true; }
        }

        try {
            const resp = await API.nodes.configHistory(nodeId, page, PER_PAGE);
            const rows    = (resp && resp.history)  || [];
            const total   = (resp && resp.total)    || 0;
            const perPage = (resp && resp.per_page) || PER_PAGE;

            if (!rows.length && page === 1) {
                wrap.innerHTML = emptyState('No config changes recorded', 'Configuration changes are recorded each time a node is updated.');
                return;
            }

            const hasMore = page * perPage < total;
            const rowsHtml = rows.map(r => `<tr>
                <td class="text-mono text-sm">${escHtml(r.field_name)}</td>
                <td class="text-dim text-sm" style="max-width:200px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="${escHtml(r.old_value)}">${r.old_value ? escHtml(r.old_value) : '<span class="text-dim">—</span>'}</td>
                <td class="text-sm" style="max-width:200px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="${escHtml(r.new_value)}">${r.new_value ? escHtml(r.new_value) : '<span class="text-dim">—</span>'}</td>
                <td class="text-dim text-sm">${r.actor_label ? escHtml(r.actor_label) : '—'}</td>
                <td class="text-dim text-sm">${fmtRelative(r.changed_at ? new Date(r.changed_at * 1000).toISOString() : null)}</td>
            </tr>`).join('');

            if (page === 1) {
                // First load: render full table + optional load-more footer.
                wrap.innerHTML = `
                    <div class="table-wrap"><table aria-label="Config change history">
                        <thead><tr>
                            <th>Field</th><th>Old Value</th><th>New Value</th><th>Changed By</th><th>When</th>
                        </tr></thead>
                        <tbody id="confighistory-tbody">${rowsHtml}</tbody>
                    </table></div>
                    <div id="confighistory-footer" style="margin-top:8px">
                        ${hasMore
                            ? `<button id="confighistory-load-more" class="btn btn-secondary btn-sm" onclick="Pages._onConfigHistoryTabOpen('${escHtml(nodeId)}', 2)">Load more (${total - rows.length} remaining)</button>`
                            : `<span class="text-dim text-sm">${total} change${total !== 1 ? 's' : ''} total</span>`}
                    </div>`;
            } else {
                // Append rows to existing tbody and update footer.
                const tbody = wrap.querySelector('#confighistory-tbody');
                if (tbody) tbody.insertAdjacentHTML('beforeend', rowsHtml);
                const footer = wrap.querySelector('#confighistory-footer');
                const loaded = page * perPage;
                if (footer) {
                    footer.innerHTML = hasMore
                        ? `<button id="confighistory-load-more" class="btn btn-secondary btn-sm" onclick="Pages._onConfigHistoryTabOpen('${escHtml(nodeId)}', ${page + 1})">Load more (${total - loaded} remaining)</button>`
                        : `<span class="text-dim text-sm">${total} change${total !== 1 ? 's' : ''} total</span>`;
                }
            }
        } catch (e) {
            if (page === 1) {
                wrap.innerHTML = alertBox(`Failed to load config history: ${e.message}`);
            } else {
                App.toast(`Failed to load more: ${e.message}`, 'error');
                const prev = wrap.querySelector('#confighistory-load-more');
                if (prev) { prev.textContent = 'Load more'; prev.disabled = false; }
            }
        }
    },

    // _refreshPowerStatus fetches the current power status from the server and
    // updates the status indicator, label, and button disabled states.
    async _refreshPowerStatus(nodeId) {
        const indicator = document.getElementById('power-indicator');
        const label     = document.getElementById('power-label');
        const lastEl    = document.getElementById('power-last-checked');
        const errEl     = document.getElementById('power-error-msg');
        if (!indicator) return; // tab not visible

        try {
            const data = await API.nodes.power.status(nodeId);
            const status = data.status || 'unknown';

            // Colour-code the status indicator.
            const colours = { on: 'var(--success)', off: 'var(--text-dim)', unknown: '#f59e0b', error: 'var(--error)' };
            const labels  = { on: 'Power On', off: 'Power Off', unknown: 'Unknown', error: 'BMC Unreachable' };
            indicator.style.background = colours[status] || colours.unknown;
            if (label) label.textContent = labels[status] || status;
            if (lastEl && data.last_checked) {
                lastEl.textContent = 'Last checked ' + fmtRelative(data.last_checked);
            }
            if (errEl) {
                if (data.error) {
                    errEl.textContent = data.error;
                    errEl.style.display = '';
                } else {
                    errEl.style.display = 'none';
                }
            }
            // Disable buttons that don't make sense for the current state.
            const btnOn    = document.getElementById('btn-power-on');
            const btnOff   = document.getElementById('btn-power-off');
            const btnCycle = document.getElementById('btn-power-cycle');
            const btnReset = document.getElementById('btn-power-reset');
            if (btnOn)    btnOn.disabled    = (status === 'on');
            if (btnOff)   btnOff.disabled   = (status === 'off');
            if (btnCycle) btnCycle.disabled = (status === 'off');
            if (btnReset) btnReset.disabled = (status === 'off');
        } catch (e) {
            if (label) label.textContent = 'Error';
            if (errEl) { errEl.textContent = e.message; errEl.style.display = ''; }
        }
    },

    // _doPowerAction executes a power action without a confirmation dialog.
    // Used for non-destructive actions (power on, boot-to-disk).
    async _doPowerAction(nodeId, action) {
        const feedback = document.getElementById('power-action-feedback');
        if (feedback) { feedback.textContent = `Sending ${action}…`; feedback.style.display = ''; feedback.className = 'alert alert-info'; }
        try {
            const fn = {
                on: ()   => API.nodes.power.on(nodeId),
                disk: () => API.nodes.power.diskBoot(nodeId),
            }[action];
            if (!fn) return;
            await fn();
            if (feedback) { feedback.textContent = `${action} command sent.`; feedback.className = 'alert alert-info'; }
            // Refresh status after a short delay to let BMC process the command.
            setTimeout(() => Pages._refreshPowerStatus(nodeId), 2000);
        } catch (e) {
            if (feedback) { feedback.textContent = `Error: ${e.message}`; feedback.className = 'alert alert-error'; }
        }
    },

    // _confirmPowerAction shows a modal dialog before executing a destructive power action.
    _confirmPowerAction(nodeId, action, title, description) {
        Pages.showConfirmModal({
            title,
            message: `${escHtml(description)}<br><br>Are you sure?`,
            confirmText: title,
            danger: true,
            onConfirm: () => {
                const actionFns = {
                    off:   () => API.nodes.power.off(nodeId),
                    cycle: () => API.nodes.power.cycle(nodeId),
                    reset: () => API.nodes.power.reset(nodeId),
                    pxe:   () => API.nodes.power.pxeBoot(nodeId),
                };
                const fn = actionFns[action];
                if (!fn) return;
                const feedback = document.getElementById('power-action-feedback');
                if (feedback) { feedback.textContent = `Sending ${action} command…`; feedback.style.display = ''; feedback.className = 'alert alert-info'; }
                fn().then(() => {
                    if (feedback) { feedback.textContent = `${title} command sent.`; }
                    setTimeout(() => Pages._refreshPowerStatus(nodeId), 2000);
                }).catch(e => {
                    if (feedback) { feedback.textContent = `Error: ${e.message}`; feedback.className = 'alert alert-error'; }
                });
            },
        });
    },

    // _refreshSensors fetches sensor readings and renders them in the sensor table.
    async _refreshSensors(nodeId) {
        const wrap = document.getElementById('sensor-table-wrap');
        if (!wrap) return;
        try {
            const data = await API.nodes.sensors(nodeId);
            const sensors = data.sensors || [];
            if (!sensors.length) {
                wrap.innerHTML = `<div style="padding:12px;color:var(--text-dim);font-size:13px">No sensor readings returned by BMC.</div>`;
                return;
            }
            const statusColour = { ok: 'var(--success)', warning: '#f59e0b', critical: 'var(--error)' };
            wrap.innerHTML = `<div class="table-wrap"><table>
                <thead><tr><th>Sensor</th><th>Value</th><th>Units</th><th>Status</th></tr></thead>
                <tbody>
                ${sensors.map(s => `<tr>
                    <td>${escHtml(s.name)}</td>
                    <td class="mono">${escHtml(s.value || '—')}</td>
                    <td class="text-dim">${escHtml(s.units || '—')}</td>
                    <td><span style="color:${statusColour[s.status] || 'inherit'};font-weight:500">${escHtml(s.status)}</span></td>
                </tr>`).join('')}
                </tbody>
            </table></div>`;
        } catch (e) {
            wrap.innerHTML = `<div style="padding:12px;color:var(--error);font-size:12px">Sensor read failed: ${escHtml(e.message)}</div>`;
        }
    },

    _hardwareProfile(hw) {
        const sections = [];

        if ((hw.Disks && hw.Disks.length) || (hw.MDArrays && hw.MDArrays.length)) {
            let diskHtml = '';

            if (hw.MDArrays && hw.MDArrays.length) {
                diskHtml += `<div style="margin-bottom:16px">
                    <div class="kv-key" style="margin-bottom:8px">Software RAID Arrays</div>
                    <div class="table-wrap"><table>
                        <thead><tr><th>Array</th><th>Level</th><th>State</th><th>Size</th><th>Chunk</th><th>Members</th><th>FS</th><th>Mountpoint</th></tr></thead>
                        <tbody>
                        ${hw.MDArrays.map(a => `<tr>
                            <td class="mono text-accent">${escHtml(a.name)}</td>
                            <td class="mono dim">${escHtml(a.level || '—')}</td>
                            <td>${this._raidStateBadge(a.state)}</td>
                            <td class="mono dim">${fmtBytes(a.size_bytes)}</td>
                            <td class="mono dim">${a.chunk_kb ? a.chunk_kb + 'K' : '—'}</td>
                            <td class="mono dim" style="white-space:normal">${(a.members || []).join(', ') || '—'}</td>
                            <td class="mono dim">${escHtml(a.filesystem || '—')}</td>
                            <td class="mono dim">${escHtml(a.mountpoint || '—')}</td>
                        </tr>`).join('')}
                        </tbody>
                    </table></div>
                </div>`;
            }

            if (hw.Disks && hw.Disks.length) {
                diskHtml += hw.Disks.map(d => {
                    const parts = d.Partitions || [];
                    const segColors = ['seg-boot', 'seg-root', 'seg-swap', 'seg-data', 'seg-other'];
                    const totalSz = d.Size || 1;
                    const barHtml = parts.map((p, i) => {
                        const pct = Math.max(3, Math.round(((p.Size || 0) / totalSz) * 100));
                        const cls = segColors[i % segColors.length];
                        return `<div class="${cls} disk-segment" style="flex:${pct}" title="${escHtml(p.Name || '')}: ${fmtBytes(p.Size)}">
                            ${pct > 8 ? escHtml(p.Name || '') : ''}
                        </div>`;
                    }).join('');

                    return `<div style="margin-bottom:16px">
                        <div style="font-size:12px;font-family:var(--font-mono);font-weight:600;color:var(--text-secondary);margin-bottom:6px">
                            /dev/${escHtml(d.Name)} — ${fmtBytes(d.Size)}
                            ${d.Model ? `<span style="font-weight:400"> (${escHtml(d.Model)})</span>` : ''}
                            <span class="badge badge-neutral badge-sm" style="margin-left:6px">${d.Rotational ? 'HDD' : 'SSD'}</span>
                            ${d.Transport ? `<span class="badge badge-neutral badge-sm">${escHtml(d.Transport)}</span>` : ''}
                        </div>
                        ${parts.length ? `<div class="disk-bar">${barHtml}</div>` : ''}
                    </div>`;
                }).join('');
            }

            sections.push(cardWrap('Disk Topology', diskHtml));
        }

        if (hw.IBDevices && hw.IBDevices.length) {
            const ibHtml = hw.IBDevices.map(dev => `
                <div style="margin-bottom:16px;padding-bottom:16px;border-bottom:1px solid var(--border)">
                    <div class="text-mono text-accent" style="font-size:13px;font-weight:600;margin-bottom:10px">${escHtml(dev.Name)}</div>
                    <div class="kv-grid mb-12">
                        <div class="kv-item"><div class="kv-key">Board ID</div><div class="kv-value">${escHtml(dev.BoardID || '—')}</div></div>
                        <div class="kv-item"><div class="kv-key">Firmware</div><div class="kv-value">${escHtml(dev.FWVersion || '—')}</div></div>
                        <div class="kv-item"><div class="kv-key">Node GUID</div><div class="kv-value" style="font-size:11px">${escHtml(dev.NodeGUID || '—')}</div></div>
                    </div>
                    ${dev.Ports && dev.Ports.length ? `
                    <div class="table-wrap"><table>
                        <thead><tr><th>Port</th><th>State</th><th>Phys State</th><th>Rate</th><th>LID</th><th>Link Layer</th><th>GID</th></tr></thead>
                        <tbody>
                        ${dev.Ports.map(p => `<tr>
                            <td class="mono">${p.Number}</td>
                            <td>${this._ibStateBadge(p.State)}</td>
                            <td class="mono dim">${escHtml(p.PhysState || '—')}</td>
                            <td class="mono dim">${escHtml(p.Rate || '—')}</td>
                            <td class="mono dim">${escHtml(p.LID || '—')}</td>
                            <td class="mono dim">${escHtml(p.LinkLayer || '—')}</td>
                            <td class="mono dim" style="font-size:11px">${escHtml(p.GID || '—')}</td>
                        </tr>`).join('')}
                        </tbody>
                    </table></div>` : ''}
                </div>`).join('');
            sections.push(cardWrap('InfiniBand Devices', ibHtml));
        }

        if (hw.NICs && hw.NICs.length) {
            const nicHtml = `<div class="table-wrap"><table>
                <thead><tr><th>Interface</th><th>MAC</th><th>Speed</th><th>Driver</th><th>State</th><th>IP (Runtime)</th></tr></thead>
                <tbody>
                ${hw.NICs.map(n => `<tr>
                    <td class="mono" style="color:var(--accent)">${escHtml(n.Name || '—')}</td>
                    <td class="mono dim">${escHtml(n.MAC || n.MACAddress || '—')}</td>
                    <td class="mono dim">${escHtml(n.Speed || '—')}</td>
                    <td class="mono dim">${escHtml(n.Driver || '—')}</td>
                    <td>${this._nicStateBadge(n.State || n.OperState)}</td>
                    <td class="mono dim">${(n.Addresses || []).join(', ') || '—'}</td>
                </tr>`).join('')}
                </tbody>
            </table></div>`;
            sections.push(cardWrap('NICs', nicHtml));
        }

        // S5-2: GPU inventory — populated via PCI sysfs enumeration in initramfs.
        if (hw.GPUs && hw.GPUs.length) {
            const gpuHtml = `<div class="table-wrap"><table>
                <thead><tr><th>Model</th><th>Vendor ID</th><th>Device ID</th><th>PCI Address</th><th>VRAM</th></tr></thead>
                <tbody>
                ${hw.GPUs.map(g => `<tr>
                    <td style="font-weight:500">${escHtml(g.model || g.Model || '—')}</td>
                    <td class="mono dim">${escHtml(g.vendor_id || g.VendorID || '—')}</td>
                    <td class="mono dim">${escHtml(g.device_id || g.DeviceID || '—')}</td>
                    <td class="mono dim">${escHtml(g.pci_address || g.PCIAddress || '—')}</td>
                    <td class="mono dim">${(g.vram_bytes || g.VRAMBytes) ? fmtBytes(g.vram_bytes || g.VRAMBytes) : '—'}</td>
                </tr>`).join('')}
                </tbody>
            </table></div>`;
            sections.push(cardWrap(`GPUs (${hw.GPUs.length})`, gpuHtml));
        }

        if (hw.BMC) {
            const bmcHtml = `<div class="kv-grid">
                <div class="kv-item"><div class="kv-key">IP</div><div class="kv-value">${escHtml(hw.BMC.IPAddress || '—')}</div></div>
                <div class="kv-item"><div class="kv-key">Firmware</div><div class="kv-value">${escHtml(hw.BMC.FirmwareVersion || '—')}</div></div>
                <div class="kv-item"><div class="kv-key">Manufacturer</div><div class="kv-value">${escHtml(hw.BMC.Manufacturer || '—')}</div></div>
            </div>`;
            sections.push(cardWrap('BMC / IPMI (Discovered)', bmcHtml));
        }

        if (!sections.length) {
            return `<div class="card"><div class="card-body">${emptyState('No hardware data', 'Detailed hardware profile is populated when the node registers via PXE.')}</div></div>`;
        }

        return sections.join('');
    },

    _raidStateBadge(state) {
        const cls = { active: 'badge-ready', degraded: 'badge-warning', rebuilding: 'badge-info' }[state] || 'badge-neutral';
        return `<span class="badge ${cls}">${escHtml(state || '—')}</span>`;
    },

    _ibStateBadge(state) {
        const s = (state || '').toUpperCase();
        const cls = s === 'ACTIVE' ? 'badge-ready' : s === 'DOWN' ? 'badge-error' : 'badge-warning';
        return `<span class="badge ${cls}">${escHtml(state || '—')}</span>`;
    },

    _nicStateBadge(state) {
        const s = (state || '').toLowerCase();
        const cls = s === 'up' ? 'badge-ready' : s === 'down' ? 'badge-error' : 'badge-neutral';
        return `<span class="badge ${cls}">${escHtml(state || '—')}</span>`;
    },

    async deleteNodeAndGoBack(id, name) {
        Pages.showConfirmModal({
            title: 'Delete Node',
            message: `Delete node <strong>${escHtml(name)}</strong>? This cannot be undone.`,
            confirmText: 'Delete',
            danger: true,
            onConfirm: async () => {
                try {
                    await API.nodes.del(id);
                    Router.navigate('/nodes');
                } catch (e) {
                    Pages.showAlertModal('Delete Failed', escHtml(e.message));
                }
            },
        });
    },

    // ── Node Groups ───────────────────────────────────────────────────────────

    async nodeGroups() {
        App.render(loading('Loading node groups…'));
        try {
            const [groupsResp, nodesResp] = await Promise.all([
                API.nodeGroups.list(),
                API.nodes.list(),
            ]);
            const groups = (groupsResp && (groupsResp.groups || groupsResp.node_groups)) || [];
            const nodes  = (nodesResp  && nodesResp.nodes)        || [];

            // Count nodes per group.
            const nodeCountMap = {};
            nodes.forEach(n => { if (n.group_id) nodeCountMap[n.group_id] = (nodeCountMap[n.group_id] || 0) + 1; });

            const tbody = groups.map(g => {
                const nodeCount = g.member_count != null ? g.member_count : (nodeCountMap[g.id] || 0);
                const hasLayout = !!(g.disk_layout_override && g.disk_layout_override.partitions && g.disk_layout_override.partitions.length);
                const partCount = hasLayout ? g.disk_layout_override.partitions.length : 0;
                const mountCount = (g.extra_mounts || []).length;
                const roleBadge = g.role
                    ? `<span class="badge badge-neutral badge-sm" style="font-size:10px">${escHtml(g.role)}</span>`
                    : '<span class="text-dim text-sm">—</span>';

                return `<tr data-key="${escHtml(g.id)}">
                    <td style="font-weight:600">
                        <a href="#/nodes/groups/${g.id}" style="color:var(--text-primary)">${escHtml(g.name)}</a>
                    </td>
                    <td>${roleBadge}</td>
                    <td class="text-dim text-sm">${escHtml(g.description || '—')}</td>
                    <td style="text-align:center">
                        ${nodeCount > 0
                            ? `<a href="#/nodes?group=${encodeURIComponent(g.id)}" style="text-decoration:none"><span class="badge badge-info" style="cursor:pointer">${nodeCount}</span></a>`
                            : `<span class="text-dim">0</span>`}
                    </td>
                    <td style="text-align:center">
                        ${hasLayout
                            ? `<span class="badge badge-ready" title="${partCount} partition${partCount !== 1 ? 's' : ''}">yes</span>`
                            : `<span class="text-dim">—</span>`}
                    </td>
                    <td style="text-align:center">
                        ${mountCount > 0
                            ? `<span class="badge badge-neutral">${mountCount}</span>`
                            : `<span class="text-dim">—</span>`}
                    </td>
                    <td class="text-dim text-sm">${fmtRelative(g.updated_at)}</td>
                    <td>
                        <div class="flex gap-6">
                            <button class="btn btn-secondary btn-sm"
                                onclick='Pages.showNodeGroupModal(${JSON.stringify(JSON.stringify(g))})'>Edit</button>
                            <button class="btn btn-primary btn-sm"
                                onclick="Pages.showGroupReimageModal('${escHtml(g.id)}', '${escHtml(g.name)}')">Reimage</button>
                            <button class="btn btn-danger btn-sm"
                                onclick="Pages.deleteNodeGroup('${escHtml(g.id)}', '${escHtml(g.name)}')">Delete</button>
                        </div>
                    </td>
                </tr>`;
            }).join('');

            const tableHtml = groups.length
                ? `<div class="table-wrap"><table>
                    <thead><tr>
                        <th>Name</th>
                        <th>Role</th>
                        <th>Description</th>
                        <th style="text-align:center">Nodes</th>
                        <th style="text-align:center">Disk Layout Override</th>
                        <th style="text-align:center">Extra Mounts</th>
                        <th>Updated</th>
                        <th>Actions</th>
                    </tr></thead>
                    <tbody>${tbody}</tbody>
                </table></div>`
                : emptyState(
                    'No node groups yet',
                    'Groups let you share disk layouts, mounts, and config across nodes with similar roles.',
                    `<button class="btn btn-primary" onclick="Pages.showNodeGroupModal(null)">Create First Group</button>`
                );

            App.render(`
                <div class="page-header">
                    <div>
                        <h1 class="page-title">Node Groups</h1>
                        <div class="page-subtitle">${groups.length} group${groups.length !== 1 ? 's' : ''} defined</div>
                    </div>
                    <button class="btn btn-primary" onclick="Pages.showNodeGroupModal(null)">
                        <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24">
                            <line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/>
                        </svg>
                        Create Group
                    </button>
                </div>

                <div class="tab-bar" style="margin-bottom:20px">
                    <div class="tab" onclick="Router.navigate('/nodes')">All Nodes</div>
                    <div class="tab active" onclick="Router.navigate('/nodes/groups')">Groups</div>
                </div>

                ${cardWrap('All Groups', tableHtml)}
            `);
        } catch (e) {
            App.render(alertBox(`Failed to load node groups: ${e.message}`));
        }
    },

    // nodeGroupDetail shows the group detail page with members and reimage action.
    async nodeGroupDetail(id) {
        App.render(loading('Loading group…'));
        try {
            // GET /node-groups/:id now returns { group, members } directly.
            const [detailResp, allNodesResp] = await Promise.all([
                API.nodeGroups.get(id),
                API.nodes.list(),
            ]);
            // Support both old format (just a NodeGroup) and new format ({group, members}).
            const group = (detailResp && detailResp.group) ? detailResp.group : detailResp;
            const members = (detailResp && detailResp.members) ? detailResp.members
                : ((allNodesResp && allNodesResp.nodes) || []).filter(n => n.group_id === id);
            const nodes = members; // alias
            const hasLayout = !!(group.disk_layout_override && group.disk_layout_override.partitions && group.disk_layout_override.partitions.length);
            const mounts = group.extra_mounts || [];

            const nodesHtml = nodes.length === 0
                ? `<div class="text-dim" style="padding:12px;font-size:13px">No nodes currently assigned to this group.
                   <button class="btn btn-secondary btn-sm" style="margin-left:12px" onclick="Pages.showAddMemberModal('${escHtml(id)}')">Add Node</button></div>`
                : `<div class="table-wrap"><table>
                    <thead><tr><th>Hostname</th><th>MAC</th><th>Status</th><th>Updated</th><th></th></tr></thead>
                    <tbody>
                    ${nodes.map(n => `<tr>
                        <td><a href="#/nodes/${n.id}" style="font-weight:500;color:var(--text-primary)">${escHtml(n.hostname || '(unassigned)')}</a></td>
                        <td class="text-mono text-dim text-sm">${escHtml(n.primary_mac || '—')}</td>
                        <td>${nodeBadge(n)}</td>
                        <td class="text-dim text-sm">${fmtRelative(n.updated_at)}</td>
                        <td><button class="btn btn-danger btn-sm" onclick="Pages.removeGroupMember('${escHtml(id)}', '${escHtml(n.id)}', '${escHtml(n.hostname || n.primary_mac)}')">Remove</button></td>
                    </tr>`).join('')}
                    </tbody>
                </table></div>
                <div style="padding:8px 12px;border-top:1px solid var(--border)">
                    <button class="btn btn-secondary btn-sm" onclick="Pages.showAddMemberModal('${escHtml(id)}')">+ Add Node</button>
                </div>`;

            const layoutHtml = hasLayout
                ? (() => {
                    const parts = group.disk_layout_override.partitions;
                    // C3-17: visual partition bar
                    const totalFixed = parts.reduce((s, p) => s + (p.size_bytes || 0), 0);
                    const hasFill = parts.some(p => !p.size_bytes);
                    const fsColors = { xfs: '#3b82f6', ext4: '#8b5cf6', vfat: '#10b981', swap: '#f59e0b', biosboot: '#6b7280', bios_grub: '#6b7280' };
                    const barParts = parts.map(p => {
                        const pct = (hasFill || totalFixed === 0)
                            ? (p.size_bytes ? Math.round(p.size_bytes / (totalFixed || 1) * 80) : 20)
                            : Math.max(2, Math.round(p.size_bytes / totalFixed * 100));
                        const bg = fsColors[p.filesystem] || '#94a3b8';
                        return `<div style="flex:${pct};background:${bg};min-width:24px;display:flex;align-items:center;justify-content:center;font-size:10px;color:#fff;overflow:hidden;white-space:nowrap;padding:0 4px" title="${escHtml(p.label||p.mountpoint||p.filesystem)}">${escHtml(p.label||p.mountpoint||'')}</div>`;
                    }).join('');
                    const bar = `<div style="display:flex;height:28px;border-radius:6px;overflow:hidden;margin-bottom:12px;border:1px solid var(--border)">${barParts}</div>`;
                    const rows = parts.map(p => `<tr>
                        <td>${escHtml(p.label || '—')}</td>
                        <td>${p.size_bytes ? fmtBytes(p.size_bytes) : '<span class="badge badge-neutral" style="font-size:10px">fill</span>'}</td>
                        <td><span class="badge badge-neutral" style="font-size:10px">${escHtml(p.filesystem || '—')}</span></td>
                        <td class="text-mono">${escHtml(p.mountpoint || '—')}</td>
                    </tr>`).join('');
                    return `${bar}<div class="table-wrap"><table>
                        <thead><tr><th>Label</th><th>Size</th><th>Filesystem</th><th>Mountpoint</th></tr></thead>
                        <tbody>${rows}</tbody>
                    </table></div>`;
                })()
                : `<div class="text-dim" style="padding:12px;font-size:13px">No disk layout override — nodes in this group use their image default.</div>`;

            const mountsHtml = mounts.length === 0
                ? `<div class="text-dim" style="padding:12px;font-size:13px">No extra mounts defined on this group.</div>`
                : `<div class="table-wrap"><table>
                    <thead><tr><th>Source</th><th>Mount Point</th><th>FS Type</th><th>Options</th><th>Auto-mkdir</th><th>Comment</th></tr></thead>
                    <tbody>
                    ${mounts.map(m => `<tr>
                        <td class="text-mono">${escHtml(m.source || '—')}</td>
                        <td class="text-mono">${escHtml(m.mount_point || '—')}</td>
                        <td><span class="badge badge-neutral badge-sm">${escHtml(m.fs_type || '—')}</span></td>
                        <td class="text-mono text-dim" style="font-size:11px">${escHtml(m.options || 'defaults')}</td>
                        <td style="text-align:center">${m.auto_mkdir ? '✓' : '—'}</td>
                        <td class="text-dim" style="font-size:11px">${escHtml(m.comment || '—')}</td>
                    </tr>`).join('')}
                    </tbody>
                </table></div>`;

            App.render(`
                <div class="breadcrumb">
                    <a href="#/nodes">Nodes</a>
                    <span class="breadcrumb-sep">/</span>
                    <a href="#/nodes/groups">Groups</a>
                    <span class="breadcrumb-sep">/</span>
                    <span>${escHtml(group.name)}</span>
                </div>
                <div class="page-header">
                    <div>
                        <h1 class="page-title">${escHtml(group.name)}${group.role ? ` <span class="badge badge-neutral" style="font-size:12px;vertical-align:middle">${escHtml(group.role)}</span>` : ''}</h1>
                        ${group.description ? `<div class="page-subtitle">${escHtml(group.description)}</div>` : ''}
                    </div>
                    <div class="flex gap-8">
                        <button class="btn btn-primary" onclick="Pages.showGroupReimageModal('${escHtml(group.id)}', '${escHtml(group.name)}')">
                            Reimage Group
                        </button>
                        <button class="btn btn-secondary" onclick='Pages.showNodeGroupModal(${JSON.stringify(JSON.stringify(group))})'>Edit Group</button>
                        <button class="btn btn-danger btn-sm" onclick="Pages.deleteNodeGroup('${escHtml(group.id)}', '${escHtml(group.name)}')">Delete</button>
                    </div>
                </div>

                <div class="kv-grid mb-16" style="max-width:480px;margin-bottom:20px">
                    <div class="kv-item"><div class="kv-key">ID</div><div class="kv-value text-mono text-sm">${escHtml(group.id)}</div></div>
                    ${group.role ? `<div class="kv-item"><div class="kv-key">Role</div><div class="kv-value">${escHtml(group.role)}</div></div>` : ''}
                    <div class="kv-item"><div class="kv-key">Nodes</div><div class="kv-value">${nodes.length}</div></div>
                    <div class="kv-item"><div class="kv-key">Created</div><div class="kv-value">${fmtDate(group.created_at)}</div></div>
                    <div class="kv-item"><div class="kv-key">Updated</div><div class="kv-value">${fmtDate(group.updated_at)}</div></div>
                </div>

                ${cardWrap('Nodes in this Group', nodesHtml)}
                ${cardWrap('Disk Layout Override', layoutHtml)}
                ${cardWrap('Extra Mounts', mountsHtml)}
            `);
        } catch (e) {
            App.render(alertBox(`Failed to load group: ${e.message}`));
        }
    },

    // showNodeGroupModal opens the Create or Edit group modal.
    showNodeGroupModal(groupJSON) {
        const group = groupJSON ? JSON.parse(groupJSON) : null;
        const isEdit = !!group;
        const mounts = (group && group.extra_mounts) || [];
        const hasLayout = !!(group && group.disk_layout_override && group.disk_layout_override.partitions && group.disk_layout_override.partitions.length);

        const existingMountRows = mounts.map((m, i) => Pages._ngMountRowHTML(i, m)).join('');
        const existingLayoutRows = hasLayout
            ? group.disk_layout_override.partitions.map((p, i) => Pages._ngLayoutRowHTML(i, p)).join('')
            : '';

        const overlay = document.createElement('div');
        overlay.id = 'node-group-modal';
        overlay.className = 'modal-overlay';
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');

        // Store state for the layout editor.
        overlay._layoutState = hasLayout
            ? JSON.parse(JSON.stringify(group.disk_layout_override))
            : { partitions: [] };

        overlay.innerHTML = `
            <div class="modal" style="max-width:760px;width:96vw;max-height:90vh;overflow-y:auto" aria-labelledby="modal-title-8">
                <div class="modal-header">
                    <span class="modal-title" id="modal-title-8">${isEdit ? 'Edit Group' : 'Create Node Group'}</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('node-group-modal').remove()">×</button>
                </div>
                <div class="modal-body" style="padding:20px">
                    <div id="ng-form-result" style="margin-bottom:10px"></div>

                    <!-- Basic fields -->
                    <div class="form-grid" style="margin-bottom:16px">
                        <div class="form-group">
                            <label>Name <span style="color:var(--error)">*</span></label>
                            <input type="text" id="ng-name" value="${escHtml(group ? group.name : '')}"
                                placeholder="compute-nodes"
                                pattern="^[a-zA-Z0-9][a-zA-Z0-9_\\-]*$"
                                oninput="Pages._ngValidateName(this)"
                                required>
                            <div id="ng-name-hint" class="form-hint" style="min-height:16px"></div>
                        </div>
                        <div class="form-group">
                            <label>Description</label>
                            <input type="text" id="ng-description" value="${escHtml(group ? (group.description || '') : '')}"
                                placeholder="Standard CPU compute nodes">
                        </div>
                        <div class="form-group">
                            <label>Role</label>
                            <select id="ng-role">
                                <option value="" ${!(group && group.role) ? 'selected' : ''}>— None —</option>
                                <option value="compute" ${(group && group.role === 'compute') ? 'selected' : ''}>compute</option>
                                <option value="login" ${(group && group.role === 'login') ? 'selected' : ''}>login</option>
                                <option value="storage" ${(group && group.role === 'storage') ? 'selected' : ''}>storage</option>
                                <option value="gpu" ${(group && group.role === 'gpu') ? 'selected' : ''}>gpu</option>
                                <option value="admin" ${(group && group.role === 'admin') ? 'selected' : ''}>admin</option>
                            </select>
                        </div>
                    </div>

                    <!-- Disk Layout Override -->
                    <details id="ng-layout-details" ${hasLayout ? 'open' : ''} style="margin-bottom:16px;border:1px solid var(--border);border-radius:6px">
                        <summary style="padding:10px 14px;cursor:pointer;font-weight:600;font-size:14px;user-select:none">
                            Disk Layout Override
                            <span style="font-weight:400;font-size:12px;color:var(--text-secondary);margin-left:8px">
                                ${hasLayout ? `${group.disk_layout_override.partitions.length} partition${group.disk_layout_override.partitions.length !== 1 ? 's' : ''} defined` : 'inherits from image'}
                            </span>
                        </summary>
                        <div style="padding:14px;border-top:1px solid var(--border)">
                            <div style="display:flex;align-items:center;gap:10px;margin-bottom:12px">
                                <label style="display:flex;align-items:center;gap:6px;cursor:pointer;font-weight:400">
                                    <input type="radio" name="ng-layout-mode" value="inherit"
                                        ${!hasLayout ? 'checked' : ''} onchange="Pages._ngLayoutModeChange(this.value)">
                                    Inherit from image (default)
                                </label>
                                <label style="display:flex;align-items:center;gap:6px;cursor:pointer;font-weight:400">
                                    <input type="radio" name="ng-layout-mode" value="custom"
                                        ${hasLayout ? 'checked' : ''} onchange="Pages._ngLayoutModeChange(this.value)">
                                    Use custom layout for this group
                                </label>
                            </div>
                            <div id="ng-layout-editor" style="display:${hasLayout ? '' : 'none'}">
                                <div id="ng-layout-warnings" style="margin-bottom:8px"></div>
                                <div class="table-wrap">
                                    <table id="ng-layout-table">
                                        <thead><tr>
                                            <th>Label</th><th>Size</th><th>Filesystem</th><th>Mountpoint</th><th></th>
                                        </tr></thead>
                                        <tbody id="ng-layout-tbody">${existingLayoutRows}</tbody>
                                    </table>
                                </div>
                                <div style="margin-top:8px;display:flex;gap:8px">
                                    <button type="button" class="btn btn-secondary btn-sm" onclick="Pages._ngLayoutAddRow()">Add Partition</button>
                                    <button type="button" class="btn btn-secondary btn-sm" onclick="Pages._ngLayoutFillLast()">Set Last → Fill</button>
                                </div>
                            </div>
                        </div>
                    </details>

                    <!-- Extra Mounts -->
                    <details id="ng-mounts-details" ${mounts.length > 0 ? 'open' : ''} style="margin-bottom:20px;border:1px solid var(--border);border-radius:6px">
                        <summary style="padding:10px 14px;cursor:pointer;font-weight:600;font-size:14px;user-select:none">
                            Extra Mounts
                            <span style="font-weight:400;font-size:12px;color:var(--text-secondary);margin-left:8px" id="ng-mounts-count">
                                ${mounts.length > 0 ? `${mounts.length} mount${mounts.length !== 1 ? 's' : ''} defined` : 'none'}
                            </span>
                        </summary>
                        <div style="padding:14px;border-top:1px solid var(--border)">
                            <div style="display:flex;align-items:center;gap:8px;margin-bottom:10px">
                                <select id="ng-mounts-preset" style="font-size:12px;padding:4px 8px;width:auto">
                                    <option value="">— Apply preset —</option>
                                    <option value="nfs-home">NFS home</option>
                                    <option value="lustre">Lustre scratch</option>
                                    <option value="beegfs">BeeGFS data</option>
                                    <option value="cifs">CIFS / Samba</option>
                                    <option value="bind">Bind mount</option>
                                    <option value="tmpfs">tmpfs</option>
                                </select>
                                <button type="button" class="btn btn-secondary btn-sm" onclick="Pages._ngMountsApplyPreset()">Apply</button>
                                <button type="button" class="btn btn-secondary btn-sm" onclick="Pages._ngMountsAddRow()">+ Add Mount</button>
                            </div>
                            <div id="ng-mounts-wrap">
                                ${mounts.length > 0 ? `<div class="table-wrap" style="overflow-x:auto"><table id="ng-mounts-table" style="width:100%;font-size:12px;border-collapse:collapse">
                                    <thead><tr style="border-bottom:1px solid var(--border)">
                                        <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">Source</th>
                                        <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">Mount Point</th>
                                        <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">FS Type</th>
                                        <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">Options</th>
                                        <th style="text-align:center;padding:4px 6px;font-weight:500;color:var(--text-secondary)" title="Auto-mkdir">mkd</th>
                                        <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">Comment</th>
                                        <th style="padding:4px"></th>
                                    </tr></thead>
                                    <tbody id="ng-mounts-tbody">${existingMountRows}</tbody>
                                </table></div>` : `<div class="table-wrap" style="overflow-x:auto;display:none"><table id="ng-mounts-table" style="width:100%;font-size:12px;border-collapse:collapse">
                                    <thead><tr style="border-bottom:1px solid var(--border)">
                                        <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">Source</th>
                                        <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">Mount Point</th>
                                        <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">FS Type</th>
                                        <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">Options</th>
                                        <th style="text-align:center;padding:4px 6px;font-weight:500;color:var(--text-secondary)" title="Auto-mkdir">mkd</th>
                                        <th style="text-align:left;padding:4px 6px;font-weight:500;color:var(--text-secondary)">Comment</th>
                                        <th style="padding:4px"></th>
                                    </tr></thead>
                                    <tbody id="ng-mounts-tbody"></tbody>
                                </table></div>
                                <div id="ng-mounts-empty" style="text-align:center;padding:16px;color:var(--text-dim);font-size:12px">No mounts configured</div>`}
                            </div>
                        </div>
                    </details>

                    <div class="form-actions">
                        <button class="btn btn-secondary" onclick="document.getElementById('node-group-modal').remove()">Cancel</button>
                        <button class="btn btn-primary" id="ng-save-btn"
                            onclick="Pages._ngSubmit(${isEdit ? `'${escHtml(group.id)}'` : 'null'})">
                            ${isEdit ? 'Save Changes' : 'Create Group'}
                        </button>
                    </div>
                </div>
            </div>`;

        document.body.appendChild(overlay);
        overlay.addEventListener('click', e => { if (e.target === overlay) overlay.remove(); });
        trapModalFocus(overlay, () => overlay.remove());

        // Initialise layout state reference on the overlay.
        // (already set above, but re-read after DOM insert for clarity)
        const modal = document.getElementById('node-group-modal');
        if (modal) modal._layoutState = overlay._layoutState;
    },

    _ngValidateName(input) {
        const hint = document.getElementById('ng-name-hint');
        if (!hint) return;
        const val = input.value;
        if (!val) { hint.textContent = ''; return; }
        if (!/^[a-zA-Z0-9][a-zA-Z0-9_\-]*$/.test(val)) {
            hint.style.color = 'var(--error)';
            hint.textContent = 'Use letters, digits, hyphens, or underscores only';
        } else {
            hint.style.color = 'var(--success)';
            hint.textContent = '';
        }
    },

    _ngLayoutModeChange(mode) {
        const editor = document.getElementById('ng-layout-editor');
        if (!editor) return;
        editor.style.display = mode === 'custom' ? '' : 'none';
    },

    _ngGetModal() {
        return document.getElementById('node-group-modal');
    },

    // ── Group layout editor ───────────────────────────────────────────────────

    _ngLayoutRowHTML(idx, p) {
        p = p || {};
        return `<tr>
            <td><input type="text" value="${escHtml(p.label||'')}"
                onchange="Pages._ngLayoutUpdate(${idx},'label',this.value)" style="width:90px"></td>
            <td><input type="text" value="${p.size_bytes ? fmtBytes(p.size_bytes) : 'fill'}"
                onchange="Pages._ngLayoutParseSize(${idx},this.value)" style="width:80px" placeholder="e.g. 100GB or fill"></td>
            <td><select onchange="Pages._ngLayoutUpdate(${idx},'filesystem',this.value)">
                ${[['xfs','xfs'],['ext4','ext4'],['vfat','vfat (ESP/FAT)'],['swap','swap'],['biosboot','biosboot'],['','none (raw/LVM PV)']].map(([val,lbl]) =>
                    `<option value="${val}" ${(p.filesystem||'')===val?'selected':''}>${lbl}</option>`).join('')}
            </select></td>
            <td><input type="text" value="${escHtml(p.mountpoint||'')}"
                onchange="Pages._ngLayoutUpdate(${idx},'mountpoint',this.value)" style="width:90px"></td>
            <td><button class="btn btn-danger btn-sm" onclick="Pages._ngLayoutRemoveRow(${idx})"
                style="padding:2px 8px">✕</button></td>
        </tr>`;
    },

    _ngLayoutUpdate(idx, field, value) {
        const modal = this._ngGetModal();
        if (!modal) return;
        modal._layoutState.partitions[idx][field] = value;
        this._ngLayoutValidate(modal);
    },

    _ngLayoutParseSize(idx, value) {
        const modal = this._ngGetModal();
        if (!modal) return;
        const trimmed = value.trim().toLowerCase();
        let bytes = 0;
        if (trimmed !== 'fill' && trimmed !== '0' && trimmed !== '') {
            const match = trimmed.match(/^([\d.]+)\s*(mb|gb|tb|kb|b)?$/);
            if (match) {
                const n = parseFloat(match[1]);
                const unit = match[2] || 'b';
                const mult = {b:1, kb:1024, mb:1024**2, gb:1024**3, tb:1024**4};
                bytes = Math.round(n * (mult[unit]||1));
            }
        }
        modal._layoutState.partitions[idx].size_bytes = bytes;
        this._ngLayoutValidate(modal);
    },

    _ngLayoutRemoveRow(idx) {
        const modal = this._ngGetModal();
        if (!modal) return;
        modal._layoutState.partitions.splice(idx, 1);
        this._ngLayoutRebuildRows(modal);
        this._ngLayoutValidate(modal);
    },

    _ngLayoutAddRow() {
        const modal = this._ngGetModal();
        if (!modal) return;
        if (!modal._layoutState) modal._layoutState = { partitions: [] };
        modal._layoutState.partitions.push({ label: '', size_bytes: 0, filesystem: 'xfs', mountpoint: '' });
        this._ngLayoutRebuildRows(modal);
    },

    _ngLayoutFillLast() {
        const modal = this._ngGetModal();
        if (!modal || !modal._layoutState || !modal._layoutState.partitions.length) return;
        modal._layoutState.partitions[modal._layoutState.partitions.length - 1].size_bytes = 0;
        this._ngLayoutRebuildRows(modal);
    },

    _ngLayoutRebuildRows(modal) {
        const tbody = document.getElementById('ng-layout-tbody');
        if (!tbody) return;
        tbody.innerHTML = (modal._layoutState.partitions || []).map((p, i) =>
            this._ngLayoutRowHTML(i, p)
        ).join('');
        this._ngLayoutValidate(modal);
    },

    _ngLayoutValidate(modal) {
        const warnEl  = document.getElementById('ng-layout-warnings');
        const saveBtn = document.getElementById('ng-save-btn');
        if (!warnEl || !modal || !modal._layoutState) return;
        const mode = document.querySelector('input[name="ng-layout-mode"]:checked');
        if (!mode || mode.value !== 'custom') { warnEl.innerHTML = ''; if (saveBtn) saveBtn.disabled = false; return; }
        const parts = modal._layoutState.partitions || [];
        const errs = [];
        if (!parts.some(p => p.mountpoint === '/')) errs.push('Must have a / (root) partition');
        if (parts.filter(p => !p.size_bytes).length > 1) errs.push('Only one partition may use "fill"');
        warnEl.innerHTML = errs.map(e => `<div class="alert alert-error" style="margin:2px 0;font-size:12px">${escHtml(e)}</div>`).join('');
        if (saveBtn) saveBtn.disabled = errs.length > 0;
    },

    // ── Group mounts editor ───────────────────────────────────────────────────

    _ngMountRowHTML(idx, m) {
        m = m || {};
        const fsTypes = ['nfs','nfs4','cifs','beegfs','lustre','gpfs','xfs','ext4','bind','9p','tmpfs','vfat','ext3','smbfs'];
        const fsSelect = fsTypes.map(t =>
            `<option value="${t}"${m.fs_type === t ? ' selected' : ''}>${t}</option>`
        ).join('');
        return `<tr data-ng-mount-idx="${idx}" style="border-bottom:1px solid var(--border)">
            <td style="padding:4px 3px">
                <input type="text" name="ng_mount_source" value="${escHtml(m.source||'')}"
                    placeholder="nfs-server:/export/home" style="width:100%;min-width:130px;font-size:12px" required>
            </td>
            <td style="padding:4px 3px">
                <input type="text" name="ng_mount_point" value="${escHtml(m.mount_point||'')}"
                    placeholder="/mnt/share" style="width:100%;min-width:110px;font-size:12px" required>
            </td>
            <td style="padding:4px 3px">
                <select name="ng_mount_fs_type" style="font-size:12px;padding:2px 4px">${fsSelect}</select>
            </td>
            <td style="padding:4px 3px">
                <input type="text" name="ng_mount_options" value="${escHtml(m.options||'')}"
                    placeholder="defaults,_netdev" style="width:100%;min-width:120px;font-size:12px">
            </td>
            <td style="padding:4px 3px;text-align:center">
                <input type="checkbox" name="ng_mount_auto_mkdir" ${m.auto_mkdir !== false ? 'checked' : ''}>
            </td>
            <td style="padding:4px 3px">
                <input type="text" name="ng_mount_comment" value="${escHtml(m.comment||'')}"
                    placeholder="optional note" style="width:100%;min-width:80px;font-size:12px">
            </td>
            <td style="padding:4px 3px">
                <button type="button" class="btn btn-danger btn-sm"
                    onclick="Pages._ngMountsRemoveRow(this)" style="padding:2px 6px;font-size:11px">✕</button>
            </td>
        </tr>`;
    },

    _ngMountsAddRow(preset) {
        const tbody = document.getElementById('ng-mounts-tbody');
        const wrap  = document.getElementById('ng-mounts-wrap');
        if (!tbody) return;
        const idx = tbody.querySelectorAll('tr').length;
        tbody.insertAdjacentHTML('beforeend', Pages._ngMountRowHTML(idx, preset || {}));
        // Show table if hidden.
        const tableWrap = wrap ? wrap.querySelector('.table-wrap') : null;
        if (tableWrap) tableWrap.style.display = '';
        const empty = document.getElementById('ng-mounts-empty');
        if (empty) empty.remove();
        this._ngUpdateMountsCount();
    },

    _ngMountsRemoveRow(btn) {
        const row = btn.closest('tr');
        if (row) row.remove();
        const tbody = document.getElementById('ng-mounts-tbody');
        const wrap  = document.getElementById('ng-mounts-wrap');
        if (tbody && tbody.querySelectorAll('tr').length === 0) {
            const tableWrap = wrap ? wrap.querySelector('.table-wrap') : null;
            if (tableWrap) tableWrap.style.display = 'none';
            if (wrap && !document.getElementById('ng-mounts-empty')) {
                wrap.insertAdjacentHTML('beforeend',
                    `<div id="ng-mounts-empty" style="text-align:center;padding:16px;color:var(--text-dim);font-size:12px">No mounts configured</div>`);
            }
        }
        this._ngUpdateMountsCount();
    },

    _ngMountsApplyPreset() {
        const sel = document.getElementById('ng-mounts-preset');
        if (!sel || !sel.value) return;
        const presets = {
            'nfs-home': { source: 'nfs-server:/export/home', mount_point: '/home/shared',  fs_type: 'nfs4',   options: 'defaults,_netdev,vers=4',               auto_mkdir: true,  comment: 'NFS home directory' },
            'lustre':   { source: 'mgs@tcp:/scratch',        mount_point: '/scratch',       fs_type: 'lustre', options: 'defaults,_netdev,flock',                auto_mkdir: true,  comment: 'Lustre scratch' },
            'beegfs':   { source: 'beegfs',                  mount_point: '/mnt/beegfs',    fs_type: 'beegfs', options: 'defaults,_netdev',                      auto_mkdir: true,  comment: 'BeeGFS data' },
            'cifs':     { source: '//winserver/share',        mount_point: '/mnt/share',     fs_type: 'cifs',   options: 'defaults,_netdev,vers=3.0,sec=ntlmssp', auto_mkdir: true,  comment: 'CIFS (Windows) share' },
            'bind':     { source: '/data/src',               mount_point: '/data/dst',      fs_type: 'bind',   options: 'defaults,bind',                         auto_mkdir: true,  comment: 'Bind mount' },
            'tmpfs':    { source: 'tmpfs',                   mount_point: '/tmp',           fs_type: 'tmpfs',  options: 'defaults,size=4G,mode=1777',            auto_mkdir: false, comment: 'tmpfs /tmp' },
        };
        const p = presets[sel.value];
        if (p) Pages._ngMountsAddRow(p);
        sel.value = '';
    },

    _ngUpdateMountsCount() {
        const count = document.getElementById('ng-mounts-count');
        if (!count) return;
        const tbody = document.getElementById('ng-mounts-tbody');
        const n = tbody ? tbody.querySelectorAll('tr').length : 0;
        count.textContent = n > 0 ? `${n} mount${n !== 1 ? 's' : ''} defined` : 'none';
    },

    _ngCollectMounts() {
        const tbody = document.getElementById('ng-mounts-tbody');
        if (!tbody) return [];
        const mounts = [];
        tbody.querySelectorAll('tr').forEach(row => {
            const source    = (row.querySelector('[name="ng_mount_source"]')?.value || '').trim();
            const mountPoint = (row.querySelector('[name="ng_mount_point"]')?.value || '').trim();
            const fsType    = row.querySelector('[name="ng_mount_fs_type"]')?.value || 'nfs';
            const options   = (row.querySelector('[name="ng_mount_options"]')?.value || '').trim();
            const autoMkdir = row.querySelector('[name="ng_mount_auto_mkdir"]')?.checked !== false;
            const comment   = (row.querySelector('[name="ng_mount_comment"]')?.value || '').trim();
            if (!source || !mountPoint) return;
            mounts.push({ source, mount_point: mountPoint, fs_type: fsType, options, auto_mkdir: autoMkdir, comment, dump: 0, pass: 0 });
        });
        return mounts;
    },

    // ── Group form submit ─────────────────────────────────────────────────────

    async _ngSubmit(groupId) {
        const nameEl   = document.getElementById('ng-name');
        const descEl   = document.getElementById('ng-description');
        const resultEl = document.getElementById('ng-form-result');
        const saveBtn  = document.getElementById('ng-save-btn');
        const modal    = this._ngGetModal();

        if (!nameEl || !modal) return;

        const name = (nameEl.value || '').trim();
        const desc = (descEl ? descEl.value : '').trim();
        const role = (document.getElementById('ng-role')?.value || '').trim();

        if (!name) {
            if (resultEl) resultEl.innerHTML = `<div class="alert alert-error">Name is required</div>`;
            return;
        }
        if (!/^[a-zA-Z0-9][a-zA-Z0-9_\-]*$/.test(name)) {
            if (resultEl) resultEl.innerHTML = `<div class="alert alert-error">Name must contain only letters, digits, hyphens, and underscores</div>`;
            return;
        }

        // Determine layout override.
        const modeEl = document.querySelector('input[name="ng-layout-mode"]:checked');
        const useCustomLayout = modeEl && modeEl.value === 'custom';
        let diskLayoutOverride = null;
        if (useCustomLayout) {
            const parts = (modal._layoutState && modal._layoutState.partitions) || [];
            if (!parts.some(p => p.mountpoint === '/')) {
                if (resultEl) resultEl.innerHTML = `<div class="alert alert-error">Disk layout must include a root (/) partition</div>`;
                return;
            }
            if (parts.filter(p => !p.size_bytes).length > 1) {
                if (resultEl) resultEl.innerHTML = `<div class="alert alert-error">Only one partition may use "fill"</div>`;
                return;
            }
            diskLayoutOverride = { partitions: parts };
        }

        const extraMounts = this._ngCollectMounts();

        const body = {
            name,
            description: desc,
            role,
            disk_layout_override: diskLayoutOverride,
            extra_mounts: extraMounts,
        };

        if (saveBtn) { saveBtn.disabled = true; saveBtn.textContent = 'Saving…'; }
        if (resultEl) resultEl.innerHTML = '';

        try {
            if (groupId) {
                await API.nodeGroups.update(groupId, body);
                App.toast(`Group "${name}" updated`, 'success');
            } else {
                await API.nodeGroups.create(body);
                App.toast(`Group "${name}" created`, 'success');
            }
            document.getElementById('node-group-modal').remove();
            Pages.nodeGroups();
        } catch (e) {
            if (resultEl) resultEl.innerHTML = `<div class="alert alert-error">${escHtml(e.message)}</div>`;
            if (saveBtn) { saveBtn.disabled = false; saveBtn.textContent = groupId ? 'Save Changes' : 'Create Group'; }
        }
    },

    // deleteNodeGroup deletes a group after checking whether nodes are using it.
    async deleteNodeGroup(id, name) {
        // Fetch current nodes to see if any are using this group.
        let usingNodes = [];
        try {
            const resp = await API.nodes.list();
            usingNodes = ((resp && resp.nodes) || []).filter(n => n.group_id === id);
        } catch (_) {}

        const message = usingNodes.length > 0
            ? (() => {
                const names = usingNodes.map(n => escHtml(n.hostname || n.primary_mac)).join(', ');
                return `<strong>${usingNodes.length} node${usingNodes.length !== 1 ? 's are' : ' is'} using this group:</strong> ${names}.<br><br>Deleting it will remove the group assignment from those nodes but keep them as standalone nodes.`;
            })()
            : `Delete group <strong>${escHtml(name)}</strong>? This cannot be undone.`;

        Pages.showConfirmModal({
            title: 'Delete Node Group',
            message,
            confirmText: 'Delete',
            danger: true,
            onConfirm: async () => {
                try {
                    await API.nodeGroups.del(id);
                    App.toast(`Group "${name}" deleted`, 'success');
                    Pages.nodeGroups();
                } catch (e) {
                    if (e.message && e.message.includes('409')) {
                        Pages.showAlertModal('Cannot Delete Group', 'This group is still in use. Remove all node assignments first.');
                    } else {
                        App.toast(`Delete failed: ${e.message}`, 'error');
                    }
                }
            },
        });
    },

    // ── Build from ISO modal ─────────────────────────────────────────���─────

    // ── Group reimage modal ───────────────────────────────────────────────────

    // showGroupReimageModal opens the Reimage Group modal with image picker,
    // concurrency, and failure threshold settings.
    async showGroupReimageModal(groupId, groupName) {
        let images = [], memberCount = null;
        try {
            // C3-8: fetch images and group detail in parallel for node count preview.
            const [imgResp, groupResp] = await Promise.allSettled([
                API.images.list('ready'),
                API.nodeGroups.get(groupId),
            ]);
            images = (imgResp.status === 'fulfilled' && imgResp.value && imgResp.value.images) || [];
            if (groupResp.status === 'fulfilled') {
                const members = (groupResp.value && groupResp.value.members) || [];
                memberCount = members.length;
            }
        } catch (_) {}

        const imageOptions = images.map(img =>
            `<option value="${escHtml(img.id)}">${escHtml(img.name)} (${escHtml(img.version || img.os || '')})</option>`
        ).join('');

        const overlay = document.createElement('div');
        overlay.id = 'group-reimage-modal';
        overlay.className = 'modal-overlay';
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        overlay.innerHTML = `
            <div class="modal" style="max-width:480px;width:96vw" aria-labelledby="modal-title-9">
                <div class="modal-header">
                    <span class="modal-title" id="modal-title-9">Reimage Group: ${escHtml(groupName)}</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('group-reimage-modal').remove()">&#215;</button>
                </div>
                <div class="modal-body" style="padding:20px">
                    <div id="grm-result" style="margin-bottom:10px"></div>
                    <div class="form-group" style="margin-bottom:14px">
                        <label>Target Image <span style="color:var(--error)">*</span></label>
                        <select id="grm-image" style="width:100%">
                            ${images.length ? imageOptions : '<option value="">No ready images available</option>'}
                        </select>
                    </div>
                    <div class="form-grid" style="margin-bottom:14px">
                        <div class="form-group">
                            <label>
                                Concurrency
                                <span tabindex="0" title="Max nodes reimaged simultaneously in this job. The server&#39;s CLUSTR_REIMAGE_MAX_CONCURRENT setting (default 20) caps the total in-flight reimages across all concurrent jobs — individual job concurrency is honored up to that ceiling."
                                    style="display:inline-block;width:14px;height:14px;border-radius:50%;background:var(--text-secondary);color:var(--bg-primary);font-size:10px;font-weight:700;text-align:center;line-height:14px;cursor:help;margin-left:4px;vertical-align:middle">?</span>
                            </label>
                            <input type="number" id="grm-concurrency" value="5" min="1" max="50">
                            <div class="form-hint">Max nodes reimaged simultaneously in this job</div>
                        </div>
                        <div class="form-group">
                            <label>Pause on failure %</label>
                            <input type="number" id="grm-pause-pct" value="20" min="0" max="100">
                            <div class="form-hint">Pause rollout if this % of a wave fails</div>
                        </div>
                    </div>
                    <div class="form-group" style="margin-bottom:14px">
                        <label style="display:flex;align-items:center;gap:8px;cursor:pointer;font-weight:400">
                            <input type="checkbox" id="grm-dry-run">
                            <span>Dry run — power-cycle and PXE boot nodes but skip disk write</span>
                        </label>
                        <div class="form-hint" style="margin-top:4px">Use for hardware discovery or deploy pipeline testing without overwriting disks.</div>
                    </div>
                    <!-- C3-8: node count preview shown before confirmation -->
                    <div style="background:var(--surface-secondary);border-radius:6px;padding:10px 14px;font-size:12px;color:var(--text-secondary);margin-bottom:16px">
                        ${memberCount !== null
                            ? `<strong>${memberCount} node${memberCount !== 1 ? 's' : ''}</strong> in this group will be power-cycled. Each node will PXE boot and deploy the selected image.`
                            : 'This will power-cycle all nodes in the group. Each node will PXE boot and deploy the selected image.'}
                    </div>
                    <div class="form-actions">
                        <button class="btn btn-secondary" onclick="document.getElementById('group-reimage-modal').remove()">Cancel</button>
                        <button class="btn btn-primary" id="grm-submit" onclick="Pages._submitGroupReimage('${escHtml(groupId)}')">
                            Start Rolling Reimage
                        </button>
                    </div>
                    <div id="grm-job-status" style="display:none;margin-top:16px"></div>
                </div>
            </div>`;
        document.body.appendChild(overlay);
        overlay.addEventListener('click', e => { if (e.target === overlay) overlay.remove(); });
        trapModalFocus(overlay, () => overlay.remove());
    },

    async _submitGroupReimage(groupId) {
        const imageId = document.getElementById('grm-image')?.value;
        const concurrency = parseInt(document.getElementById('grm-concurrency')?.value || '5', 10);
        const pausePct = parseInt(document.getElementById('grm-pause-pct')?.value || '20', 10);
        // S5-7: dry_run checkbox support.
        const dryRun = !!(document.getElementById('grm-dry-run')?.checked);
        const resultEl = document.getElementById('grm-result');
        const submitBtn = document.getElementById('grm-submit');
        const statusEl = document.getElementById('grm-job-status');

        if (!imageId) {
            if (resultEl) resultEl.innerHTML = `<div class="alert alert-error">Please select an image.</div>`;
            return;
        }
        if (submitBtn) { submitBtn.disabled = true; submitBtn.textContent = 'Starting...'; }
        if (resultEl) resultEl.innerHTML = '';

        try {
            const job = await API.nodeGroups.reimage(groupId, {
                image_id: imageId,
                concurrency: concurrency || 5,
                pause_on_failure_pct: pausePct >= 0 ? pausePct : 20,
                dry_run: dryRun,
            });

            if (submitBtn) submitBtn.style.display = 'none';
            if (statusEl) {
                statusEl.style.display = '';
                // C3-19: effective vs requested concurrency.
                const reqConcurrency = concurrency || 5;
                const effConcurrency = job.concurrency || reqConcurrency;
                const concurrencyNote = effConcurrency !== reqConcurrency
                    ? `<span style="color:var(--warning);font-size:11px;margin-left:4px">(requested ${reqConcurrency}, effective ${effConcurrency})</span>`
                    : '';
                statusEl.innerHTML = `
                    <div style="border:1px solid var(--border);border-radius:6px;padding:12px">
                        <div style="font-weight:600;margin-bottom:8px">Reimage Job Started</div>
                        <div class="kv-grid" style="font-size:12px">
                            <div class="kv-item"><div class="kv-key">Job ID</div><div class="kv-value text-mono" style="font-size:11px">${escHtml((job.job_id || '').substring(0, 16))}...</div></div>
                            <div class="kv-item"><div class="kv-key">Status</div><div class="kv-value" id="grm-job-status-val">${escHtml(job.status || 'running')}</div></div>
                            <div class="kv-item"><div class="kv-key">Total Nodes</div><div class="kv-value">${job.total_nodes || 0}</div></div>
                            <div class="kv-item"><div class="kv-key">Concurrency</div><div class="kv-value">${effConcurrency}${concurrencyNote}</div></div>
                            <div class="kv-item"><div class="kv-key">Triggered</div><div class="kv-value" id="grm-triggered">${job.triggered_nodes || 0} / ${job.total_nodes || 0}</div></div>
                        </div>
                        <button class="btn btn-secondary btn-sm" style="margin-top:10px" onclick="document.getElementById('group-reimage-modal').remove()">Close</button>
                    </div>`;
                if (job.job_id) Pages._pollGroupReimageJob(job.job_id);
            }
            App.toast('Rolling reimage started', 'success');
        } catch (e) {
            if (resultEl) resultEl.innerHTML = `<div class="alert alert-error">${escHtml(e.message)}</div>`;
            if (submitBtn) { submitBtn.disabled = false; submitBtn.textContent = 'Start Rolling Reimage'; }
        }
    },

    async _pollGroupReimageJob(jobId) {
        // C3-3: guard — stop polling when the modal is gone.
        const isModalAlive = () => !!document.getElementById('grm-job-status-val');
        const poll = async () => {
            // Stop immediately if the modal has been closed.
            if (!isModalAlive()) return;
            try {
                const job = await API.nodeGroups.jobStatus(jobId);
                // Re-check after the async call (user may have closed during fetch).
                if (!isModalAlive()) return;
                const statusEl = document.getElementById('grm-job-status-val');
                const trigEl = document.getElementById('grm-triggered');
                if (statusEl) statusEl.textContent = job.status || '—';
                if (trigEl) trigEl.textContent = `${job.triggered_nodes || 0} / ${job.total_nodes || 0}`;
                if (job.status === 'complete' || job.status === 'failed' || job.status === 'paused') return;
                setTimeout(poll, 3000);
            } catch (_) {
                if (isModalAlive()) setTimeout(poll, 5000);
            }
        };
        setTimeout(poll, 2000);
    },

    async removeGroupMember(groupId, nodeId, nodeLabel) {
        Pages.showConfirmModal({
            title: 'Remove from Group',
            message: `Remove <strong>${escHtml(nodeLabel)}</strong> from this group?`,
            confirmText: 'Remove',
            danger: true,
            onConfirm: async () => {
                try {
                    await API.nodeGroups.removeMember(groupId, nodeId);
                    App.toast(`${nodeLabel} removed from group`, 'success');
                    Pages.nodeGroupDetail(groupId);
                } catch (e) {
                    App.toast(`Failed to remove member: ${e.message}`, 'error');
                }
            },
        });
    },

    async showAddMemberModal(groupId) {
        let allNodes = [], existingMemberIds = [];
        try {
            const [nodesResp, groupResp] = await Promise.all([
                API.nodes.list(),
                API.nodeGroups.get(groupId),
            ]);
            allNodes = (nodesResp && nodesResp.nodes) || [];
            existingMemberIds = ((groupResp && groupResp.members) || []).map(n => n.id);
        } catch (_) {}

        const available = allNodes.filter(n => !existingMemberIds.includes(n.id));
        const nodeOptions = available.map(n =>
            `<option value="${escHtml(n.id)}">${escHtml(n.hostname || n.primary_mac)}</option>`
        ).join('');

        const overlay = document.createElement('div');
        overlay.id = 'add-member-modal';
        overlay.className = 'modal-overlay';
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        overlay.innerHTML = `
            <div class="modal" style="max-width:400px;width:96vw" aria-labelledby="modal-title-10">
                <div class="modal-header">
                    <span class="modal-title" id="modal-title-10">Add Node to Group</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('add-member-modal').remove()">&#215;</button>
                </div>
                <div class="modal-body" style="padding:20px">
                    <div id="amm-result" style="margin-bottom:10px"></div>
                    <div class="form-group" style="margin-bottom:16px">
                        <label>Select Node</label>
                        <select id="amm-node" style="width:100%">
                            ${available.length ? nodeOptions : '<option value="">All nodes are already in this group</option>'}
                        </select>
                    </div>
                    <div class="form-actions">
                        <button class="btn btn-secondary" onclick="document.getElementById('add-member-modal').remove()">Cancel</button>
                        <button class="btn btn-primary" onclick="Pages._submitAddMember('${escHtml(groupId)}')">Add to Group</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(overlay);
        overlay.addEventListener('click', e => { if (e.target === overlay) overlay.remove(); });
        trapModalFocus(overlay, () => overlay.remove());
    },

    async _submitAddMember(groupId) {
        const nodeId = document.getElementById('amm-node')?.value;
        const resultEl = document.getElementById('amm-result');
        if (!nodeId) {
            if (resultEl) resultEl.innerHTML = `<div class="alert alert-error">Please select a node.</div>`;
            return;
        }
        try {
            await API.nodeGroups.addMembers(groupId, [nodeId]);
            document.getElementById('add-member-modal')?.remove();
            App.toast('Node added to group', 'success');
            Pages.nodeGroupDetail(groupId);
        } catch (e) {
            if (resultEl) resultEl.innerHTML = `<div class="alert alert-error">${escHtml(e.message)}</div>`;
        }
    },

    // _isoDetectDistro parses common ISO URL patterns and returns
    // { distro, version, os, name } best-effort pre-fills.
    _isoDetectDistro(url) {
        const lower = url.toLowerCase();
        const base  = lower.split('?')[0].split('/').pop();

        // Version: grab first X.Y (or X.Y.Z) token from the filename.
        const verMatch = base.match(/[\-_](\d+\.\d+(?:\.\d+)?)/);
        const version  = verMatch ? verMatch[1] : '';

        if (lower.includes('rockylinux.org') || base.startsWith('rocky-')) {
            return { distro: 'rocky', os: 'Rocky Linux ' + (version || ''), version };
        }
        if (lower.includes('almalinux.org') || base.startsWith('almalinux-')) {
            return { distro: 'almalinux', os: 'AlmaLinux ' + (version || ''), version };
        }
        if (lower.includes('centos.org') || base.startsWith('centos-')) {
            return { distro: 'centos', os: 'CentOS ' + (version || ''), version };
        }
        if (lower.includes('ubuntu.com') || lower.includes('releases.ubuntu.com') || lower.includes('cdimage.ubuntu.com') || base.startsWith('ubuntu-')) {
            return { distro: 'ubuntu', os: 'Ubuntu ' + (version || ''), version };
        }
        if (lower.includes('debian.org') || base.startsWith('debian-')) {
            return { distro: 'debian', os: 'Debian ' + (version || ''), version };
        }
        if (lower.includes('opensuse.org') || lower.includes('suse.com') || base.startsWith('opensuse-') || base.startsWith('sle-')) {
            return { distro: 'suse', os: 'SUSE / openSUSE', version };
        }
        if (lower.includes('alpinelinux.org') || base.startsWith('alpine-')) {
            return { distro: 'alpine', os: 'Alpine Linux', version };
        }
        return { distro: '', os: '', version };
    },

    // _isoFormatHint returns a human-readable label for the auto-install format.
    _isoFormatHint(distro) {
        const fmts = {
            rocky: 'kickstart install', almalinux: 'kickstart install',
            centos: 'kickstart install', rhel: 'kickstart install',
            ubuntu: 'cloud-init autoinstall', debian: 'preseed install',
            suse: 'AutoYaST install', alpine: 'answers file',
        };
        return fmts[distro] || 'automated install';
    },

    // _countUniquePackages returns the number of unique packages across all
    // provided role objects (which have a package_count field from the API).
    // Since the server pre-computes per-role cross-distro unique counts, we
    // cannot trivially deduplicate across roles client-side without knowing the
    // actual package lists. We approximate by summing and noting the overlap.
    // The "Preview" line is best-effort; exact count is computed server-side.
    _countUniquePackages(selectedRoles) {
        // Use the max single-role count plus 30% of remaining as an approximation.
        if (!selectedRoles.length) return 0;
        const counts = selectedRoles.map(r => r.package_count).sort((a, b) => b - a);
        let total = counts[0];
        for (let i = 1; i < counts.length; i++) {
            total += Math.round(counts[i] * 0.6); // ~40% overlap heuristic
        }
        return total;
    },

    async showBuildFromISOModal(prefillUrl) {
        // Load roles in parallel with modal render.
        let roles = [];
        try {
            const resp = await API.imageRoles.list();
            roles = resp.roles || [];
        } catch (e) {
            console.warn('Failed to load image roles:', e.message);
        }

        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        overlay.id = 'build-iso-modal';

        const rolesHtml = roles.length
            ? roles.map(r => `
                <label class="role-card" data-role-id="${escHtml(r.id)}">
                    <div class="role-card-header">
                        <input type="checkbox" name="role_ids" value="${escHtml(r.id)}" onchange="Pages._onRoleToggle()">
                        <span class="role-card-name">${escHtml(r.name)}</span>
                        <span class="role-card-count">${r.package_count} pkgs</span>
                    </div>
                    <div class="role-card-desc">${escHtml(r.description)}</div>
                    ${r.notes ? `<div class="role-card-note">
                        <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24" style="width:11px;height:11px;flex-shrink:0"><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>
                        ${escHtml(r.notes)}
                    </div>` : ''}
                </label>`).join('')
            : '<div style="color:var(--text-secondary);font-size:13px;padding:12px 0">No roles available — build will use minimal install.</div>';

        // Store roles on overlay for access in event handlers.
        overlay._roles = roles;

        overlay.innerHTML = `
            <div class="modal modal-wide" aria-labelledby="modal-title-11">
                <div class="modal-header">
                    <span class="modal-title" id="modal-title-11">Build Image from ISO</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('build-iso-modal').remove()">×</button>
                </div>
                <div class="modal-body">
                    <form id="build-iso-form" onsubmit="Pages.submitBuildFromISO(event)">

                        <div class="form-group" style="margin-bottom:6px">
                            <label>ISO URL *</label>
                            <input type="url" id="build-iso-url" name="url"
                                placeholder="https://download.rockylinux.org/pub/rocky/10/isos/x86_64/Rocky-10.1-x86_64-dvd1.iso"
                                oninput="Pages._onISOUrlChange(this.value)"
                                required>
                            <div id="build-iso-url-hint" class="form-hint" style="min-height:18px"></div>
                        </div>

                        <div class="form-grid" style="margin-bottom:16px">
                            <div class="form-group">
                                <label>Name *</label>
                                <input type="text" name="name" id="build-iso-name" placeholder="rocky10-compute" required>
                            </div>
                            <div class="form-group">
                                <label>Version</label>
                                <input type="text" name="version" id="build-iso-version" placeholder="10.1">
                            </div>
                            <div class="form-group">
                                <label>OS</label>
                                <input type="text" name="os" id="build-iso-os" placeholder="Rocky Linux 10">
                            </div>
                        </div>

                        <div style="margin-bottom:16px">
                            <div style="font-size:12px;font-weight:600;color:var(--text-secondary);margin-bottom:8px;text-transform:uppercase;letter-spacing:0.5px">Node roles</div>
                            <div class="role-picker" id="build-iso-roles">
                                ${rolesHtml}
                            </div>
                            <div id="build-iso-role-preview" class="form-hint" style="margin-top:8px;min-height:18px"></div>
                        </div>

                        <input type="hidden" name="firmware" value="uefi">

                        <div class="form-group" style="margin-bottom:16px">
                            <label style="font-size:12px;font-weight:600;color:var(--text-secondary);display:block;margin-bottom:8px;text-transform:uppercase;letter-spacing:0.5px">SELinux Mode</label>
                            <select name="selinux_mode" id="build-iso-selinux" style="width:100%;padding:8px 10px;border:1px solid var(--border);border-radius:6px;background:var(--bg-secondary);color:var(--text-primary);font-size:13px">
                                <option value="disabled" selected>Disabled (default — recommended for HPC)</option>
                                <option value="permissive">Permissive (logs violations, does not enforce)</option>
                                <option value="enforcing">Enforcing (full SELinux enforcement)</option>
                            </select>
                            <div class="form-hint">Controls the SELinux enforcement mode written into the kickstart. Most HPC clusters run with SELinux disabled.</div>
                        </div>

                        <div class="form-group" style="margin-bottom:16px">
                            <label style="display:flex;align-items:center;gap:8px;cursor:pointer;font-weight:400">
                                <input type="checkbox" name="install_updates" id="build-iso-updates">
                                <span>Install OS updates during build</span>
                            </label>
                            <div class="form-hint">Adds ~5-10 min, produces a fully patched image</div>
                        </div>

                        <div style="margin-bottom:16px">
                            <div style="font-size:12px;font-weight:600;color:var(--text-secondary);margin-bottom:12px;text-transform:uppercase;letter-spacing:0.5px">VM Resources</div>
                            <div class="form-grid">
                                <div class="form-group">
                                    <label>Disk: <span id="build-iso-disk-val">20 GB</span></label>
                                    <input type="range" name="disk_size_gb" id="build-iso-disk"
                                        min="10" max="100" step="5" value="20"
                                        oninput="document.getElementById('build-iso-disk-val').textContent=this.value+' GB'">
                                </div>
                                <div class="form-group">
                                    <label>Memory: <span id="build-iso-mem-val">2 GB</span></label>
                                    <input type="range" name="memory_gb" id="build-iso-mem"
                                        min="1" max="8" step="1" value="2"
                                        oninput="document.getElementById('build-iso-mem-val').textContent=this.value+' GB'">
                                </div>
                                <div class="form-group">
                                    <label>CPUs: <span id="build-iso-cpu-val">2</span></label>
                                    <input type="range" name="cpus" id="build-iso-cpu"
                                        min="1" max="8" step="1" value="2"
                                        oninput="document.getElementById('build-iso-cpu-val').textContent=this.value">
                                </div>
                            </div>
                        </div>

                        <div style="margin-bottom:16px">
                            <div style="font-size:12px;font-weight:600;color:var(--text-secondary);margin-bottom:12px;text-transform:uppercase;letter-spacing:0.5px">Default Login (optional)</div>
                            <div class="form-hint" style="margin-bottom:10px">Creates a user account in the image with sudo/wheel access. Supported for RHEL-family (Rocky, Alma, RHEL) builds. Leave blank to use only the built-in root account.</div>
                            <div class="form-grid">
                                <div class="form-group">
                                    <label for="build-iso-username">Username</label>
                                    <input type="text" name="default_username" id="build-iso-username"
                                        placeholder="e.g. admin" autocomplete="off">
                                </div>
                                <div class="form-group">
                                    <label for="build-iso-password">Password</label>
                                    <input type="password" name="default_password" id="build-iso-password"
                                        autocomplete="new-password">
                                </div>
                            </div>
                        </div>

                        <details id="build-iso-advanced">
                            <summary style="font-size:12px;font-weight:600;color:var(--text-secondary);cursor:pointer;user-select:none;margin-bottom:10px">
                                Advanced: Custom Kickstart
                            </summary>
                            <div class="alert alert-warning" style="font-size:12px;margin-bottom:10px">
                                Overrides role-based package list. Use only if you need full control.
                            </div>
                            <!-- B5-3: View default template link -->
                            <div style="margin-bottom:8px;font-size:12px;">
                                <a href="#" id="build-iso-ks-default-toggle"
                                   style="color:var(--accent);text-decoration:underline;"
                                   onclick="Pages._toggleKickstartDefault(event)">View default kickstart template</a>
                            </div>
                            <pre id="build-iso-ks-default-preview" style="display:none;background:var(--bg-code,#f8fafc);border:1px solid var(--border);border-radius:6px;
                                 padding:10px;font-size:11px;max-height:240px;overflow:auto;white-space:pre;margin-bottom:8px;">
# clustr auto-generated kickstart (RHEL family — representative template)
# Variables like {{.Distro}} are substituted at build time from image settings.
cdrom
lang en_US.UTF-8
keyboard us
timezone UTC --utc
rootpw --iscrypted &lt;bcrypt-hash&gt;
selinux --disabled
firewall --disabled
network --bootproto=dhcp --device=link --activate
skipx
firstboot --disabled

zerombr
clearpart --all --initlabel --disklabel=gpt
bootloader --location=mbr --boot-drive=sda --append="console=ttyS0,115200"
part /boot/efi --fstype=vfat --size=512  --ondisk=sda --label=esp
part /boot     --fstype=xfs  --size=1024 --ondisk=sda --label=boot
part /         --fstype=xfs  --size=1    --grow       --ondisk=sda --label=root

%packages --ignoremissing
@^minimal-environment
openssh-server
grub2-pc
grub2-efi-x64
shim-x64
efibootmgr
%end

%post --log=/root/ks-post.log
systemctl enable sshd
mkdir -p /etc/ssh/sshd_config.d
echo "PasswordAuthentication yes" &gt; /etc/ssh/sshd_config.d/60-clustr-password-auth.conf
# Remove machine-specific identifiers so captures produce clean base images.
rm -f /etc/machine-id /var/lib/dbus/machine-id /etc/hostname
rm -f /etc/ssh/ssh_host_* /root/.bash_history
%end

reboot</pre>
                            <div class="form-group">
                                <textarea name="custom_kickstart" id="build-iso-kickstart" rows="8"
                                    placeholder="# Paste your kickstart/autoinstall/preseed here...&#10;# Leave blank to use the auto-generated config from roles."
                                    style="font-family:var(--font-mono);font-size:12px"></textarea>
                            </div>
                        </details>

                        <div id="build-iso-result" style="margin-top:12px"></div>
                        <div class="form-actions">
                            <button type="button" class="btn btn-secondary" onclick="document.getElementById('build-iso-modal').remove()">Cancel</button>
                            <button type="submit" class="btn btn-primary" id="build-iso-btn">Build Image</button>
                        </div>
                    </form>
                </div>
            </div>`;

        document.body.appendChild(overlay);
        overlay.addEventListener('click', e => { if (e.target === overlay) overlay.remove(); });
        trapModalFocus(overlay, () => overlay.remove());

        // Prefill the URL field if provided (e.g. retrying an interrupted build).
        if (prefillUrl) {
            const urlInput = overlay.querySelector('#build-iso-url');
            if (urlInput) {
                urlInput.value = prefillUrl;
                Pages._onISOUrlChange(prefillUrl);
            }
        }

        overlay.querySelector('#build-iso-url').focus();

        // Initial role preview state.
        Pages._onRoleToggle();
    },

    _onISOUrlChange(url) {
        const hintEl  = document.getElementById('build-iso-url-hint');
        const nameEl  = document.getElementById('build-iso-name');
        const verEl   = document.getElementById('build-iso-version');
        const osEl    = document.getElementById('build-iso-os');
        if (!hintEl) return;

        const det = Pages._isoDetectDistro(url);
        if (det.distro) {
            hintEl.textContent = 'Detected: ' + det.os + ' (' + Pages._isoFormatHint(det.distro) + ')';
            hintEl.style.color = 'var(--success)';
            if (nameEl && !nameEl.value && det.distro) {
                // Auto-generate a slug name from distro + version.
                const slug = (det.distro + (det.version ? det.version.replace(/\./g, '') : '')).toLowerCase();
                nameEl.value = slug;
            }
            if (verEl && !verEl.value && det.version) {
                verEl.value = det.version;
            }
            if (osEl && !osEl.value && det.os) {
                osEl.value = det.os;
            }
        } else {
            hintEl.textContent = url.toLowerCase().endsWith('.iso') ? 'ISO URL — distro could not be detected' : '';
            hintEl.style.color = 'var(--text-secondary)';
        }
    },

    _onRoleToggle() {
        const modal   = document.getElementById('build-iso-modal');
        const preview = document.getElementById('build-iso-role-preview');
        const btn     = document.getElementById('build-iso-btn');
        if (!preview || !modal) return;

        const checked  = [...document.querySelectorAll('#build-iso-roles input[type=checkbox]:checked')];
        const roleIds  = checked.map(cb => cb.value);
        const roles    = (modal._roles || []).filter(r => roleIds.includes(r.id));
        const hasKS    = !!(document.getElementById('build-iso-kickstart') || {}).value;

        if (roles.length) {
            const pkgEst = Pages._countUniquePackages(roles);
            preview.textContent = 'Preview: ~' + pkgEst + ' unique packages will be installed';
            preview.style.color = 'var(--accent)';
        } else if (hasKS) {
            preview.textContent = 'Using custom kickstart — role packages ignored';
            preview.style.color = 'var(--text-secondary)';
        } else {
            preview.textContent = 'No roles selected — minimal base install only';
            preview.style.color = 'var(--text-secondary)';
        }

        // Disable submit if no roles AND no custom kickstart.
        if (btn) btn.disabled = (roles.length === 0 && !hasKS);
    },

    // B5-3: Toggle the default kickstart template preview.
    _toggleKickstartDefault(e) {
        e.preventDefault();
        const preview = document.getElementById('build-iso-ks-default-preview');
        const link    = document.getElementById('build-iso-ks-default-toggle');
        if (!preview) return;
        const showing = preview.style.display !== 'none';
        preview.style.display = showing ? 'none' : '';
        if (link) link.textContent = showing ? 'View default kickstart template' : 'Hide default kickstart template';
    },

    async submitBuildFromISO(e) {
        e.preventDefault();
        const form   = e.target;
        const btn    = document.getElementById('build-iso-btn');
        const result = document.getElementById('build-iso-result');
        const data   = new FormData(form);

        btn.disabled  = true;
        btn.textContent = 'Submitting…';
        result.innerHTML = '';

        const roleIds = [...form.querySelectorAll('input[name="role_ids"]:checked')].map(cb => cb.value);

        const firmwareEl = form.querySelector('input[name="firmware"]:checked');
        const body = {
            url:              data.get('url'),
            name:             data.get('name'),
            version:          data.get('version') || '',
            os:               data.get('os') || '',
            disk_size_gb:     parseInt(data.get('disk_size_gb') || '20', 10),
            memory_mb:        parseInt(data.get('memory_gb') || '2', 10) * 1024,
            cpus:             parseInt(data.get('cpus') || '2', 10),
            role_ids:         roleIds,
            firmware:         firmwareEl ? firmwareEl.value : 'uefi',
            selinux_mode:     data.get('selinux_mode') || 'disabled',
            install_updates:  form.querySelector('input[name="install_updates"]').checked,
            custom_kickstart: data.get('custom_kickstart') || '',
            default_username: data.get('default_username') || undefined,
            default_password: data.get('default_password') || undefined,
        };

        try {
            const img = await API.factory.buildFromISO(body);
            result.innerHTML = alertBox(
                'Build started: ' + img.name + ' (' + img.id.substring(0, 8) + ') — status: ' + img.status +
                '. This may take 10-30 minutes.',
                'success'
            );
            App.setAutoRefresh(() => Pages.images(), 5000);
            setTimeout(() => {
                const modal = document.getElementById('build-iso-modal');
                if (modal) modal.remove();
                Router.navigate('/images/' + img.id);
            }, 1800);
        } catch (ex) {
            result.innerHTML = alertBox('Build failed: ' + ex.message);
            btn.disabled = false;
            btn.textContent = 'Build Image';
        }
    },

    // ── ISO build progress panel ───────────────────────────────────────────

    // _isoBuildInProgress returns the HTML for the inline build progress panel
    // shown on the image detail page when status=building.
    _isoBuildInProgress(img) {
        if (img.build_method !== 'iso') {
            return `<div class="alert alert-info" role="status" style="margin-bottom:16px">Build in progress — connecting to live stream…</div>`;
        }
        return `
            <div class="card iso-build-panel" style="margin-bottom:16px" id="iso-build-card">
                <div class="card-header">
                    <h2 class="card-title">Building ${escHtml(img.name)} from ISO</h2>
                    <span class="badge badge-building" id="iso-build-badge">building</span>
                </div>
                <div class="card-body">
                    <div id="iso-build-interrupted-banner" style="display:none;background:var(--bg-warning,#fff3cd);border:1px solid var(--border-warning,#ffc107);border-radius:4px;padding:12px 14px;margin-bottom:14px;font-size:13px">
                        <strong>Build state not available</strong> — the build may have been interrupted by a server restart.
                        Check the build log for details, or delete this image and retry.
                        <div style="margin-top:8px;display:flex;gap:8px;align-items:center">
                            <a class="btn btn-secondary btn-sm" href="${escHtml(API.buildProgress.buildLogUrl(img.id))}" target="_blank" rel="noreferrer">View Build Log</a>
                            <button class="btn btn-danger btn-sm" onclick="Pages._deleteAndRetryBuild('${escHtml(img.id)}', ${JSON.stringify(img.source_url || '')})">Delete and Retry</button>
                        </div>
                    </div>
                    <div id="iso-build-phase" style="font-size:13px;margin-bottom:12px;color:var(--text-secondary)">
                        Phase: <span id="iso-build-phase-value" style="font-weight:600;color:var(--text-primary)">Connecting…</span>
                    </div>
                    <div class="progress-bar-wrap" style="margin-bottom:6px">
                        <div class="progress-bar-fill" id="iso-build-bar" style="width:5%;transition:width 0.4s ease"></div>
                    </div>
                    <div id="iso-build-bytes" style="font-size:11px;color:var(--text-secondary);margin-bottom:10px;min-height:16px"></div>
                    <div style="display:flex;gap:24px;font-size:12px;color:var(--text-secondary);margin-bottom:16px">
                        <span>Elapsed: <span id="iso-build-elapsed" class="text-mono">—</span></span>
                    </div>
                    <div style="font-size:11px;font-weight:600;color:var(--text-secondary);margin-bottom:6px;text-transform:uppercase;letter-spacing:0.5px">Serial console</div>
                    <div id="iso-serial-log" class="iso-serial-log log-viewer" style="height:240px;overflow-y:auto;font-family:var(--font-mono);font-size:11px;background:var(--bg-tertiary);border-radius:4px;padding:8px 10px;white-space:pre-wrap;word-break:break-all"></div>
                    <div style="margin-top:12px;display:flex;gap:8px;align-items:center">
                        <button class="btn btn-danger btn-sm" id="iso-cancel-btn" onclick="Pages._cancelIsoBuild('${escHtml(img.id)}')">Cancel Build</button>
                        <a class="btn btn-secondary btn-sm" href="${escHtml(API.buildProgress.buildLogUrl(img.id))}" target="_blank" rel="noreferrer">Download Full Log</a>
                    </div>
                </div>
            </div>`;
    },

    // _startIsoBuildSSE opens an SSE connection for an ISO build and wires all
    // UI updates. Call this once after the page HTML is rendered.
    _startIsoBuildSSE(imageId) {
        const sseUrl = API.buildProgress.sseUrl(imageId);
        const es = new EventSource(sseUrl);
        let userScrolled = false;
        let _elapsedTimer = null;

        const serialEl  = document.getElementById('iso-serial-log');
        const phaseEl   = document.getElementById('iso-build-phase-value');
        const barEl     = document.getElementById('iso-build-bar');
        const bytesEl   = document.getElementById('iso-build-bytes');
        const elapsedEl = document.getElementById('iso-build-elapsed');
        const badgeEl   = document.getElementById('iso-build-badge');

        if (serialEl) {
            serialEl.addEventListener('scroll', () => {
                const atBottom = serialEl.scrollHeight - serialEl.scrollTop - serialEl.clientHeight < 40;
                userScrolled = !atBottom;
            });
        }

        const _appendSerial = (line) => {
            if (!serialEl) return;
            const div = document.createElement('div');
            div.className = Pages._serialLineClass(line);
            div.textContent = line;
            serialEl.appendChild(div);
            // Trim to 500 visible lines to avoid runaway DOM growth.
            while (serialEl.children.length > 500) serialEl.removeChild(serialEl.firstChild);
            if (!userScrolled) serialEl.scrollTop = serialEl.scrollHeight;
        };

        const _applyPhase = (phase, elapsedMs) => {
            if (!phase) return;
            if (phaseEl) phaseEl.textContent = Pages._phaseLabel(phase);
            if (badgeEl) {
                badgeEl.className = 'badge ' + Pages._phaseBadgeClass(phase);
                badgeEl.textContent = phase.replace(/_/g, ' ');
            }
            if (barEl) {
                const pct = Pages._phasePercent(phase);
                barEl.style.width = pct + '%';
            }
            if (phase === 'complete' || phase === 'failed' || phase === 'canceled') {
                clearInterval(_elapsedTimer);
                es.close();
                setTimeout(() => Pages.imageDetail(imageId), 1800);
            }
        };

        const _applyProgress = (done, total) => {
            if (!barEl) return;
            if (total > 0) {
                const pct = Math.min(100, Math.round((done / total) * 100));
                if (bytesEl) bytesEl.textContent = `${fmtBytes(done)} / ${fmtBytes(total)}`;
                barEl.style.width = pct + '%';
            } else if (done > 0) {
                if (bytesEl) bytesEl.textContent = fmtBytes(done);
            }
        };

        const _startElapsedTimer = (startedAt) => {
            clearInterval(_elapsedTimer);
            // Derive the base time from the server-provided wall-clock timestamp so
            // the elapsed display is correct across page reloads and reconnects.
            const base = startedAt ? new Date(startedAt).getTime() : Date.now();
            _elapsedTimer = setInterval(() => {
                const secs = Math.floor((Date.now() - base) / 1000);
                if (elapsedEl) elapsedEl.textContent = fmtETA(secs);
            }, 1000);
        };

        // Track consecutive SSE errors to detect a dead build-progress endpoint.
        // EventSource auto-reconnects; after 3 failed attempts with no snapshot
        // received we surface the "interrupted" banner rather than spinning forever.
        let _sseErrorCount = 0;
        let _snapshotReceived = false;

        // C3-4: single merged 'snapshot' listener (was two separate listeners, causing
        // duplicate state application on every connect and missing _snapshotReceived flag).
        es.addEventListener('snapshot', (e) => {
            _snapshotReceived = true;
            _sseErrorCount = 0;
            try {
                const state = JSON.parse(e.data);
                _startElapsedTimer(state.started_at);
                _applyPhase(state.phase, state.elapsed_ms);
                _applyProgress(state.bytes_done, state.bytes_total);
                if (serialEl && Array.isArray(state.serial_tail)) {
                    serialEl.innerHTML = '';
                    state.serial_tail.forEach(_appendSerial);
                    if (!userScrolled) serialEl.scrollTop = serialEl.scrollHeight;
                }
            } catch (_) {}
        });

        // Incremental update events.
        es.onmessage = (e) => {
            try {
                const ev = JSON.parse(e.data);
                if (ev.phase)       _applyPhase(ev.phase, ev.elapsed_ms);
                if (ev.bytes_done)  _applyProgress(ev.bytes_done, ev.bytes_total);
                if (ev.serial_line) _appendSerial(ev.serial_line);
                if (ev.elapsed_ms && elapsedEl) {
                    elapsedEl.textContent = fmtETA(Math.floor(ev.elapsed_ms / 1000));
                }
            } catch (_) {}
        };

        es.onerror = () => {
            clearInterval(_elapsedTimer);
            if (!_snapshotReceived) {
                _sseErrorCount++;
                if (_sseErrorCount >= 3) {
                    // The build-progress endpoint returned 404 or is unreachable.
                    // The image is still in "building" state but has no live goroutine.
                    es.close();
                    const banner = document.getElementById('iso-build-interrupted-banner');
                    const phaseDiv = document.getElementById('iso-build-phase');
                    if (banner) banner.style.display = '';
                    if (phaseDiv) phaseDiv.style.display = 'none';
                    const badgeEl2 = document.getElementById('iso-build-badge');
                    if (badgeEl2) {
                        badgeEl2.className = 'badge badge-error';
                        badgeEl2.textContent = 'interrupted';
                    }
                }
            }
        };

        Pages._isoBuildSSE = es;
        Pages._isoBuildElapsedTimer = _elapsedTimer;
    },

    _serialLineClass(line) {
        if (/kernel panic|BUG:|OOPS:|call trace/i.test(line)) return 'serial-line serial-panic';
        if (/\[\s*OK\s*\]/.test(line))                         return 'serial-line serial-ok';
        if (/warning|warn/i.test(line))                        return 'serial-line serial-warn';
        if (/error|fail|failed/i.test(line))                   return 'serial-line serial-error';
        return 'serial-line';
    },

    _phaseLabel(phase) {
        const labels = {
            downloading_iso:   'Downloading ISO',
            generating_config: 'Generating config',
            creating_disk:     'Creating disk',
            launching_vm:      'Launching VM',
            installing:        'Installing OS',
            extracting:        'Extracting rootfs',
            scrubbing:         'Scrubbing identity',
            finalizing:        'Finalizing',
            complete:          'Complete',
            failed:            'Failed',
            canceled:          'Canceled',
        };
        return labels[phase] || phase;
    },

    _phaseBadgeClass(phase) {
        if (phase === 'complete') return 'badge-success';
        if (phase === 'failed' || phase === 'canceled') return 'badge-error';
        return 'badge-building';
    },

    _phasePercent(phase) {
        const pcts = {
            downloading_iso:   10, generating_config: 20, creating_disk: 25,
            launching_vm: 30, installing: 60, extracting: 80,
            scrubbing: 90, finalizing: 95, complete: 100, failed: 100, canceled: 100,
        };
        return pcts[phase] || 5;
    },

    // _updateIsoBuildProgress is kept as a no-op; ISO builds now use SSE.
    _updateIsoBuildProgress() {},

    async _cancelIsoBuild(imageId) {
        // C3-5: Use POST /images/{id}/cancel instead of DELETE.
        // Cancel marks the image as error state without deleting the record;
        // the admin can still view the build log and retry via resume.
        Pages.showConfirmModal({
            title: 'Cancel Build',
            message: 'Cancel this build? The image will be set to error state. You can resume or delete it afterwards.',
            confirmText: 'Cancel Build',
            danger: true,
            onConfirm: async () => {
                try {
                    if (Pages._isoBuildSSE) { Pages._isoBuildSSE.close(); Pages._isoBuildSSE = null; }
                    clearInterval(Pages._isoBuildElapsedTimer);
                    await API.images.cancelBuild(imageId);
                    Router.navigate('/images');
                } catch (e) {
                    Pages.showAlertModal('Cancel Failed', escHtml(e.message));
                }
            },
        });
    },

    // _deleteAndRetryBuild deletes an interrupted image and reopens the Build from
    // ISO modal prefilled with the original source URL so the admin can retry
    // without having to retype anything.
    async _deleteAndRetryBuild(imageId, sourceUrl) {
        Pages.showConfirmModal({
            title: 'Delete and Retry Build',
            message: 'Delete this image and open the Build from ISO modal to retry?',
            confirmText: 'Delete and Retry',
            danger: true,
            onConfirm: async () => {
                try {
                    if (Pages._isoBuildSSE) { Pages._isoBuildSSE.close(); Pages._isoBuildSSE = null; }
                    clearInterval(Pages._isoBuildElapsedTimer);
                    await API.images.delete(imageId);
                    Router.navigate('/images');
                    setTimeout(() => Pages.showBuildFromISOModal(sourceUrl), 300);
                } catch (e) {
                    Pages.showAlertModal('Delete Failed', escHtml(e.message));
                }
            },
        });
    },

    // ── Role mismatch warning ───────────────────────────────────────────��──

    // _checkRoleMismatch is called when the admin changes the image selection on the
    // node edit modal. It reads the image's built_for_roles and compares against
    // the node's groups to surface a warning when there is a mismatch.
    _checkRoleMismatch(imageId, node, images) {
        const warnEl = document.getElementById('role-mismatch-warning');
        if (!warnEl) return;

        if (!imageId) { warnEl.style.display = 'none'; return; }

        const img = (images || []).find(i => i.id === imageId);
        if (!img) { warnEl.style.display = 'none'; return; }

        const builtFor = img.built_for_roles || [];
        if (!builtFor.length) { warnEl.style.display = 'none'; return; }

        // Compare the node's groups array against the image's built_for_roles.
        // A mismatch is when the node has a group that looks like a role ID
        // (compute, gpu-compute, storage, etc.) but it's not in the image's roles.
        const roleKeywords = ['compute', 'gpu-compute', 'gpu', 'storage', 'head-node', 'management', 'minimal'];
        const nodeRoles = (node.tags || node.groups || []).filter(g => roleKeywords.some(k => g.toLowerCase().includes(k)));
        const mismatched = nodeRoles.filter(g => !builtFor.some(r => g.toLowerCase().includes(r) || r.toLowerCase().includes(g)));

        if (mismatched.length) {
            const nodeRoleStr  = mismatched.join(', ');
            const imageRoleStr = builtFor.join(', ');
            warnEl.innerHTML = `
                <div style="display:flex;gap:10px;align-items:flex-start">
                    <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24" style="width:18px;height:18px;flex-shrink:0;margin-top:2px;color:var(--warning)">
                        <path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/>
                        <line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/>
                    </svg>
                    <div>
                        <div style="font-weight:600;margin-bottom:2px">Role mismatch</div>
                        <div style="font-size:12px">This node has group <strong>${escHtml(nodeRoleStr)}</strong> but the image
                        <strong>${escHtml(img.name)}</strong> was built for <strong>${escHtml(imageRoleStr)}</strong>.
                        Required packages may not be present on the deployed node.</div>
                    </div>
                </div>`;
            warnEl.style.display = '';
        } else {
            warnEl.style.display = 'none';
        }
    },

    // ── Deployments (S5-10) ───────────────────────────────────────────────

    // deploys — full-page deployment history and live progress view.
    // Shows all reimage records (pending, in_progress, complete, failed) with
    // live progress for in-flight deploys. Auto-refreshes every 30 s.
    async deploys() {
        App.render(loading('Loading deployments…'));
        try {
            const [reimagesResp, progressEntries, nodesResp] = await Promise.all([
                API.reimages.list({ limit: 100 }).catch(() => ({ requests: [] })),
                API.progress.list().catch(() => []),
                API.nodes.list().catch(() => ({ nodes: [] })),
            ]);

            const requests  = (reimagesResp && reimagesResp.requests) || [];
            const nodes     = (nodesResp && nodesResp.nodes) || [];
            const nodeMap   = Object.fromEntries(nodes.map(n => [n.id, n]));

            // Build MAC → live progress map for merging into reimage rows.
            const deployMap = new Map();
            (progressEntries || []).forEach(p => deployMap.set(p.node_mac, p));
            Pages._deploysProgressMap = deployMap;
            Pages._deploysNodeMap = nodeMap;

            App.render(`
                <div class="page-header">
                    <div>
                        <h1 class="page-title">Deployments</h1>
                        <div class="page-subtitle">Reimage history and live deploy progress</div>
                    </div>
                    <div class="flex gap-8">
                        <button class="btn btn-secondary btn-sm" onclick="Pages.deploys()" title="Refresh">
                            <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24" style="width:14px;height:14px"><polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/></svg>
                            Refresh
                        </button>
                    </div>
                </div>

                ${cardWrap('Live Progress',
                    `<div id="deploys-live-container">${Pages._deployProgressTable(deployMap)}</div>`)}

                ${cardWrap('Reimage History',
                    Pages._deploysHistoryTable(requests, nodeMap))}
            `);

            // Start SSE stream so live progress updates automatically.
            App._progressStream = new ProgressStream(deployMap, () => {
                const container = document.getElementById('deploys-live-container');
                if (container) container.innerHTML = Pages._deployProgressTable(deployMap);
            });
            App._progressStream.connect();

            App.setAutoRefresh(async () => {
                if (!document.getElementById('deploys-live-container')) return;
                try {
                    const [fresh, freshProgress] = await Promise.all([
                        API.reimages.list({ limit: 100 }).catch(() => null),
                        API.progress.list().catch(() => null),
                    ]);
                    if (fresh) {
                        const tbody = document.querySelector('#deploys-history-tbody');
                        if (tbody) tbody.innerHTML = Pages._deploysHistoryRows((fresh.requests || []), Pages._deploysNodeMap || {});
                    }
                    if (freshProgress) {
                        deployMap.clear();
                        freshProgress.forEach(p => deployMap.set(p.node_mac, p));
                        const liveEl = document.getElementById('deploys-live-container');
                        if (liveEl) liveEl.innerHTML = Pages._deployProgressTable(deployMap);
                    }
                } catch (_) {}
            });

        } catch (e) {
            App.render(alertBox(`Failed to load deployments: ${e.message}`));
        }
    },

    // _deploysHistoryTable renders the full reimage history table.
    _deploysHistoryTable(requests, nodeMap) {
        if (!requests.length) return emptyState('No reimage history yet', 'Reimage a node to see deploy records here.');
        return `<div class="table-wrap"><table aria-label="Reimage history">
            <thead><tr>
                <th>Node</th><th>Image</th><th>Status</th><th>Started</th><th>Duration</th><th>Actions</th>
            </tr></thead>
            <tbody id="deploys-history-tbody">
                ${Pages._deploysHistoryRows(requests, nodeMap)}
            </tbody>
        </table></div>`;
    },

    // _deploysHistoryRows renders tbody rows for reimage records.
    _deploysHistoryRows(requests, nodeMap) {
        const canMutate = Auth._role === 'admin' || Auth._role === 'operator';
        return requests.map(r => {
            const node = nodeMap[r.node_id];
            const displayName = node
                ? escHtml(node.hostname || node.primary_mac || r.node_id)
                : `<span class="text-mono text-dim">${r.node_id ? r.node_id.substring(0, 8) + '…' : '—'}</span>`;
            const nodeLink = node
                ? `<a href="#/nodes/${r.node_id}" style="font-weight:500">${displayName}</a>`
                : displayName;

            const statusCls = {
                complete:    'badge-ready',
                pending:     'badge-neutral',
                triggered:   'badge-info',
                in_progress: 'badge-warning',
                failed:      'badge-error',
                cancelled:   'badge-neutral',
            }[r.status] || 'badge-neutral';

            const duration = (() => {
                if (!r.started_at) return '—';
                const start = new Date(r.started_at).getTime();
                const end   = r.completed_at ? new Date(r.completed_at).getTime() : Date.now();
                const secs  = Math.round((end - start) / 1000);
                if (secs < 60) return `${secs}s`;
                return `${Math.floor(secs / 60)}m ${secs % 60}s`;
            })();

            const retryBtn = (canMutate && r.status === 'failed')
                ? `<button class="btn btn-secondary btn-sm" onclick="Pages._deploysRetry('${r.id}')">Retry</button>`
                : '';
            const cancelBtn = (canMutate && (r.status === 'pending' || r.status === 'triggered' || r.status === 'in_progress'))
                ? `<button class="btn btn-danger btn-sm" onclick="Pages._deploysCancel('${r.id}')">Cancel</button>`
                : '';

            return `<tr data-key="${escHtml(r.id)}">
                <td>${nodeLink}</td>
                <td class="text-dim text-sm text-mono">${r.base_image_id ? r.base_image_id.substring(0, 8) + '…' : '—'}</td>
                <td><span class="badge ${statusCls}">${escHtml(r.status || '—')}</span></td>
                <td class="text-dim text-sm">${r.started_at ? fmtRelative(r.started_at) : '—'}</td>
                <td class="text-dim text-sm">${duration}</td>
                <td><div class="flex gap-6">${retryBtn}${cancelBtn}</div></td>
            </tr>`;
        }).join('');
    },

    async _deploysRetry(id) {
        try {
            await API.reimages.retry(id);
            App.toast('Reimage queued for retry', 'success');
            Pages.deploys();
        } catch (e) {
            App.toast(`Retry failed: ${e.message}`, 'error');
        }
    },

    async _deploysCancel(id) {
        Pages.showConfirmModal({
            title: 'Cancel Deploy',
            message: 'Cancel this reimage request? The node will not be reimaged.',
            confirmText: 'Cancel Deploy',
            danger: true,
            onConfirm: async () => {
                try {
                    await API.reimages.cancel(id);
                    App.toast('Deploy cancelled', 'success');
                    Pages.deploys();
                } catch (e) {
                    App.toast(`Cancel failed: ${e.message}`, 'error');
                }
            },
        });
    },

    // ── Settings ───────────────────────────────────────────────────────────

    _settingsTab: 'api-keys', // tracks active tab

    async settings() {
        App.render(loading('Loading settings…'));
        await Pages._settingsRender(Pages._settingsTab);
    },

    async _settingsRender(tab) {
        Pages._settingsTab = tab;
        const isAdmin = Auth._role === 'admin';
        // B3-1: Webhooks tab is admin-only. B3-4/B3-5: About tab for all roles.
        const tabs = isAdmin
            ? ['api-keys', 'users', 'webhooks', 'notifications', 'governance', 'server-info', 'about']
            : ['api-keys', 'server-info', 'about'];
        const tabBar = tabs.map(t => {
            const active = t === tab ? 'style="border-bottom:2px solid var(--accent);color:var(--accent);"' : '';
            const label  = { 'api-keys': 'API Keys', 'users': 'Users', 'webhooks': 'Webhooks', 'notifications': 'Notifications', 'governance': 'Governance', 'server-info': 'System', 'about': 'About' }[t];
            return `<button class="btn btn-ghost" ${active} onclick="Pages._settingsRender('${t}')">${label}</button>`;
        }).join('');

        let body = loading('Loading…');
        if (tab === 'api-keys') {
            body = await Pages._settingsAPIKeysTab();
        } else if (tab === 'users') {
            body = await Pages._settingsUsersTab();
        } else if (tab === 'webhooks') {
            body = await Pages._settingsWebhooksTab();
        } else if (tab === 'notifications') {
            body = await Pages._settingsNotificationsTab();
        } else if (tab === 'governance') {
            body = await Pages._settingsGovernanceTab();
        } else if (tab === 'about') {
            body = await Pages._settingsAboutTab();
        } else if (tab === 'server-info') {
            body = await Pages._settingsServerInfoTab();
            body += `
            <div class="card" style="margin-top:20px;" id="app-logs-card">
                <div class="card-header" style="justify-content:space-between;">
                    <h2 class="card-title">Application Logs</h2>
                    <div style="display:flex;align-items:center;gap:10px;">
                        <span class="follow-indicator" id="app-follow-ind"><span class="follow-dot"></span>static</span>
                        <label class="toggle" style="margin:0;">
                            <input type="checkbox" id="app-follow-toggle" onchange="Pages.toggleFollow(this.checked)">
                            Live
                        </label>
                        <button class="btn btn-secondary btn-sm" onclick="Pages.clearLogs()">Clear</button>
                    </div>
                </div>
                <div style="padding:8px 12px 4px;display:flex;flex-wrap:wrap;gap:8px;border-bottom:1px solid var(--border);">
                    <input id="lf-mac"       type="text"   placeholder="MAC address"   style="width:155px">
                    <input id="lf-hostname"  type="text"   placeholder="Hostname"      style="width:130px">
                    <select id="lf-level" style="width:130px">
                        <option value="">All levels</option>
                        <option value="debug">debug</option>
                        <option value="info">info</option>
                        <option value="warn">warn</option>
                        <option value="error">error</option>
                    </select>
                    <select id="lf-component" style="width:145px">
                        <option value="">All components</option>
                        <option value="hardware">hardware</option>
                        <option value="deploy">deploy</option>
                        <option value="chroot">chroot</option>
                        <option value="ipmi">ipmi</option>
                        <option value="efiboot">efiboot</option>
                        <option value="network">network</option>
                        <option value="rsync">rsync</option>
                        <option value="raid">raid</option>
                    </select>
                    <input id="lf-since" type="datetime-local" title="Since (local time)" style="width:185px">
                    <button class="btn btn-secondary btn-sm" onclick="Pages.loadLogs()">Query</button>
                </div>
                <div style="padding:0 0 4px;">
                    <div id="log-viewer" class="log-viewer" style="height:360px;border-radius:0 0 var(--radius) var(--radius);"></div>
                </div>
            </div>`;
        } else {
            body = await Pages._settingsAboutTab();
        }

        // C3-14: Disconnect the settings log stream before replacing the DOM.
        // App.render() destroys the log-viewer element; keeping a reference to it
        // in App._logStream would prevent proper re-init when the tab is revisited.
        if (App._logStream) { App._logStream.disconnect(); App._logStream = null; }

        App.render(`
            <div class="page-header">
                <div>
                    <h1 class="page-title">Settings</h1>
                    <div class="page-subtitle">Server and API key management</div>
                </div>
            </div>
            <div style="display:flex;gap:8px;margin-bottom:20px;border-bottom:1px solid var(--border);padding-bottom:0;">
                ${tabBar}
            </div>
            ${body}
        `);
        // Populate the logs card now that the DOM is ready.
        await Pages._loadAppLogs();
    },

    // _loadAppLogs populates the Application Logs card embedded in the settings page.
    async _loadAppLogs() {
        const viewer = document.getElementById('log-viewer');
        if (!viewer) return;
        try {
            const resp    = await API.logs.query({ limit: '500' });
            const entries = resp.logs || [];
            if (!App._logStream) App._logStream = new LogStream(viewer);
            App._logStream.loadEntries(entries);
            if (!entries.length) {
                viewer.innerHTML = `<div class="empty-state" style="padding:30px">
                    <div class="empty-state-text">No log entries found</div>
                </div>`;
            }
        } catch (e) {
            if (viewer) viewer.innerHTML = `<div style="padding:12px;color:var(--error);font-size:12px;font-family:var(--font-mono)">Error: ${escHtml(e.message)}</div>`;
        }
    },

    _appLogFilters() {
        const mac       = (document.getElementById('lf-mac')       || {}).value || '';
        const hostname  = (document.getElementById('lf-hostname')  || {}).value || '';
        const level     = (document.getElementById('lf-level')     || {}).value || '';
        const component = (document.getElementById('lf-component') || {}).value || '';
        const sinceEl   = document.getElementById('lf-since');
        let since = '';
        if (sinceEl && sinceEl.value) {
            since = new Date(sinceEl.value).toISOString();
        }
        return { mac, hostname, level, component, since, limit: '500' };
    },

    async loadLogs() {
        const viewer = document.getElementById('log-viewer');
        if (!viewer) return;

        const followToggle = document.getElementById('app-follow-toggle');
        if (App._logStream && followToggle && followToggle.checked) {
            App._logStream.setFilters(this._appLogFilters());
            return;
        }

        try {
            const params  = this._appLogFilters();
            const resp    = await API.logs.query(params);
            const entries = resp.logs || [];

            if (!App._logStream) App._logStream = new LogStream(viewer);
            App._logStream.loadEntries(entries);

            if (!entries.length) {
                viewer.innerHTML = `<div class="empty-state" style="padding:30px">
                    <div class="empty-state-text">No log entries match your filters</div>
                </div>`;
            }
        } catch (e) {
            if (viewer) viewer.innerHTML = `<div style="padding:12px;color:var(--error);font-size:12px;font-family:var(--font-mono)">Error: ${escHtml(e.message)}</div>`;
        }
    },

    clearLogs() {
        if (App._logStream) App._logStream.clear();
    },

    toggleFollow(enabled) {
        const viewer = document.getElementById('log-viewer');
        const ind    = document.getElementById('app-follow-ind');
        if (!viewer) return;

        if (enabled) {
            if (!App._logStream) App._logStream = new LogStream(viewer);
            App._logStream.setFilters(this._appLogFilters());
            App._logStream.setAutoScroll(true);
            App._logStream.onConnect(() => {
                if (ind) { ind.className = 'follow-indicator live'; ind.innerHTML = '<span class="follow-dot"></span>Live'; }
            });
            App._logStream.onDisconnect(() => {
                if (ind) { ind.className = 'follow-indicator'; ind.innerHTML = '<span class="follow-dot"></span>Reconnecting…'; }
            });
            App._logStream.connect();
        } else {
            if (App._logStream) {
                App._logStream.disconnect();
                if (ind) { ind.className = 'follow-indicator'; ind.innerHTML = '<span class="follow-dot"></span>static'; }
            }
        }
    },

    async _settingsAPIKeysTab() {
        try {
            const resp = await API.apiKeys.list();
            const keys = (resp && resp.api_keys) ? resp.api_keys : [];

            const rows = keys.length === 0
                ? `<tr><td colspan="7" style="text-align:center;color:var(--text-secondary);padding:24px">No active API keys</td></tr>`
                : keys.map(k => {
                    const expires = k.expires_at ? fmtDate(k.expires_at) : '<span class="text-dim">never</span>';
                    const lastUsed = k.last_used_at ? fmtRelative(k.last_used_at) : '<span class="text-dim">never</span>';
                    const label = k.label || '<span class="text-dim">—</span>';
                    const scopeBadge = k.scope === 'admin'
                        ? `<span class="badge badge-info">admin</span>`
                        : `<span class="badge badge-neutral">node</span>`;
                    return `<tr>
                        <td class="text-mono text-sm">${escHtml(k.hash_prefix)}…</td>
                        <td>${scopeBadge}</td>
                        <td>${escHtml(k.label || '—')}</td>
                        <td class="text-sm text-secondary">${lastUsed}</td>
                        <td class="text-sm text-secondary">${expires}</td>
                        <td class="text-sm text-secondary">${escHtml(k.created_by || '—')}</td>
                        <td>
                            <div style="display:flex;gap:6px;">
                                <button class="btn btn-secondary btn-sm" onclick="Pages._settingsRotateKey('${k.id}')">Rotate</button>
                                <button class="btn btn-danger btn-sm" onclick="Pages._settingsRevokeKey('${k.id}', '${escHtml(k.label || k.hash_prefix)}')">Revoke</button>
                            </div>
                        </td>
                    </tr>`;
                }).join('');

            return `
                <div class="card">
                    <div class="card-header">
                        <h2 class="card-title">API Keys</h2>
                        <button class="btn btn-primary btn-sm" onclick="Pages._settingsCreateKeyModal()">+ Create Key</button>
                    </div>
                    <table class="table">
                        <thead>
                            <tr>
                                <th>Hash Prefix</th><th>Scope</th><th>Label</th>
                                <th>Last Used</th><th>Expires</th><th>Created By</th><th>Actions</th>
                            </tr>
                        </thead>
                        <tbody>${rows}</tbody>
                    </table>
                </div>`;
        } catch (err) {
            return alertBox('Failed to load API keys: ' + err.message);
        }
    },

    // ── Users tab ────────────────────────────────────────────────────────────

    async _settingsUsersTab() {
        try {
            const [usersResp, groupsResp] = await Promise.all([
                API.users.list(),
                API.nodeGroups.list().catch(() => ({ groups: [] })),
            ]);
            const users  = (usersResp && usersResp.users) ? usersResp.users : [];
            const groups = (groupsResp && (groupsResp.groups || groupsResp.node_groups)) || [];
            const groupMap = Object.fromEntries(groups.map(g => [g.id, g]));

            const adminCount = users.filter(u => u.role === 'admin' && !u.disabled).length;

            const rows = users.length === 0
                ? `<tr><td colspan="7" style="text-align:center;color:var(--text-secondary);padding:24px">No users</td></tr>`
                : users.map(u => {
                    const roleBadge = u.role === 'admin'
                        ? `<span class="badge badge-info">admin</span>`
                        : u.role === 'operator'
                            ? `<span class="badge badge-neutral">operator</span>`
                            : `<span class="badge" style="background:var(--bg-secondary);color:var(--text-secondary)">readonly</span>`;
                    const disabledBadge = u.disabled ? `<span class="badge" style="background:#fee2e2;color:#dc2626;margin-left:4px">disabled</span>` : '';
                    const lastLogin = u.last_login_at ? fmtRelative(u.last_login_at) : '<span class="text-dim">never</span>';
                    const mustChange = u.must_change_password ? '<span title="Must change password" style="color:var(--warning)">&#9888;</span>' : '';
                    // Disable delete/disable for the last admin.
                    const isLastAdmin = u.role === 'admin' && !u.disabled && adminCount <= 1;
                    const actionDisabled = isLastAdmin ? 'disabled title="Cannot disable/delete the last admin"' : '';

                    // Group memberships column — only meaningful for operators.
                    let groupCell = '<span class="text-dim">—</span>';
                    if (u.role === 'operator') {
                        const memberGroupIDs = Array.isArray(u.group_ids) ? u.group_ids : [];
                        const chips = memberGroupIDs.map(gid => {
                            const g = groupMap[gid];
                            return g
                                ? `<span class="badge badge-info badge-sm" style="cursor:default">${escHtml(g.name)}</span>`
                                : `<span class="badge badge-neutral badge-sm text-mono" style="font-size:10px">${gid.substring(0,8)}</span>`;
                        });
                        const editBtn = `<button class="btn btn-secondary btn-sm" style="margin-left:4px" onclick="Pages._settingsEditGroupMemberships('${u.id}','${escHtml(u.username)}',${JSON.stringify(memberGroupIDs)},${JSON.stringify(groups)})">Edit</button>`;
                        groupCell = `<div style="display:flex;align-items:center;gap:4px;flex-wrap:wrap">${chips.length ? chips.join('') : '<span class="text-dim">none</span>'}${editBtn}</div>`;
                    }

                    return `<tr>
                        <td>${escHtml(u.username)} ${mustChange}</td>
                        <td>${roleBadge}${disabledBadge}</td>
                        <td class="text-sm text-secondary">${fmtDate(u.created_at)}</td>
                        <td class="text-sm text-secondary">${lastLogin}</td>
                        <td>${groupCell}</td>
                        <td>
                            <div style="display:flex;gap:6px;flex-wrap:wrap;">
                                <button class="btn btn-secondary btn-sm" onclick="Pages._settingsResetUserPassword('${u.id}')">Reset PW</button>
                                <button class="btn btn-secondary btn-sm" onclick="Pages._settingsChangeUserRole('${u.id}','${u.role}')">Role</button>
                                <button class="btn btn-danger btn-sm" onclick="Pages._settingsDisableUser('${u.id}','${escHtml(u.username)}')" ${!u.disabled ? actionDisabled : 'disabled title="Already disabled"'}>${u.disabled ? 'Disabled' : 'Disable'}</button>
                            </div>
                        </td>
                    </tr>`;
                }).join('');

            return `
                <div class="card">
                    <div class="card-header">
                        <h2 class="card-title">Users</h2>
                        <button class="btn btn-primary btn-sm" onclick="Pages._settingsCreateUserModal()">+ Create User</button>
                    </div>
                    <table class="table">
                        <thead>
                            <tr>
                                <th>Username</th><th>Role</th><th>Created</th><th>Last Login</th><th>Group Memberships</th><th>Actions</th>
                            </tr>
                        </thead>
                        <tbody>${rows}</tbody>
                    </table>
                </div>`;
        } catch (err) {
            return alertBox('Failed to load users: ' + err.message);
        }
    },

    // _settingsEditGroupMemberships opens a modal to assign operator → NodeGroups.
    _settingsEditGroupMemberships(userID, username, currentGroupIDs, allGroups) {
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = 'group-membership-modal';
        const checklist = allGroups.map(g => `
            <label style="display:flex;align-items:center;gap:8px;padding:6px 0;cursor:pointer">
                <input type="checkbox" value="${escHtml(g.id)}"
                    ${currentGroupIDs.includes(g.id) ? 'checked' : ''}>
                <span>${escHtml(g.name)}</span>
            </label>`).join('');

        overlay.innerHTML = `
            <div class="modal" style="max-width:420px" role="dialog" aria-modal="true">
                <div class="modal-header">
                    <span class="modal-title">Group Memberships — ${escHtml(username)}</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('group-membership-modal').remove()">&#215;</button>
                </div>
                <div class="modal-body">
                    <p style="margin:0 0 12px;font-size:13px;color:var(--text-secondary)">
                        Select the NodeGroups this operator can manage. Operator scope allows reimage and
                        power actions on nodes within the selected groups.
                    </p>
                    ${allGroups.length
                        ? `<div style="max-height:260px;overflow-y:auto;border:1px solid var(--border);border-radius:var(--radius);padding:8px 12px">${checklist}</div>`
                        : `<p class="text-dim" style="font-size:13px">No node groups defined yet. Create groups first.</p>`}
                    <div id="gm-error" style="color:var(--error);font-size:13px;margin-top:8px;display:none"></div>
                </div>
                <div class="modal-footer">
                    <button class="btn btn-secondary" onclick="document.getElementById('group-membership-modal').remove()">Cancel</button>
                    <button class="btn btn-primary" id="gm-save-btn" onclick="Pages._settingsEditGroupMembershipsSubmit('${userID}')">Save</button>
                </div>
            </div>`;
        document.body.appendChild(overlay);
    },

    async _settingsEditGroupMembershipsSubmit(userID) {
        const overlay = document.getElementById('group-membership-modal');
        const errEl   = document.getElementById('gm-error');
        const btn     = document.getElementById('gm-save-btn');
        if (!overlay) return;

        const selected = Array.from(overlay.querySelectorAll('input[type=checkbox]:checked')).map(el => el.value);
        btn.disabled = true;
        btn.textContent = 'Saving…';
        if (errEl) { errEl.style.display = 'none'; errEl.textContent = ''; }
        try {
            await API.users.setGroupMemberships(userID, selected);
            overlay.remove();
            App.toast('Group memberships updated', 'success');
            Pages._settingsRender('users');
        } catch (e) {
            btn.disabled = false;
            btn.textContent = 'Save';
            if (errEl) { errEl.textContent = e.message; errEl.style.display = ''; }
        }
    },

    async _settingsServerInfoTab() {
        try {
            const [info, nodesResp, initramfsResp, repoHealthResp] = await Promise.allSettled([
                API.health.get(),
                API.nodes.list(),
                API.system.initramfs().catch(() => null),
                // C3-26: bundle info from /repo/health (public endpoint, no /api/v1 prefix).
                fetch('/repo/health').then(r => r.ok ? r.json() : null).catch(() => null),
            ]);
            const health        = info.status === 'fulfilled'        ? info.value        : null;
            const nodes         = nodesResp.status === 'fulfilled'   ? nodesResp.value   : null;
            // C3-23: initramfs card moved here from Images page.
            const initramfsInfo = initramfsResp.status === 'fulfilled' ? initramfsResp.value : null;
            // C3-26: bundle info.
            const repoHealth = repoHealthResp.status === 'fulfilled' ? repoHealthResp.value : null;
            const bundles = repoHealth?.installed || [];
            const version    = health?.version    || 'dev';
            const commit     = health?.commit     || 'unknown';
            const buildTime  = health?.build_time || 'unknown';
            const flipFails  = health?.flip_back_failures != null ? String(health.flip_back_failures) : '—';
            const nodeTotal  = nodes?.total != null ? String(nodes.total) : '—';

            const row = (label, value) => `
                <tr>
                    <td style="padding:7px 20px 7px 0;color:var(--text-secondary);white-space:nowrap;font-size:13px;vertical-align:top;">${escHtml(label)}</td>
                    <td style="padding:7px 0;font-family:monospace;font-size:13px;">${value}</td>
                </tr>`;

            // C3-26: bundle card
            const bundleCard = cardWrap('Installed Bundles',
                bundles.length === 0
                    ? `<div class="text-dim" style="padding:12px;font-size:13px">No bundles installed. Run <code>clustr-serverd bundle install</code> to install the Slurm RPM bundle.</div>`
                    : `<div class="table-wrap"><table>
                        <thead><tr><th>Distro/Arch</th><th>Slurm Version</th><th>Release</th><th>Installed At</th></tr></thead>
                        <tbody>${bundles.map(b => `<tr>
                            <td class="text-mono">${escHtml(b.distro || '—')}-${escHtml(b.arch || '—')}</td>
                            <td class="text-mono">${escHtml(b.slurm_version || '—')}</td>
                            <td class="text-mono">${escHtml(b.clustr_release || '—')}</td>
                            <td>${escHtml(b.installed_at || '—')}</td>
                        </tr>`).join('')}</tbody>
                    </table></div>
                    <details style="margin-top:12px">
                        <summary style="cursor:pointer;font-size:12px;color:var(--text-secondary)">Re-install bundle</summary>
                        <div style="margin-top:8px;font-size:12px;color:var(--text-secondary)">
                            Run on the server to update or re-install the Slurm RPM bundle:
                            <pre style="background:var(--bg-secondary);border:1px solid var(--border);border-radius:4px;padding:8px;margin-top:6px;overflow-x:auto">clustr-serverd bundle install</pre>
                        </div>
                    </details>`
            );

            return `${Pages._initramfsCard(initramfsInfo)}
            ${bundleCard}
            <div class="card" style="margin-top:20px">
                <div class="card-header"><h2 class="card-title">Server Info</h2></div>
                <div style="padding:16px 20px;">
                    <table style="border-collapse:collapse;width:100%;max-width:560px;">
                        <tbody>
                            ${row('Version', escHtml(version))}
                            ${row('Commit', escHtml(commit))}
                            ${row('Built', escHtml(buildTime))}
                            ${row('Registered nodes', escHtml(nodeTotal))}
                            ${row('Flip-back failures', escHtml(flipFails))}
                            ${row('Metrics endpoint', `<a href="${window.location.origin}/metrics" target="_blank" rel="noopener" style="color:var(--accent)">${escHtml(window.location.origin + '/metrics')}</a>`)}
                        </tbody>
                    </table>
                    ${health?.flip_back_failures > 0 ? `<div style="margin-top:12px;" class="alert alert-warn">One or more verify-boot flip-back failures detected. Check logs — Proxmox persistent boot order may still be PXE-first on affected nodes.</div>` : ''}
                </div>
            </div>`;
        } catch (err) {
            return `<div class="card"><div class="card-header"><h2 class="card-title">System</h2></div><div style="padding:16px 20px;">${alertBox('Could not load system info: ' + err.message)}</div></div>`;
        }
    },

    async _settingsAboutTab() {
        try {
            const info = await API.health.get();
            const version   = info.version   || 'dev';
            const commit    = info.commit     || 'unknown';
            const buildTime = info.build_time || 'unknown';
            return `
                <div class="card">
                    <div class="card-header"><h2 class="card-title">About clustr</h2></div>
                    <div style="padding:16px 20px;">
                        <p style="margin:0 0 16px;color:var(--text-secondary);">
                            clustr — open-source node cloning and image management for HPC clusters.
                        </p>
                        <table style="border-collapse:collapse;width:100%;max-width:480px;">
                            <tbody>
                                <tr>
                                    <td style="padding:6px 16px 6px 0;color:var(--text-secondary);white-space:nowrap;font-size:13px;">Version</td>
                                    <td style="padding:6px 0;font-family:monospace;font-size:13px;">${escHtml(version)}</td>
                                </tr>
                                <tr>
                                    <td style="padding:6px 16px 6px 0;color:var(--text-secondary);white-space:nowrap;font-size:13px;">Commit</td>
                                    <td style="padding:6px 0;font-family:monospace;font-size:13px;">${escHtml(commit)}</td>
                                </tr>
                                <tr>
                                    <td style="padding:6px 16px 6px 0;color:var(--text-secondary);white-space:nowrap;font-size:13px;">Built</td>
                                    <td style="padding:6px 0;font-family:monospace;font-size:13px;">${escHtml(buildTime)}</td>
                                </tr>
                            </tbody>
                        </table>
                    </div>
                </div>`;
        } catch (err) {
            return `
                <div class="card">
                    <div class="card-header"><h2 class="card-title">About clustr</h2></div>
                    <div style="padding:16px 20px;">
                        <p style="margin:0 0 16px;color:var(--text-secondary);">
                            clustr — open-source node cloning and image management for HPC clusters.
                        </p>
                        ${alertBox('Could not load build info: ' + err.message)}
                    </div>
                </div>`;
        }
    },

    _settingsCreateUserModal() {
        const modal = document.createElement('div');
        modal.id = 'create-user-modal';
        modal.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.5);display:flex;align-items:center;justify-content:center;z-index:1000;';
        modal.innerHTML = `
            <div class="card" style="width:480px;max-width:95vw;">
                <div class="card-header">
                    <h2 class="card-title">Create User</h2>
                    <button class="btn btn-ghost btn-sm" onclick="document.getElementById('create-user-modal').remove()">×</button>
                </div>
                <div style="padding:16px;display:flex;flex-direction:column;gap:12px;">
                    <label class="form-label">Username
                        <input id="cum-username" class="form-input" type="text" placeholder="alice" style="margin-top:4px;">
                    </label>
                    <label class="form-label">Password (min 8 chars)
                        <input id="cum-password" class="form-input" type="password" style="margin-top:4px;">
                    </label>
                    <label class="form-label">Role
                        <select id="cum-role" class="form-input" style="margin-top:4px;">
                            <option value="admin">admin — full access</option>
                            <option value="operator" selected>operator — nodes/deploys, no user management</option>
                            <option value="readonly">readonly — read-only access</option>
                        </select>
                    </label>
                    <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:8px;">
                        <button class="btn btn-secondary" onclick="document.getElementById('create-user-modal').remove()">Cancel</button>
                        <button class="btn btn-primary" onclick="Pages._settingsCreateUserSubmit()">Create</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(modal);
    },

    async _settingsCreateUserSubmit() {
        const username = document.getElementById('cum-username').value.trim();
        const password = document.getElementById('cum-password').value;
        const role     = document.getElementById('cum-role').value;

        if (!username || !password) {
            App.toast('Username and password are required', 'error');
            return;
        }
        if (password.length < 8) {
            App.toast('Password must be at least 8 characters', 'error');
            return;
        }

        try {
            await API.users.create({ username, password, role });
            document.getElementById('create-user-modal').remove();
            App.toast('User created', 'success');
            Pages._settingsRender('users');
        } catch (err) {
            App.toast('Create failed: ' + err.message, 'error');
        }
    },

    // B4-5: replaced prompt() with proper modals for password reset and role change.
    _settingsResetUserPassword(id) {
        const mid = 'reset-pw-modal';
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = mid;
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        overlay.innerHTML = `
            <div class="modal" style="max-width:420px" aria-labelledby="${mid}-title">
                <div class="modal-header">
                    <span class="modal-title" id="${mid}-title">Reset Password</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('${mid}').remove()">×</button>
                </div>
                <div class="modal-body" style="display:flex;flex-direction:column;gap:14px;">
                    <p style="margin:0;font-size:13px;color:var(--text-secondary);">
                        Set a temporary password. The user will be required to change it on next login.
                    </p>
                    <label class="form-label">New temporary password (min 8 chars)
                        <input id="rp-pw" class="form-input" type="password" placeholder="••••••••" autocomplete="new-password" style="margin-top:4px;">
                    </label>
                    <div style="display:flex;gap:8px;justify-content:flex-end;">
                        <button class="btn btn-secondary" onclick="document.getElementById('${mid}').remove()">Cancel</button>
                        <button class="btn btn-primary" onclick="Pages._settingsResetUserPasswordSubmit('${mid}', '${escHtml(id)}')">Reset Password</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(overlay);
        overlay.addEventListener('click', e => { if (e.target === overlay) overlay.remove(); });
        trapModalFocus(overlay, () => overlay.remove());
    },

    async _settingsResetUserPasswordSubmit(mid, id) {
        const pw = (document.getElementById('rp-pw')?.value || '').trim();
        if (pw.length < 8) { App.toast('Password must be at least 8 characters', 'error'); return; }
        try {
            await API.users.resetPassword(id, pw);
            document.getElementById(mid)?.remove();
            App.toast('Password reset — user must change on next login', 'success');
        } catch (err) {
            App.toast('Reset failed: ' + err.message, 'error');
        }
    },

    _settingsChangeUserRole(id, currentRole) {
        const roles = ['admin', 'operator', 'readonly'];
        const mid = 'change-role-modal';
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = mid;
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        overlay.innerHTML = `
            <div class="modal" style="max-width:420px" aria-labelledby="${mid}-title">
                <div class="modal-header">
                    <span class="modal-title" id="${mid}-title">Change Role</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('${mid}').remove()">×</button>
                </div>
                <div class="modal-body" style="display:flex;flex-direction:column;gap:14px;">
                    <p style="margin:0;font-size:13px;color:var(--text-secondary);">
                        Current role: <strong>${escHtml(currentRole)}</strong>
                    </p>
                    <label class="form-label">New role
                        <select id="cr-role" class="form-input" style="margin-top:4px;">
                            ${roles.filter(r => r !== currentRole).map(r =>
                                `<option value="${r}">${r}</option>`).join('')}
                        </select>
                    </label>
                    <div style="display:flex;gap:8px;justify-content:flex-end;">
                        <button class="btn btn-secondary" onclick="document.getElementById('${mid}').remove()">Cancel</button>
                        <button class="btn btn-primary" onclick="Pages._settingsChangeUserRoleSubmit('${mid}', '${escHtml(id)}')">Change Role</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(overlay);
        overlay.addEventListener('click', e => { if (e.target === overlay) overlay.remove(); });
        trapModalFocus(overlay, () => overlay.remove());
    },

    async _settingsChangeUserRoleSubmit(mid, id) {
        const role = document.getElementById('cr-role')?.value;
        if (!role) return;
        try {
            await API.users.update(id, { role });
            document.getElementById(mid)?.remove();
            App.toast('Role updated', 'success');
            Pages._settingsRender('users');
        } catch (err) {
            App.toast('Update failed: ' + err.message, 'error');
        }
    },

    async _settingsDisableUser(id, username) {
        Pages.showConfirmModal({
            title: 'Disable User',
            message: `Disable user <strong>${escHtml(username)}</strong>? They will not be able to log in.`,
            confirmText: 'Disable',
            danger: true,
            onConfirm: async () => {
                try {
                    await API.users.update(id, { disabled: true });
                    App.toast('User disabled', 'success');
                    Pages._settingsRender('users');
                } catch (err) {
                    App.toast('Disable failed: ' + err.message, 'error');
                }
            },
        });
    },

    _settingsCreateKeyModal() {
        // S5-6: CI key preset — default expires 30 days out.
        const defaultExpires = new Date(Date.now() + 30 * 24 * 3600 * 1000);
        const expiresLocal = new Date(defaultExpires.getTime() - defaultExpires.getTimezoneOffset() * 60000)
            .toISOString().slice(0, 16);

        const modal = document.createElement('div');
        modal.id = 'create-key-modal';
        modal.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.5);display:flex;align-items:center;justify-content:center;z-index:1000;';
        modal.innerHTML = `
            <div class="card" style="width:520px;max-width:95vw;">
                <div class="card-header">
                    <h2 class="card-title">Create API Key</h2>
                    <button class="btn btn-ghost btn-sm" onclick="document.getElementById('create-key-modal').remove()">×</button>
                </div>
                <div style="padding:16px;display:flex;flex-direction:column;gap:12px;">
                    <!-- S5-6: CI preset shortcut -->
                    <div style="background:var(--surface-secondary);border:1px solid var(--border);border-radius:6px;padding:10px 14px;display:flex;align-items:center;gap:10px">
                        <span style="font-size:12px;color:var(--text-secondary)">Quick preset:</span>
                        <button class="btn btn-secondary btn-sm" onclick="Pages._settingsCIKeyPreset()">
                            CI / Pipeline key (node-scoped, 30-day TTL)
                        </button>
                    </div>
                    <label class="form-label">Scope
                        <select id="ckm-scope" class="form-input" style="margin-top:4px;">
                            <option value="admin">admin — full access</option>
                            <option value="node">node — deploy agent only</option>
                        </select>
                    </label>
                    <label class="form-label">Label (e.g. "ci-runner", "robert-laptop")
                        <input id="ckm-label" class="form-input" type="text" placeholder="ci-runner" style="margin-top:4px;">
                    </label>
                    <label class="form-label" id="ckm-nodeid-row" style="display:none;">Node ID (required for node scope)
                        <input id="ckm-nodeid" class="form-input" type="text" placeholder="node UUID" style="margin-top:4px;">
                    </label>
                    <label class="form-label">Expires (optional — leave blank for no expiry)
                        <input id="ckm-expires" class="form-input" type="datetime-local" style="margin-top:4px;">
                    </label>
                    <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:8px;">
                        <button class="btn btn-secondary" onclick="document.getElementById('create-key-modal').remove()">Cancel</button>
                        <button class="btn btn-primary" onclick="Pages._settingsCreateKeySubmit()">Create</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(modal);

        document.getElementById('ckm-scope').addEventListener('change', (e) => {
            document.getElementById('ckm-nodeid-row').style.display = e.target.value === 'node' ? '' : 'none';
        });
    },

    // S5-6: Pre-fill the create-key modal with CI/pipeline defaults.
    // node scope, label "ci-key", 30-day TTL.
    _settingsCIKeyPreset() {
        const scopeEl   = document.getElementById('ckm-scope');
        const labelEl   = document.getElementById('ckm-label');
        const expiresEl = document.getElementById('ckm-expires');
        const nodeRow   = document.getElementById('ckm-nodeid-row');
        if (scopeEl)   { scopeEl.value = 'node'; }
        if (labelEl)   { labelEl.value = 'ci-key'; }
        if (nodeRow)   { nodeRow.style.display = ''; }
        if (expiresEl) {
            const d = new Date(Date.now() + 30 * 24 * 3600 * 1000);
            expiresEl.value = new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
        }
        App.toast('CI preset applied — enter the Node ID and click Create', 'info');
    },

    async _settingsCreateKeySubmit() {
        const scope    = document.getElementById('ckm-scope').value;
        const label    = document.getElementById('ckm-label').value.trim();
        const nodeID   = document.getElementById('ckm-nodeid').value.trim();
        const expiresV = document.getElementById('ckm-expires').value;

        if (scope === 'node' && !nodeID) {
            App.toast('Node ID is required for node-scoped keys', 'error');
            return;
        }

        let expiresAt = '';
        if (expiresV) {
            expiresAt = new Date(expiresV).toISOString();
        }

        try {
            const resp = await API.apiKeys.create({ scope, label, node_id: nodeID, expires_at: expiresAt });
            document.getElementById('create-key-modal').remove();
            // S5-6: For CI (node-scoped) keys, show a curl snippet after creation.
            if (scope === 'node') {
                Pages._settingsShowCIKeySnippet(resp.key, nodeID || 'NODE_ID');
            } else {
                Pages._settingsShowRawKey(resp.key, 'New API Key Created');
            }
        } catch (err) {
            App.toast('Create failed: ' + err.message, 'error');
        }
    },

    // S5-6: After creating a node-scoped key, show the raw key plus a curl snippet
    // that the operator can paste directly into their CI pipeline.
    _settingsShowCIKeySnippet(rawKey, nodeId) {
        const origin = window.location.origin;
        const curlSnippet = `curl -s -X POST ${origin}/api/v1/nodes/${nodeId}/reimage \\
  -H "Authorization: Bearer ${rawKey}" \\
  -H "Content-Type: application/json" \\
  -d '{"dry_run": false}'`;

        const modal = document.createElement('div');
        modal.id = 'rawkey-modal';
        modal.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.6);display:flex;align-items:center;justify-content:center;z-index:1001;';
        modal.innerHTML = `
            <div class="card" style="width:600px;max-width:95vw;">
                <div class="card-header">
                    <h2 class="card-title">CI Key Created</h2>
                </div>
                <div style="padding:16px;display:flex;flex-direction:column;gap:12px;">
                    <div class="alert alert-warning">
                        <strong>Save this key now.</strong> It will not be shown again.
                    </div>
                    <div>
                        <div style="font-size:12px;font-weight:600;margin-bottom:6px;color:var(--text-secondary)">API KEY</div>
                        <div style="background:var(--bg-primary);border:1px solid var(--border);border-radius:6px;padding:10px 12px;font-family:var(--font-mono);font-size:13px;word-break:break-all;">${escHtml(rawKey)}</div>
                    </div>
                    <div>
                        <div style="font-size:12px;font-weight:600;margin-bottom:6px;color:var(--text-secondary)">CURL SNIPPET — trigger a reimage from CI</div>
                        <pre style="background:var(--bg-primary);border:1px solid var(--border);border-radius:6px;padding:10px 12px;font-size:12px;overflow:auto;white-space:pre;margin:0">${escHtml(curlSnippet)}</pre>
                    </div>
                    <div style="display:flex;justify-content:flex-end;gap:8px;margin-top:4px">
                        <button class="btn btn-secondary" onclick="navigator.clipboard&&navigator.clipboard.writeText(${JSON.stringify(rawKey)}).then(()=>App.toast('Key copied','success'))">Copy Key</button>
                        <button class="btn btn-primary" onclick="document.getElementById('rawkey-modal').remove()">Done</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(modal);
    },

    async _settingsRotateKey(id) {
        Pages.showConfirmModal({
            title: 'Rotate API Key',
            message: 'Rotate this key? The old key will stop working immediately.',
            confirmText: 'Rotate',
            danger: true,
            onConfirm: async () => {
                try {
                    const resp = await API.apiKeys.rotate(id);
                    Pages._settingsShowRawKey(resp.key, 'Key Rotated');
                } catch (err) {
                    App.toast('Rotate failed: ' + err.message, 'error');
                }
            },
        });
    },

    async _settingsRevokeKey(id, label) {
        Pages.showConfirmModal({
            title: 'Revoke API Key',
            message: `Revoke key <strong>${escHtml(label)}</strong>? This cannot be undone.`,
            confirmText: 'Revoke',
            danger: true,
            onConfirm: async () => {
                try {
                    await API.apiKeys.revoke(id);
                    App.toast('Key revoked', 'success');
                    Pages._settingsRender('api-keys');
                } catch (err) {
                    App.toast('Revoke failed: ' + err.message, 'error');
                }
            },
        });
    },

    _settingsShowRawKey(rawKey, title) {
        const modal = document.createElement('div');
        modal.id = 'rawkey-modal';
        modal.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.6);display:flex;align-items:center;justify-content:center;z-index:1001;';
        modal.innerHTML = `
            <div class="card" style="width:560px;max-width:95vw;">
                <div class="card-header">
                    <h2 class="card-title">${escHtml(title)}</h2>
                </div>
                <div style="padding:16px;">
                    <div class="alert alert-warning" style="margin-bottom:12px;">
                        <strong>Save this key now.</strong> It will not be shown again.
                    </div>
                    <div style="background:var(--bg-primary);border:1px solid var(--border);border-radius:6px;padding:12px;font-family:var(--font-mono);font-size:13px;word-break:break-all;margin-bottom:12px;">
                        ${escHtml(rawKey)}
                    </div>
                    <div style="display:flex;gap:8px;justify-content:flex-end;">
                        <button class="btn btn-secondary" onclick="navigator.clipboard.writeText(${JSON.stringify(rawKey)}).then(()=>App.toast('Copied','success'))">Copy to clipboard</button>
                        <button class="btn btn-primary" onclick="document.getElementById('rawkey-modal').remove();Pages._settingsRender('api-keys')">Done</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(modal);
    },

    // ─── B3-1/B3-2: Webhooks Settings Tab ────────────────────────────────────

    async _settingsWebhooksTab() {
        try {
            const data = await API.request('GET', '/admin/webhooks');
            const webhooks = (data && data.webhooks) ? data.webhooks : [];
            const rows = webhooks.length === 0
                ? `<tr><td colspan="5" style="text-align:center;color:var(--text-secondary);padding:24px;">No webhook subscriptions. Click + Add Webhook to create one.</td></tr>`
                : webhooks.map(wh => `
                    <tr>
                        <td style="max-width:280px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" title="${escHtml(wh.url)}">
                            <code style="font-size:12px;">${escHtml(wh.url)}</code>
                        </td>
                        <td>${(wh.events || []).map(e => `<span class="badge badge-neutral badge-sm" style="margin-right:2px;">${escHtml(e)}</span>`).join('')}</td>
                        <td>${wh.enabled ? '<span class="badge badge-ready">Enabled</span>' : '<span class="badge badge-neutral">Disabled</span>'}</td>
                        <td style="font-size:12px;color:var(--text-secondary);">${fmtDate(wh.created_at)}</td>
                        <td style="text-align:right;">
                            <button class="btn btn-secondary btn-sm" onclick="Pages._webhookDeliveriesModal('${escHtml(wh.id)}', '${escHtml(wh.url)}')">Deliveries</button>
                            <button class="btn btn-secondary btn-sm" style="margin-left:4px;" onclick="Pages._webhookEditModal('${escHtml(wh.id)}', '${escHtml(wh.url)}', ${JSON.stringify(wh.events||[])}, ${wh.enabled})">Edit</button>
                            <button class="btn btn-danger btn-sm" style="margin-left:4px;" onclick="Pages._webhookDelete('${escHtml(wh.id)}', '${escHtml(wh.url)}')">Delete</button>
                        </td>
                    </tr>`).join('');
            return `
                <div class="card">
                    <div class="card-header">
                        <h2 class="card-title">Webhook Subscriptions</h2>
                        <button class="btn btn-primary btn-sm" onclick="Pages._webhookCreateModal()">+ Add Webhook</button>
                    </div>
                    <div class="card-body">
                        <p style="font-size:13px;color:var(--text-secondary);margin-bottom:12px;">
                            Webhooks notify external services when cluster events occur. Clustr signs each delivery with your configured secret.
                        </p>
                        <div style="overflow-x:auto;">
                            <table class="table">
                                <thead><tr>
                                    <th>URL</th><th>Events</th><th>Status</th><th>Created</th><th></th>
                                </tr></thead>
                                <tbody>${rows}</tbody>
                            </table>
                        </div>
                    </div>
                </div>`;
        } catch (err) {
            return alertBox('Failed to load webhooks: ' + err.message);
        }
    },

    // ─── Sprint D — Notifications / SMTP Settings Tab ────────────────────────

    async _settingsNotificationsTab() {
        let smtp = {};
        let groups = [];
        try {
            smtp = await API.request('GET', '/admin/smtp');
        } catch (_) {}
        try {
            const gData = await API.request('GET', '/node-groups');
            groups = (gData && gData.groups) ? gData.groups : [];
        } catch (_) {}
        const groupOptions = groups.map(g => `<option value="${escHtml(g.id)}">${escHtml(g.name)}</option>`).join('');
        const checked = (v) => v ? 'checked' : '';
        return `
            <div class="card" style="margin-bottom:20px;">
                <div class="card-header">
                    <h2 class="card-title">SMTP Configuration</h2>
                    <div style="font-size:12px;color:var(--text-secondary);">Used for member notifications and group broadcasts. Password is stored encrypted.</div>
                </div>
                <div class="card-body">
                    <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px;">
                        <label class="form-label">Host
                            <input id="smtp-host" class="form-input" type="text" value="${escHtml(smtp.host || '')}" placeholder="smtp.example.com" style="margin-top:4px;">
                        </label>
                        <label class="form-label">Port
                            <input id="smtp-port" class="form-input" type="number" value="${smtp.port || 587}" placeholder="587" style="margin-top:4px;">
                        </label>
                        <label class="form-label">Username
                            <input id="smtp-user" class="form-input" type="text" value="${escHtml(smtp.username || '')}" placeholder="noreply@example.com" style="margin-top:4px;">
                        </label>
                        <label class="form-label">Password
                            <input id="smtp-pass" class="form-input" type="password" value="" placeholder="leave blank to keep existing" style="margin-top:4px;" autocomplete="new-password">
                        </label>
                        <label class="form-label">From address
                            <input id="smtp-from" class="form-input" type="text" value="${escHtml(smtp.from_addr || '')}" placeholder="clustr &lt;noreply@example.com&gt;" style="margin-top:4px;">
                        </label>
                        <div style="display:flex;flex-direction:column;gap:8px;padding-top:20px;">
                            <label style="display:flex;align-items:center;gap:8px;font-size:13px;">
                                <input type="checkbox" id="smtp-tls" ${checked(smtp.use_tls)}>
                                STARTTLS (port 587)
                            </label>
                            <label style="display:flex;align-items:center;gap:8px;font-size:13px;">
                                <input type="checkbox" id="smtp-ssl" ${checked(smtp.use_ssl)}>
                                Implicit TLS (port 465)
                            </label>
                        </div>
                    </div>
                    <div style="display:flex;gap:8px;margin-top:16px;align-items:center;">
                        <button class="btn btn-primary" onclick="Pages._smtpSave()">Save SMTP settings</button>
                        <button class="btn btn-secondary" onclick="Pages._smtpTest()">Send test email</button>
                        <span id="smtp-status" style="font-size:13px;"></span>
                    </div>
                </div>
            </div>
            <div class="card">
                <div class="card-header">
                    <h2 class="card-title">Broadcast to NodeGroup</h2>
                    <div style="font-size:12px;color:var(--text-secondary);">Send a message to all approved members of a NodeGroup. Rate-limited to once per hour.</div>
                </div>
                <div class="card-body">
                    <label class="form-label" style="margin-bottom:12px;">NodeGroup
                        <select id="bc-group" class="form-input" style="margin-top:4px;">
                            <option value="">Select a group…</option>
                            ${groupOptions}
                        </select>
                    </label>
                    <label class="form-label" style="margin-bottom:12px;">Subject
                        <input id="bc-subject" class="form-input" type="text" placeholder="Message subject" style="margin-top:4px;">
                    </label>
                    <label class="form-label" style="margin-bottom:12px;">Body
                        <textarea id="bc-body" class="form-input" rows="5" placeholder="Message body (plain text)" style="margin-top:4px;min-height:100px;resize:vertical;"></textarea>
                    </label>
                    <button class="btn btn-primary" onclick="Pages._bcSend()">Send broadcast</button>
                    <span id="bc-status" style="font-size:13px;margin-left:12px;"></span>
                </div>
            </div>`;
    },

    // ── Governance tab (E1/E2/E3/E4 admin surfaces) ───────────────────────
    async _settingsGovernanceTab() {
        // Load all data in parallel: pending change requests, recent history, FOS list, vis defaults.
        let pendingRequests = [];
        let historicalRequests = [];
        let fosList = [];
        let visDefaults = [];
        try {
            const [pendingData, histData] = await Promise.all([
                API.request('GET', '/admin/change-requests?status=pending&limit=50'),
                API.request('GET', '/admin/change-requests?status=approved&limit=20'),
            ]);
            pendingRequests = (pendingData && pendingData.requests) || [];
            historicalRequests = (histData && histData.requests) || [];
        } catch (_) {}
        // Also fetch denied history.
        try {
            const deniedData = await API.request('GET', '/admin/change-requests?status=denied&limit=20');
            const denied = (deniedData && deniedData.requests) || [];
            historicalRequests = [...historicalRequests, ...denied].sort((a, b) =>
                new Date(b.updated_at || b.created_at) - new Date(a.updated_at || a.created_at)
            ).slice(0, 20);
        } catch (_) {}
        try {
            const d = await API.request('GET', '/admin/fields-of-science');
            fosList = d.fields_of_science || [];
        } catch (_) {}
        try {
            const d = await API.request('GET', '/admin/attribute-visibility-defaults');
            visDefaults = d.defaults || [];
        } catch (_) {}

        const statusBadge = (s) => {
            const map = { pending: 'badge-warn', approved: 'badge-ready', denied: 'badge-error', expired: 'badge-neutral', withdrawn: 'badge-neutral' };
            return `<span class="badge ${map[s] || 'badge-neutral'}" style="font-size:11px">${escHtml(s)}</span>`;
        };
        const typeBadge = (t) => `<span class="badge badge-info" style="font-size:11px">${escHtml((t||'').replace(/_/g,' '))}</span>`;

        const pendingRows = pendingRequests.length
            ? pendingRequests.map(r => `<tr>
                <td style="font-size:12px;color:var(--text-secondary)">${escHtml(r.id.substring(0,8))}</td>
                <td>${escHtml(r.pi_username || r.pi_user_id || '—')}</td>
                <td>${escHtml(r.group_name || r.group_id || '—')}</td>
                <td>${typeBadge(r.request_type)}</td>
                <td style="font-size:12px;max-width:200px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="${escHtml(r.justification||'')}">${escHtml(r.justification||'—')}</td>
                <td>${statusBadge(r.status)}</td>
                <td style="font-size:12px">${fmtRelative(r.created_at)}</td>
                <td>
                    <div class="flex gap-6">
                        <button class="btn btn-primary btn-sm" onclick="Pages._acrReview('${escHtml(r.id)}', 'approved')">Approve</button>
                        <button class="btn btn-danger btn-sm" onclick="Pages._acrReview('${escHtml(r.id)}', 'denied')">Deny</button>
                    </div>
                </td>
            </tr>`).join('')
            : `<tr><td colspan="8" style="text-align:center;color:var(--text-secondary);padding:24px">No pending requests</td></tr>`;

        const histRows = historicalRequests.slice(0, 20).map(r => `<tr>
            <td style="font-size:12px;color:var(--text-secondary)">${escHtml(r.id.substring(0,8))}</td>
            <td>${escHtml(r.pi_username || r.pi_user_id || '—')}</td>
            <td>${escHtml(r.group_name || r.group_id || '—')}</td>
            <td>${typeBadge(r.request_type)}</td>
            <td>${statusBadge(r.status)}</td>
            <td style="font-size:12px">${fmtRelative(r.updated_at || r.created_at)}</td>
        </tr>`).join('') || `<tr><td colspan="6" style="text-align:center;color:var(--text-secondary);padding:16px">No history yet</td></tr>`;

        // FOS table rows — two-level hierarchy (group by parent)
        const topLevel = fosList.filter(f => !f.parent_id);
        const fosRows = topLevel.map(parent => {
            const children = fosList.filter(f => f.parent_id === parent.id);
            const parentRow = `<tr style="background:var(--bg-subtle,#161b22)">
                <td colspan="3" style="font-weight:600;padding:6px 12px">${escHtml(parent.name)} <span style="font-size:11px;color:var(--text-secondary)">${escHtml(parent.nsf_code||'')}</span></td>
                <td style="padding:6px 12px">
                    <button class="btn btn-secondary btn-sm" onclick="Pages._fosEdit(${JSON.stringify(JSON.stringify(parent))})">Edit</button>
                </td>
            </tr>`;
            const childRows = children.map(c => `<tr>
                <td style="padding:4px 12px 4px 28px;color:var(--text-secondary);font-size:13px">${escHtml(c.nsf_code||'')}</td>
                <td style="padding:4px 12px;font-size:13px" colspan="2">${escHtml(c.name)}</td>
                <td style="padding:4px 12px">
                    <button class="btn btn-secondary btn-sm" onclick="Pages._fosEdit(${JSON.stringify(JSON.stringify(c))})">Edit</button>
                </td>
            </tr>`).join('');
            return parentRow + childRows;
        }).join('');

        // Visibility defaults table
        const visLevels = ['public', 'member', 'pi', 'admin_only'];
        const visRows = visDefaults.map(v => `<tr>
            <td style="font-size:13px;font-weight:600">${escHtml(v.attribute_name)}</td>
            <td>
                <select class="form-input" style="font-size:12px;padding:4px 8px" onchange="Pages._visDefaultUpdate('${escHtml(v.attribute_name)}', this.value)">
                    ${visLevels.map(l => `<option value="${l}" ${v.default_visibility === l ? 'selected' : ''}>${l}</option>`).join('')}
                </select>
            </td>
            <td style="font-size:11px;color:var(--text-secondary)">${escHtml(v.description||'—')}</td>
        </tr>`).join('') || `<tr><td colspan="3" style="padding:16px;color:var(--text-secondary)">No defaults configured</td></tr>`;

        return `
            <!-- E1: Allocation Change Requests -->
            <div class="card" style="margin-bottom:20px">
                <div class="card-header" style="justify-content:space-between">
                    <div>
                        <h2 class="card-title">Allocation Change Requests</h2>
                        <div style="font-size:12px;color:var(--text-secondary)">PI-submitted requests for allocation changes requiring admin review</div>
                    </div>
                    <button class="btn btn-secondary btn-sm" onclick="Pages._settingsRender('governance')">Refresh</button>
                </div>
                <div class="card-body" style="padding:0">
                    <div style="padding:10px 16px;font-size:12px;font-weight:600;color:var(--text-secondary);text-transform:uppercase;letter-spacing:.05em;border-bottom:1px solid var(--border)">
                        Pending (${pendingRequests.length})
                    </div>
                    <div class="table-wrap" style="margin:0">
                        <table>
                            <thead><tr>
                                <th>ID</th><th>PI</th><th>Group</th><th>Type</th><th>Justification</th><th>Status</th><th>Submitted</th><th>Actions</th>
                            </tr></thead>
                            <tbody id="acr-pending-tbody">${pendingRows}</tbody>
                        </table>
                    </div>
                    <div style="padding:10px 16px;font-size:12px;font-weight:600;color:var(--text-secondary);text-transform:uppercase;letter-spacing:.05em;border-bottom:1px solid var(--border);border-top:1px solid var(--border);margin-top:12px">
                        Recent History
                    </div>
                    <div class="table-wrap" style="margin:0">
                        <table>
                            <thead><tr>
                                <th>ID</th><th>PI</th><th>Group</th><th>Type</th><th>Status</th><th>Updated</th>
                            </tr></thead>
                            <tbody>${histRows}</tbody>
                        </table>
                    </div>
                </div>
            </div>

            <!-- E2: Fields of Science -->
            <div class="card" style="margin-bottom:20px">
                <div class="card-header" style="justify-content:space-between">
                    <div>
                        <h2 class="card-title">Fields of Science</h2>
                        <div style="font-size:12px;color:var(--text-secondary)">NSF FOS taxonomy — ${fosList.length} entries. PIs assign their group's primary field.</div>
                    </div>
                    <button class="btn btn-primary btn-sm" onclick="Pages._fosCreate()">Add FOS entry</button>
                </div>
                <div class="card-body" style="padding:0">
                    <div class="table-wrap" style="margin:0">
                        <table>
                            <thead><tr><th>NSF Code</th><th colspan="2">Name</th><th>Actions</th></tr></thead>
                            <tbody>${fosRows || '<tr><td colspan="4" style="padding:16px;text-align:center;color:var(--text-secondary)">No FOS entries yet</td></tr>'}</tbody>
                        </table>
                    </div>
                </div>
            </div>

            <!-- E3: Attribute Visibility Defaults -->
            <div class="card">
                <div class="card-header">
                    <div>
                        <h2 class="card-title">Attribute Visibility Defaults</h2>
                        <div style="font-size:12px;color:var(--text-secondary)">Global default visibility levels per attribute. PIs can override per-group. Levels: public > member > pi > admin_only.</div>
                    </div>
                </div>
                <div class="card-body" style="padding:0">
                    <div class="table-wrap" style="margin:0">
                        <table>
                            <thead><tr><th>Attribute</th><th>Default Visibility</th><th>Description</th></tr></thead>
                            <tbody id="vis-defaults-tbody">${visRows}</tbody>
                        </table>
                    </div>
                </div>
            </div>`;
    },

    // _acrReview opens a confirm-then-review modal for allocation change requests.
    async _acrReview(reqID, decision) {
        const note = decision === 'denied'
            ? window.prompt(`Deny request ${reqID.substring(0,8)}? Add a note for the PI (optional):`, '')
            : '';
        if (note === null) return; // user cancelled prompt
        try {
            await API.request('POST', `/admin/change-requests/${reqID}/review`, {
                status: decision,
                review_notes: note || '',
            });
            App.toast(`Request ${decision}`, decision === 'approved' ? 'success' : 'error');
            Pages._settingsRender('governance');
        } catch (err) {
            App.toast(`Failed: ${err.message}`, 'error');
        }
    },

    // _fosCreate opens a modal to add a new FOS entry.
    _fosCreate() {
        Pages._fosModal(null);
    },

    // _fosEdit opens a modal to edit an existing FOS entry.
    _fosEdit(jsonStr) {
        const fos = JSON.parse(jsonStr);
        Pages._fosModal(fos);
    },

    _fosModal(existing) {
        const id = 'fos-modal';
        const old = document.getElementById(id);
        if (old) old.remove();
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = id;
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        overlay.innerHTML = `
            <div class="modal" style="max-width:480px" aria-labelledby="${id}-title">
                <div class="modal-header">
                    <span class="modal-title" id="${id}-title">${existing ? 'Edit Field of Science' : 'Add Field of Science'}</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('${id}').remove()">×</button>
                </div>
                <div class="modal-body" style="display:flex;flex-direction:column;gap:14px">
                    <label class="form-label">NSF Code (optional)
                        <input id="fos-code" class="form-input" type="text" value="${escHtml(existing?.nsf_code||'')}" placeholder="e.g. 2103" style="margin-top:4px">
                    </label>
                    <label class="form-label">Name
                        <input id="fos-name" class="form-input" type="text" value="${escHtml(existing?.name||'')}" placeholder="e.g. Artificial Intelligence" style="margin-top:4px">
                    </label>
                    <label class="form-label">Parent ID (leave blank for top-level)
                        <input id="fos-parent" class="form-input" type="text" value="${escHtml(existing?.parent_id||'')}" placeholder="parent FOS ID" style="margin-top:4px">
                    </label>
                    <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:8px">
                        <button class="btn btn-secondary" onclick="document.getElementById('${id}').remove()">Cancel</button>
                        <button class="btn btn-primary" onclick="Pages._fosSubmit('${id}', ${existing ? `'${escHtml(existing.id)}'` : 'null'})">${existing ? 'Save' : 'Create'}</button>
                    </div>
                    <div id="fos-err" style="color:var(--error,#f85149);font-size:13px;display:none"></div>
                </div>
            </div>`;
        document.body.appendChild(overlay);
        overlay.addEventListener('click', e => { if (e.target === overlay) overlay.remove(); });
    },

    async _fosSubmit(modalId, fosID) {
        const errEl = document.getElementById('fos-err');
        const name = (document.getElementById('fos-name')?.value || '').trim();
        const code = (document.getElementById('fos-code')?.value || '').trim();
        const parent = (document.getElementById('fos-parent')?.value || '').trim();
        if (!name) { if (errEl) { errEl.textContent = 'Name is required'; errEl.style.display=''; } return; }
        try {
            const body = { name, nsf_code: code || null, parent_id: parent || null };
            if (fosID) {
                await API.request('PUT', `/admin/fields-of-science/${fosID}`, body);
            } else {
                await API.request('POST', '/admin/fields-of-science', body);
            }
            document.getElementById(modalId)?.remove();
            App.toast(fosID ? 'FOS entry updated' : 'FOS entry created', 'success');
            Pages._settingsRender('governance');
        } catch (err) {
            if (errEl) { errEl.textContent = 'Error: ' + err.message; errEl.style.display=''; }
        }
    },

    // _visDefaultUpdate updates a global attribute visibility default.
    async _visDefaultUpdate(attr, level) {
        try {
            await API.request('PUT', `/admin/attribute-visibility-defaults/${encodeURIComponent(attr)}`, { default_visibility: level });
            App.toast(`${attr} default set to ${level}`, 'success');
        } catch (err) {
            App.toast(`Failed to update: ${err.message}`, 'error');
        }
    },

    async _smtpSave() {
        const statusEl = document.getElementById('smtp-status');
        if (statusEl) statusEl.textContent = 'Saving…';
        const body = {
            host:      (document.getElementById('smtp-host')?.value || '').trim(),
            port:      parseInt(document.getElementById('smtp-port')?.value || '587', 10),
            username:  (document.getElementById('smtp-user')?.value || '').trim(),
            from_addr: (document.getElementById('smtp-from')?.value || '').trim(),
            use_tls:   document.getElementById('smtp-tls')?.checked || false,
            use_ssl:   document.getElementById('smtp-ssl')?.checked || false,
        };
        const pass = (document.getElementById('smtp-pass')?.value || '').trim();
        if (pass) body.password = pass;
        try {
            await API.request('PUT', '/admin/smtp', body);
            if (statusEl) { statusEl.textContent = 'Saved.'; statusEl.style.color = 'var(--success, #3fb950)'; }
            App.toast('SMTP settings saved', 'success');
        } catch (err) {
            if (statusEl) { statusEl.textContent = 'Error: ' + err.message; statusEl.style.color = 'var(--error, #f85149)'; }
        }
    },

    async _smtpTest() {
        const statusEl = document.getElementById('smtp-status');
        if (statusEl) { statusEl.textContent = 'Sending test…'; statusEl.style.color = ''; }
        try {
            await API.request('POST', '/admin/smtp/test', {});
            if (statusEl) { statusEl.textContent = 'Test sent. Check your inbox.'; statusEl.style.color = 'var(--success, #3fb950)'; }
            App.toast('Test email sent', 'success');
        } catch (err) {
            if (statusEl) { statusEl.textContent = 'Test failed: ' + err.message; statusEl.style.color = 'var(--error, #f85149)'; }
        }
    },

    async _bcLoadGroup(groupID) {
        // Pre-populate when switching groups — no-op here, send does validation.
    },

    async _bcSend() {
        const groupID = (document.getElementById('bc-group')?.value || '').trim();
        const subject = (document.getElementById('bc-subject')?.value || '').trim();
        const body    = (document.getElementById('bc-body')?.value || '').trim();
        const statusEl = document.getElementById('bc-status');
        if (!groupID) { App.toast('Select a group', 'error'); return; }
        if (!subject) { App.toast('Subject is required', 'error'); return; }
        if (!body)    { App.toast('Body is required', 'error'); return; }
        if (!confirm(`Send broadcast to all approved members of this group?\n\nSubject: ${subject}`)) return;
        if (statusEl) { statusEl.textContent = 'Sending…'; statusEl.style.color = ''; }
        try {
            const resp = await API.request('POST', `/node-groups/${groupID}/broadcast`, { subject, body });
            if (statusEl) { statusEl.textContent = `Sent to ${resp.sent || 0} recipient(s).`; statusEl.style.color = 'var(--success, #3fb950)'; }
            App.toast('Broadcast sent', 'success');
        } catch (err) {
            if (statusEl) { statusEl.textContent = 'Error: ' + err.message; statusEl.style.color = 'var(--error, #f85149)'; }
        }
    },

    _webhookCreateModal() {
        const id = 'webhook-create-modal';
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = id;
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        const events = ['deploy.complete', 'deploy.failed', 'verify_boot.timeout', 'image.ready'];
        overlay.innerHTML = `
            <div class="modal" style="max-width:500px" aria-labelledby="${id}-title">
                <div class="modal-header">
                    <span class="modal-title" id="${id}-title">Add Webhook</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('${id}').remove()">×</button>
                </div>
                <div class="modal-body" style="display:flex;flex-direction:column;gap:14px;">
                    <label class="form-label">Endpoint URL
                        <input id="wh-url" class="form-input" type="url" placeholder="https://example.com/hook" style="margin-top:4px;">
                    </label>
                    <label class="form-label">Secret (optional — used for HMAC signature)
                        <input id="wh-secret" class="form-input" type="password" placeholder="leave blank for unsigned" style="margin-top:4px;">
                    </label>
                    <div>
                        <div class="form-label" style="margin-bottom:6px;">Events</div>
                        ${events.map(e => `<label style="display:flex;align-items:center;gap:6px;margin-bottom:4px;font-size:13px;">
                            <input type="checkbox" class="wh-event-cb" value="${e}"> ${e}
                        </label>`).join('')}
                    </div>
                    <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:8px;">
                        <button class="btn btn-secondary" onclick="document.getElementById('${id}').remove()">Cancel</button>
                        <button class="btn btn-primary" onclick="Pages._webhookCreateSubmit('${id}')">Create</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(overlay);
        overlay.addEventListener('click', e => { if (e.target === overlay) overlay.remove(); });
        trapModalFocus(overlay, () => overlay.remove());
    },

    async _webhookCreateSubmit(modalId) {
        const overlay = document.getElementById(modalId);
        const url = (document.getElementById('wh-url')?.value || '').trim();
        const secret = (document.getElementById('wh-secret')?.value || '').trim();
        const events = [...document.querySelectorAll('.wh-event-cb:checked')].map(cb => cb.value);
        if (!url) { App.toast('URL is required', 'error'); return; }
        if (events.length === 0) { App.toast('Select at least one event', 'error'); return; }
        try {
            const body = { url, events, enabled: true };
            if (secret) body.secret = secret;
            await API.request('POST', '/admin/webhooks', body);
            overlay.remove();
            App.toast('Webhook created', 'success');
            Pages._settingsRender('webhooks');
        } catch (err) {
            App.toast('Failed to create webhook: ' + err.message, 'error');
        }
    },

    _webhookEditModal(whId, url, events, enabled) {
        const id = 'webhook-edit-modal';
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = id;
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        const allEvents = ['deploy.complete', 'deploy.failed', 'verify_boot.timeout', 'image.ready'];
        overlay.innerHTML = `
            <div class="modal" style="max-width:500px" aria-labelledby="${id}-title">
                <div class="modal-header">
                    <span class="modal-title" id="${id}-title">Edit Webhook</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('${id}').remove()">×</button>
                </div>
                <div class="modal-body" style="display:flex;flex-direction:column;gap:14px;">
                    <label class="form-label">Endpoint URL
                        <input id="whe-url" class="form-input" type="url" value="${escHtml(url)}" style="margin-top:4px;">
                    </label>
                    <label class="form-label">New Secret (leave blank to keep existing)
                        <input id="whe-secret" class="form-input" type="password" placeholder="unchanged" style="margin-top:4px;">
                    </label>
                    <div>
                        <div class="form-label" style="margin-bottom:6px;">Events</div>
                        ${allEvents.map(e => `<label style="display:flex;align-items:center;gap:6px;margin-bottom:4px;font-size:13px;">
                            <input type="checkbox" class="whe-event-cb" value="${e}" ${events.includes(e) ? 'checked' : ''}> ${e}
                        </label>`).join('')}
                    </div>
                    <label style="display:flex;align-items:center;gap:8px;font-size:13px;">
                        <input type="checkbox" id="whe-enabled" ${enabled ? 'checked' : ''}> Enabled
                    </label>
                    <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:8px;">
                        <button class="btn btn-secondary" onclick="document.getElementById('${id}').remove()">Cancel</button>
                        <button class="btn btn-primary" onclick="Pages._webhookEditSubmit('${id}', '${escHtml(whId)}')">Save</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(overlay);
        overlay.addEventListener('click', e => { if (e.target === overlay) overlay.remove(); });
        trapModalFocus(overlay, () => overlay.remove());
    },

    async _webhookEditSubmit(modalId, whId) {
        const overlay = document.getElementById(modalId);
        const url = (document.getElementById('whe-url')?.value || '').trim();
        const secret = (document.getElementById('whe-secret')?.value || '').trim();
        const events = [...document.querySelectorAll('.whe-event-cb:checked')].map(cb => cb.value);
        const enabled = document.getElementById('whe-enabled')?.checked ?? true;
        if (!url) { App.toast('URL is required', 'error'); return; }
        if (events.length === 0) { App.toast('Select at least one event', 'error'); return; }
        try {
            const body = { url, events, enabled };
            if (secret) body.secret = secret;
            await API.request('PUT', `/admin/webhooks/${encodeURIComponent(whId)}`, body);
            overlay.remove();
            App.toast('Webhook updated', 'success');
            Pages._settingsRender('webhooks');
        } catch (err) {
            App.toast('Failed to update webhook: ' + err.message, 'error');
        }
    },

    _webhookDelete(whId, url) {
        Pages.showConfirmModal({
            title: 'Delete Webhook',
            message: `Delete webhook for <code>${escHtml(url)}</code>? This cannot be undone.`,
            confirmText: 'Delete',
            danger: true,
            onConfirm: async () => {
                try {
                    await API.request('DELETE', `/admin/webhooks/${encodeURIComponent(whId)}`);
                    App.toast('Webhook deleted', 'success');
                    Pages._settingsRender('webhooks');
                } catch (err) {
                    App.toast('Delete failed: ' + err.message, 'error');
                }
            },
        });
    },

    async _webhookDeliveriesModal(whId, url) {
        const id = 'webhook-deliveries-modal';
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = id;
        overlay.setAttribute('role', 'dialog');
        overlay.setAttribute('aria-modal', 'true');
        overlay.innerHTML = `
            <div class="modal" style="max-width:700px;max-height:85vh;display:flex;flex-direction:column;" aria-labelledby="${id}-title">
                <div class="modal-header">
                    <span class="modal-title" id="${id}-title">Last Deliveries — ${escHtml(url)}</span>
                    <button class="modal-close" aria-label="Close" onclick="document.getElementById('${id}').remove()">×</button>
                </div>
                <div class="modal-body" style="overflow-y:auto;flex:1;">
                    <div id="wh-deliveries-body">${loading('Loading deliveries…')}</div>
                </div>
            </div>`;
        document.body.appendChild(overlay);
        overlay.addEventListener('click', e => { if (e.target === overlay) overlay.remove(); });
        trapModalFocus(overlay, () => overlay.remove());

        try {
            const data = await API.request('GET', `/admin/webhooks/${encodeURIComponent(whId)}/deliveries`);
            const deliveries = (data && data.deliveries) ? data.deliveries : [];
            const body = document.getElementById('wh-deliveries-body');
            if (!body) return;
            if (deliveries.length === 0) {
                body.innerHTML = `<div style="text-align:center;color:var(--text-secondary);padding:24px;">No deliveries recorded yet.</div>`;
                return;
            }
            body.innerHTML = `
                <table class="table" style="font-size:13px;">
                    <thead><tr><th>Status</th><th>Event</th><th>Delivered</th><th>Error</th></tr></thead>
                    <tbody>
                        ${deliveries.map(d => `<tr>
                            <td>${d.status_code >= 200 && d.status_code < 300
                                ? `<span class="badge badge-ready">${d.status_code}</span>`
                                : `<span class="badge badge-error">${d.status_code || '—'}</span>`}</td>
                            <td><span class="badge badge-neutral badge-sm">${escHtml(d.event_type || '')}</span></td>
                            <td style="color:var(--text-secondary);">${fmtDate(d.delivered_at)}</td>
                            <td style="color:var(--error);font-size:12px;">${escHtml(d.error_message || '')}</td>
                        </tr>`).join('')}
                    </tbody>
                </table>`;
        } catch (err) {
            const body = document.getElementById('wh-deliveries-body');
            if (body) body.innerHTML = alertBox('Failed to load deliveries: ' + err.message);
        }
    },

    // ─── B3-4/B3-5: About Tab ────────────────────────────────────────────────

    async _settingsAboutTab() {
        // Fetch system version info if available; non-fatal.
        let versionInfo = null;
        try {
            versionInfo = await API.request('GET', '/system/version').catch(() => null);
        } catch (_) {}
        const version    = (versionInfo && versionInfo.version)    || 'v1.1.0';
        const buildDate  = (versionInfo && versionInfo.build_date) || '—';
        const uptime     = (versionInfo && versionInfo.uptime)     || '—';
        const gitSHA     = (versionInfo && versionInfo.git_sha)    || '—';

        return `
            <div class="card">
                <div class="card-header"><h2 class="card-title">About clustr</h2></div>
                <div class="card-body">
                    <table style="font-size:14px;border-collapse:collapse;width:100%;">
                        <tr><td style="padding:6px 0;color:var(--text-secondary);width:180px;">Server Version</td><td><code>${escHtml(version)}</code></td></tr>
                        <tr><td style="padding:6px 0;color:var(--text-secondary);">Build Date</td><td>${escHtml(buildDate)}</td></tr>
                        <tr><td style="padding:6px 0;color:var(--text-secondary);">Uptime</td><td>${escHtml(uptime)}</td></tr>
                        <tr><td style="padding:6px 0;color:var(--text-secondary);">Git SHA</td><td><code style="font-size:12px;">${escHtml(gitSHA)}</code></td></tr>
                        <tr><td style="padding:6px 0;color:var(--text-secondary);">Changelog</td>
                            <td><a href="https://github.com/sqoia-dev/clustr/blob/main/CHANGELOG.md" target="_blank" rel="noopener" style="color:var(--accent);">CHANGELOG.md</a></td></tr>
                    </table>
                </div>
            </div>
            <div class="card" style="margin-top:16px;">
                <div class="card-header"><h2 class="card-title">Security</h2></div>
                <div class="card-body">
                    <p style="font-size:13px;color:var(--text-secondary);margin-bottom:12px;">Trust signals for this clustr deployment.</p>
                    <table style="font-size:14px;border-collapse:collapse;width:100%;">
                        <tr><td style="padding:6px 0;color:var(--text-secondary);width:260px;">At-rest encryption</td>
                            <td><span class="badge badge-ready">AES-256-GCM</span> <span style="font-size:12px;color:var(--text-secondary);">LDAP &amp; BMC credentials</span></td></tr>
                        <tr><td style="padding:6px 0;color:var(--text-secondary);">Session auth</td>
                            <td><span class="badge badge-ready">HMAC-SHA256</span> <span style="font-size:12px;color:var(--text-secondary);">12h TTL, 30min sliding window</span></td></tr>
                        <tr><td style="padding:6px 0;color:var(--text-secondary);">Bundle signing</td>
                            <td><span class="badge badge-ready">GPG verified</span> <span style="font-size:12px;color:var(--text-secondary);">All Slurm packages signed at build time</span></td></tr>
                    </table>
                </div>
            </div>`;
    },

    // ─── B3-3: Audit Log Page ─────────────────────────────────────────────────

    async auditLog() {
        // Admin-only guard — non-admins are redirected to dashboard.
        if (Auth._role !== 'admin') {
            Router.navigate('/');
            return;
        }
        App.render(loading('Loading audit log…'));
        await Pages._auditLogRender({});
    },

    async _auditLogRender(filters) {
        const params = {};
        if (filters.actor)   params.actor  = filters.actor;
        if (filters.action)  params.action = filters.action;
        if (filters.since)   params.since  = filters.since;
        if (filters.until)   params.until  = filters.until;
        params.limit  = 100;
        params.offset = filters.offset || 0;

        try {
            const data = await API.audit.query(params);
            const records = (data && data.records) ? data.records : [];
            const total   = (data && data.total)   ? data.total   : 0;

            const tableRows = records.length === 0
                ? `<tr><td colspan="5" style="text-align:center;color:var(--text-secondary);padding:24px;">No audit events match the current filters.</td></tr>`
                : records.map(r => `
                    <tr>
                        <td style="color:var(--text-secondary);font-size:12px;white-space:nowrap;" title="${escHtml(r.created_at)}">${fmtRelative(r.created_at)}</td>
                        <td style="font-size:13px;">${escHtml(r.actor_label || r.actor_id || '—')}</td>
                        <td><code style="font-size:12px;background:var(--bg-secondary);padding:2px 6px;border-radius:4px;">${escHtml(r.action || '—')}</code></td>
                        <td style="font-size:12px;color:var(--text-secondary);">
                            ${r.resource_type ? `<span class="badge badge-neutral badge-sm">${escHtml(r.resource_type)}</span>` : ''}
                            ${r.resource_id ? `<code style="font-size:11px;margin-left:4px;">${escHtml(r.resource_id.substring(0, 12))}…</code>` : ''}
                        </td>
                        <td style="font-size:12px;color:var(--text-secondary);">${escHtml(r.ip_addr || '—')}</td>
                    </tr>`).join('');

            const offset  = filters.offset || 0;
            const hasNext = (offset + 100) < total;
            const hasPrev = offset > 0;
            const pager   = (total > 100) ? `
                <div style="display:flex;align-items:center;gap:8px;padding:12px 0;">
                    ${hasPrev ? `<button class="btn btn-secondary btn-sm" onclick="Pages._auditLogRender({...Pages._auditFilters,offset:${offset - 100}})">← Previous</button>` : ''}
                    <span style="font-size:13px;color:var(--text-secondary);">Showing ${offset + 1}–${Math.min(offset + 100, total)} of ${total}</span>
                    ${hasNext ? `<button class="btn btn-secondary btn-sm" onclick="Pages._auditLogRender({...Pages._auditFilters,offset:${offset + 100}})">Next →</button>` : ''}
                </div>` : '';

            Pages._auditFilters = filters;

            App.render(`
                <div class="page-header">
                    <div>
                        <h1 class="page-title">Audit Log</h1>
                        <div class="page-subtitle">${total} events total</div>
                    </div>
                </div>
                <div class="card" style="margin-bottom:16px;">
                    <div class="card-body" style="padding:12px 16px;">
                        <div style="display:flex;flex-wrap:wrap;gap:8px;align-items:flex-end;">
                            <label class="form-label" style="margin:0;font-size:12px;">Actor
                                <input id="af-actor" class="form-input" type="text" placeholder="actor ID or label"
                                    value="${escHtml(filters.actor || '')}"
                                    style="margin-top:4px;width:180px;font-size:13px;">
                            </label>
                            <label class="form-label" style="margin:0;font-size:12px;">Action
                                <input id="af-action" class="form-input" type="text" placeholder="e.g. node.update"
                                    value="${escHtml(filters.action || '')}"
                                    style="margin-top:4px;width:180px;font-size:13px;">
                            </label>
                            <label class="form-label" style="margin:0;font-size:12px;">Since
                                <input id="af-since" class="form-input" type="date"
                                    value="${escHtml(filters.since || '')}"
                                    style="margin-top:4px;font-size:13px;">
                            </label>
                            <label class="form-label" style="margin:0;font-size:12px;">Until
                                <input id="af-until" class="form-input" type="date"
                                    value="${escHtml(filters.until || '')}"
                                    style="margin-top:4px;font-size:13px;">
                            </label>
                            <button class="btn btn-primary btn-sm" onclick="Pages._auditApplyFilters()">Apply</button>
                            <button class="btn btn-secondary btn-sm" onclick="Pages._auditLogRender({})">Clear</button>
                        </div>
                    </div>
                </div>
                ${cardWrap('Events',
                    `<div style="overflow-x:auto;">
                        <table class="table" style="font-size:13px;">
                            <thead><tr>
                                <th style="width:140px">When</th>
                                <th>Actor</th>
                                <th>Action</th>
                                <th>Resource</th>
                                <th>IP</th>
                            </tr></thead>
                            <tbody>${tableRows}</tbody>
                        </table>
                        ${pager}
                    </div>`)}
            `);
        } catch (err) {
            App.render(alertBox('Failed to load audit log: ' + err.message));
        }
    },

    _auditFilters: {},

    _auditApplyFilters() {
        const actor  = (document.getElementById('af-actor')?.value  || '').trim();
        const action = (document.getElementById('af-action')?.value || '').trim();
        const since  = (document.getElementById('af-since')?.value  || '').trim();
        const until  = (document.getElementById('af-until')?.value  || '').trim();
        // Convert date strings to RFC3339 midnight UTC for the API.
        const toRFC = (d) => d ? new Date(d + 'T00:00:00Z').toISOString() : '';
        Pages._auditLogRender({ actor, action, since: toRFC(since), until: toRFC(until), offset: 0 });
    },

    // ─── DHCP Allocations (Alpine.js pilot — Sprint B.5) ──────────────────
    //
    // This is the first production surface using Alpine.js (D23). The vanilla
    // string-building pattern is replaced with a declarative x-data component.
    //
    // Why Alpine here (not HTMX):
    //   - The data is already JSON from API.dhcp.leases(); no server-rendered
    //     HTML partial is needed. Alpine's x-for is ideal for JSON → table rows.
    //   - HTMX shines when the server returns partial HTML. That pattern is used
    //     on the audit log and anomaly card (Sprint C). Here we stay in Alpine.
    //
    // Alpine conventions used:
    //   x-data   — component state + methods scoped to the root div
    //   x-show   — conditional visibility (loading / error / empty / filled)
    //   x-for    — iterate leases array into <tr> rows (keyed by lease.mac)
    //   x-text   — safe interpolation (auto-escapes; no manual escHtml needed)
    //   :href    — safe attribute binding
    //   :title   — safe attribute binding
    //   x-on:click (shorthand @click) — event binding on the Refresh button
    //
    // The App.setAutoRefresh wrapper is retained so the router's navigation
    // cleanup (clearInterval on hash change) keeps working unchanged.

    dhcpLeases() {
        // Render the Alpine root — a single div with x-data. Alpine picks it up
        // on the next microtask tick because it watches for DOM mutations.
        // The `init()` method is called automatically by Alpine after mounting.
        App.render(`
            <div x-data="dhcpLeasesComponent()" x-init="init()">

                <!-- Loading state -->
                <div x-show="loading" class="loading">
                    <div class="spinner"></div>Loading DHCP allocations&hellip;
                </div>

                <!-- Error state -->
                <div x-show="!loading && error" class="alert alert-error" role="alert" x-text="error"></div>

                <!-- Loaded state -->
                <div x-show="!loading && !error">

                    <!-- Page header -->
                    <div class="page-header" style="display:flex;align-items:center;justify-content:space-between;margin-bottom:20px;">
                        <div>
                            <h1 class="page-title">DHCP Allocations</h1>
                            <p class="page-subtitle" style="color:var(--text-secondary);margin:4px 0 0;">
                                MAC&rarr;IP mappings from the management network &mdash;
                                <span x-text="count + ' node' + (count === 1 ? '' : 's')"></span>
                            </p>
                        </div>
                        <button class="btn btn-secondary btn-sm" @click="refresh()" title="Refresh">
                            <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24" width="14" height="14" style="margin-right:4px;">
                                <polyline points="23 4 23 10 17 10"/><polyline points="1 20 1 14 7 14"/>
                                <path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"/>
                            </svg>
                            Refresh
                        </button>
                    </div>

                    <!-- Empty state -->
                    <div x-show="leases.length === 0" class="card">
                        <div class="empty-state" style="padding:40px">
                            <div class="empty-state-icon">
                                <svg xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24" width="40" height="40">
                                    <path d="M5 12.55a11 11 0 0 1 14.08 0"/><path d="M1.42 9a16 16 0 0 1 21.16 0"/>
                                    <path d="M8.53 16.11a6 6 0 0 1 6.95 0"/><line x1="12" y1="20" x2="12.01" y2="20"/>
                                </svg>
                            </div>
                            <div class="empty-state-title">No DHCP allocations yet</div>
                            <div class="empty-state-text">Nodes appear here once they PXE-boot and register with the server. The management network layout will be visible once nodes are online.</div>
                        </div>
                    </div>

                    <!-- Lease table -->
                    <div x-show="leases.length > 0" class="card">
                        <div class="table-wrapper" style="overflow-x:auto;">
                            <table class="table" style="width:100%;">
                                <thead>
                                    <tr>
                                        <th>Hostname</th>
                                        <th>MAC</th>
                                        <th>IP</th>
                                        <th>Role</th>
                                        <th>State</th>
                                        <th>Last Seen</th>
                                    </tr>
                                </thead>
                                <tbody>
                                    <!-- x-for iterates the leases array reactively.
                                         :key is Alpine's hint for efficient DOM diffing.
                                         x-text auto-escapes — no manual escHtml needed. -->
                                    <template x-for="lease in leases" :key="lease.mac">
                                        <tr>
                                            <!-- Hostname: deep-link when node_id is present -->
                                            <td>
                                                <a x-show="lease.node_id"
                                                   :href="'#/nodes/' + lease.node_id"
                                                   style="color:var(--accent);text-decoration:none;"
                                                   x-text="lease.hostname"></a>
                                                <span x-show="!lease.node_id" x-text="lease.hostname"></span>
                                            </td>
                                            <!-- MAC: monospace code -->
                                            <td><code style="font-family:var(--font-mono);font-size:12px;" x-text="lease.mac"></code></td>
                                            <!-- IP: monospace code or dash -->
                                            <td>
                                                <code x-show="lease.ip" style="font-family:var(--font-mono);font-size:12px;" x-text="lease.ip"></code>
                                                <span x-show="!lease.ip" style="color:var(--text-secondary)">&#8212;</span>
                                            </td>
                                            <!-- Role badge or dash -->
                                            <td>
                                                <span x-show="lease.role" class="badge badge-neutral badge-sm" x-text="lease.role"></span>
                                                <span x-show="!lease.role" style="color:var(--text-secondary)">&#8212;</span>
                                            </td>
                                            <!-- Deploy state badge — class bound to stateBadgeClass() -->
                                            <td>
                                                <span :class="stateBadgeClass(lease.deploy_state)"
                                                      x-text="stateBadgeLabel(lease.deploy_state)"></span>
                                            </td>
                                            <!-- Last seen: relative time with full ISO as tooltip -->
                                            <td>
                                                <span x-show="lease.last_seen_at"
                                                      :title="lease.last_seen_at"
                                                      x-text="fmtRelative(lease.last_seen_at)"></span>
                                                <span x-show="!lease.last_seen_at" style="color:var(--text-secondary)">&#8212;</span>
                                            </td>
                                        </tr>
                                    </template>
                                </tbody>
                            </table>
                        </div>
                    </div>

                </div><!-- /loaded state -->
            </div><!-- /x-data -->
        `);

        // Auto-refresh every 30s. The router clears this timer on navigation so
        // the Alpine component is not called after the page is unmounted.
        App.setAutoRefresh(() => Pages.dhcpLeases(), 30000);
    },
};

// ─── ProgressStream ───────────────────────────────────────────────────────
//
// Subscribes to /api/v1/deploy/progress/stream (SSE) and updates the shared
// deployMap (MAC → DeployProgress) on each event. Calls onUpdate() after each
// update so the caller can re-render only the affected part of the DOM.
//
// Completed or failed entries are removed from the map after 60 seconds so they
// don't accumulate in the table indefinitely.

class ProgressStream {
    constructor(deployMap, onUpdate) {
        this._map       = deployMap;    // Map<mac, DeployProgress>
        this._onUpdate  = onUpdate;     // () => void
        this._es        = null;
        this._timers    = new Map();    // mac → setTimeout handle
        this._stopped   = false;
    }

    connect() {
        if (this._stopped) return;
        const url = API.progress.sseUrl();
        this._es = new EventSource(url);

        this._es.onmessage = (e) => {
            let prog;
            try { prog = JSON.parse(e.data); } catch { return; }
            if (!prog || !prog.node_mac) return;

            const mac = prog.node_mac;
            this._map.set(mac, prog);

            // Cancel any pending removal for this node (phase may have changed).
            if (this._timers.has(mac)) {
                clearTimeout(this._timers.get(mac));
                this._timers.delete(mac);
            }

            // Schedule removal 60 seconds after the final state.
            if (prog.phase === 'complete' || prog.phase === 'error') {
                const t = setTimeout(() => {
                    this._map.delete(mac);
                    this._timers.delete(mac);
                    if (this._onUpdate) this._onUpdate();
                }, 60000);
                this._timers.set(mac, t);
            }

            if (this._onUpdate) this._onUpdate();
        };

        this._es.onerror = () => {
            if (this._stopped) return;
            // EventSource will automatically attempt to reconnect — no action needed.
        };
    }

    disconnect() {
        this._stopped = true;
        if (this._es) { this._es.close(); this._es = null; }
        this._timers.forEach(t => clearTimeout(t));
        this._timers.clear();
    }
}

// ─── Auth ─────────────────────────────────────────────────────────────────
//
// Auth manages the browser session (ADR-0006).
// The session is carried by an HttpOnly cookie set by POST /api/v1/auth/login.
// On 401/403 api.js redirects to /login — no modal needed.
// Auth.logout() calls POST /api/v1/auth/logout then redirects to /login.

const Auth = {
    // A-10 fix: default to 'readonly' (lowest privilege) until /auth/me confirms the real role.
    _role: 'readonly',
    // B1-4: assigned_groups for operator scope — empty until /auth/me populates it.
    _groups: [],

    async logout() {
        try {
            await fetch('/api/v1/auth/logout', {
                method: 'POST',
                credentials: 'same-origin',
            });
        } catch (_) {
            // Best-effort; redirect regardless.
        }
        try { localStorage.removeItem('clustr_admin_key'); } catch (_) {}
        window.location.href = '/login';
    },

    // extendSession re-validates the session (GET /auth/me with valid cookie slides it).
    async extendSession() {
        try {
            await fetch('/api/v1/auth/me', { credentials: 'same-origin' });
            const banner = document.getElementById('session-expiry-banner');
            if (banner) banner.style.display = 'none';
            App.toast('Session extended', 'success');
        } catch (_) {
            window.location.href = '/login';
        }
    },

    // boot verifies the session via GET /api/v1/auth/me.
    // Valid session → start the app (unless force-password-change is set).
    // No session / expired → redirect to /login.
    //
    // A-10 fix: Auth._role defaults to 'readonly' (lowest privilege) until a
    // successful /auth/me response promotes it to the real role.  On transient
    // network failure we retry up to 3 times with exponential backoff before
    // giving up and showing an error banner.  We never fall back to 'admin' —
    // a missing auth response must never grant elevated UI affordances.
    async boot() {
        // If the server flagged a forced password change, redirect immediately.
        // B2-8: preserve ?next= param so the user lands on the right page after password change.
        if (document.cookie.split(';').some(c => c.trim().startsWith('clustr_force_password_change='))) {
            const existingNext = new URLSearchParams(window.location.search).get('next') || '';
            const hash = window.location.hash || '';
            const nextParam = existingNext || (hash ? encodeURIComponent(hash) : '');
            window.location.href = '/set-password' + (nextParam ? '?next=' + nextParam : '');
            return;
        }

        // Attempt /auth/me up to 3 times with exponential backoff (500ms, 1s, 2s).
        // A 401/403 is definitive (not retried) — redirect to login immediately.
        // A network error or unexpected non-2xx is retried.
        let lastErr = null;
        for (let attempt = 0; attempt < 3; attempt++) {
            if (attempt > 0) {
                await new Promise(r => setTimeout(r, 500 * Math.pow(2, attempt - 1)));
            }
            try {
                const resp = await fetch('/api/v1/auth/me', { credentials: 'same-origin' });
                if (resp.status === 401 || resp.status === 403) {
                    window.location.href = '/login';
                    return;
                }
                if (!resp.ok) {
                    // Non-auth server error — retry.
                    lastErr = new Error(`/auth/me returned HTTP ${resp.status}`);
                    continue;
                }
                // Success: promote role from lowest-privilege default to actual role.
                // If the role field is absent (key-based session), keep 'readonly' —
                // never escalate to 'admin' on a missing or unexpected response shape.
                const me = await resp.json().catch(() => ({}));
                Auth._role = me.role || 'readonly';
                // B1-4: store assigned_groups for operator scope filtering.
                Auth._groups = Array.isArray(me.assigned_groups) ? me.assigned_groups : [];

                // Populate sidebar footer with user info.
                const userAvatar = document.getElementById('user-avatar');
                const userName = document.getElementById('user-name');
                const userRole = document.getElementById('user-role');
                if (me.username) {
                    if (userAvatar) userAvatar.textContent = me.username.substring(0, 2).toUpperCase();
                    if (userName) userName.textContent = me.username;
                }
                if (userRole) userRole.textContent = Auth._role;
                lastErr = null;
                break;
            } catch (err) {
                // Network-level failure (offline, DNS, etc.) — retry.
                lastErr = err;
            }
        }

        if (lastErr) {
            // All 3 attempts failed.  Auth._role stays at 'readonly' (the default).
            // Show a persistent error banner so the operator knows the session state
            // could not be confirmed.  Do NOT fall back to 'admin'.
            const banner = document.getElementById('session-expiry-banner');
            if (banner) {
                banner.style.background = 'var(--error, #ef4444)';
                banner.style.color = '#fff';
                banner.style.cursor = 'default';
                banner.textContent = "Couldn’t load your account — refresh to retry. Admin controls are hidden until your session is confirmed.";
                banner.style.display = 'block';
                // Remove the onclick so clicking the banner doesn't call extendSession.
                banner.onclick = null;
            }
            // Still allow the app to start in read-only mode; api.js will redirect
            // on the first 401 from any actual data fetch.
        }
        // Bootstrap LDAP nav visibility. Non-fatal — missing nav section is safe.
        if (typeof LDAPPages !== 'undefined') {
            LDAPPages.bootstrapNav().catch(() => {});
        }
        // Bootstrap System Accounts nav visibility.
        if (typeof SysAccountsPages !== 'undefined') {
            SysAccountsPages.bootstrapNav().catch(() => {});
        }
        // Bootstrap Network nav visibility.
        if (typeof NetworkPages !== 'undefined') {
            NetworkPages.bootstrapNav().catch(() => {});
        }
        // Bootstrap Slurm nav visibility.
        if (typeof SlurmPages !== 'undefined') {
            SlurmPages.bootstrapNav().catch(() => {});
        }
        // B1: Apply role-aware nav restrictions after role is known.
        Auth._applyRoleNav();
        App.init();
    },

    // _applyRoleNav hides nav sections that are not appropriate for the current role.
    // B1-1: operator — hides Slurm, LDAP, System Accounts, Network sections + restricts Settings.
    // B1-2: readonly — same hides as operator; mutation buttons are disabled per-page via Auth._role checks.
    // Admin sees everything (no change from current behaviour).
    _applyRoleNav() {
        const role = Auth._role;
        if (role === 'admin') return; // admins see full nav — nothing to hide

        // operator and readonly both lose: Slurm section, LDAP section,
        // System Accounts section, Network Switches/Profiles section.
        const hide = (id) => {
            const el = document.getElementById(id);
            if (el) el.style.display = 'none';
        };

        hide('nav-slurm-section');
        hide('nav-ldap-section');
        hide('nav-system-section');
        hide('nav-network-section');

        // Also hide the Audit log link if present (admin-only page, added by B3).
        hide('nav-audit-link');
    },

    _role: 'readonly', // cached role from /auth/me — defaults to lowest privilege until /auth/me succeeds
};

// ─── Boot ─────────────────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => Auth.boot());
