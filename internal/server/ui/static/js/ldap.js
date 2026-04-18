// ldap.js — LDAP module webui: settings, users, and groups.
// Conventions mirror app.js: hash-based routing, App.render(), App.toast(),
// cardWrap(), emptyState(), badge(), escHtml(), fmtDate(), fmtRelative().
// API calls go through API.ldap.* (defined in api.js).

// ─── LDAPPages namespace ───────────────────────────────────────────────────

const LDAPPages = {

    // ── Nav bootstrap ────────────────────────────────────────────────────────

    // bootstrapNav fetches /api/v1/ldap/status and shows/hides the LDAP nav
    // section. Called once on app boot. Re-called after enable/disable.
    async bootstrapNav() {
        const section = document.getElementById('nav-ldap-section');
        if (!section) return;
        try {
            const st = await API.ldap.status();
            section.style.display = (st && st.enabled) ? '' : 'none';
        } catch (_) {
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

        // Enable form (shown when disabled).
        const enableForm = !st.enabled ? `
            <div class="card" style="margin-top:20px;">
                <div class="card-header"><span class="card-title">Enable LDAP Module</span></div>
                <div style="padding:16px;display:flex;flex-direction:column;gap:12px;max-width:480px;">
                    <p style="margin:0;color:var(--text-secondary);font-size:14px;">
                        Enabling the LDAP module will start a self-hosted OpenLDAP instance on this server
                        and configure new nodes to authenticate via it on reimage.
                    </p>
                    <label class="form-label">Base DN
                        <input id="ldap-enable-basedn" class="form-input" type="text"
                            value="dc=cluster,dc=local"
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
                        <button class="btn btn-primary" onclick="LDAPPages._enable()" ${!isAdmin ? 'disabled' : ''}>Enable</button>
                    </div>
                </div>
            </div>` : '';

        // Action buttons (shown when enabled).
        const actionButtons = st.enabled ? `
            <div style="display:flex;gap:8px;margin-top:4px;">
                <button class="btn btn-secondary" onclick="LDAPPages._backup()">Backup LDIF</button>
                <button class="btn btn-danger" onclick="LDAPPages._disableModal(${st.configured_node_count || 0})" ${!isAdmin ? 'disabled' : ''}>Disable</button>
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
        `;
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
        const modal = document.createElement('div');
        modal.id = 'ldap-disable-modal';
        modal.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.5);display:flex;align-items:center;justify-content:center;z-index:1000;';
        modal.innerHTML = `
            <div class="card" style="width:520px;max-width:95vw;">
                <div class="card-header">
                    <span class="card-title">Disable LDAP Module</span>
                    <button class="btn btn-ghost btn-sm" onclick="document.getElementById('ldap-disable-modal').remove()">×</button>
                </div>
                <div style="padding:16px;display:flex;flex-direction:column;gap:14px;">
                    ${needsAck ? `<div class="alert" style="background:#fef3c7;color:#92400e;border:1px solid #fbbf24;border-radius:6px;padding:10px 14px;">
                        <strong>${nodeCount} node(s)</strong> are configured with LDAP. Disabling will break their authentication until they are reimaged.
                    </div>` : ''}
                    <label class="form-label">Disable mode
                        <select id="ldap-disable-mode" class="form-input" style="margin-top:4px;">
                            <option value="detach">detach — stop slapd, preserve data (can re-enable later)</option>
                            <option value="destroy">destroy — stop slapd and wipe all LDAP data</option>
                        </select>
                    </label>
                    ${needsAck ? `<label style="display:flex;align-items:flex-start;gap:10px;cursor:pointer;">
                        <input type="checkbox" id="ldap-disable-ack" style="margin-top:2px;">
                        <span style="font-size:14px;">I understand that ${nodeCount} node(s) will lose LDAP authentication</span>
                    </label>` : ''}
                    <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:8px;">
                        <button class="btn btn-secondary" onclick="document.getElementById('ldap-disable-modal').remove()">Cancel</button>
                        <button class="btn btn-danger" onclick="LDAPPages._disable(${needsAck})">Disable</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(modal);
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
            ? `<tr><td colspan="7" style="text-align:center;color:var(--text-secondary);padding:24px">No LDAP users. Create the first one.</td></tr>`
            : users.map(u => {
                const lockedBadge = u.locked
                    ? `<span class="badge" style="background:#fee2e2;color:#dc2626;margin-left:4px;">locked</span>` : '';
                // JSON-encode the user safely for passing into onclick attr.
                const userJson = escHtml(JSON.stringify(u));
                return `<tr>
                    <td class="text-mono text-sm">${escHtml(u.uid)}</td>
                    <td>${escHtml(u.cn || '—')}</td>
                    <td class="text-mono text-sm">${escHtml(String(u.uid_number || '—'))}</td>
                    <td class="text-mono text-sm">${escHtml(String(u.gid_number || '—'))}</td>
                    <td>${escHtml(u.login_shell || '/bin/bash')}${lockedBadge}</td>
                    <td>
                        <div style="display:flex;gap:6px;flex-wrap:wrap;">
                            <button class="btn btn-secondary btn-sm" onclick="LDAPPages._editUserModal(${userJson})" ${!isAdmin ? 'disabled' : ''}>Edit</button>
                            <button class="btn btn-secondary btn-sm" onclick="LDAPPages._resetPasswordModal('${escHtml(u.uid)}')" ${!isAdmin ? 'disabled' : ''}>Reset PW</button>
                            <button class="btn btn-secondary btn-sm" onclick="LDAPPages._toggleLock('${escHtml(u.uid)}', ${!!u.locked})" ${!isAdmin ? 'disabled' : ''}>${u.locked ? 'Unlock' : 'Lock'}</button>
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
                            <th>UID</th><th>Full Name</th><th>UID Number</th><th>GID Number</th><th>Shell / Status</th><th>Actions</th>
                        </tr>
                    </thead>
                    <tbody>${rows}</tbody>
                </table>
            </div>`;
    },

    _createUserModal() {
        const existingModal = document.getElementById('ldap-create-user-modal');
        if (existingModal) existingModal.remove();
        const modal = document.createElement('div');
        modal.id = 'ldap-create-user-modal';
        modal.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.5);display:flex;align-items:center;justify-content:center;z-index:1000;';
        modal.innerHTML = `
            <div class="card" style="width:540px;max-width:95vw;max-height:90vh;overflow-y:auto;">
                <div class="card-header">
                    <span class="card-title">Create LDAP User</span>
                    <button class="btn btn-ghost btn-sm" onclick="document.getElementById('ldap-create-user-modal').remove()">×</button>
                </div>
                <div style="padding:16px;display:flex;flex-direction:column;gap:12px;">
                    <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px;">
                        <label class="form-label">UID (username)
                            <input id="lcu-uid" class="form-input" type="text" placeholder="jdoe" style="margin-top:4px;font-family:monospace;">
                        </label>
                        <label class="form-label">Full Name
                            <input id="lcu-fullname" class="form-input" type="text" placeholder="Jane Doe" style="margin-top:4px;">
                        </label>
                    </div>
                    <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px;">
                        <label class="form-label">UID Number
                            <input id="lcu-uidnum" class="form-input" type="number" placeholder="10001" min="1000" style="margin-top:4px;">
                        </label>
                        <label class="form-label">GID Number
                            <input id="lcu-gidnum" class="form-input" type="number" placeholder="10001" min="1000" style="margin-top:4px;">
                        </label>
                    </div>
                    <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px;">
                        <label class="form-label">Home Directory
                            <input id="lcu-home" class="form-input" type="text" placeholder="/home/jdoe" style="margin-top:4px;font-family:monospace;">
                        </label>
                        <label class="form-label">Shell
                            <input id="lcu-shell" class="form-input" type="text" value="/bin/bash" style="margin-top:4px;font-family:monospace;">
                        </label>
                    </div>
                    <label class="form-label">SSH Public Key (optional)
                        <textarea id="lcu-sshkey" class="form-input" rows="2" placeholder="ssh-ed25519 AAAA..." style="margin-top:4px;font-family:monospace;resize:vertical;"></textarea>
                    </label>
                    <label class="form-label">Initial Password
                        <input id="lcu-password" class="form-input" type="password" style="margin-top:4px;">
                    </label>
                    <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:8px;">
                        <button class="btn btn-secondary" onclick="document.getElementById('ldap-create-user-modal').remove()">Cancel</button>
                        <button class="btn btn-primary" onclick="LDAPPages._createUserSubmit()">Create</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(modal);
    },

    async _createUserSubmit() {
        const uid      = (document.getElementById('lcu-uid')      || {}).value || '';
        const fullName = (document.getElementById('lcu-fullname') || {}).value || '';
        const uidNum   = parseInt((document.getElementById('lcu-uidnum') || {}).value || '0', 10);
        const gidNum   = parseInt((document.getElementById('lcu-gidnum') || {}).value || '0', 10);
        const home     = (document.getElementById('lcu-home')     || {}).value || '';
        const shell    = (document.getElementById('lcu-shell')    || {}).value || '/bin/bash';
        const sshKey   = (document.getElementById('lcu-sshkey')   || {}).value || '';
        const password = (document.getElementById('lcu-password') || {}).value || '';

        if (!uid.trim())    { App.toast('UID is required', 'error'); return; }
        if (!uidNum)        { App.toast('UID Number is required', 'error'); return; }
        if (!gidNum)        { App.toast('GID Number is required', 'error'); return; }
        if (!password)      { App.toast('Initial password is required', 'error'); return; }

        const body = {
            uid:            uid.trim(),
            cn:             fullName || uid.trim(),
            uid_number:     uidNum,
            gid_number:     gidNum,
            home_directory: home || '/home/' + uid.trim(),
            login_shell:    shell,
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
            App.toast('Create failed: ' + err.message, 'error');
        }
    },

    _editUserModal(user) {
        const existingModal = document.getElementById('ldap-edit-user-modal');
        if (existingModal) existingModal.remove();
        const modal = document.createElement('div');
        modal.id = 'ldap-edit-user-modal';
        modal.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.5);display:flex;align-items:center;justify-content:center;z-index:1000;';
        modal.innerHTML = `
            <div class="card" style="width:520px;max-width:95vw;">
                <div class="card-header">
                    <span class="card-title">Edit User: ${escHtml(user.uid)}</span>
                    <button class="btn btn-ghost btn-sm" onclick="document.getElementById('ldap-edit-user-modal').remove()">×</button>
                </div>
                <div style="padding:16px;display:flex;flex-direction:column;gap:12px;">
                    <label class="form-label">Full Name (CN)
                        <input id="leu-cn" class="form-input" type="text" value="${escHtml(user.cn || '')}" style="margin-top:4px;">
                    </label>
                    <label class="form-label">Shell
                        <input id="leu-shell" class="form-input" type="text" value="${escHtml(user.login_shell || '/bin/bash')}" style="margin-top:4px;font-family:monospace;">
                    </label>
                    <label class="form-label">SSH Public Key
                        <textarea id="leu-sshkey" class="form-input" rows="2" style="margin-top:4px;font-family:monospace;resize:vertical;">${escHtml(user.ssh_public_key || '')}</textarea>
                    </label>
                    <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:8px;">
                        <button class="btn btn-secondary" onclick="document.getElementById('ldap-edit-user-modal').remove()">Cancel</button>
                        <button class="btn btn-primary" onclick="LDAPPages._editUserSubmit('${escHtml(user.uid)}')">Save</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(modal);
    },

    async _editUserSubmit(uid) {
        const cn     = (document.getElementById('leu-cn')     || {}).value || '';
        const shell  = (document.getElementById('leu-shell')  || {}).value || '';
        const sshKey = (document.getElementById('leu-sshkey') || {}).value || '';

        const body = { cn, login_shell: shell };
        if (sshKey.trim()) body.ssh_public_key = sshKey.trim();

        try {
            await API.ldap.updateUser(uid, body);
            const modal = document.getElementById('ldap-edit-user-modal');
            if (modal) modal.remove();
            App.toast('User updated', 'success');
            LDAPPages.users();
        } catch (err) {
            App.toast('Update failed: ' + err.message, 'error');
        }
    },

    _resetPasswordModal(uid) {
        const existingModal = document.getElementById('ldap-reset-pw-modal');
        if (existingModal) existingModal.remove();
        const modal = document.createElement('div');
        modal.id = 'ldap-reset-pw-modal';
        modal.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.5);display:flex;align-items:center;justify-content:center;z-index:1000;';
        modal.innerHTML = `
            <div class="card" style="width:400px;max-width:95vw;">
                <div class="card-header">
                    <span class="card-title">Reset Password: ${escHtml(uid)}</span>
                    <button class="btn btn-ghost btn-sm" onclick="document.getElementById('ldap-reset-pw-modal').remove()">×</button>
                </div>
                <div style="padding:16px;display:flex;flex-direction:column;gap:12px;">
                    <label class="form-label">New Password
                        <input id="lrp-password" class="form-input" type="password" style="margin-top:4px;" autofocus>
                    </label>
                    <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:8px;">
                        <button class="btn btn-secondary" onclick="document.getElementById('ldap-reset-pw-modal').remove()">Cancel</button>
                        <button class="btn btn-primary" onclick="LDAPPages._resetPasswordSubmit('${escHtml(uid)}')">Reset</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(modal);
    },

    async _resetPasswordSubmit(uid) {
        const password = (document.getElementById('lrp-password') || {}).value || '';
        if (!password) { App.toast('Password is required', 'error'); return; }
        try {
            await API.ldap.setPassword(uid, password);
            const modal = document.getElementById('ldap-reset-pw-modal');
            if (modal) modal.remove();
            App.toast('Password reset for ' + uid, 'success');
        } catch (err) {
            App.toast('Reset failed: ' + err.message, 'error');
        }
    },

    async _toggleLock(uid, currentlyLocked) {
        try {
            if (currentlyLocked) {
                await API.ldap.unlockUser(uid);
                App.toast('User unlocked: ' + uid, 'success');
            } else {
                await API.ldap.lockUser(uid);
                App.toast('User locked: ' + uid, 'success');
            }
            LDAPPages.users();
        } catch (err) {
            App.toast('Failed: ' + err.message, 'error');
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
                return `<tr style="cursor:pointer;" onclick="LDAPPages._groupDetailModal(${JSON.stringify(g).replace(/"/g, '&quot;')})">
                    <td class="text-mono text-sm">${escHtml(g.cn)}</td>
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

    _createGroupModal() {
        const existingModal = document.getElementById('ldap-create-group-modal');
        if (existingModal) existingModal.remove();
        const modal = document.createElement('div');
        modal.id = 'ldap-create-group-modal';
        modal.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.5);display:flex;align-items:center;justify-content:center;z-index:1000;';
        modal.innerHTML = `
            <div class="card" style="width:420px;max-width:95vw;">
                <div class="card-header">
                    <span class="card-title">Create LDAP Group</span>
                    <button class="btn btn-ghost btn-sm" onclick="document.getElementById('ldap-create-group-modal').remove()">×</button>
                </div>
                <div style="padding:16px;display:flex;flex-direction:column;gap:12px;">
                    <label class="form-label">Group Name (CN)
                        <input id="lcg-cn" class="form-input" type="text" placeholder="hpc-users" style="margin-top:4px;font-family:monospace;">
                    </label>
                    <label class="form-label">GID Number
                        <input id="lcg-gidnum" class="form-input" type="number" placeholder="10001" min="1000" style="margin-top:4px;">
                    </label>
                    <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:8px;">
                        <button class="btn btn-secondary" onclick="document.getElementById('ldap-create-group-modal').remove()">Cancel</button>
                        <button class="btn btn-primary" onclick="LDAPPages._createGroupSubmit()">Create</button>
                    </div>
                </div>
            </div>`;
        document.body.appendChild(modal);
    },

    async _createGroupSubmit() {
        const cn     = (document.getElementById('lcg-cn')     || {}).value || '';
        const gidNum = parseInt((document.getElementById('lcg-gidnum') || {}).value || '0', 10);
        if (!cn.trim())  { App.toast('Group name is required', 'error'); return; }
        if (!gidNum)     { App.toast('GID Number is required', 'error'); return; }
        try {
            await API.ldap.createGroup({ cn: cn.trim(), gid_number: gidNum });
            const modal = document.getElementById('ldap-create-group-modal');
            if (modal) modal.remove();
            App.toast('Group created: ' + cn.trim(), 'success');
            LDAPPages.groups();
        } catch (err) {
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

    // _groupDetailModal shows group members with add/remove controls.
    async _groupDetailModal(group) {
        const isAdmin = typeof Auth !== 'undefined' && Auth._role === 'admin';
        const existingModal = document.getElementById('ldap-group-detail-modal');
        if (existingModal) existingModal.remove();

        const members = group.member_uids || [];

        // Fetch user list for the UID picker.
        let allUsers = [];
        try {
            const resp = await API.ldap.listUsers();
            allUsers = (resp && resp.users) ? resp.users.map(u => u.uid) : [];
        } catch (_) {}

        const nonMembers = allUsers.filter(u => !members.includes(u));
        const memberRows = members.length === 0
            ? `<p style="color:var(--text-secondary);font-size:13px;">No members.</p>`
            : members.map(uid => `
                <div style="display:flex;align-items:center;gap:8px;padding:4px 0;border-bottom:1px solid var(--border);">
                    <span class="text-mono" style="flex:1;font-size:13px;">${escHtml(uid)}</span>
                    <button class="btn btn-danger btn-sm" onclick="LDAPPages._removeMember('${escHtml(group.cn)}','${escHtml(uid)}')" ${!isAdmin ? 'disabled' : ''}>Remove</button>
                </div>`).join('');

        const addRow = isAdmin && nonMembers.length > 0 ? `
            <div style="display:flex;gap:8px;margin-top:12px;">
                <select id="lgd-adduid" class="form-input" style="flex:1;">
                    <option value="">Select user…</option>
                    ${nonMembers.map(u => `<option value="${escHtml(u)}">${escHtml(u)}</option>`).join('')}
                </select>
                <button class="btn btn-primary" onclick="LDAPPages._addMember('${escHtml(group.cn)}')">Add</button>
            </div>` : '';

        const modal = document.createElement('div');
        modal.id = 'ldap-group-detail-modal';
        modal.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.5);display:flex;align-items:center;justify-content:center;z-index:1000;';
        modal.innerHTML = `
            <div class="card" style="width:480px;max-width:95vw;max-height:80vh;display:flex;flex-direction:column;">
                <div class="card-header">
                    <span class="card-title">Group: ${escHtml(group.cn)}</span>
                    <button class="btn btn-ghost btn-sm" onclick="document.getElementById('ldap-group-detail-modal').remove()">×</button>
                </div>
                <div style="padding:16px;overflow-y:auto;">
                    <p style="font-size:13px;color:var(--text-secondary);margin:0 0 12px;">GID Number: <span class="text-mono">${escHtml(String(group.gid_number || '—'))}</span></p>
                    <div id="lgd-members">${memberRows}</div>
                    ${addRow}
                </div>
            </div>`;
        document.body.appendChild(modal);
    },

    async _addMember(cn) {
        const uid = (document.getElementById('lgd-adduid') || {}).value || '';
        if (!uid) { App.toast('Select a user to add', 'error'); return; }
        try {
            await API.ldap.addMember(cn, uid);
            App.toast('Added ' + uid + ' to ' + cn, 'success');
            // Refresh the groups page and re-open the modal.
            const modal = document.getElementById('ldap-group-detail-modal');
            if (modal) modal.remove();
            // Re-fetch group and reopen.
            const resp = await API.ldap.listGroups();
            const groups = (resp && resp.groups) ? resp.groups : [];
            const updated = groups.find(g => g.cn === cn);
            if (updated) LDAPPages._groupDetailModal(updated);
        } catch (err) {
            App.toast('Add failed: ' + err.message, 'error');
        }
    },

    async _removeMember(cn, uid) {
        try {
            await API.ldap.removeMember(cn, uid);
            App.toast('Removed ' + uid + ' from ' + cn, 'success');
            const modal = document.getElementById('ldap-group-detail-modal');
            if (modal) modal.remove();
            const resp = await API.ldap.listGroups();
            const groups = (resp && resp.groups) ? resp.groups : [];
            const updated = groups.find(g => g.cn === cn);
            if (updated) LDAPPages._groupDetailModal(updated);
        } catch (err) {
            App.toast('Remove failed: ' + err.message, 'error');
        }
    },
};
