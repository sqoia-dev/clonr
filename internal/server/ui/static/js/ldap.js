// ldap.js — LDAP module webui: settings, users, and groups.
// Conventions mirror app.js: hash-based routing, App.render(), App.toast(),
// cardWrap(), emptyState(), badge(), escHtml(), fmtDate(), fmtRelative().
// API calls go through API.ldap.* (defined in api.js).

// ─── LDAPPages namespace ───────────────────────────────────────────────────

const LDAPPages = {

    // ── Nav bootstrap ────────────────────────────────────────────────────────

    // bootstrapNav fetches /api/v1/ldap/status and updates LDAP nav visibility.
    // Called once on app boot and re-called after enable/disable.
    //
    // Layout contract (index.html):
    //   #nav-ldap-section  — outer wrapper, always visible for admins
    //   #nav-ldap-status-badge — "Disabled" pill, shown when !enabled
    //   #nav-ldap-managed  — inner wrapper for Users + Groups, shown when enabled
    //
    // On 403 / fetch error (non-admin or server down) the entire section is
    // hidden to avoid a broken-looking entry for non-admin users.
    async bootstrapNav() {
        const section = document.getElementById('nav-ldap-section');
        if (!section) return;
        try {
            const st = await API.ldap.status();
            const enabled = !!(st && st.enabled);

            section.style.display = '';

            const badge   = document.getElementById('nav-ldap-status-badge');
            const managed = document.getElementById('nav-ldap-managed');
            if (badge)   badge.style.display   = enabled ? 'none' : '';
            if (managed) managed.style.display = enabled ? ''     : 'none';
        } catch (_) {
            // Non-admin (403) or unreachable — hide entire section.
            section.style.display = 'none';
        }
    },

    // ── Settings page ─────────────────────────────────────────────────────

    async settings() {
        App.render(loading('Loading LDAP settings…'));
        try {
            const st = await API.ldap.status();
            App.render(LDAPPages._settingsHtml(st));
        } catch (err) {
            App.render(alertBox('Failed to load LDAP status: ' + err.message));
        }
    },

    _settingsHtml(st) {
        const isAdmin = typeof Auth !== 'undefined' && Auth._role === 'admin';

        // Status badge.
        const statusColors = {
            ready:        'badge-ready',
            provisioning: 'badge-building',
            error:        'badge-error',
            disabled:     'badge-neutral',
        };
        const statusBadge = `<span class="badge ${statusColors[st.status] || 'badge-neutral'}">${escHtml(st.status)}</span>`;

        // Cert expiry warning banner.
        const expiryBanner = st.cert_expiry_warning
            ? `<div class="alert alert-warning" style="background:#fef3c7;color:#92400e;border:1px solid #fbbf24;border-radius:6px;padding:10px 14px;margin-bottom:16px;">
                   Certificate expires soon (${st.server_cert_expires_at ? new Date(st.server_cert_expires_at).toLocaleDateString() : 'unknown'}).
                   Re-enable the module to regenerate certificates.
               </div>`
            : '';

        // Status detail row.
        const detailRow = st.status_detail
            ? `<tr><td style="color:var(--text-secondary);padding:6px 16px 6px 0;white-space:nowrap;font-size:13px;">Detail</td><td style="padding:6px 0;font-size:13px;">${escHtml(st.status_detail)}</td></tr>`
            : '';

        // Info table.
        const infoTable = `
            <table style="border-collapse:collapse;width:100%;max-width:540px;">
                <tbody>
                    <tr>
                        <td style="color:var(--text-secondary);padding:6px 16px 6px 0;white-space:nowrap;font-size:13px;">Status</td>
                        <td style="padding:6px 0;">${statusBadge}</td>
                    </tr>
                    ${detailRow}
                    <tr>
                        <td style="color:var(--text-secondary);padding:6px 16px 6px 0;white-space:nowrap;font-size:13px;">Base DN</td>
                        <td style="padding:6px 0;font-family:monospace;font-size:13px;">${st.base_dn ? escHtml(st.base_dn) : '<span style="color:var(--text-secondary)">—</span>'}</td>
                    </tr>
                    <tr>
                        <td style="color:var(--text-secondary);padding:6px 16px 6px 0;white-space:nowrap;font-size:13px;">Base DN Locked</td>
                        <td style="padding:6px 0;font-size:13px;">${st.base_dn_locked ? '&#128274; Locked (first node provisioned)' : 'Unlocked'}</td>
                    </tr>
                    <tr>
                        <td style="color:var(--text-secondary);padding:6px 16px 6px 0;white-space:nowrap;font-size:13px;">CA Fingerprint</td>
                        <td style="padding:6px 0;font-family:monospace;font-size:12px;word-break:break-all;">${st.ca_fingerprint ? escHtml(st.ca_fingerprint) : '<span style="color:var(--text-secondary)">—</span>'}</td>
                    </tr>
                    <tr>
                        <td style="color:var(--text-secondary);padding:6px 16px 6px 0;white-space:nowrap;font-size:13px;">Cert Expires</td>
                        <td style="padding:6px 0;font-size:13px;">${st.server_cert_expires_at ? fmtDate(st.server_cert_expires_at) : '<span style="color:var(--text-secondary)">—</span>'}</td>
                    </tr>
                    <tr>
                        <td style="color:var(--text-secondary);padding:6px 16px 6px 0;white-space:nowrap;font-size:13px;">Configured Nodes</td>
                        <td style="padding:6px 0;font-size:13px;">${st.configured_node_count || 0}</td>
                    </tr>
                </tbody>
            </table>`;

        // Enable / Re-provision form.
        // Shown when disabled OR when ready (re-provision) OR when error (retry).
        // Hidden only during active provisioning.
        const showEnableForm = !st.enabled || st.status === 'ready' || st.status === 'error';
        const enableBtnLabel = st.status === 'ready' ? 'Re-provision'
                             : st.status === 'error'   ? 'Retry'
                             : 'Enable';
        const enableOnclick  = st.status === 'ready'
            ? 'LDAPPages._reprovisionConfirmModal()'
            : 'LDAPPages._enable()';
        const enableForm = showEnableForm ? `
            <div class="card" style="margin-top:20px;">
                <div class="card-header"><span class="card-title">${st.status === 'ready' ? 'Re-provision LDAP Module' : st.status === 'error' ? 'Retry Provisioning' : 'Enable LDAP Module'}</span></div>
                <div style="padding:16px;display:flex;flex-direction:column;gap:12px;max-width:480px;">
                    <p style="margin:0;color:var(--text-secondary);font-size:14px;">
                        ${st.status === 'ready'
                            ? 'Re-provisioning regenerates the server certificate (existing CA is preserved), re-seeds slapd configuration, and restarts the LDAP service.'
                            : 'Enabling the LDAP module will start a self-hosted OpenLDAP instance on this server and configure new nodes to authenticate via it on reimage.'}
                    </p>
                    <label class="form-label">Base DN
                        <input id="ldap-enable-basedn" class="form-input" type="text"
                            value="${st.base_dn ? escHtml(st.base_dn) : 'dc=cluster,dc=local'}"
                            placeholder="dc=cluster,dc=local"
                            style="margin-top:4px;font-family:monospace;"
                            ${st.base_dn_locked ? 'disabled title="Locked after first node was provisioned"' : ''}>
                        <span style="font-size:12px;color:var(--text-secondary);margin-top:4px;display:block;">
                            Directory base DN. Cannot be changed after the first node is provisioned.
                        </span>
                    </label>
                    <label class="form-label">Directory Manager Password
                        <input id="ldap-enable-password" class="form-input" type="password"
                            placeholder="Strong password required" style="margin-top:4px;">
                        <span style="font-size:12px;color:var(--text-secondary);margin-top:4px;display:block;">
                            This password is never stored in plaintext. Keep it safe — it cannot be recovered.
                        </span>
                    </label>
                    <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:8px;">
                        <button class="btn btn-primary" onclick="${enableOnclick}" ${!isAdmin ? 'disabled' : ''}>${enableBtnLabel}</button>
                    </div>
                </div>
            </div>` : '';

        // Action buttons (shown when enabled).
        const actionButtons = st.enabled ? `
            <div style="display:flex;gap:8px;margin-top:4px;">
                <button class="btn btn-secondary" onclick="LDAPPages._backup()">Backup LDIF</button>
                <button class="btn btn-danger" onclick="LDAPPages._disableModal(${st.configured_node_count || 0})" ${!isAdmin ? 'disabled' : ''}>Disable</button>
            </div>` : '';

        // Repair form (shown when enabled so the operator can backfill admin_passwd
        // on installs provisioned before migration 028, or recover from node-reader
        // bind divergence without a full Disable/Re-enable cycle).
        const repairForm = st.enabled ? `
            <div class="card" style="margin-top:20px;">
                <div class="card-header"><span class="card-title">Admin password</span></div>
                <div style="padding:16px;display:flex;flex-direction:column;gap:12px;max-width:480px;">
                    <p style="margin:0;color:var(--text-secondary);font-size:14px;">
                        Re-enter the admin password you set on Enable. Persists it across restarts
                        and repairs the node-reader bind if it has drifted.
                    </p>
                    <div class="form-group" style="margin-bottom:0;">
                        <label class="form-label">Admin password
                            <div style="display:flex;gap:6px;align-items:center;margin-top:4px;">
                                <input id="ldap-repair-password" class="form-input" type="password"
                                    placeholder="Directory Manager password" style="flex:1;">
                                <button type="button" id="ldap-repair-pw-toggle" class="btn btn-secondary btn-sm"
                                    onclick="LDAPPages._repairTogglePassword()">Show</button>
                            </div>
                            <div id="ldap-repair-err" style="color:var(--error);font-size:12px;margin-top:6px;display:none;"></div>
                        </label>
                    </div>
                    <div style="display:flex;gap:8px;justify-content:flex-end;">
                        <button class="btn btn-primary" id="ldap-repair-submit"
                            onclick="LDAPPages._repairAdminBind()" ${!isAdmin ? 'disabled' : ''}>Verify &amp; Repair</button>
                    </div>
                </div>
            </div>` : '';

        return `
            <div class="page-header">
                <div>
                    <div class="page-title">LDAP</div>
                    <div class="page-subtitle">Self-hosted OpenLDAP module — opt-in directory for node authentication</div>
                </div>
            </div>
            ${expiryBanner}
            <div class="card">
                <div class="card-header">
                    <span class="card-title">Module Status</span>
                    ${actionButtons}
                </div>
                <div style="padding:16px 20px;">
                    ${infoTable}
                </div>
            </div>
            ${enableForm}
            ${repairForm}
        `;
    },

    // _reprovisionConfirmModal shows a confirmation before re-provisioning a ready module.
    _reprovisionConfirmModal() {
        const existingModal = document.getElementById('ldap-reprovision-modal');
        if (existingModal) existingModal.remove();

        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = 'ldap-reprovision-modal';
        overlay.innerHTML = `
            <div class="modal">
                <div class="modal-header">
                    <span class="modal-title">Re-provision LDAP module?</span>
                    <button class="modal-close" onclick="document.getElementById('ldap-reprovision-modal').remove()">×</button>
                </div>
                <div class="modal-body">
                    <p style="margin:0 0 16px;font-size:14px;line-height:1.6;">
                        The module is currently active. Re-provisioning will regenerate the server
                        certificate (existing CA is preserved), re-seed slapd configuration, and
                        restart the LDAP service. In-flight LDAP operations may briefly fail during
                        the restart.
                    </p>
                    <div class="form-actions">
                        <button class="btn btn-secondary" onclick="document.getElementById('ldap-reprovision-modal').remove()">Cancel</button>
                        <button class="btn btn-primary" onclick="document.getElementById('ldap-reprovision-modal').remove();LDAPPages._enable()">Re-provision</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(overlay);
    },

    async _enable() {
        const baseDN   = (document.getElementById('ldap-enable-basedn')   || {}).value || '';
        const password = (document.getElementById('ldap-enable-password') || {}).value || '';
        if (!baseDN.trim()) { App.toast('Base DN is required', 'error'); return; }
        if (!password)       { App.toast('Directory Manager password is required', 'error'); return; }
        try {
            await API.ldap.enable({ base_dn: baseDN.trim(), admin_password: password });
            App.toast('LDAP provisioning started — polling status…', 'info');
            await LDAPPages.bootstrapNav();
            LDAPPages._pollUntilReady();
        } catch (err) {
            App.toast('Enable failed: ' + err.message, 'error');
        }
    },

    // Poll /status every 3 seconds until status is ready or error.
    async _pollUntilReady() {
        for (let i = 0; i < 20; i++) {
            await new Promise(r => setTimeout(r, 3000));
            try {
                const st = await API.ldap.status();
                App.render(LDAPPages._settingsHtml(st));
                if (st.status === 'ready') {
                    App.toast('LDAP module is ready', 'success');
                    await LDAPPages.bootstrapNav();
                    return;
                }
                if (st.status === 'error') {
                    App.toast('LDAP provisioning failed: ' + (st.status_detail || 'unknown error'), 'error');
                    return;
                }
            } catch (_) {}
        }
        App.toast('LDAP status polling timed out — check server logs', 'error');
    },

    _disableModal(nodeCount) {
        const existingModal = document.getElementById('ldap-disable-modal');
        if (existingModal) existingModal.remove();

        const needsAck = nodeCount > 0;
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = 'ldap-disable-modal';
        overlay.innerHTML = `
            <div class="modal">
                <div class="modal-header">
                    <span class="modal-title">Disable LDAP Module</span>
                    <button class="modal-close" onclick="document.getElementById('ldap-disable-modal').remove()">×</button>
                </div>
                <div class="modal-body">
                    ${needsAck ? `<div class="alert alert-warning" style="margin-bottom:16px;">
                        <strong>${nodeCount} node(s)</strong> are configured with LDAP. Disabling will break their authentication until they are reimaged.
                    </div>` : ''}
                    <div class="form-group" style="margin-bottom:12px;">
                        <label>Disable mode</label>
                        <select id="ldap-disable-mode">
                            <option value="detach">detach — stop slapd, preserve data (can re-enable later)</option>
                            <option value="destroy">destroy — stop slapd and wipe all LDAP data</option>
                        </select>
                    </div>
                    ${needsAck ? `<label style="display:flex;align-items:flex-start;gap:10px;cursor:pointer;margin-bottom:8px;">
                        <input type="checkbox" id="ldap-disable-ack" style="margin-top:2px;flex-shrink:0;">
                        <span style="font-size:13px;">I understand that ${nodeCount} node(s) will lose LDAP authentication</span>
                    </label>` : ''}
                    <div class="form-actions">
                        <button class="btn btn-secondary" onclick="document.getElementById('ldap-disable-modal').remove()">Cancel</button>
                        <button class="btn btn-danger" onclick="LDAPPages._disable(${needsAck})">Disable</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(overlay);
    },

    async _disable(needsAck) {
        const mode = (document.getElementById('ldap-disable-mode') || {}).value || 'detach';
        const ack  = needsAck ? !!(document.getElementById('ldap-disable-ack') || {}).checked : true;
        if (needsAck && !ack) {
            App.toast('You must acknowledge the impact on configured nodes', 'error');
            return;
        }
        try {
            await API.ldap.disable({ confirm: mode, nodes_acknowledged: ack });
            const modal = document.getElementById('ldap-disable-modal');
            if (modal) modal.remove();
            App.toast('LDAP module disabled', 'success');
            await LDAPPages.bootstrapNav();
            LDAPPages.settings();
        } catch (err) {
            App.toast('Disable failed: ' + err.message, 'error');
        }
    },

    async _backup() {
        try {
            const resp = await API.ldap.backup();
            App.toast('Backup complete: ' + (resp.filename || 'saved'), 'success');
        } catch (err) {
            App.toast('Backup failed: ' + err.message, 'error');
        }
    },

    _repairTogglePassword() {
        const el  = document.getElementById('ldap-repair-password');
        const btn = document.getElementById('ldap-repair-pw-toggle');
        if (!el) return;
        if (el.type === 'password') { el.type = 'text';     if (btn) btn.textContent = 'Hide'; }
        else                        { el.type = 'password'; if (btn) btn.textContent = 'Show'; }
    },

    async _repairAdminBind() {
        const errEl = document.getElementById('ldap-repair-err');
        if (errEl) { errEl.textContent = ''; errEl.style.display = 'none'; }

        const password = (document.getElementById('ldap-repair-password') || {}).value || '';
        if (!password) {
            if (errEl) { errEl.textContent = 'Password is required'; errEl.style.display = ''; }
            return;
        }

        const submitBtn = document.getElementById('ldap-repair-submit');
        if (submitBtn) { submitBtn.disabled = true; submitBtn.textContent = 'Verifying…'; }

        try {
            await API.ldap.repairAdminBind({ admin_password: password });
            if (submitBtn) { submitBtn.disabled = false; submitBtn.textContent = 'Verify & Repair'; }
            // Clear the password field after success.
            const pwEl = document.getElementById('ldap-repair-password');
            if (pwEl) { pwEl.value = ''; pwEl.type = 'password'; }
            const toggleBtn = document.getElementById('ldap-repair-pw-toggle');
            if (toggleBtn) toggleBtn.textContent = 'Show';
            App.toast('Admin bind repaired.', 'success');
        } catch (err) {
            if (submitBtn) { submitBtn.disabled = false; submitBtn.textContent = 'Verify & Repair'; }
            const msg = err.message || 'Repair failed';
            if (errEl) { errEl.textContent = msg; errEl.style.display = ''; }
        }
    },

    // ── Users page ────────────────────────────────────────────────────────

    async users() {
        App.render(loading('Loading LDAP users…'));
        try {
            const resp = await API.ldap.listUsers();
            const users = (resp && resp.users) ? resp.users : [];
            App.render(LDAPPages._usersHtml(users));
        } catch (err) {
            App.render(alertBox('Failed to load users: ' + err.message));
        }
    },

    _usersHtml(users) {
        const isAdmin = typeof Auth !== 'undefined' && Auth._role === 'admin';

        const rows = users.length === 0
            ? `<tr><td colspan="6" style="text-align:center;color:var(--text-secondary);padding:24px">No LDAP users. Create the first one.</td></tr>`
            : users.map(u => {
                const lockedBadge = u.locked
                    ? `<span class="badge" style="background:#fee2e2;color:#dc2626;margin-left:6px;font-size:11px;padding:1px 6px;border-radius:4px;font-weight:600;vertical-align:middle;">Locked</span>` : '';
                // JSON-encode the user safely for passing into onclick attr.
                const userJson = escHtml(JSON.stringify(u));
                // Locked users get muted row styling for visual distinction.
                const rowStyle = u.locked ? ' style="opacity:0.6;"' : '';
                const lockBtn = u.locked
                    ? `<button class="btn btn-secondary btn-sm" onclick="LDAPPages._unlockUser('${escHtml(u.uid)}')" ${!isAdmin ? 'disabled' : ''}>Unlock</button>`
                    : `<button class="btn btn-secondary btn-sm" onclick="LDAPPages._lockConfirm('${escHtml(u.uid)}')" ${!isAdmin ? 'disabled' : ''}>Lock</button>`;
                return `<tr${rowStyle}>
                    <td class="text-mono text-sm">${escHtml(u.uid)}${lockedBadge}</td>
                    <td>${escHtml(u.cn || '—')}</td>
                    <td class="text-mono text-sm">${escHtml(String(u.uid_number || '—'))}</td>
                    <td class="text-mono text-sm">${escHtml(String(u.gid_number || '—'))}</td>
                    <td>${escHtml(u.login_shell || '/bin/bash')}</td>
                    <td>
                        <div style="display:flex;gap:6px;flex-wrap:wrap;">
                            <button class="btn btn-secondary btn-sm" onclick="LDAPPages._resetPasswordModal('${escHtml(u.uid)}')" ${!isAdmin ? 'disabled' : ''}>Reset Password</button>
                            ${lockBtn}
                            <button class="btn btn-secondary btn-sm" onclick="LDAPPages._editUserModal(${userJson})" ${!isAdmin ? 'disabled' : ''}>Edit</button>
                            <button class="btn btn-danger btn-sm" onclick="LDAPPages._deleteUser('${escHtml(u.uid)}')" ${!isAdmin ? 'disabled' : ''}>Delete</button>
                        </div>
                    </td>
                </tr>`;
            }).join('');

        return `
            <div class="page-header">
                <div>
                    <div class="page-title">LDAP Users</div>
                    <div class="page-subtitle">posixAccount entries in ou=people</div>
                </div>
                <div style="display:flex;gap:8px;">
                    <button class="btn btn-primary" onclick="LDAPPages._createUserModal()" ${!isAdmin ? 'disabled' : ''}>+ Create User</button>
                </div>
            </div>
            <div class="card">
                <table class="table">
                    <thead>
                        <tr>
                            <th>UID</th><th>Full Name</th><th>UID Number</th><th>GID Number</th><th>Shell</th><th>Actions</th>
                        </tr>
                    </thead>
                    <tbody>${rows}</tbody>
                </table>
            </div>`;
    },

    // _lcuNextId fetches /api/v1/ldap/users and /api/v1/ldap/groups to determine
    // the next available UID/GID (max existing + 1, floor 10001).
    async _lcuPrefillIds() {
        let nextUid = 10001;
        let nextGid = 10001;
        try {
            const [ur, gr] = await Promise.all([API.ldap.listUsers(), API.ldap.listGroups()]);
            const users  = (ur && ur.users)   ? ur.users   : [];
            const groups = (gr && gr.groups)  ? gr.groups  : [];
            const maxUid = users.reduce( (m, u) => Math.max(m, u.uid_number  || 0), 0);
            const maxGid = groups.reduce((m, g) => Math.max(m, g.gid_number  || 0), 0);
            if (maxUid >= 10000) nextUid = maxUid + 1;
            if (maxGid >= 10000) nextGid = maxGid + 1;
        } catch (_) { /* fall back to defaults */ }
        const uidEl = document.getElementById('lcu-uidnum');
        const gidEl = document.getElementById('lcu-gidnum');
        if (uidEl && !uidEl.value) uidEl.value = nextUid;
        if (gidEl && !gidEl.value) gidEl.value = nextGid;
    },

    // _lcuGenPassword generates a 20-char URL-safe random string client-side.
    _lcuGenPassword() {
        const chars = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789-_';
        const arr   = new Uint8Array(32);
        crypto.getRandomValues(arr);
        let out = '';
        for (let i = 0; i < arr.length && out.length < 20; i++) {
            const idx = arr[i] % chars.length;
            out += chars[idx];
        }
        const el = document.getElementById('lcu-password');
        if (el) { el.value = out; el.type = 'text'; }
        const btn = document.getElementById('lcu-pw-toggle');
        if (btn) btn.textContent = 'Hide';
    },

    _createUserModal() {
        const existingModal = document.getElementById('ldap-create-user-modal');
        if (existingModal) existingModal.remove();

        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = 'ldap-create-user-modal';
        overlay.innerHTML = `
            <div class="modal modal-wide">
                <div class="modal-header">
                    <span class="modal-title">Create LDAP User</span>
                    <button class="modal-close" onclick="document.getElementById('ldap-create-user-modal').remove()">×</button>
                </div>
                <div class="modal-body">

                    <!-- Section: Identity -->
                    <div style="font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:0.08em;color:var(--text-secondary);margin-bottom:10px;">Identity</div>
                    <div class="form-grid">
                        <div class="form-group">
                            <label>UID (username)</label>
                            <input id="lcu-uid" type="text" placeholder="jdoe"
                                style="font-family:var(--font-mono);"
                                oninput="LDAPPages._lcuOnUidInput(this.value)">
                            <div class="form-hint">Lowercase POSIX login name. Permanent — change requires recreating the user.</div>
                            <div id="lcu-uid-err" class="form-hint" style="color:var(--error);display:none;"></div>
                        </div>
                        <div class="form-group">
                            <label>Full Name</label>
                            <input id="lcu-fullname" type="text" placeholder="Jane Doe">
                            <div class="form-hint">Display name shown in account listings. Stored as cn.</div>
                        </div>
                    </div>

                    <div class="divider"></div>

                    <!-- Section: POSIX -->
                    <div style="font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:0.08em;color:var(--text-secondary);margin-bottom:10px;">POSIX</div>
                    <div class="form-grid">
                        <div class="form-group">
                            <label>UID Number</label>
                            <input id="lcu-uidnum" type="number" placeholder="10001" min="1000">
                            <div class="form-hint">Numeric POSIX UID. Cluster convention: ≥10000 for users.</div>
                            <div id="lcu-uidnum-err" class="form-hint" style="color:var(--error);display:none;"></div>
                        </div>
                        <div class="form-group">
                            <label>GID Number</label>
                            <input id="lcu-gidnum" type="number" placeholder="10001" min="1000">
                            <div class="form-hint">Primary group's numeric GID. Usually matches UID Number.</div>
                            <div id="lcu-gidnum-err" class="form-hint" style="color:var(--error);display:none;"></div>
                        </div>
                        <div class="form-group">
                            <label>Home Directory</label>
                            <input id="lcu-home" type="text" placeholder="/home/jdoe"
                                style="font-family:var(--font-mono);"
                                oninput="this._manuallyEdited=true">
                            <div class="form-hint">Auto-created on first SSH login via pam_mkhomedir.</div>
                        </div>
                        <div class="form-group">
                            <label>Shell</label>
                            <select id="lcu-shell" onchange="LDAPPages._lcuOnShellChange(this.value)">
                                <option value="/bin/bash" selected>/bin/bash</option>
                                <option value="/bin/zsh">/bin/zsh</option>
                                <option value="/bin/sh">/bin/sh</option>
                                <option value="/sbin/nologin">/sbin/nologin — disable shell access</option>
                                <option value="__other__">Other…</option>
                            </select>
                            <input id="lcu-shell-other" type="text" placeholder="/usr/bin/fish"
                                style="font-family:var(--font-mono);display:none;margin-top:6px;">
                            <div class="form-hint">Login shell. Use /sbin/nologin to disable shell access.</div>
                        </div>
                    </div>

                    <div class="divider"></div>

                    <!-- Section: Authentication -->
                    <div style="font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:0.08em;color:var(--text-secondary);margin-bottom:10px;">Authentication</div>
                    <div class="form-group" style="margin-bottom:12px;">
                        <label>SSH Public Key (optional)</label>
                        <textarea id="lcu-sshkey" rows="4"
                            placeholder="ssh-ed25519 AAAA... user@host"
                            style="font-family:var(--font-mono);font-size:12px;resize:vertical;"></textarea>
                        <div class="form-hint">Optional. Pasted into the user's authorized_keys at first login. One key per user.</div>
                    </div>
                    <div class="form-group">
                        <label>Initial Password</label>
                        <div style="display:flex;gap:6px;align-items:center;">
                            <input id="lcu-password" type="password" style="flex:1;">
                            <button type="button" id="lcu-pw-toggle" class="btn btn-secondary btn-sm"
                                onclick="LDAPPages._lcuTogglePassword()">Show</button>
                            <button type="button" class="btn btn-secondary btn-sm"
                                onclick="LDAPPages._lcuGenPassword()">Generate</button>
                        </div>
                        <div class="form-hint">Operator can change this; user can change it themselves after first login.</div>
                        <div id="lcu-pw-err" class="form-hint" style="color:var(--error);display:none;"></div>
                    </div>

                    <div class="form-actions">
                        <button class="btn btn-secondary" onclick="document.getElementById('ldap-create-user-modal').remove()">Cancel</button>
                        <button id="lcu-submit" class="btn btn-primary" onclick="LDAPPages._createUserSubmit()">Create</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(overlay);

        // Prefill UID/GID from API after modal is in the DOM.
        LDAPPages._lcuPrefillIds();
    },

    _lcuOnUidInput(val) {
        // Auto-fill home directory unless operator has manually overridden it.
        const homeEl = document.getElementById('lcu-home');
        if (homeEl && !homeEl._manuallyEdited) {
            homeEl.value = val ? '/home/' + val : '';
        }
    },

    _lcuOnShellChange(val) {
        const other = document.getElementById('lcu-shell-other');
        if (other) other.style.display = val === '__other__' ? '' : 'none';
    },

    _lcuTogglePassword() {
        const el  = document.getElementById('lcu-password');
        const btn = document.getElementById('lcu-pw-toggle');
        if (!el) return;
        if (el.type === 'password') { el.type = 'text';     if (btn) btn.textContent = 'Hide'; }
        else                        { el.type = 'password'; if (btn) btn.textContent = 'Show'; }
    },

    // _lcuSetFieldError shows/clears an inline validation error under a field.
    _lcuSetFieldError(id, msg) {
        const el = document.getElementById(id);
        if (!el) return;
        if (msg) { el.textContent = msg; el.style.display = ''; }
        else     { el.textContent = '';  el.style.display = 'none'; }
    },

    async _createUserSubmit() {
        // Clear previous inline errors.
        ['lcu-uid-err', 'lcu-uidnum-err', 'lcu-gidnum-err', 'lcu-pw-err'].forEach(id => LDAPPages._lcuSetFieldError(id, ''));

        const uid      = (document.getElementById('lcu-uid')      || {}).value || '';
        const fullName = (document.getElementById('lcu-fullname') || {}).value || '';
        const uidNum   = parseInt((document.getElementById('lcu-uidnum') || {}).value || '0', 10);
        const gidNum   = parseInt((document.getElementById('lcu-gidnum') || {}).value || '0', 10);
        const home     = (document.getElementById('lcu-home')     || {}).value || '';
        const shellSel = (document.getElementById('lcu-shell')    || {}).value || '/bin/bash';
        const shell    = shellSel === '__other__'
            ? ((document.getElementById('lcu-shell-other') || {}).value || '').trim()
            : shellSel;
        const sshKey   = (document.getElementById('lcu-sshkey')   || {}).value || '';
        const password = (document.getElementById('lcu-password') || {}).value || '';

        let hasErr = false;
        if (!uid.trim())  { LDAPPages._lcuSetFieldError('lcu-uid-err',    'UID is required'); hasErr = true; }
        if (!uidNum)      { LDAPPages._lcuSetFieldError('lcu-uidnum-err', 'UID Number is required'); hasErr = true; }
        if (!gidNum)      { LDAPPages._lcuSetFieldError('lcu-gidnum-err', 'GID Number is required'); hasErr = true; }
        if (!password)    { LDAPPages._lcuSetFieldError('lcu-pw-err',     'Initial password is required'); hasErr = true; }
        if (hasErr) return;

        const submitBtn = document.getElementById('lcu-submit');
        if (submitBtn) { submitBtn.disabled = true; submitBtn.textContent = 'Creating…'; }

        const body = {
            uid:            uid.trim(),
            cn:             fullName || uid.trim(),
            uid_number:     uidNum,
            gid_number:     gidNum,
            home_directory: home || '/home/' + uid.trim(),
            login_shell:    shell || '/bin/bash',
            password:       password,
        };
        if (sshKey.trim()) body.ssh_public_key = sshKey.trim();

        try {
            await API.ldap.createUser(body);
            const modal = document.getElementById('ldap-create-user-modal');
            if (modal) modal.remove();
            App.toast('User created: ' + uid.trim(), 'success');
            LDAPPages.users();
        } catch (err) {
            if (submitBtn) { submitBtn.disabled = false; submitBtn.textContent = 'Create'; }
            App.toast('Create failed: ' + err.message, 'error');
        }
    },

    _editUserModal(user) {
        const existingModal = document.getElementById('ldap-edit-user-modal');
        if (existingModal) existingModal.remove();

        // Determine the current shell: known option or "other".
        const knownShells = ['/bin/bash', '/bin/zsh', '/bin/sh', '/sbin/nologin'];
        const currentShell = user.login_shell || '/bin/bash';
        const isOtherShell = !knownShells.includes(currentShell);
        const shellOptions = knownShells.map(s =>
            `<option value="${escHtml(s)}" ${!isOtherShell && currentShell === s ? 'selected' : ''}>${escHtml(s)}${s === '/sbin/nologin' ? ' — disable shell access' : ''}</option>`
        ).join('');

        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = 'ldap-edit-user-modal';
        overlay.innerHTML = `
            <div class="modal modal-wide">
                <div class="modal-header">
                    <span class="modal-title">Edit User: ${escHtml(user.uid)}</span>
                    <button class="modal-close" onclick="document.getElementById('ldap-edit-user-modal').remove()">×</button>
                </div>
                <div class="modal-body">

                    <!-- Section: Identity -->
                    <div style="font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:0.08em;color:var(--text-secondary);margin-bottom:10px;">Identity</div>
                    <div class="form-grid">
                        <div class="form-group">
                            <label>UID (username)</label>
                            <input type="text" value="${escHtml(user.uid)}" disabled
                                style="font-family:var(--font-mono);opacity:0.6;">
                            <div class="form-hint">UID cannot be changed after creation.</div>
                        </div>
                        <div class="form-group">
                            <label>Full Name</label>
                            <input id="leu-cn" type="text" value="${escHtml(user.cn || '')}">
                            <div class="form-hint">Display name shown in account listings. Stored as cn.</div>
                        </div>
                    </div>

                    <div class="divider"></div>

                    <!-- Section: POSIX -->
                    <div style="font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:0.08em;color:var(--text-secondary);margin-bottom:10px;">POSIX</div>
                    <div class="form-group">
                        <label>Shell</label>
                        <select id="leu-shell" onchange="LDAPPages._leuOnShellChange(this.value)">
                            ${shellOptions}
                            <option value="__other__" ${isOtherShell ? 'selected' : ''}>Other…</option>
                        </select>
                        <input id="leu-shell-other" type="text" placeholder="/usr/bin/fish"
                            value="${isOtherShell ? escHtml(currentShell) : ''}"
                            style="font-family:var(--font-mono);display:${isOtherShell ? '' : 'none'};margin-top:6px;">
                        <div class="form-hint">Login shell. Use /sbin/nologin to disable shell access.</div>
                    </div>

                    <div class="divider"></div>

                    <!-- Section: Authentication -->
                    <div style="font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:0.08em;color:var(--text-secondary);margin-bottom:10px;">Authentication</div>
                    <div class="form-group" style="margin-bottom:12px;">
                        <label>SSH Public Key</label>
                        <textarea id="leu-sshkey" rows="4"
                            style="font-family:var(--font-mono);font-size:12px;resize:vertical;">${escHtml(user.ssh_public_key || '')}</textarea>
                        <div class="form-hint">Optional. Pasted into the user's authorized_keys at first login. One key per user.</div>
                    </div>
                    <div style="padding:12px 14px;background:var(--bg-primary);border:1px solid var(--border);border-radius:var(--radius-sm);font-size:13px;color:var(--text-secondary);">
                        To reset this user's password, use the <strong>Reset PW</strong> button on the Users table.
                    </div>

                    <div class="form-actions">
                        <button class="btn btn-secondary" onclick="document.getElementById('ldap-edit-user-modal').remove()">Cancel</button>
                        <button id="leu-submit" class="btn btn-primary" onclick="LDAPPages._editUserSubmit('${escHtml(user.uid)}')">Save</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(overlay);
    },

    _leuOnShellChange(val) {
        const other = document.getElementById('leu-shell-other');
        if (other) other.style.display = val === '__other__' ? '' : 'none';
    },

    async _editUserSubmit(uid) {
        const cn       = (document.getElementById('leu-cn')         || {}).value || '';
        const shellSel = (document.getElementById('leu-shell')       || {}).value || '/bin/bash';
        const shell    = shellSel === '__other__'
            ? ((document.getElementById('leu-shell-other') || {}).value || '').trim()
            : shellSel;
        const sshKey   = (document.getElementById('leu-sshkey')     || {}).value || '';

        const submitBtn = document.getElementById('leu-submit');
        if (submitBtn) { submitBtn.disabled = true; submitBtn.textContent = 'Saving…'; }

        const body = { cn, login_shell: shell || '/bin/bash' };
        if (sshKey.trim()) body.ssh_public_key = sshKey.trim();

        try {
            await API.ldap.updateUser(uid, body);
            const modal = document.getElementById('ldap-edit-user-modal');
            if (modal) modal.remove();
            App.toast('User updated', 'success');
            LDAPPages.users();
        } catch (err) {
            if (submitBtn) { submitBtn.disabled = false; submitBtn.textContent = 'Save'; }
            App.toast('Update failed: ' + err.message, 'error');
        }
    },

    _resetPasswordModal(uid) {
        const existingModal = document.getElementById('ldap-reset-pw-modal');
        if (existingModal) existingModal.remove();

        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = 'ldap-reset-pw-modal';
        overlay.innerHTML = `
            <div class="modal">
                <div class="modal-header">
                    <span class="modal-title">Reset Password: ${escHtml(uid)}</span>
                    <button class="modal-close" onclick="document.getElementById('ldap-reset-pw-modal').remove()">×</button>
                </div>
                <div class="modal-body">
                    <div class="form-group" style="margin-bottom:12px;">
                        <label>New Password</label>
                        <div style="display:flex;gap:6px;align-items:center;">
                            <input id="lrp-password" type="password" style="flex:1;" autofocus>
                            <button type="button" id="lrp-pw-toggle" class="btn btn-secondary btn-sm"
                                onclick="LDAPPages._lrpTogglePassword()">Show</button>
                            <button type="button" class="btn btn-secondary btn-sm"
                                onclick="LDAPPages._lrpGenPassword()">Generate</button>
                        </div>
                        <div class="form-hint">Operator can change this; user can change it themselves after first login.</div>
                        <div id="lrp-pw-err" class="form-hint" style="color:var(--error);display:none;"></div>
                    </div>
                    <div class="form-actions">
                        <button class="btn btn-secondary" onclick="document.getElementById('ldap-reset-pw-modal').remove()">Cancel</button>
                        <button id="lrp-submit" class="btn btn-primary" onclick="LDAPPages._resetPasswordSubmit('${escHtml(uid)}')">Reset</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(overlay);
    },

    _lrpTogglePassword() {
        const el  = document.getElementById('lrp-password');
        const btn = document.getElementById('lrp-pw-toggle');
        if (!el) return;
        if (el.type === 'password') { el.type = 'text';     if (btn) btn.textContent = 'Hide'; }
        else                        { el.type = 'password'; if (btn) btn.textContent = 'Show'; }
    },

    _lrpGenPassword() {
        const chars = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789-_';
        const arr   = new Uint8Array(32);
        crypto.getRandomValues(arr);
        let out = '';
        for (let i = 0; i < arr.length && out.length < 20; i++) {
            out += chars[arr[i] % chars.length];
        }
        const el = document.getElementById('lrp-password');
        if (el) { el.value = out; el.type = 'text'; }
        const btn = document.getElementById('lrp-pw-toggle');
        if (btn) btn.textContent = 'Hide';
    },

    async _resetPasswordSubmit(uid) {
        const errEl = document.getElementById('lrp-pw-err');
        if (errEl) { errEl.textContent = ''; errEl.style.display = 'none'; }

        const password = (document.getElementById('lrp-password') || {}).value || '';
        if (!password) {
            if (errEl) { errEl.textContent = 'Password is required'; errEl.style.display = ''; }
            return;
        }

        const submitBtn = document.getElementById('lrp-submit');
        if (submitBtn) { submitBtn.disabled = true; submitBtn.textContent = 'Resetting…'; }

        try {
            await API.ldap.setPassword(uid, password);
            const modal = document.getElementById('ldap-reset-pw-modal');
            if (modal) modal.remove();
            App.toast('Password reset for ' + uid, 'success');
        } catch (err) {
            if (submitBtn) { submitBtn.disabled = false; submitBtn.textContent = 'Reset'; }
            App.toast('Reset failed: ' + err.message, 'error');
        }
    },

    // _lockConfirm shows an inline confirmation before locking a user account.
    // Uses a modal (not browser confirm()) per the brief. Unlock is a single click.
    _lockConfirm(uid) {
        const existingModal = document.getElementById('ldap-lock-confirm-modal');
        if (existingModal) existingModal.remove();

        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = 'ldap-lock-confirm-modal';
        overlay.innerHTML = `
            <div class="modal">
                <div class="modal-header">
                    <span class="modal-title">Lock user account?</span>
                    <button class="modal-close" onclick="document.getElementById('ldap-lock-confirm-modal').remove()">×</button>
                </div>
                <div class="modal-body">
                    <p style="margin:0 0 16px;font-size:14px;">
                        Lock <strong>${escHtml(uid)}</strong>? The account will be disabled immediately.
                        Active sessions are not terminated, but new logins will be rejected.
                    </p>
                    <div class="form-actions">
                        <button class="btn btn-secondary" onclick="document.getElementById('ldap-lock-confirm-modal').remove()">Cancel</button>
                        <button class="btn btn-primary" onclick="document.getElementById('ldap-lock-confirm-modal').remove();LDAPPages._doLockUser('${escHtml(uid)}')">Lock</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(overlay);
    },

    async _doLockUser(uid) {
        try {
            await API.ldap.lockUser(uid);
            App.toast('User locked: ' + uid, 'success');
            LDAPPages.users();
        } catch (err) {
            App.toast('Lock failed: ' + err.message, 'error');
        }
    },

    async _unlockUser(uid) {
        try {
            await API.ldap.unlockUser(uid);
            App.toast('User unlocked: ' + uid, 'success');
            LDAPPages.users();
        } catch (err) {
            App.toast('Unlock failed: ' + err.message, 'error');
        }
    },

    async _deleteUser(uid) {
        if (!confirm(`Delete user "${uid}"? This cannot be undone.`)) return;
        try {
            await API.ldap.deleteUser(uid);
            App.toast('User deleted: ' + uid, 'success');
            LDAPPages.users();
        } catch (err) {
            App.toast('Delete failed: ' + err.message, 'error');
        }
    },

    // ── Groups page ───────────────────────────────────────────────────────

    async groups() {
        App.render(loading('Loading LDAP groups…'));
        try {
            const resp = await API.ldap.listGroups();
            const groups = (resp && resp.groups) ? resp.groups : [];
            App.render(LDAPPages._groupsHtml(groups));
        } catch (err) {
            App.render(alertBox('Failed to load groups: ' + err.message));
        }
    },

    _groupsHtml(groups) {
        const isAdmin = typeof Auth !== 'undefined' && Auth._role === 'admin';

        const rows = groups.length === 0
            ? `<tr><td colspan="4" style="text-align:center;color:var(--text-secondary);padding:24px">No LDAP groups. Create the first one.</td></tr>`
            : groups.map(g => {
                const members = (g.member_uids || []).length;
                const desc = g.description || '';
                const descPreview = desc
                    ? `<div style="color:var(--text-secondary);font-size:12px;margin-top:2px;">${escHtml(desc.length > 60 ? desc.slice(0, 60) + '…' : desc)}</div>`
                    : '';
                return `<tr style="cursor:pointer;" onclick="LDAPPages._groupDetailModal(${JSON.stringify(g).replace(/"/g, '&quot;')})">
                    <td class="text-mono text-sm"><div>${escHtml(g.cn)}</div>${descPreview}</td>
                    <td class="text-mono text-sm">${escHtml(String(g.gid_number || '—'))}</td>
                    <td>${members} member${members === 1 ? '' : 's'}</td>
                    <td>
                        <div style="display:flex;gap:6px;">
                            <button class="btn btn-secondary btn-sm" onclick="event.stopPropagation();LDAPPages._groupDetailModal(${JSON.stringify(g).replace(/"/g, '&quot;')})">Members</button>
                            <button class="btn btn-danger btn-sm" onclick="event.stopPropagation();LDAPPages._deleteGroup('${escHtml(g.cn)}')" ${!isAdmin ? 'disabled' : ''}>Delete</button>
                        </div>
                    </td>
                </tr>`;
            }).join('');

        return `
            <div class="page-header">
                <div>
                    <div class="page-title">LDAP Groups</div>
                    <div class="page-subtitle">posixGroup entries in ou=groups</div>
                </div>
                <div style="display:flex;gap:8px;">
                    <button class="btn btn-primary" onclick="LDAPPages._createGroupModal()" ${!isAdmin ? 'disabled' : ''}>+ Create Group</button>
                </div>
            </div>
            <div class="card">
                <table class="table">
                    <thead>
                        <tr><th>CN</th><th>GID Number</th><th>Members</th><th>Actions</th></tr>
                    </thead>
                    <tbody>${rows}</tbody>
                </table>
            </div>`;
    },

    async _lcgPrefillGid() {
        let nextGid = 10001;
        try {
            const gr = await API.ldap.listGroups();
            const groups = (gr && gr.groups) ? gr.groups : [];
            const maxGid = groups.reduce((m, g) => Math.max(m, g.gid_number || 0), 0);
            if (maxGid >= 10000) nextGid = maxGid + 1;
        } catch (_) {}
        const el = document.getElementById('lcg-gidnum');
        if (el && !el.value) el.value = nextGid;
    },

    _createGroupModal() {
        const existingModal = document.getElementById('ldap-create-group-modal');
        if (existingModal) existingModal.remove();

        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = 'ldap-create-group-modal';
        overlay.innerHTML = `
            <div class="modal modal-wide">
                <div class="modal-header">
                    <span class="modal-title">Create LDAP Group</span>
                    <button class="modal-close" onclick="document.getElementById('ldap-create-group-modal').remove()">×</button>
                </div>
                <div class="modal-body">
                    <p style="margin:0 0 16px;font-size:13px;color:var(--text-secondary);line-height:1.5;">
                        POSIX groups give multiple users shared file ownership and access control on cluster nodes.
                        Members can be added after the group is created.
                    </p>

                    <!-- Section: Identity -->
                    <div style="font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:0.08em;color:var(--text-secondary);margin-bottom:10px;">Identity</div>
                    <div class="form-grid">
                        <div class="form-group">
                            <label>Group Name (CN)</label>
                            <input id="lcg-cn" type="text" placeholder="hpc-users"
                                style="font-family:var(--font-mono);">
                            <div class="form-hint">POSIX group name. Used for membership lookups on nodes. Permanent after creation.</div>
                            <div id="lcg-cn-err" class="form-hint" style="color:var(--error);display:none;"></div>
                        </div>
                        <div class="form-group">
                            <label>GID Number</label>
                            <input id="lcg-gidnum" type="number" placeholder="10001" min="1000">
                            <div class="form-hint">Numeric POSIX GID. Cluster convention: ≥10000 for user-defined groups.</div>
                            <div id="lcg-gidnum-err" class="form-hint" style="color:var(--error);display:none;"></div>
                        </div>
                    </div>

                    <div class="divider"></div>

                    <!-- Section: Details -->
                    <div style="font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:0.08em;color:var(--text-secondary);margin-bottom:10px;">Details</div>
                    <div class="form-group">
                        <label>Description <span style="font-weight:400;color:var(--text-secondary)">(optional)</span></label>
                        <input id="lcg-description" type="text" placeholder="e.g. HPC cluster users with GPU access">
                        <div class="form-hint">Optional description shown in the group listing.</div>
                    </div>

                    <div class="form-actions">
                        <button class="btn btn-secondary" onclick="document.getElementById('ldap-create-group-modal').remove()">Cancel</button>
                        <button id="lcg-submit" class="btn btn-primary" onclick="LDAPPages._createGroupSubmit()">Create</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(overlay);
        LDAPPages._lcgPrefillGid();
    },

    async _createGroupSubmit() {
        // Clear previous inline errors.
        ['lcg-cn-err', 'lcg-gidnum-err'].forEach(id => {
            const el = document.getElementById(id);
            if (el) { el.textContent = ''; el.style.display = 'none'; }
        });

        const cn     = (document.getElementById('lcg-cn')     || {}).value || '';
        const gidNum = parseInt((document.getElementById('lcg-gidnum') || {}).value || '0', 10);

        let hasErr = false;
        if (!cn.trim()) {
            const el = document.getElementById('lcg-cn-err');
            if (el) { el.textContent = 'Group name is required'; el.style.display = ''; }
            hasErr = true;
        }
        if (!gidNum) {
            const el = document.getElementById('lcg-gidnum-err');
            if (el) { el.textContent = 'GID Number is required'; el.style.display = ''; }
            hasErr = true;
        }
        if (hasErr) return;

        const submitBtn = document.getElementById('lcg-submit');
        if (submitBtn) { submitBtn.disabled = true; submitBtn.textContent = 'Creating…'; }

        const description = ((document.getElementById('lcg-description') || {}).value || '').trim();

        try {
            const body = { cn: cn.trim(), gid_number: gidNum };
            if (description) body.description = description;
            await API.ldap.createGroup(body);
            const modal = document.getElementById('ldap-create-group-modal');
            if (modal) modal.remove();
            App.toast('Group created: ' + cn.trim(), 'success');
            LDAPPages.groups();
        } catch (err) {
            if (submitBtn) { submitBtn.disabled = false; submitBtn.textContent = 'Create'; }
            App.toast('Create failed: ' + err.message, 'error');
        }
    },

    async _deleteGroup(cn) {
        if (!confirm(`Delete group "${cn}"? This cannot be undone.`)) return;
        try {
            await API.ldap.deleteGroup(cn);
            App.toast('Group deleted: ' + cn, 'success');
            LDAPPages.groups();
        } catch (err) {
            App.toast('Delete failed: ' + err.message, 'error');
        }
    },

    // _groupDetailModal opens the group edit / member management modal.
    // Fetches users in parallel with rendering so the add-member picker is
    // populated without a second round-trip after the modal opens.
    async _groupDetailModal(group) {
        const isAdmin = typeof Auth !== 'undefined' && Auth._role === 'admin';
        const existingModal = document.getElementById('ldap-group-detail-modal');
        if (existingModal) existingModal.remove();

        // Build the skeleton immediately so there is no perceived delay.
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = 'ldap-group-detail-modal';
        overlay.innerHTML = `
            <div class="modal modal-wide" style="max-height:80vh;display:flex;flex-direction:column;">
                <div class="modal-header">
                    <span class="modal-title">Group: ${escHtml(group.cn)}</span>
                    <button class="modal-close" onclick="document.getElementById('ldap-group-detail-modal').remove()">×</button>
                </div>
                <div class="modal-body" style="overflow-y:auto;flex:1;">

                    <!-- Section: Properties -->
                    <div style="font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:0.08em;color:var(--text-secondary);margin-bottom:10px;">Properties</div>
                    <div class="form-grid">
                        <div class="form-group">
                            <label>Group Name (CN)</label>
                            <input type="text" value="${escHtml(group.cn)}" disabled
                                style="font-family:var(--font-mono);opacity:0.6;">
                            <div class="form-hint">CN cannot be changed after creation.</div>
                        </div>
                        <div class="form-group">
                            <label>GID Number</label>
                            <input type="text" value="${escHtml(String(group.gid_number || '—'))}" disabled
                                style="font-family:var(--font-mono);opacity:0.6;">
                            <div class="form-hint">Numeric POSIX GID. Read-only after creation.</div>
                        </div>
                    </div>
                    ${isAdmin ? `
                    <div class="form-group" style="margin-top:12px;">
                        <label>Description <span style="font-weight:400;color:var(--text-secondary)">(optional)</span></label>
                        <div style="display:flex;gap:8px;align-items:center;">
                            <input id="lgd-description" type="text" maxlength="256"
                                value="${escHtml(group.description || '')}"
                                placeholder="e.g. HPC cluster users with GPU access"
                                style="flex:1;">
                            <button class="btn btn-secondary" onclick="LDAPPages._saveGroupDescription('${escHtml(group.cn)}')">Save</button>
                        </div>
                        <div class="form-hint">Shown as a preview in the group list. Clear and save to remove.</div>
                        <div id="lgd-desc-err" class="form-hint" style="color:var(--error);display:none;margin-top:4px;"></div>
                    </div>` : (group.description ? `
                    <div class="form-group" style="margin-top:12px;">
                        <label>Description</label>
                        <div style="font-size:13px;color:var(--text-secondary);padding:6px 0;">${escHtml(group.description)}</div>
                    </div>` : '')}

                    <div class="divider"></div>

                    <!-- Section: Members -->
                    <div style="font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:0.08em;color:var(--text-secondary);margin-bottom:10px;">Members</div>
                    <div id="lgd-members-wrap">
                        <div style="color:var(--text-secondary);font-size:13px;padding:8px 0;">Loading members…</div>
                    </div>

                    <!-- Add member row — populated after user list loads -->
                    <div id="lgd-add-wrap" style="margin-top:12px;display:none;">
                        <div style="font-size:12px;font-weight:500;color:var(--text-secondary);margin-bottom:6px;">Add member</div>
                        <div style="display:flex;gap:8px;align-items:center;">
                            <input id="lgd-adduid" list="lgd-users-datalist" type="text"
                                placeholder="Type a username…"
                                style="flex:1;font-family:var(--font-mono);">
                            <datalist id="lgd-users-datalist"></datalist>
                            <button class="btn btn-primary" onclick="LDAPPages._addMember('${escHtml(group.cn)}')">Add</button>
                        </div>
                        <div id="lgd-add-err" class="form-hint" style="color:var(--error);display:none;margin-top:4px;"></div>
                    </div>

                    <div class="form-actions">
                        <button class="btn btn-secondary" onclick="document.getElementById('ldap-group-detail-modal').remove()">Close</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(overlay);

        // Fetch users and render member list + picker in the background.
        LDAPPages._lgdRefreshMembers(group.cn, group.member_uids || [], isAdmin);
    },

    // _lgdRefreshMembers re-fetches group state and re-populates the members
    // section and add-member picker without closing the modal.
    async _lgdRefreshMembers(cn, currentMembers, isAdmin) {
        // Re-fetch the live group state so member list is fresh.
        let members = currentMembers;
        try {
            const resp = await API.ldap.listGroups();
            const groups = (resp && resp.groups) ? resp.groups : [];
            const live = groups.find(g => g.cn === cn);
            if (live) members = live.member_uids || [];
        } catch (_) {}

        // Fetch all users for the add-member picker.
        let allUsers = [];
        try {
            const resp = await API.ldap.listUsers();
            allUsers = (resp && resp.users) ? resp.users : [];
        } catch (_) {}

        // Render the member rows.
        const membersWrap = document.getElementById('lgd-members-wrap');
        if (!membersWrap) return; // modal was closed

        if (members.length === 0) {
            membersWrap.innerHTML = `<p style="color:var(--text-secondary);font-size:13px;margin:0;padding:4px 0;">No members yet. Add the first one below.</p>`;
        } else {
            membersWrap.innerHTML = members.map(uid => {
                const userObj = allUsers.find(u => u.uid === uid);
                const displayName = userObj && userObj.cn ? `<span style="color:var(--text-secondary);font-size:12px;margin-left:6px;">${escHtml(userObj.cn)}</span>` : '';
                return `
                <div class="lgd-member-row" style="display:flex;align-items:center;gap:8px;padding:6px 0;border-bottom:1px solid var(--border);">
                    <span class="text-mono" style="flex:1;font-size:13px;">${escHtml(uid)}${displayName}</span>
                    ${isAdmin ? `<button class="btn btn-danger btn-sm" onclick="LDAPPages._removeMember('${escHtml(cn)}','${escHtml(uid)}')">Remove</button>` : ''}
                </div>`;
            }).join('');
        }

        // Populate the add-member picker (only users not already in the group).
        if (isAdmin) {
            const addWrap = document.getElementById('lgd-add-wrap');
            const datalist = document.getElementById('lgd-users-datalist');
            if (addWrap) addWrap.style.display = '';
            if (datalist) {
                const nonMembers = allUsers.filter(u => !members.includes(u.uid));
                datalist.innerHTML = nonMembers
                    .map(u => `<option value="${escHtml(u.uid)}">${escHtml(u.uid)}${u.cn ? ' — ' + escHtml(u.cn) : ''}</option>`)
                    .join('');
            }
        }
    },

    async _addMember(cn) {
        const uidInput = document.getElementById('lgd-adduid');
        const errEl    = document.getElementById('lgd-add-err');
        const uid = (uidInput || {}).value || '';

        if (errEl) { errEl.textContent = ''; errEl.style.display = 'none'; }
        if (!uid.trim()) {
            if (errEl) { errEl.textContent = 'Enter a username to add'; errEl.style.display = ''; }
            return;
        }

        // Optimistically disable the button while the request is in flight.
        // The add button is the btn-primary inside lgd-add-wrap (after the input + datalist).
        const addWrap = document.getElementById('lgd-add-wrap');
        const addBtn = addWrap ? addWrap.querySelector('button.btn-primary') : null;
        if (addBtn) { addBtn.disabled = true; addBtn.textContent = 'Adding…'; }

        try {
            await API.ldap.addMember(cn, uid.trim());
            if (uidInput) uidInput.value = '';
            if (addBtn) { addBtn.disabled = false; addBtn.textContent = 'Add'; }
            App.toast('Added ' + uid.trim() + ' to ' + cn, 'success');
            // Refresh member list inline without closing the modal.
            LDAPPages._lgdRefreshMembers(cn, [], true);
        } catch (err) {
            if (addBtn) { addBtn.disabled = false; addBtn.textContent = 'Add'; }
            if (errEl) { errEl.textContent = 'Add failed: ' + err.message; errEl.style.display = ''; }
        }
    },

    async _removeMember(cn, uid) {
        try {
            await API.ldap.removeMember(cn, uid);
            App.toast('Removed ' + uid + ' from ' + cn, 'success');
            // Refresh member list inline without closing the modal.
            LDAPPages._lgdRefreshMembers(cn, [], true);
        } catch (err) {
            App.toast('Remove failed: ' + err.message, 'error');
        }
    },

    async _saveGroupDescription(cn) {
        const errEl = document.getElementById('lgd-desc-err');
        if (errEl) { errEl.textContent = ''; errEl.style.display = 'none'; }

        const description = ((document.getElementById('lgd-description') || {}).value || '').trim();
        if (description.length > 256) {
            if (errEl) { errEl.textContent = 'Description must be 256 characters or fewer'; errEl.style.display = ''; }
            return;
        }

        try {
            await API.ldap.updateGroup(cn, { description });
            App.toast('Description updated', 'success');
        } catch (err) {
            if (errEl) { errEl.textContent = 'Save failed: ' + err.message; errEl.style.display = ''; }
            App.toast('Save failed: ' + err.message, 'error');
        }
    },

    // ── Logs page ─────────────────────────────────────────────────────────────

    // _ldapLogStream holds the active EventSource for the LDAP log follow mode.
    // Disconnected on page navigation via the Router teardown (App._logStream).
    // We reuse App._logStream so the Router's existing disconnect-on-navigate
    // hook cleans it up automatically without any extra wiring.

    async logs() {
        App.render(`
            <div class="page-header">
                <div>
                    <div class="page-title">LDAP Logs</div>
                    <div class="page-subtitle">Journal of clonr-slapd.service</div>
                </div>
            </div>

            <div class="log-filter-bar">
                <div class="follow-toggle-wrap">
                    <label class="toggle">
                        <input type="checkbox" id="ldap-follow-toggle" onchange="LDAPPages.toggleFollow(this.checked)">
                        Live
                    </label>
                    <span class="follow-indicator" id="ldap-follow-ind">
                        <span class="follow-dot"></span>static
                    </span>
                </div>
                <button class="btn btn-secondary btn-sm" onclick="LDAPPages.clearLogs()">Clear</button>
            </div>

            <div id="ldap-log-viewer" class="log-viewer tall"></div>
        `);

        await LDAPPages._loadLogs();
    },

    async _loadLogs() {
        const viewer = document.getElementById('ldap-log-viewer');
        if (!viewer) return;

        const followToggle = document.getElementById('ldap-follow-toggle');
        if (App._logStream && followToggle && followToggle.checked) {
            // Already streaming — nothing to do.
            return;
        }

        try {
            const resp = await API.ldap.logs({ lines: '500' });
            const lines = Array.isArray(resp) ? resp : [];

            if (!App._logStream) {
                App._logStream = new LogStream(viewer);
            }

            // Convert raw journal lines into LogEntry-shaped objects for the
            // existing LogStream renderer. We set level=info and put the raw
            // line into message — the journal already contains timestamps inline.
            const entries = lines.map(l => ({
                timestamp: l.timestamp || new Date().toISOString(),
                level: 'info',
                message: l.line || '',
            }));
            App._logStream.loadEntries(entries);

            if (!lines.length) {
                viewer.innerHTML = `<div class="empty-state" style="padding:30px">
                    <div class="empty-state-text">No log entries yet — slapd may not have started</div>
                </div>`;
            }
        } catch (e) {
            const viewer2 = document.getElementById('ldap-log-viewer');
            if (viewer2) {
                viewer2.innerHTML = `<div style="padding:12px;color:var(--error);font-size:12px;font-family:var(--font-mono)">Error: ${escHtml(e.message)}</div>`;
            }
        }
    },

    clearLogs() {
        if (App._logStream) App._logStream.clear();
    },

    toggleFollow(enabled) {
        const viewer = document.getElementById('ldap-log-viewer');
        const ind    = document.getElementById('ldap-follow-ind');
        if (!viewer) return;

        if (enabled) {
            if (!App._logStream) App._logStream = new LogStream(viewer);

            // Build the SSE stream URL for the LDAP log endpoint.
            const url = new URL('/api/v1/ldap/logs/stream', window.location.origin);
            const tok = document.querySelector('meta[name="clonr-token"]');
            if (tok && tok.content) url.searchParams.set('token', tok.content);

            // Swap LogStream's internal URL by monkey-patching _attemptConnect
            // for this instance so it hits /api/v1/ldap/logs/stream.
            // We do this by overriding _attemptConnect on the instance rather
            // than modifying the shared LogStream class.
            const stream = App._logStream;
            stream._ldapStreamURL = url.toString();
            stream._attemptConnect = function() {
                const src = new EventSource(this._ldapStreamURL);
                this.source = src;

                src.onopen = () => {
                    this._retryCount = 0;
                    if (this._onConnect) this._onConnect();
                };

                src.onerror = () => {
                    if (this.source) { this.source.close(); this.source = null; }
                    if (!this._shouldReconnect) return;
                    this._retryCount++;
                    if (this._retryCount > this._maxRetries) {
                        this._shouldReconnect = false;
                        if (this._onDisconnect) this._onDisconnect(true);
                        return;
                    }
                    if (this._onDisconnect) this._onDisconnect(false);
                    const delay = 3000 * Math.pow(2, this._retryCount - 1);
                    setTimeout(() => { if (this._shouldReconnect) this._attemptConnect(); }, delay);
                };

                src.onmessage = (evt) => {
                    try {
                        const raw = JSON.parse(evt.data);
                        // Map raw journal line to LogEntry shape.
                        this.appendEntry({
                            timestamp: raw.timestamp || new Date().toISOString(),
                            level: 'info',
                            message: raw.line || '',
                        });
                    } catch (_) {}
                };

                this._shouldReconnect = true;
            };

            stream.setAutoScroll(true);
            stream.onConnect(() => {
                if (ind) { ind.className = 'follow-indicator live'; ind.innerHTML = '<span class="follow-dot"></span>Live'; }
            });
            stream.onDisconnect(() => {
                if (ind) { ind.className = 'follow-indicator'; ind.innerHTML = '<span class="follow-dot"></span>Reconnecting…'; }
            });
            stream.connect();
        } else {
            if (App._logStream) {
                App._logStream.disconnect();
                if (ind) { ind.className = 'follow-indicator'; ind.innerHTML = '<span class="follow-dot"></span>static'; }
            }
        }
    },
};
