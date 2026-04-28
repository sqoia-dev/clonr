// portal-director.js — Alpine component for the Director portal (/portal/director/)
// Extracted from portal_director.html inline <script> block in Sprint F (v1.5.0)
// to satisfy script-src 'self' CSP.  No logic changes.

function directorPortal() {
    return {
        loading: true,
        error: null,
        username: '',
        summary: {},
        groups: [],
        tab: 'groups',
        reviewCycles: [],
        reviewCyclesLoaded: false,
        showDetail: false,
        detailLoading: false,
        detail: null,
        // E2: FOS utilization
        fosLoading: false,
        fosLoaded: false,
        fosError: null,
        fosSummary: [],
        fosTotal: 0,
        fosUnclassified: 0,

        async init() {
            // Verify role.
            try {
                const resp = await fetch('/api/v1/auth/me', { credentials: 'same-origin' });
                if (!resp.ok) { window.location.href = '/login'; return; }
                const me = await resp.json();
                if (me.role !== 'director' && me.role !== 'admin') {
                    if (me.role === 'pi') { window.location.href = '/portal/pi/'; return; }
                    if (me.role === 'viewer') { window.location.href = '/portal/'; return; }
                    window.location.href = '/login';
                    return;
                }
                this.username = me.username || '';
            } catch (e) {
                this.error = 'Failed to verify session.';
                this.loading = false;
                return;
            }

            await Promise.all([this.loadSummary(), this.loadGroups()]);
            this.loading = false;
        },

        async loadSummary() {
            try {
                const r = await fetch('/api/v1/portal/director/summary', { credentials: 'same-origin' });
                if (r.ok) this.summary = await r.json();
            } catch (_) {}
        },

        async loadGroups() {
            try {
                const r = await fetch('/api/v1/portal/director/groups', { credentials: 'same-origin' });
                if (r.ok) {
                    const d = await r.json();
                    this.groups = d.groups || [];
                }
            } catch (_) {}
        },

        async loadReviewCycles() {
            if (this.reviewCyclesLoaded) return;
            try {
                const r = await fetch('/api/v1/portal/director/review-cycles', { credentials: 'same-origin' });
                if (r.ok) {
                    const d = await r.json();
                    this.reviewCycles = d.cycles || [];
                }
            } catch (_) {}
            this.reviewCyclesLoaded = true;
        },

        async loadFOSUtilization() {
            if (this.fosLoaded) return;
            this.fosLoading = true;
            this.fosError = null;
            try {
                const r = await fetch('/api/v1/portal/director/fos-utilization', { credentials: 'same-origin' });
                if (!r.ok) throw new Error('HTTP ' + r.status);
                const d = await r.json();
                this.fosSummary = d.summary || [];
                this.fosTotal = this.fosSummary.reduce((acc, e) => acc + (e.group_count || 0), 0);
                this.fosUnclassified = d.unclassified || 0;
            } catch (e) {
                this.fosError = 'Failed to load FOS utilization: ' + e.message;
            }
            this.fosLoading = false;
            this.fosLoaded = true;
        },

        async showGroupDetail(g) {
            this.showDetail = true;
            this.detailLoading = true;
            this.detail = g;
            try {
                const r = await fetch('/api/v1/portal/director/groups/' + g.id, { credentials: 'same-origin' });
                if (r.ok) this.detail = await r.json();
            } catch (_) {}
            this.detailLoading = false;
        },

        async logout() {
            await fetch('/api/v1/auth/logout', { method: 'POST', credentials: 'same-origin' });
            window.location.href = '/login';
        },
    };
}
