// network.js — Network module webui: switch registry, network profiles, IB/OpenSM.
// Conventions mirror sysaccounts.js: hash-based routing, App.render(), App.toast(),
// escHtml(), badge(), loading(), alertBox(). API calls go through API.network.*.

// ─── NetworkPages namespace ────────────────────────────────────────────────────

// Module-level caches used across pages and modals.
let _netProfilesCache = [];
let _netSwitchesCache = [];

const NetworkPages = {

    // ── Nav bootstrap ──────────────────────────────────────────────────────────

    // bootstrapNav probes GET /api/v1/network/switches.
    // On 403 (non-admin) the section is hidden. Any 2xx shows it.
    async bootstrapNav() {
        const section = document.getElementById('nav-network-section');
        if (!section) return;
        try {
            await API.network.listSwitches();
            section.style.display = '';
        } catch (_) {
            section.style.display = 'none';
        }
    },

    // ── Switches page ──────────────────────────────────────────────────────────

    async switches() {
        App.render(loading('Loading switches\u2026'));
        try {
            const r = await API.network.listSwitches();
            const switches = (r && r.switches) ? r.switches : [];
            _netSwitchesCache = switches;
            App.render(NetworkPages._switchesHtml(switches));
        } catch (err) {
            App.render(alertBox('Failed to load switches: ' + err.message));
        }
    },

    _switchesHtml(switches) {
        const hasUnmanagedIB = switches.some(s => s.role === 'infiniband' && !s.is_managed);

        const warningBanner = hasUnmanagedIB ? `
            <div style="background:#fef3c7;color:#92400e;border:1px solid #fbbf24;border-radius:6px;padding:10px 14px;margin-bottom:16px;font-size:13px;">
                Unmanaged InfiniBand switch detected &mdash; OpenSM must run on a cluster host.
                Configure it on the <a href="#/network/profiles" style="color:#92400e;font-weight:600;">Profiles</a> page under OpenSM Settings.
            </div>` : '';

        const rows = switches.length === 0
            ? `<tr><td colspan="7" style="text-align:center;color:var(--text-secondary);padding:24px">No switches defined. Register the first one.</td></tr>`
            : switches.map(s => {
                const sJson = escHtml(JSON.stringify(s));
                return `<tr>
                    <td class="text-mono text-sm" style="font-weight:500">${escHtml(s.name)}</td>
                    <td>${NetworkPages._roleBadge(s.role)}</td>
                    <td style="color:var(--text-secondary);font-size:13px;">${escHtml(s.vendor || '\u2014')}</td>
                    <td style="color:var(--text-secondary);font-size:13px;">${escHtml(s.model || '\u2014')}</td>
                    <td class="text-mono text-sm">${escHtml(s.mgmt_ip || '\u2014')}</td>
                    <td>${NetworkPages._managedCell(s)}</td>
                    <td>
                        <div style="display:flex;gap:6px;flex-wrap:wrap;">
                            <button class="btn btn-secondary btn-sm" onclick="NetworkPages._editSwitchModal(${sJson})">Edit</button>
                            <button class="btn btn-danger btn-sm" onclick="NetworkPages._deleteSwitch('${escHtml(s.id)}','${escHtml(s.name)}')">Delete</button>
                        </div>
                    </td>
                </tr>`;
            }).join('');

        return `
            <div class="page-header">
                <div>
                    <div class="page-title">Switches</div>
                    <div class="page-subtitle">Switch inventory for the cluster fabric. Inventory-only in v1 &mdash; clonr does not program switches via SNMP.</div>
                </div>
                <div style="display:flex;gap:8px;">
                    <button class="btn btn-primary" onclick="NetworkPages._createSwitchModal()">+ Add Switch</button>
                </div>
            </div>
            ${warningBanner}
            <div class="card">
                <table class="table">
                    <thead>
                        <tr>
                            <th>Name</th><th>Role</th><th>Vendor</th><th>Model</th><th>Mgmt IP</th><th>Managed</th><th>Actions</th>
                        </tr>
                    </thead>
                    <tbody>${rows}</tbody>
                </table>
            </div>`;
    },

    _roleBadge(role) {
        const map = {
            management: ['badge-info',    'Management'],
            data:        ['badge-ready',   'Data'],
            infiniband:  ['badge-neutral', 'InfiniBand'],
        };
        const [cls, label] = map[role] || ['badge-neutral', role];
        return `<span class="badge ${cls}" style="${role === 'infiniband' ? 'background:#ede9fe;color:#5b21b6;' : ''}">${label}</span>`;
    },

    _managedCell(s) {
        if (s.role !== 'infiniband') return `<span style="color:var(--text-secondary);font-size:13px;">\u2014</span>`;
        return s.is_managed
            ? `<span style="color:#10b981;font-size:16px;" title="Has built-in SM">&#10003;</span>`
            : `<span style="color:#f59e0b;font-size:16px;" title="No built-in SM \u2014 OpenSM required">&#9888;</span>`;
    },

    // ── Switch modals ──────────────────────────────────────────────────────────

    _switchModalHtml(title, s) {
        const v = s || {};
        const role = v.role || 'management';
        const ibChecked = (v.is_managed !== false) ? 'checked' : '';
        return `
            <div class="modal">
                <div class="modal-header">
                    <span class="modal-title">${title}</span>
                    <button class="modal-close" onclick="document.getElementById('net-switch-modal').remove()">&times;</button>
                </div>
                <div class="modal-body">
                    <div class="form-grid">
                        <div class="form-group">
                            <label>Name <span style="color:var(--error)">*</span></label>
                            <input id="ns-name" type="text" value="${escHtml(v.name || '')}" placeholder="mgmt-sw-01" style="font-family:var(--font-mono);">
                            <div class="form-hint">Pattern: <code>^[a-zA-Z0-9._-]+$</code>, max 64 chars.</div>
                        </div>
                        <div class="form-group">
                            <label>Role <span style="color:var(--error)">*</span></label>
                            <select id="ns-role" onchange="NetworkPages._onSwitchRoleChange()">
                                <option value="management" ${role === 'management' ? 'selected' : ''}>Management</option>
                                <option value="data"       ${role === 'data'       ? 'selected' : ''}>Data</option>
                                <option value="infiniband" ${role === 'infiniband' ? 'selected' : ''}>InfiniBand</option>
                            </select>
                        </div>
                    </div>
                    <div class="form-grid">
                        <div class="form-group">
                            <label>Vendor</label>
                            <input id="ns-vendor" type="text" value="${escHtml(v.vendor || '')}" placeholder="Mellanox, Cisco, Arista\u2026">
                        </div>
                        <div class="form-group">
                            <label>Model</label>
                            <input id="ns-model" type="text" value="${escHtml(v.model || '')}" placeholder="SX6036, 9348GC-FXP\u2026">
                        </div>
                    </div>
                    <div class="form-group">
                        <label>Management IP</label>
                        <input id="ns-mgmt-ip" type="text" value="${escHtml(v.mgmt_ip || '')}" placeholder="10.0.0.254" style="font-family:var(--font-mono);">
                    </div>
                    <div class="form-group">
                        <label>Notes</label>
                        <textarea id="ns-notes" rows="2" placeholder="VLAN ranges, port mappings, admin notes\u2026" style="resize:vertical;">${escHtml(v.notes || '')}</textarea>
                    </div>
                    <div id="ns-ib-row" class="form-group" style="display:${role === 'infiniband' ? '' : 'none'};">
                        <label style="display:flex;align-items:center;gap:8px;cursor:pointer;">
                            <input id="ns-is-managed" type="checkbox" ${ibChecked}>
                            Has built-in Subnet Manager (SM)
                        </label>
                        <div class="form-hint">Uncheck if this is a dumb IB switch without an embedded SM. OpenSM must then run on a cluster host.</div>
                    </div>
                    <div id="ns-error" style="color:var(--error);font-size:13px;display:none;margin-top:8px;"></div>
                </div>
                <div class="modal-footer">
                    <button class="btn btn-secondary" onclick="document.getElementById('net-switch-modal').remove()">Cancel</button>
                    <button class="btn btn-primary" id="ns-submit-btn">${s ? 'Save Changes' : 'Add Switch'}</button>
                </div>
            </div>`;
    },

    _onSwitchRoleChange() {
        const role = document.getElementById('ns-role').value;
        const ibRow = document.getElementById('ns-ib-row');
        if (ibRow) ibRow.style.display = role === 'infiniband' ? '' : 'none';
    },

    _createSwitchModal() {
        const existing = document.getElementById('net-switch-modal');
        if (existing) existing.remove();
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = 'net-switch-modal';
        overlay.innerHTML = NetworkPages._switchModalHtml('Add Switch', null);
        document.body.appendChild(overlay);
        document.getElementById('ns-submit-btn').onclick = () => NetworkPages._submitCreateSwitch();
        document.getElementById('ns-name').focus();
    },

    _editSwitchModal(s) {
        const existing = document.getElementById('net-switch-modal');
        if (existing) existing.remove();
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = 'net-switch-modal';
        overlay.dataset.switchId = s.id;
        overlay.innerHTML = NetworkPages._switchModalHtml('Edit Switch: ' + escHtml(s.name), s);
        document.body.appendChild(overlay);
        document.getElementById('ns-submit-btn').onclick = () => NetworkPages._submitUpdateSwitch();
    },

    _readSwitchForm() {
        return {
            name:       (document.getElementById('ns-name').value || '').trim(),
            role:       document.getElementById('ns-role').value,
            vendor:     (document.getElementById('ns-vendor').value || '').trim(),
            model:      (document.getElementById('ns-model').value || '').trim(),
            mgmt_ip:    (document.getElementById('ns-mgmt-ip').value || '').trim(),
            notes:      (document.getElementById('ns-notes').value || '').trim(),
            is_managed: document.getElementById('ns-is-managed') ? document.getElementById('ns-is-managed').checked : true,
        };
    },

    _validateSwitchForm(data, errEl) {
        if (!data.name) { errEl.textContent = 'Name is required.'; errEl.style.display = ''; return false; }
        if (!data.role) { errEl.textContent = 'Role is required.'; errEl.style.display = ''; return false; }
        return true;
    },

    async _submitCreateSwitch() {
        const data  = NetworkPages._readSwitchForm();
        const errEl = document.getElementById('ns-error');
        errEl.style.display = 'none';
        if (!NetworkPages._validateSwitchForm(data, errEl)) return;
        try {
            await API.network.createSwitch(data);
            document.getElementById('net-switch-modal').remove();
            App.toast('Switch added', 'success');
            NetworkPages.switches();
        } catch (err) {
            errEl.textContent = err.message;
            errEl.style.display = '';
        }
    },

    async _submitUpdateSwitch() {
        const overlay = document.getElementById('net-switch-modal');
        const id      = overlay.dataset.switchId;
        const data    = NetworkPages._readSwitchForm();
        const errEl   = document.getElementById('ns-error');
        errEl.style.display = 'none';
        if (!NetworkPages._validateSwitchForm(data, errEl)) return;
        try {
            await API.network.updateSwitch(id, data);
            overlay.remove();
            App.toast('Switch updated', 'success');
            NetworkPages.switches();
        } catch (err) {
            errEl.textContent = err.message;
            errEl.style.display = '';
        }
    },

    async _deleteSwitch(id, name) {
        if (!confirm(`Delete switch "${name}"?\n\nThis only removes the inventory record.`)) return;
        try {
            await API.network.deleteSwitch(id);
            App.toast('Switch deleted', 'success');
            NetworkPages.switches();
        } catch (err) {
            App.toast('Delete failed: ' + err.message, 'error');
        }
    },

    // ── Profiles page ──────────────────────────────────────────────────────────

    async profiles() {
        App.render(loading('Loading network profiles\u2026'));
        try {
            const [pr, ibr] = await Promise.all([
                API.network.listProfiles(),
                API.network.getIBStatus().catch(() => null),
            ]);
            const profiles  = (pr && pr.profiles) ? pr.profiles : [];
            _netProfilesCache = profiles;
            App.render(NetworkPages._profilesHtml(profiles, ibr));
        } catch (err) {
            App.render(alertBox('Failed to load network profiles: ' + err.message));
        }
    },

    _assignedGroupsText(p) {
        if (!p.assigned_groups || p.assigned_groups.length === 0) return '\u2014';
        return p.assigned_groups.map(g => escHtml(g)).join(', ');
    },

    _profilesHtml(profiles, ibStatus) {
        const rows = profiles.length === 0
            ? `<tr><td colspan="6" style="text-align:center;color:var(--text-secondary);padding:24px">No network profiles defined. Create the first one.</td></tr>`
            : profiles.map(p => {
                const pJson = escHtml(JSON.stringify(p));
                const bondCount  = (p.bonds || []).length;
                const hasIB      = !!p.ib;
                return `<tr>
                    <td style="font-weight:500;">${escHtml(p.name)}</td>
                    <td style="color:var(--text-secondary);font-size:13px;">${escHtml(p.description || '\u2014')}</td>
                    <td style="text-align:center;">${bondCount}</td>
                    <td style="text-align:center;">${hasIB ? '<span style="color:#10b981;">&#10003;</span>' : '<span style="color:var(--text-secondary);">&#8212;</span>'}</td>
                    <td style="color:var(--text-secondary);font-size:13px;">${NetworkPages._assignedGroupsText(p)}</td>
                    <td>
                        <div style="display:flex;gap:6px;flex-wrap:wrap;">
                            <button class="btn btn-secondary btn-sm" onclick="NetworkPages._editProfileModal(${pJson})">Edit</button>
                            <button class="btn btn-danger btn-sm" onclick="NetworkPages._deleteProfile('${escHtml(p.id)}','${escHtml(p.name)}')">Delete</button>
                        </div>
                    </td>
                </tr>`;
            }).join('');

        const ibSection = NetworkPages._opensmSectionHtml(profiles, ibStatus);

        return `
            <div class="page-header">
                <div>
                    <div class="page-title">Network Profiles</div>
                    <div class="page-subtitle">Bond and IPoIB configurations assigned to node groups. Changes take effect on next reimage.</div>
                </div>
                <div style="display:flex;gap:8px;">
                    <button class="btn btn-primary" onclick="NetworkPages._createProfileModal()">+ Add Profile</button>
                </div>
            </div>
            <div class="card" style="margin-bottom:24px;">
                <table class="table">
                    <thead>
                        <tr>
                            <th>Name</th><th>Description</th><th style="text-align:center;">Bonds</th><th style="text-align:center;">Has IB</th><th>Assigned Groups</th><th>Actions</th>
                        </tr>
                    </thead>
                    <tbody>${rows}</tbody>
                </table>
            </div>
            ${ibSection}`;
    },

    // ── OpenSM settings section ────────────────────────────────────────────────

    _opensmSectionHtml(profiles, ibStatus) {
        const status = ibStatus || {};
        const profileOptions = profiles.map(p =>
            `<option value="${escHtml(p.id)}">${escHtml(p.name)}</option>`
        ).join('');

        const statusCards = `
            <div style="display:flex;gap:12px;flex-wrap:wrap;margin-bottom:16px;">
                <div style="flex:1;min-width:160px;background:var(--bg-card);border:1px solid var(--border);border-radius:8px;padding:14px;">
                    <div style="font-size:11px;font-weight:600;text-transform:uppercase;color:var(--text-secondary);margin-bottom:4px;">Unmanaged IB Switch</div>
                    <div style="font-size:20px;font-weight:700;color:${status.has_unmanaged_ib_switch ? '#f59e0b' : '#10b981'};">
                        ${status.has_unmanaged_ib_switch ? 'Yes' : 'No'}
                    </div>
                </div>
                <div style="flex:1;min-width:160px;background:var(--bg-card);border:1px solid var(--border);border-radius:8px;padding:14px;">
                    <div style="font-size:11px;font-weight:600;text-transform:uppercase;color:var(--text-secondary);margin-bottom:4px;">OpenSM Required</div>
                    <div style="font-size:20px;font-weight:700;color:${status.opensm_required ? '#f59e0b' : '#10b981'};">
                        ${status.opensm_required ? 'Yes' : 'No'}
                    </div>
                </div>
                <div style="flex:1;min-width:160px;background:var(--bg-card);border:1px solid var(--border);border-radius:8px;padding:14px;">
                    <div style="font-size:11px;font-weight:600;text-transform:uppercase;color:var(--text-secondary);margin-bottom:4px;">OpenSM Configured</div>
                    <div style="font-size:20px;font-weight:700;color:${status.opensm_configured ? '#10b981' : '#94a3b8'};">
                        ${status.opensm_configured ? 'Yes' : 'No'}
                    </div>
                </div>
            </div>`;

        return `
            <div class="card">
                <div class="card-header">
                    <span class="card-title">OpenSM Settings</span>
                </div>
                <div style="padding:16px;">
                    ${statusCards}
                    <div style="background:#fef3c7;color:#92400e;border:1px solid #fbbf24;border-radius:6px;padding:10px 14px;margin-bottom:16px;font-size:13px;">
                        Changes take effect on next reimage of nodes in the head node profile.
                    </div>
                    <div id="opensm-form-area">
                        <div style="text-align:center;color:var(--text-secondary);padding:16px;">
                            <div class="spinner" style="display:inline-block;"></div> Loading OpenSM config&hellip;
                        </div>
                    </div>
                </div>
            </div>`;
    },

    // Called after profiles page renders — loads OpenSM config into the form area.
    async _loadOpenSMForm(profiles) {
        const area = document.getElementById('opensm-form-area');
        if (!area) return;
        let cfg = null;
        try {
            cfg = await API.network.getOpenSM();
        } catch (_) {
            cfg = { enabled: false, head_node_profile_id: '', conf_content: '', log_prefix: '/var/log/opensm', sm_priority: 0 };
        }

        const profileOptions = ['<option value="">— None —</option>']
            .concat((profiles || _netProfilesCache).map(p =>
                `<option value="${escHtml(p.id)}" ${cfg && cfg.head_node_profile_id === p.id ? 'selected' : ''}>${escHtml(p.name)}</option>`
            )).join('');

        area.innerHTML = `
            <div class="form-grid">
                <div class="form-group">
                    <label style="display:flex;align-items:center;gap:8px;cursor:pointer;">
                        <input id="opensm-enabled" type="checkbox" ${cfg && cfg.enabled ? 'checked' : ''}>
                        Enable OpenSM injection
                    </label>
                    <div class="form-hint">When enabled, <code>opensm.conf</code> is injected into the head node group&rsquo;s rootfs at reimage time and <code>opensm.service</code> is enabled.</div>
                </div>
                <div class="form-group">
                    <label>Head Node Profile</label>
                    <select id="opensm-profile">${profileOptions}</select>
                    <div class="form-hint">The network profile assigned to the head/login node group. OpenSM is injected only for nodes in this profile&rsquo;s group.</div>
                </div>
            </div>
            <div class="form-grid">
                <div class="form-group">
                    <label>SM Priority <span style="color:var(--text-secondary);font-weight:400;">(0&ndash;15)</span></label>
                    <input id="opensm-priority" type="number" min="0" max="15" value="${cfg ? (cfg.sm_priority || 0) : 0}">
                    <div class="form-hint">Higher value wins mastership election. Use 0 for a single-SM cluster.</div>
                </div>
                <div class="form-group">
                    <label>Log Prefix</label>
                    <input id="opensm-logprefix" type="text" value="${escHtml(cfg ? (cfg.log_prefix || '/var/log/opensm') : '/var/log/opensm')}" style="font-family:var(--font-mono);">
                </div>
            </div>
            <div class="form-group">
                <label>opensm.conf Content</label>
                <textarea id="opensm-conf" rows="12" style="font-family:var(--font-mono);font-size:12px;resize:vertical;">${escHtml(cfg ? cfg.conf_content || '' : '')}</textarea>
                <div class="form-hint">Full <code>opensm.conf</code> content. Leave blank to use the default generated config. Injected to <code>/etc/opensm/opensm.conf</code> on the head node at reimage.</div>
            </div>
            <div id="opensm-error" style="color:var(--error);font-size:13px;display:none;margin-bottom:8px;"></div>
            <div style="display:flex;gap:8px;justify-content:flex-end;">
                <button class="btn btn-secondary" onclick="NetworkPages._loadDefaultOpenSMConf()">Load Default</button>
                <button class="btn btn-primary" onclick="NetworkPages._saveOpenSM()">Save OpenSM Settings</button>
            </div>`;
    },

    _loadDefaultOpenSMConf() {
        const defaultConf = `# opensm.conf generated by clonr Network module
# Edit as needed. Injected into /etc/opensm/opensm.conf on head node deploy.

guid            0x0000000000000000
sm_priority     0
lmc             0
max_wire_smps   4
transaction_timeout 200
max_op_vls      5
log_flags       0x83
force_log_flush 0
log_file        /var/log/opensm/opensm.log
partition_config /etc/opensm/partitions.conf

# Routing engine: minhop is correct for most flat-tree IB fabrics.
routing_engine  minhop
`;
        const ta = document.getElementById('opensm-conf');
        if (ta) ta.value = defaultConf;
    },

    async _saveOpenSM() {
        const enabled     = document.getElementById('opensm-enabled').checked;
        const profileId   = (document.getElementById('opensm-profile').value || '').trim();
        const priority    = parseInt(document.getElementById('opensm-priority').value, 10) || 0;
        const logPrefix   = (document.getElementById('opensm-logprefix').value || '/var/log/opensm').trim();
        const confContent = (document.getElementById('opensm-conf').value || '').trimEnd();
        const errEl       = document.getElementById('opensm-error');
        errEl.style.display = 'none';

        if (priority < 0 || priority > 15) {
            errEl.textContent = 'SM Priority must be 0\u201315.';
            errEl.style.display = '';
            return;
        }

        try {
            await API.network.setOpenSM({
                enabled,
                head_node_profile_id: profileId,
                sm_priority:          priority,
                log_prefix:           logPrefix,
                conf_content:         confContent,
            });
            App.toast('OpenSM settings saved', 'success');
        } catch (err) {
            errEl.textContent = err.message;
            errEl.style.display = '';
        }
    },

    // ── Profile modals ─────────────────────────────────────────────────────────

    _createProfileModal() {
        NetworkPages._openProfileModal(null);
    },

    _editProfileModal(p) {
        NetworkPages._openProfileModal(p);
    },

    _openProfileModal(p) {
        const existing = document.getElementById('net-profile-modal');
        if (existing) existing.remove();

        // Build initial bond state from existing profile or empty.
        const bonds = (p && p.bonds) ? JSON.parse(JSON.stringify(p.bonds)) : [];
        const ibEnabled = !!(p && p.ib);
        const ib = (p && p.ib) ? JSON.parse(JSON.stringify(p.ib)) : { ipoib_mode: 'connected', ipoib_mtu: 65520, ip_method: 'dhcp', pkeys: [], device_match: '' };

        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = 'net-profile-modal';
        if (p) overlay.dataset.profileId = p.id;

        overlay.innerHTML = `
            <div class="modal modal-wide" style="max-width:780px;">
                <div class="modal-header">
                    <span class="modal-title">${p ? 'Edit Profile: ' + escHtml(p.name) : 'Create Network Profile'}</span>
                    <button class="modal-close" onclick="document.getElementById('net-profile-modal').remove()">&times;</button>
                </div>
                <div class="modal-body" style="max-height:70vh;overflow-y:auto;">
                    <div class="form-grid">
                        <div class="form-group">
                            <label>Profile Name <span style="color:var(--error)">*</span></label>
                            <input id="np-name" type="text" value="${escHtml(p ? p.name : '')}" placeholder="compute-profile" style="font-family:var(--font-mono);">
                            <div class="form-hint">Pattern: <code>^[a-zA-Z0-9._-]+$</code>, max 64 chars.</div>
                        </div>
                        <div class="form-group">
                            <label>Description</label>
                            <input id="np-desc" type="text" value="${escHtml(p ? (p.description || '') : '')}" placeholder="Compute node Ethernet bonding + IPoIB">
                        </div>
                    </div>

                    <!-- Bonds section -->
                    <div style="margin-top:16px;border-top:1px solid var(--border);padding-top:16px;">
                        <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:12px;">
                            <div style="font-weight:600;font-size:14px;">Bond Interfaces</div>
                            <button class="btn btn-secondary btn-sm" onclick="NetworkPages._addBond()">+ Add Bond</button>
                        </div>
                        <div id="np-bonds-list"></div>
                    </div>

                    <!-- InfiniBand section -->
                    <div style="margin-top:16px;border-top:1px solid var(--border);padding-top:16px;">
                        <div style="display:flex;align-items:center;gap:12px;margin-bottom:12px;">
                            <label style="display:flex;align-items:center;gap:8px;cursor:pointer;font-weight:600;font-size:14px;margin:0;">
                                <input id="np-ib-enabled" type="checkbox" ${ibEnabled ? 'checked' : ''} onchange="NetworkPages._toggleIBSection()">
                                InfiniBand / IPoIB
                            </label>
                        </div>
                        <div id="np-ib-section" style="display:${ibEnabled ? '' : 'none'};">
                            <div class="form-grid">
                                <div class="form-group">
                                    <label>IPoIB Mode</label>
                                    <select id="np-ib-mode" onchange="NetworkPages._onIBModeChange()">
                                        <option value="connected" ${(!ibEnabled || ib.ipoib_mode === 'connected') ? 'selected' : ''}>connected (higher throughput)</option>
                                        <option value="datagram"  ${ibEnabled && ib.ipoib_mode === 'datagram'  ? 'selected' : ''}>datagram (more compatible)</option>
                                    </select>
                                    <div class="form-hint">Connected mode requires kernel module support. MTU is set automatically.</div>
                                </div>
                                <div class="form-group">
                                    <label>IPoIB MTU</label>
                                    <input id="np-ib-mtu" type="number" value="${ibEnabled ? ib.ipoib_mtu : 65520}" readonly style="background:var(--bg-input,#f8fafc);color:var(--text-secondary);">
                                    <div class="form-hint">65520 for connected, 2044 for datagram. Auto-filled.</div>
                                </div>
                            </div>
                            <div class="form-grid">
                                <div class="form-group">
                                    <label>IP Method</label>
                                    <select id="np-ib-ipmethod">
                                        <option value="dhcp"   ${(!ibEnabled || ib.ip_method === 'dhcp')   ? 'selected' : ''}>DHCP</option>
                                        <option value="static" ${ibEnabled && ib.ip_method === 'static' ? 'selected' : ''}>Static</option>
                                        <option value="none"   ${ibEnabled && ib.ip_method === 'none'   ? 'selected' : ''}>None (link-only)</option>
                                    </select>
                                </div>
                                <div class="form-group">
                                    <label>Device Match</label>
                                    <input id="np-ib-device" type="text" value="${escHtml(ibEnabled ? (ib.device_match || '') : '')}" placeholder="mlx5_, hfi1_ (empty = first device)" style="font-family:var(--font-mono);">
                                    <div class="form-hint">Kernel IB device name prefix. Leave empty to use the first IB device found.</div>
                                </div>
                            </div>
                        </div>
                    </div>

                    <div id="np-error" style="color:var(--error);font-size:13px;display:none;margin-top:8px;"></div>
                </div>
                <div class="modal-footer">
                    <button class="btn btn-secondary" onclick="document.getElementById('net-profile-modal').remove()">Cancel</button>
                    <button class="btn btn-primary" id="np-submit-btn">${p ? 'Save Changes' : 'Create Profile'}</button>
                </div>
            </div>`;

        document.body.appendChild(overlay);

        // Inject bond list state into the modal's bond renderer.
        overlay._bonds = bonds;
        NetworkPages._renderBonds(overlay);

        document.getElementById('np-submit-btn').onclick = () => p
            ? NetworkPages._submitUpdateProfile()
            : NetworkPages._submitCreateProfile();

        if (!p) document.getElementById('np-name').focus();
    },

    _toggleIBSection() {
        const enabled = document.getElementById('np-ib-enabled').checked;
        const section = document.getElementById('np-ib-section');
        if (section) section.style.display = enabled ? '' : 'none';
    },

    _onIBModeChange() {
        const mode   = document.getElementById('np-ib-mode').value;
        const mtuEl  = document.getElementById('np-ib-mtu');
        if (mtuEl) mtuEl.value = mode === 'connected' ? 65520 : 2044;
    },

    // ── Bond builder helpers ───────────────────────────────────────────────────

    _addBond() {
        const overlay = document.getElementById('net-profile-modal');
        if (!overlay) return;
        const idx = overlay._bonds.length;
        overlay._bonds.push({
            bond_name: `bond${idx}`,
            mode: '802.3ad',
            mtu: 1500,
            vlan_id: 0,
            ip_method: 'static',
            lacp_rate: 'fast',
            xmit_hash_policy: 'layer3+4',
            members: [],
        });
        NetworkPages._renderBonds(overlay);
    },

    _removeBond(idx) {
        const overlay = document.getElementById('net-profile-modal');
        if (!overlay) return;
        overlay._bonds.splice(idx, 1);
        NetworkPages._renderBonds(overlay);
    },

    _addMember(bondIdx) {
        const overlay = document.getElementById('net-profile-modal');
        if (!overlay) return;
        overlay._bonds[bondIdx].members.push({ match_mac: '', match_name: '' });
        NetworkPages._renderBonds(overlay);
    },

    _removeMember(bondIdx, memberIdx) {
        const overlay = document.getElementById('net-profile-modal');
        if (!overlay) return;
        overlay._bonds[bondIdx].members.splice(memberIdx, 1);
        NetworkPages._renderBonds(overlay);
    },

    // _syncBonds reads current DOM values back into overlay._bonds before re-render.
    // Called before any structural change (add/remove bond or member).
    _syncBonds() {
        const overlay = document.getElementById('net-profile-modal');
        if (!overlay || !overlay._bonds) return;
        overlay._bonds.forEach((b, bi) => {
            const nameEl   = document.getElementById(`nb-name-${bi}`);
            const modeEl   = document.getElementById(`nb-mode-${bi}`);
            const mtuEl    = document.getElementById(`nb-mtu-${bi}`);
            const vlanEl   = document.getElementById(`nb-vlan-${bi}`);
            const ipEl     = document.getElementById(`nb-ip-${bi}`);
            const lacpEl   = document.getElementById(`nb-lacp-${bi}`);
            const xmitEl   = document.getElementById(`nb-xmit-${bi}`);
            if (nameEl) b.bond_name       = nameEl.value;
            if (modeEl) b.mode            = modeEl.value;
            if (mtuEl)  b.mtu             = parseInt(mtuEl.value, 10) || 1500;
            if (vlanEl) b.vlan_id         = parseInt(vlanEl.value, 10) || 0;
            if (ipEl)   b.ip_method       = ipEl.value;
            if (lacpEl) b.lacp_rate       = lacpEl.value;
            if (xmitEl) b.xmit_hash_policy = xmitEl.value;
            b.members.forEach((m, mi) => {
                const macEl  = document.getElementById(`nm-mac-${bi}-${mi}`);
                const nameEl2 = document.getElementById(`nm-name-${bi}-${mi}`);
                if (macEl)  m.match_mac  = macEl.value.trim();
                if (nameEl2) m.match_name = nameEl2.value.trim();
            });
        });
    },

    _renderBonds(overlay) {
        const list = document.getElementById('np-bonds-list');
        if (!list || !overlay._bonds) return;
        if (overlay._bonds.length === 0) {
            list.innerHTML = `<div style="color:var(--text-secondary);font-size:13px;padding:8px 0;">No bonds defined. Click "Add Bond" to create one.</div>`;
            return;
        }
        list.innerHTML = overlay._bonds.map((b, bi) => {
            const isLACP = b.mode === '802.3ad';
            const memberRows = b.members.map((m, mi) => `
                <div style="display:flex;gap:8px;align-items:flex-start;margin-bottom:8px;">
                    <div style="flex:1;">
                        <input id="nm-mac-${bi}-${mi}" type="text" value="${escHtml(m.match_mac || '')}"
                            placeholder="aa:bb:cc:dd:ee:ff (preferred)"
                            style="font-family:var(--font-mono);width:100%;"
                            oninput="NetworkPages._syncBonds()">
                    </div>
                    <div style="flex:1;">
                        <input id="nm-name-${bi}-${mi}" type="text" value="${escHtml(m.match_name || '')}"
                            placeholder="ens2f0 (name, less stable)"
                            style="font-family:var(--font-mono);width:100%;"
                            oninput="NetworkPages._syncBonds()">
                        ${!m.match_mac && m.match_name ? `<div style="color:#f59e0b;font-size:11px;margin-top:2px;">Interface name matching is not stable across reboots &mdash; use MAC address matching.</div>` : ''}
                    </div>
                    <button class="btn btn-danger btn-sm" style="flex-shrink:0;"
                        onclick="NetworkPages._syncBonds();NetworkPages._removeMember(${bi},${mi})">Remove</button>
                </div>`).join('');

            return `
                <div style="border:1px solid var(--border);border-radius:8px;padding:14px;margin-bottom:12px;background:var(--bg-card,#fff);">
                    <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:12px;">
                        <div style="font-weight:600;font-size:13px;color:var(--text-secondary);">Bond ${bi}</div>
                        <button class="btn btn-danger btn-sm" onclick="NetworkPages._syncBonds();NetworkPages._removeBond(${bi})">Remove Bond</button>
                    </div>
                    <div class="form-grid">
                        <div class="form-group" style="margin-bottom:8px;">
                            <label style="font-size:12px;">Bond Name</label>
                            <input id="nb-name-${bi}" type="text" value="${escHtml(b.bond_name)}" placeholder="bond0"
                                style="font-family:var(--font-mono);" oninput="NetworkPages._syncBonds()">
                        </div>
                        <div class="form-group" style="margin-bottom:8px;">
                            <label style="font-size:12px;">Mode</label>
                            <select id="nb-mode-${bi}" onchange="NetworkPages._syncBonds();NetworkPages._renderBonds(document.getElementById('net-profile-modal'))">
                                <option value="802.3ad"      ${b.mode === '802.3ad'      ? 'selected' : ''}>802.3ad (LACP)</option>
                                <option value="active-backup" ${b.mode === 'active-backup' ? 'selected' : ''}>active-backup</option>
                                <option value="balance-rr"   ${b.mode === 'balance-rr'   ? 'selected' : ''}>balance-rr</option>
                                <option value="balance-xor"  ${b.mode === 'balance-xor'  ? 'selected' : ''}>balance-xor</option>
                                <option value="broadcast"    ${b.mode === 'broadcast'    ? 'selected' : ''}>broadcast</option>
                                <option value="balance-alb"  ${b.mode === 'balance-alb'  ? 'selected' : ''}>balance-alb (ALB)</option>
                                <option value="balance-tlb"  ${b.mode === 'balance-tlb'  ? 'selected' : ''}>balance-tlb (TLB)</option>
                            </select>
                        </div>
                    </div>
                    <div class="form-grid">
                        <div class="form-group" style="margin-bottom:8px;">
                            <label style="font-size:12px;">MTU</label>
                            <input id="nb-mtu-${bi}" type="number" value="${b.mtu || 1500}" min="576" max="65535"
                                oninput="NetworkPages._syncBonds()">
                        </div>
                        <div class="form-group" style="margin-bottom:8px;">
                            <label style="font-size:12px;">VLAN ID <span style="font-weight:400;color:var(--text-secondary);">(0 = none)</span></label>
                            <input id="nb-vlan-${bi}" type="number" value="${b.vlan_id || 0}" min="0" max="4094"
                                oninput="NetworkPages._syncBonds()">
                        </div>
                    </div>
                    <div class="form-grid">
                        <div class="form-group" style="margin-bottom:8px;">
                            <label style="font-size:12px;">IP Method</label>
                            <select id="nb-ip-${bi}" onchange="NetworkPages._syncBonds()">
                                <option value="static" ${b.ip_method === 'static' ? 'selected' : ''}>Static</option>
                                <option value="dhcp"   ${b.ip_method === 'dhcp'   ? 'selected' : ''}>DHCP</option>
                                <option value="none"   ${b.ip_method === 'none'   ? 'selected' : ''}>None (link-only)</option>
                            </select>
                        </div>
                        ${isLACP ? `
                        <div class="form-group" style="margin-bottom:8px;">
                            <label style="font-size:12px;">LACP Rate</label>
                            <select id="nb-lacp-${bi}" onchange="NetworkPages._syncBonds()">
                                <option value="fast" ${b.lacp_rate === 'fast' ? 'selected' : ''}>fast</option>
                                <option value="slow" ${b.lacp_rate === 'slow' ? 'selected' : ''}>slow</option>
                            </select>
                            <div style="font-size:11px;color:var(--text-secondary);margin-top:2px;">Requires LACP enabled on connected switch ports.</div>
                        </div>` : '<div></div>'}
                    </div>
                    ${isLACP ? `
                    <div class="form-group" style="margin-bottom:8px;">
                        <label style="font-size:12px;">Xmit Hash Policy</label>
                        <select id="nb-xmit-${bi}" onchange="NetworkPages._syncBonds()">
                            <option value="layer3+4" ${b.xmit_hash_policy === 'layer3+4' ? 'selected' : ''}>layer3+4 (IP+port)</option>
                            <option value="layer2"   ${b.xmit_hash_policy === 'layer2'   ? 'selected' : ''}>layer2 (MAC)</option>
                            <option value="layer2+3" ${b.xmit_hash_policy === 'layer2+3' ? 'selected' : ''}>layer2+3 (MAC+IP)</option>
                        </select>
                    </div>` : ''}
                    <!-- Member NICs -->
                    <div style="margin-top:12px;">
                        <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:8px;">
                            <div style="font-size:12px;font-weight:600;color:var(--text-secondary);">Member NICs</div>
                            <button class="btn btn-secondary btn-sm" onclick="NetworkPages._syncBonds();NetworkPages._addMember(${bi})">+ Add Member</button>
                        </div>
                        ${memberRows || `<div style="font-size:12px;color:var(--text-secondary);">No members yet. A bond must have at least one member.</div>`}
                    </div>
                </div>`;
        }).join('');
    },

    // ── Profile read/validate/submit ───────────────────────────────────────────

    _readProfileForm(overlay) {
        NetworkPages._syncBonds();
        const name   = (document.getElementById('np-name').value || '').trim();
        const desc   = (document.getElementById('np-desc').value || '').trim();
        const ibEn   = document.getElementById('np-ib-enabled').checked;
        const bonds  = overlay._bonds || [];

        let ib = null;
        if (ibEn) {
            const mode = document.getElementById('np-ib-mode').value;
            ib = {
                ipoib_mode:   mode,
                ipoib_mtu:    mode === 'connected' ? 65520 : 2044,
                ip_method:    document.getElementById('np-ib-ipmethod').value,
                device_match: (document.getElementById('np-ib-device').value || '').trim(),
                pkeys:        [],
            };
        }

        return { name, description: desc, bonds, ib };
    },

    _validateProfileForm(data, errEl) {
        if (!data.name) { errEl.textContent = 'Profile name is required.'; errEl.style.display = ''; return false; }
        for (let i = 0; i < data.bonds.length; i++) {
            const b = data.bonds[i];
            if (!b.bond_name) { errEl.textContent = `Bond ${i}: bond name is required.`; errEl.style.display = ''; return false; }
            if (!b.members || b.members.length === 0) {
                errEl.textContent = `Bond ${i} (${b.bond_name}): must have at least one member NIC.`;
                errEl.style.display = '';
                return false;
            }
            for (let j = 0; j < b.members.length; j++) {
                const m = b.members[j];
                if (!m.match_mac && !m.match_name) {
                    errEl.textContent = `Bond ${i} (${b.bond_name}), member ${j}: provide a MAC address or interface name.`;
                    errEl.style.display = '';
                    return false;
                }
            }
        }
        return true;
    },

    async _submitCreateProfile() {
        const overlay = document.getElementById('net-profile-modal');
        const data    = NetworkPages._readProfileForm(overlay);
        const errEl   = document.getElementById('np-error');
        errEl.style.display = 'none';
        if (!NetworkPages._validateProfileForm(data, errEl)) return;
        try {
            await API.network.createProfile(data);
            overlay.remove();
            App.toast('Network profile created', 'success');
            NetworkPages.profiles();
        } catch (err) {
            errEl.textContent = err.message;
            errEl.style.display = '';
        }
    },

    async _submitUpdateProfile() {
        const overlay = document.getElementById('net-profile-modal');
        const id      = overlay.dataset.profileId;
        const data    = NetworkPages._readProfileForm(overlay);
        const errEl   = document.getElementById('np-error');
        errEl.style.display = 'none';
        if (!NetworkPages._validateProfileForm(data, errEl)) return;
        try {
            await API.network.updateProfile(id, data);
            overlay.remove();
            App.toast('Network profile updated', 'success');
            NetworkPages.profiles();
        } catch (err) {
            errEl.textContent = err.message;
            errEl.style.display = '';
        }
    },

    async _deleteProfile(id, name) {
        if (!confirm(`Delete profile "${name}"?\n\nAlready-deployed nodes retain their injected network config. Re-image to remove it.`)) return;
        try {
            await API.network.deleteProfile(id);
            App.toast('Profile deleted', 'success');
            NetworkPages.profiles();
        } catch (err) {
            App.toast('Delete failed: ' + err.message, 'error');
        }
    },
};

// ── Post-render hook: load OpenSM form after profiles page renders ─────────────
// We hook into App.render by wrapping it once after the network module loads.
// This avoids modifying app.js and is self-contained.
(function _hookNetworkPostRender() {
    const origRender = App.render.bind(App);
    App.render = function (html) {
        origRender(html);
        // After render settles, check if we're on the profiles page and load OpenSM.
        if (window.location.hash === '#/network/profiles') {
            // Use microtask so the DOM is fully injected before we query it.
            Promise.resolve().then(() => {
                if (document.getElementById('opensm-form-area')) {
                    NetworkPages._loadOpenSMForm(_netProfilesCache).catch(() => {});
                }
            });
        }
    };
})();
