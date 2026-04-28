// portal.js — Alpine component for the Researcher portal (/portal/)
// Extracted from portal.html inline <script> block in Sprint F (v1.5.0) to
// satisfy script-src 'self' CSP.  No logic changes.

// ─── Portal Alpine component ───────────────────────────────────────────────
function portalApp() {
    return {
        // Top-level state
        loading: true,
        error:   null,
        ldapEnabled: false,
        ondemandURL: '',

        // LDAP user info
        user: null,

        // Storage quota
        quota: null,

        // Password change form
        pw: {
            current: '',
            new1:    '',
            new2:    '',
            saving:  false,
            success: false,
            error:   null,
        },

        async init() {
            // Verify the session is valid and we're viewer role.
            try {
                const me = await apiFetch('/api/v1/auth/me');
                if (!me.ok) {
                    window.location.href = '/login';
                    return;
                }
                const meData = await me.json();
                // Admin/operator users see /admin/ not /portal/.
                if (meData.role === 'admin' || meData.role === 'operator') {
                    window.location.href = '/';
                    return;
                }
            } catch (_) {
                window.location.href = '/login';
                return;
            }

            // Load portal status.
            await this.loadStatus();
            await this.loadQuota();
            this.loading = false;
        },

        async loadStatus() {
            try {
                const r = await apiFetch('/api/v1/portal/status');
                if (!r.ok) {
                    const e = await r.json().catch(() => ({ error: 'Failed to load portal status' }));
                    this.error = e.error || 'Failed to load portal status';
                    return;
                }
                const data = await r.json();
                this.ondemandURL  = data.ondemand_url || '';
                this.ldapEnabled  = data.ldap_enabled || false;
                this.user         = data.user || null;
            } catch (err) {
                this.error = 'Network error: ' + err.message;
            }
        },

        async loadQuota() {
            try {
                const r = await apiFetch('/api/v1/portal/me/quota');
                if (r.ok) {
                    const data = await r.json();
                    this.quota = data;
                }
            } catch (_) {
                // Non-fatal — quota card stays hidden.
            }
        },

        async changePassword() {
            this.pw.success = false;
            this.pw.error   = null;

            if (!this.pw.current) {
                this.pw.error = 'Please enter your current password.';
                return;
            }
            if (!this.pw.new1) {
                this.pw.error = 'Please enter a new password.';
                return;
            }
            if (this.pw.new1.length < 8) {
                this.pw.error = 'New password must be at least 8 characters.';
                return;
            }
            if (this.pw.new1 !== this.pw.new2) {
                this.pw.error = 'New passwords do not match.';
                return;
            }

            this.pw.saving = true;
            try {
                const r = await apiFetch('/api/v1/portal/me/password', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        current_password: this.pw.current,
                        new_password:     this.pw.new1,
                    }),
                });
                if (r.ok) {
                    this.pw.success = true;
                    this.pw.current = '';
                    this.pw.new1    = '';
                    this.pw.new2    = '';
                } else {
                    const e = await r.json().catch(() => ({ error: 'Password change failed' }));
                    this.pw.error = e.error || 'Password change failed';
                }
            } catch (err) {
                this.pw.error = 'Network error: ' + err.message;
            } finally {
                this.pw.saving = false;
            }
        },

        async logout() {
            try {
                await apiFetch('/api/v1/auth/logout', { method: 'POST' });
            } catch (_) {}
            window.location.href = '/login';
        },

        // ── Quota helpers ──
        quotaPct() {
            if (!this.quota || this.quota.limit_bytes == null || this.quota.limit_bytes === 0) return 0;
            return Math.min(100, (this.quota.used_bytes / this.quota.limit_bytes) * 100);
        },
        quotaBarClass() {
            const pct = this.quotaPct();
            if (pct >= 90) return 'quota-bar danger';
            if (pct >= 75) return 'quota-bar warn';
            return 'quota-bar';
        },

        // ── Byte formatting helper ──
        fmtBytes(n) {
            if (n == null) return '—';
            const abs = Math.abs(n);
            if (abs < 1024) return n + ' B';
            const units = ['KiB','MiB','GiB','TiB','PiB'];
            let u = 0, v = abs;
            while (v >= 1024 && u < units.length - 1) { v /= 1024; u++; }
            return (n < 0 ? '-' : '') + v.toFixed(2) + ' ' + units[u];
        },
    };
}

// ─── Minimal fetch wrapper ─────────────────────────────────────────────────
function apiFetch(url, opts) {
    return fetch(url, Object.assign({ credentials: 'same-origin' }, opts || {}));
}
