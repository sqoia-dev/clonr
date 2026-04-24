// sysaccounts.js — System Accounts module webui: local POSIX accounts and groups.
// Conventions mirror ldap.js: hash-based routing, App.render(), App.toast(),
// cardWrap(), emptyState(), badge(), escHtml(), fmtDate(), fmtRelative().
// API calls go through API.sysaccounts.* (defined in api.js).

// ─── SysAccountsPages namespace ───────────────────────────────────────────────

// Module-level cache for groups, used by account modals.
let _saGroupsCache = [];

const SysAccountsPages = {

    // ── Nav bootstrap ─────────────────────────────────────────────────────────

    // bootstrapNav fetches GET /api/v1/system/accounts to determine admin access.
    // On 403 (non-admin) the entire section is hidden.
    // On any 2xx (even empty list) the section is shown.
    async bootstrapNav() {
        const section = document.getElementById('nav-system-section');
        if (!section) return;
        try {
            await API.sysaccounts.listAccounts();
            section.style.display = '';
        } catch (_) {
            // 403 or network error — hide for non-admins.
            section.style.display = 'none';
        }
    },

    // ── Accounts page ─────────────────────────────────────────────────────────

    async accounts() {
        App.render(loading('Loading system accounts\u2026'));
        try {
            const [ar, gr] = await Promise.all([
                API.sysaccounts.listAccounts(),
                API.sysaccounts.listGroups(),
            ]);
            const accounts = (ar && ar.accounts) ? ar.accounts : [];
            const groups   = (gr && gr.groups)   ? gr.groups   : [];
            _saGroupsCache = groups;
            App.render(SysAccountsPages._accountsHtml(accounts, groups));
        } catch (err) {
            App.render(alertBox('Failed to load system accounts: ' + err.message));
        }
    },

    _accountsHtml(accounts, groups) {
        const isAdmin = typeof Auth !== 'undefined' && Auth._role === 'admin';

        const rows = accounts.length === 0
            ? `<tr><td colspan="7" style="text-align:center;color:var(--text-secondary);padding:24px">No system accounts defined. Create the first one.</td></tr>`
            : accounts.map(a => {
                const acctJson = escHtml(JSON.stringify(a));
                return `<tr>
                    <td class="text-mono text-sm">${escHtml(a.username)}${sysbage(a)}</td>
                    <td class="text-mono text-sm">${a.uid}</td>
                    <td class="text-mono text-sm">${a.primary_gid}</td>
                    <td class="text-mono text-sm">${escHtml(a.shell)}</td>
                    <td class="text-mono text-sm">${escHtml(a.home_dir)}</td>
                    <td style="color:var(--text-secondary);font-size:13px;">${escHtml(a.comment || '\u2014')}</td>
                    <td>
                        <div style="display:flex;gap:6px;flex-wrap:wrap;">
                            <button class="btn btn-secondary btn-sm" onclick="SysAccountsPages._editAccountModal(${acctJson},_saGroupsCache)" ${!isAdmin ? 'disabled' : ''}>Edit</button>
                            <button class="btn btn-danger btn-sm" onclick="SysAccountsPages._deleteAccount('${escHtml(a.id)}','${escHtml(a.username)}')" ${!isAdmin ? 'disabled' : ''}>Delete</button>
                        </div>
                    </td>
                </tr>`;
            }).join('');

        return `
            <div class="page-header">
                <div>
                    <h1 class="page-title">System Accounts</h1>
                    <div class="page-subtitle">Local POSIX accounts injected into every deployed node at reimage time. Changes take effect on next reimage.</div>
                </div>
                <div style="display:flex;gap:8px;">
                    <button class="btn btn-primary" onclick="SysAccountsPages._createAccountModal(_saGroupsCache)" ${!isAdmin ? 'disabled' : ''}>+ Create Account</button>
                </div>
            </div>
            <div class="alert" style="background:#fef3c7;color:#92400e;border:1px solid #fbbf24;border-radius:6px;padding:10px 14px;margin-bottom:16px;font-size:13px;">
                Ensure UIDs defined here do not overlap with UIDs in your LDAP directory. Accounts are injected via <code>useradd</code> in the deployed OS chroot.
            </div>
            <div class="card">
                <table class="table">
                    <thead>
                        <tr>
                            <th>Username</th><th>UID</th><th>Primary GID</th><th>Shell</th><th>Home Dir</th><th>Comment</th><th>Actions</th>
                        </tr>
                    </thead>
                    <tbody>${rows}</tbody>
                </table>
            </div>`;
    },

    // ── Groups page ───────────────────────────────────────────────────────────

    async groups() {
        App.render(loading('Loading system groups\u2026'));
        try {
            const gr = await API.sysaccounts.listGroups();
            const groups = (gr && gr.groups) ? gr.groups : [];
            App.render(SysAccountsPages._groupsHtml(groups));
        } catch (err) {
            App.render(alertBox('Failed to load system groups: ' + err.message));
        }
    },

    _groupsHtml(groups) {
        const isAdmin = typeof Auth !== 'undefined' && Auth._role === 'admin';

        const rows = groups.length === 0
            ? `<tr><td colspan="4" style="text-align:center;color:var(--text-secondary);padding:24px">No system groups defined. Create the first one.</td></tr>`
            : groups.map(g => {
                const gJson = escHtml(JSON.stringify(g));
                return `<tr>
                    <td class="text-mono text-sm">${escHtml(g.name)}</td>
                    <td class="text-mono text-sm">${g.gid}</td>
                    <td style="color:var(--text-secondary);font-size:13px;">${escHtml(g.description || '\u2014')}</td>
                    <td>
                        <div style="display:flex;gap:6px;flex-wrap:wrap;">
                            <button class="btn btn-secondary btn-sm" onclick="SysAccountsPages._editGroupModal(${gJson})">Edit</button>
                            <button class="btn btn-danger btn-sm" onclick="SysAccountsPages._deleteGroup('${escHtml(g.id)}','${escHtml(g.name)}')">Delete</button>
                        </div>
                    </td>
                </tr>`;
            }).join('');

        return `
            <div class="page-header">
                <div>
                    <h1 class="page-title">System Groups</h1>
                    <div class="page-subtitle">Local POSIX groups injected into every deployed node at reimage time. Changes take effect on next reimage.</div>
                </div>
                <div style="display:flex;gap:8px;">
                    <button class="btn btn-primary" onclick="SysAccountsPages._createGroupModal()" ${!isAdmin ? 'disabled' : ''}>+ Create Group</button>
                </div>
            </div>
            <div class="card">
                <table class="table">
                    <thead>
                        <tr>
                            <th>Name</th><th>GID</th><th>Description</th><th>Actions</th>
                        </tr>
                    </thead>
                    <tbody>${rows}</tbody>
                </table>
            </div>`;
    },

    // ── Group modals ──────────────────────────────────────────────────────────

    _createGroupModal() {
        const existing = document.getElementById('sa-group-modal');
        if (existing) existing.remove();

        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = 'sa-group-modal';
        overlay.innerHTML = `
            <div class="modal">
                <div class="modal-header">
                    <span class="modal-title">Create System Group</span>
                    <button class="modal-close" onclick="document.getElementById('sa-group-modal').remove()">\xd7</button>
                </div>
                <div class="modal-body">
                    <div class="form-group">
                        <label>Name <span style="color:var(--error)">*</span></label>
                        <input id="sa-group-name" type="text" placeholder="munge" style="font-family:var(--font-mono);">
                        <div class="form-hint">Lowercase, max 32 chars, pattern: <code>^[a-z_][a-z0-9_-]*$</code></div>
                    </div>
                    <div class="form-group">
                        <label>GID <span style="color:var(--error)">*</span></label>
                        <input id="sa-group-gid" type="number" placeholder="1002" min="1" max="65534">
                        <div class="form-hint">Must be unique, range 1\u201365534. Treat as immutable once nodes are deployed.</div>
                    </div>
                    <div class="form-group">
                        <label>Description</label>
                        <input id="sa-group-desc" type="text" placeholder="Optional description">
                    </div>
                    <div id="sa-group-error" style="color:var(--error);font-size:13px;display:none;margin-top:8px;"></div>
                </div>
                <div class="modal-footer">
                    <button class="btn btn-secondary" onclick="document.getElementById('sa-group-modal').remove()">Cancel</button>
                    <button class="btn btn-primary" onclick="SysAccountsPages._submitCreateGroup()">Create Group</button>
                </div>
            </div>`;
        document.body.appendChild(overlay);
        document.getElementById('sa-group-name').focus();
    },

    async _submitCreateGroup() {
        const name = (document.getElementById('sa-group-name').value || '').trim();
        const gid  = parseInt(document.getElementById('sa-group-gid').value, 10);
        const desc = (document.getElementById('sa-group-desc').value || '').trim();
        const errEl = document.getElementById('sa-group-error');
        errEl.style.display = 'none';

        if (!name) { errEl.textContent = 'Name is required.'; errEl.style.display = ''; return; }
        if (!gid || isNaN(gid)) { errEl.textContent = 'GID is required.'; errEl.style.display = ''; return; }

        try {
            await API.sysaccounts.createGroup({ name, gid, description: desc });
            document.getElementById('sa-group-modal').remove();
            App.toast('Group created', 'success');
            SysAccountsPages.groups();
        } catch (err) {
            errEl.textContent = err.message;
            errEl.style.display = '';
        }
    },

    _editGroupModal(g) {
        const existing = document.getElementById('sa-group-modal');
        if (existing) existing.remove();

        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = 'sa-group-modal';
        overlay.dataset.groupId = g.id;
        overlay.innerHTML = `
            <div class="modal">
                <div class="modal-header">
                    <span class="modal-title">Edit Group: ${escHtml(g.name)}</span>
                    <button class="modal-close" onclick="document.getElementById('sa-group-modal').remove()">\xd7</button>
                </div>
                <div class="modal-body">
                    <div class="form-group">
                        <label>Name <span style="color:var(--error)">*</span></label>
                        <input id="sa-group-name" type="text" value="${escHtml(g.name)}" style="font-family:var(--font-mono);">
                    </div>
                    <div class="form-group">
                        <label>GID <span style="color:var(--error)">*</span></label>
                        <input id="sa-group-gid" type="number" value="${g.gid}" min="1" max="65534">
                        <div class="form-hint">Changing GID on an already-deployed cluster causes file ownership drift. Treat as immutable.</div>
                    </div>
                    <div class="form-group">
                        <label>Description</label>
                        <input id="sa-group-desc" type="text" value="${escHtml(g.description || '')}">
                    </div>
                    <div id="sa-group-error" style="color:var(--error);font-size:13px;display:none;margin-top:8px;"></div>
                </div>
                <div class="modal-footer">
                    <button class="btn btn-secondary" onclick="document.getElementById('sa-group-modal').remove()">Cancel</button>
                    <button class="btn btn-primary" onclick="SysAccountsPages._submitUpdateGroup()">Save Changes</button>
                </div>
            </div>`;
        document.body.appendChild(overlay);
    },

    async _submitUpdateGroup() {
        const overlay = document.getElementById('sa-group-modal');
        const id   = overlay.dataset.groupId;
        const name = (document.getElementById('sa-group-name').value || '').trim();
        const gid  = parseInt(document.getElementById('sa-group-gid').value, 10);
        const desc = (document.getElementById('sa-group-desc').value || '').trim();
        const errEl = document.getElementById('sa-group-error');
        errEl.style.display = 'none';

        if (!name) { errEl.textContent = 'Name is required.'; errEl.style.display = ''; return; }
        if (!gid || isNaN(gid)) { errEl.textContent = 'GID is required.'; errEl.style.display = ''; return; }

        try {
            await API.sysaccounts.updateGroup(id, { name, gid, description: desc });
            overlay.remove();
            App.toast('Group updated', 'success');
            SysAccountsPages.groups();
        } catch (err) {
            errEl.textContent = err.message;
            errEl.style.display = '';
        }
    },

    async _deleteGroup(id, name) {
        if (!confirm(`Delete group "${name}"?\n\nAlready-deployed nodes retain this group. Re-image to remove it from nodes.`)) return;
        try {
            await API.sysaccounts.deleteGroup(id);
            App.toast('Group deleted', 'success');
            SysAccountsPages.groups();
        } catch (err) {
            App.toast('Delete failed: ' + err.message, 'error');
        }
    },

    // ── Account modals ────────────────────────────────────────────────────────

    _groupOptions(groups, selectedGid) {
        if (!groups || groups.length === 0) return '<option value="">— no groups defined —</option>';
        return groups.map(g =>
            `<option value="${g.gid}" ${g.gid === selectedGid ? 'selected' : ''}>${escHtml(g.name)} (${g.gid})</option>`
        ).join('');
    },

    _createAccountModal(groups) {
        const existing = document.getElementById('sa-account-modal');
        if (existing) existing.remove();

        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = 'sa-account-modal';
        overlay.innerHTML = `
            <div class="modal modal-wide">
                <div class="modal-header">
                    <span class="modal-title">Create System Account</span>
                    <button class="modal-close" onclick="document.getElementById('sa-account-modal').remove()">\xd7</button>
                </div>
                <div class="modal-body">
                    <div class="form-grid">
                        <div class="form-group">
                            <label>Username <span style="color:var(--error)">*</span></label>
                            <input id="sa-acct-username" type="text" placeholder="munge" style="font-family:var(--font-mono);">
                            <div class="form-hint">Lowercase, max 32 chars, pattern: <code>^[a-z_][a-z0-9_-]*$</code></div>
                        </div>
                        <div class="form-group">
                            <label>UID <span style="color:var(--error)">*</span></label>
                            <input id="sa-acct-uid" type="number" placeholder="1002" min="1" max="65534">
                            <div class="form-hint">Range 1\u201365534. Treat as immutable once nodes are deployed.</div>
                        </div>
                    </div>
                    <div class="form-grid">
                        <div class="form-group">
                            <label>Primary GID <span style="color:var(--error)">*</span></label>
                            ${groups && groups.length > 0
                                ? `<select id="sa-acct-gid">${SysAccountsPages._groupOptions(groups, 0)}</select>`
                                : `<input id="sa-acct-gid" type="number" placeholder="1002" min="1" max="65534">`
                            }
                            <div class="form-hint">May reference a GID baked into the base image (not just defined above).</div>
                        </div>
                        <div class="form-group">
                            <label>Shell</label>
                            <input id="sa-acct-shell" type="text" value="/sbin/nologin" style="font-family:var(--font-mono);">
                            <div class="form-hint">Must be an absolute path. Default: <code>/sbin/nologin</code></div>
                        </div>
                    </div>
                    <div class="form-grid">
                        <div class="form-group">
                            <label>Home Directory</label>
                            <input id="sa-acct-home" type="text" value="/dev/null" style="font-family:var(--font-mono);">
                            <div class="form-hint">Must be an absolute path. Default: <code>/dev/null</code></div>
                        </div>
                        <div class="form-group">
                            <label>Comment</label>
                            <input id="sa-acct-comment" type="text" placeholder="Optional GECOS comment">
                        </div>
                    </div>
                    <div class="form-grid">
                        <div class="form-group">
                            <label style="display:flex;align-items:center;gap:8px;cursor:pointer;">
                                <input id="sa-acct-create-home" type="checkbox">
                                Create home directory
                            </label>
                            <div class="form-hint">When checked, <code>useradd --create-home</code> is used. Most service accounts leave this off.</div>
                        </div>
                        <div class="form-group">
                            <label style="display:flex;align-items:center;gap:8px;cursor:pointer;">
                                <input id="sa-acct-system" type="checkbox" checked>
                                System account (<code>--system</code>)
                            </label>
                            <div class="form-hint">Passes <code>--system</code> to <code>useradd</code>. Conventional for service accounts.</div>
                        </div>
                    </div>
                    <div id="sa-acct-error" style="color:var(--error);font-size:13px;display:none;margin-top:8px;"></div>
                </div>
                <div class="modal-footer">
                    <button class="btn btn-secondary" onclick="document.getElementById('sa-account-modal').remove()">Cancel</button>
                    <button class="btn btn-primary" onclick="SysAccountsPages._submitCreateAccount()">Create Account</button>
                </div>
            </div>`;
        document.body.appendChild(overlay);
        document.getElementById('sa-acct-username').focus();
    },

    async _submitCreateAccount() {
        const username    = (document.getElementById('sa-acct-username').value || '').trim();
        const uid         = parseInt(document.getElementById('sa-acct-uid').value, 10);
        const gidRaw      = document.getElementById('sa-acct-gid').value;
        const primary_gid = parseInt(gidRaw, 10);
        const shell       = (document.getElementById('sa-acct-shell').value || '/sbin/nologin').trim();
        const home_dir    = (document.getElementById('sa-acct-home').value || '/dev/null').trim();
        const comment     = (document.getElementById('sa-acct-comment').value || '').trim();
        const create_home   = document.getElementById('sa-acct-create-home').checked;
        const system_account = document.getElementById('sa-acct-system').checked;
        const errEl = document.getElementById('sa-acct-error');
        errEl.style.display = 'none';

        if (!username) { errEl.textContent = 'Username is required.'; errEl.style.display = ''; return; }
        if (!uid || isNaN(uid)) { errEl.textContent = 'UID is required.'; errEl.style.display = ''; return; }
        if (!primary_gid || isNaN(primary_gid)) { errEl.textContent = 'Primary GID is required.'; errEl.style.display = ''; return; }

        try {
            await API.sysaccounts.createAccount({
                username, uid, primary_gid, shell, home_dir,
                create_home, system_account, comment,
            });
            document.getElementById('sa-account-modal').remove();
            App.toast('Account created', 'success');
            SysAccountsPages.accounts();
        } catch (err) {
            errEl.textContent = err.message;
            errEl.style.display = '';
        }
    },

    _editAccountModal(a, groups) {
        const existing = document.getElementById('sa-account-modal');
        if (existing) existing.remove();

        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = 'sa-account-modal';
        overlay.dataset.accountId = a.id;
        overlay.innerHTML = `
            <div class="modal modal-wide">
                <div class="modal-header">
                    <span class="modal-title">Edit Account: ${escHtml(a.username)}</span>
                    <button class="modal-close" onclick="document.getElementById('sa-account-modal').remove()">\xd7</button>
                </div>
                <div class="modal-body">
                    <div class="form-grid">
                        <div class="form-group">
                            <label>Username <span style="color:var(--error)">*</span></label>
                            <input id="sa-acct-username" type="text" value="${escHtml(a.username)}" style="font-family:var(--font-mono);">
                        </div>
                        <div class="form-group">
                            <label>UID <span style="color:var(--error)">*</span></label>
                            <input id="sa-acct-uid" type="number" value="${a.uid}" min="1" max="65534">
                            <div class="form-hint">Changing UID on a deployed cluster causes file ownership drift. Treat as immutable.</div>
                        </div>
                    </div>
                    <div class="form-grid">
                        <div class="form-group">
                            <label>Primary GID <span style="color:var(--error)">*</span></label>
                            ${groups && groups.length > 0
                                ? `<select id="sa-acct-gid">${SysAccountsPages._groupOptions(groups, a.primary_gid)}</select>`
                                : `<input id="sa-acct-gid" type="number" value="${a.primary_gid}" min="1" max="65534">`
                            }
                        </div>
                        <div class="form-group">
                            <label>Shell</label>
                            <input id="sa-acct-shell" type="text" value="${escHtml(a.shell)}" style="font-family:var(--font-mono);">
                        </div>
                    </div>
                    <div class="form-grid">
                        <div class="form-group">
                            <label>Home Directory</label>
                            <input id="sa-acct-home" type="text" value="${escHtml(a.home_dir)}" style="font-family:var(--font-mono);">
                        </div>
                        <div class="form-group">
                            <label>Comment</label>
                            <input id="sa-acct-comment" type="text" value="${escHtml(a.comment || '')}">
                        </div>
                    </div>
                    <div class="form-grid">
                        <div class="form-group">
                            <label style="display:flex;align-items:center;gap:8px;cursor:pointer;">
                                <input id="sa-acct-create-home" type="checkbox" ${a.create_home ? 'checked' : ''}>
                                Create home directory
                            </label>
                        </div>
                        <div class="form-group">
                            <label style="display:flex;align-items:center;gap:8px;cursor:pointer;">
                                <input id="sa-acct-system" type="checkbox" ${a.system_account ? 'checked' : ''}>
                                System account (<code>--system</code>)
                            </label>
                        </div>
                    </div>
                    <div id="sa-acct-error" style="color:var(--error);font-size:13px;display:none;margin-top:8px;"></div>
                </div>
                <div class="modal-footer">
                    <button class="btn btn-secondary" onclick="document.getElementById('sa-account-modal').remove()">Cancel</button>
                    <button class="btn btn-primary" onclick="SysAccountsPages._submitUpdateAccount()">Save Changes</button>
                </div>
            </div>`;
        document.body.appendChild(overlay);
    },

    async _submitUpdateAccount() {
        const overlay     = document.getElementById('sa-account-modal');
        const id          = overlay.dataset.accountId;
        const username    = (document.getElementById('sa-acct-username').value || '').trim();
        const uid         = parseInt(document.getElementById('sa-acct-uid').value, 10);
        const primary_gid = parseInt(document.getElementById('sa-acct-gid').value, 10);
        const shell       = (document.getElementById('sa-acct-shell').value || '/sbin/nologin').trim();
        const home_dir    = (document.getElementById('sa-acct-home').value || '/dev/null').trim();
        const comment     = (document.getElementById('sa-acct-comment').value || '').trim();
        const create_home   = document.getElementById('sa-acct-create-home').checked;
        const system_account = document.getElementById('sa-acct-system').checked;
        const errEl = document.getElementById('sa-acct-error');
        errEl.style.display = 'none';

        if (!username) { errEl.textContent = 'Username is required.'; errEl.style.display = ''; return; }
        if (!uid || isNaN(uid)) { errEl.textContent = 'UID is required.'; errEl.style.display = ''; return; }
        if (!primary_gid || isNaN(primary_gid)) { errEl.textContent = 'Primary GID is required.'; errEl.style.display = ''; return; }

        try {
            await API.sysaccounts.updateAccount(id, {
                username, uid, primary_gid, shell, home_dir,
                create_home, system_account, comment,
            });
            overlay.remove();
            App.toast('Account updated', 'success');
            SysAccountsPages.accounts();
        } catch (err) {
            errEl.textContent = err.message;
            errEl.style.display = '';
        }
    },

    async _deleteAccount(id, username) {
        if (!confirm(`Delete account "${username}"?\n\nAlready-deployed nodes retain this account. Re-image to remove it from nodes.`)) return;
        try {
            await API.sysaccounts.deleteAccount(id);
            App.toast('Account deleted', 'success');
            SysAccountsPages.accounts();
        } catch (err) {
            App.toast('Delete failed: ' + err.message, 'error');
        }
    },
};

// ── Helper: system badge inline ────────────────────────────────────────────────
function sysbage(a) {
    return a.system_account
        ? ' <span class="badge badge-neutral badge-sm">sys</span>'
        : '';
}
