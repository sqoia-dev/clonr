// slurm.js — Slurm module webui: settings and config file management.
// Conventions mirror app.js and ldap.js: hash-based routing, App.render(),
// App.toast(), cardWrap(), escHtml(), fmtDate(), API.slurm.* (api.js).

// ─── SlurmPages namespace ──────────────────────────────────────────────────

const SlurmPages = {

    // ── Nav bootstrap ──────────────────────────────────────────────────────

    // bootstrapNav fetches /api/v1/slurm/status and updates Slurm nav visibility.
    // Called once on app boot and re-called after enable/disable.
    async bootstrapNav() {
        const section = document.getElementById('nav-slurm-section');
        if (!section) return;
        try {
            const st = await API.slurm.status();
            const enabled = !!(st && st.enabled && st.status === 'ready');

            section.style.display = '';

            const badge   = document.getElementById('nav-slurm-status-badge');
            const managed = document.getElementById('nav-slurm-managed');
            if (badge) {
                if (enabled) {
                    badge.textContent = 'Ready';
                    badge.className   = 'badge badge-ready badge-sm';
                    badge.style.display = '';
                } else if (st.status === 'error') {
                    badge.textContent = 'Error';
                    badge.className   = 'badge badge-error badge-sm';
                    badge.style.display = '';
                } else {
                    badge.textContent = st.status === 'not_configured' ? 'Setup' : 'Disabled';
                    badge.className   = 'badge badge-neutral badge-sm';
                    badge.style.display = '';
                }
            }
            if (managed) managed.style.display = enabled ? '' : 'none';
        } catch (_) {
            // Non-admin (403) or unreachable — hide entire section.
            section.style.display = 'none';
        }
    },

    // ── Settings page ──────────────────────────────────────────────────────

    async settings() {
        App.render(loading('Loading Slurm settings…'));
        try {
            const st = await API.slurm.status();
            App.render(SlurmPages._settingsHtml(st));
            SlurmPages._bindSettingsEvents(st);
        } catch (err) {
            App.render(alertBox('Failed to load Slurm status: ' + err.message));
        }
    },

    _settingsHtml(st) {
        const isAdmin = typeof Auth !== 'undefined' && Auth._role === 'admin';

        const statusColors = {
            ready:          'badge-ready',
            disabled:       'badge-neutral',
            error:          'badge-error',
            not_configured: 'badge-neutral',
        };
        const statusBadge = `<span class="badge ${statusColors[st.status] || 'badge-neutral'}">${escHtml(st.status)}</span>`;

        const driftHtml = st.drift_summary
            ? `<tr>
                <td style="color:var(--text-secondary);padding:6px 16px 6px 0;font-size:13px;">Drift</td>
                <td style="padding:6px 0;font-size:13px;">
                    ${st.drift_summary.out_of_sync > 0
                        ? `<span style="color:#dc2626;">${st.drift_summary.out_of_sync} node(s) out of sync</span>`
                        : `<span style="color:#16a34a;">All ${st.drift_summary.in_sync_nodes} node(s) in sync</span>`}
                </td>
               </tr>`
            : '';

        const connectedHtml = `<tr>
            <td style="color:var(--text-secondary);padding:6px 16px 6px 0;font-size:13px;">Connected Nodes</td>
            <td style="padding:6px 0;font-size:13px;">${st.connected_nodes ? st.connected_nodes.length : 0}</td>
            </tr>`;

        const enableForm = (!st.enabled || st.status === 'not_configured' || st.status === 'disabled') && isAdmin ? `
            <div style="margin-top:20px;padding:20px;border:2px dashed var(--border);border-radius:8px;background:var(--bg-secondary);max-width:480px;">
                <div style="font-size:15px;font-weight:700;margin-bottom:6px;">Enable Slurm Management</div>
                <p style="font-size:13px;color:var(--text-secondary);margin:0 0 16px;">
                    clustr will manage Slurm config files, scripts, munge keys, and rolling upgrades
                    for all nodes in this cluster.
                </p>
                <form id="slurm-enable-form" style="display:flex;flex-direction:column;gap:10px;">
                    <div>
                        <label style="display:block;font-size:13px;font-weight:500;margin-bottom:4px;">Cluster Name</label>
                        <input type="text" id="slurm-cluster-name" placeholder="e.g. hpc-prod"
                               style="width:100%;padding:8px 12px;border:1px solid var(--border);border-radius:6px;font-size:13px;background:var(--bg-input,#fff);color:var(--text);"
                               value="${st.cluster_name ? escHtml(st.cluster_name) : ''}">
                        <div style="font-size:11px;color:var(--text-secondary);margin-top:4px;">Used as ClusterName in slurm.conf templates</div>
                    </div>
                    <div>
                        <button type="submit" class="btn btn-primary" style="font-size:13px;padding:8px 20px;">
                            Enable Slurm Module
                        </button>
                    </div>
                </form>
            </div>` : '';

        // B2-3: Restore Defaults button — re-seeds clustr default Slurm config files.
        const restoreDefaultsBtn = st.enabled && isAdmin ? `
            <div style="margin-top:20px;padding-top:16px;border-top:1px solid var(--border);">
                <h3 style="font-size:14px;font-weight:600;margin:0 0 6px;">Restore Default Config Files</h3>
                <p style="font-size:13px;color:var(--text-secondary);margin:0 0 12px;">
                    Re-seeds the clustr built-in Slurm config templates. Only files that still have clustr default content will be updated —
                    any files you have manually edited are preserved.
                </p>
                <button id="slurm-reseed-btn" class="btn btn-secondary" style="font-size:13px;padding:6px 16px;">
                    Restore Defaults
                </button>
                <span id="slurm-reseed-status" style="margin-left:10px;font-size:12px;color:var(--text-secondary);"></span>
            </div>` : '';

        const disableBtn = st.enabled && isAdmin ? `
            <div style="margin-top:24px;padding-top:16px;border-top:1px solid var(--border);">
                <h3 style="font-size:14px;font-weight:600;margin:0 0 8px;color:var(--text-secondary);">Danger Zone</h3>
                <p style="font-size:13px;color:var(--text-secondary);margin:0 0 12px;">
                    Disabling the Slurm module stops clustr from managing Slurm configs. It does <strong>not</strong> remove configs from deployed nodes.
                </p>
                <button id="slurm-disable-btn" class="btn btn-danger" style="font-size:13px;padding:6px 16px;">
                    Disable Module
                </button>
            </div>` : '';

        // Quick-stat cards (only when enabled)
        const quickStats = st.enabled && st.drift_summary ? `
            <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(120px,1fr));gap:12px;max-width:600px;margin-bottom:20px;">
                <div style="background:var(--bg-secondary);border:1px solid var(--border);border-radius:8px;padding:14px;text-align:center;">
                    <div style="font-size:22px;font-weight:700;">${st.drift_summary.total_nodes || 0}</div>
                    <div style="font-size:12px;color:var(--text-secondary);margin-top:2px;">Total Nodes</div>
                </div>
                <div style="background:var(--bg-secondary);border:1px solid var(--border);border-radius:8px;padding:14px;text-align:center;">
                    <div style="font-size:22px;font-weight:700;color:#16a34a;">${st.drift_summary.in_sync_nodes || 0}</div>
                    <div style="font-size:12px;color:var(--text-secondary);margin-top:2px;">In Sync</div>
                </div>
                <div style="background:var(--bg-secondary);border:1px solid var(--border);border-radius:8px;padding:14px;text-align:center;">
                    <div style="font-size:22px;font-weight:700;${(st.drift_summary.out_of_sync || 0) > 0 ? 'color:#dc2626;' : ''}">${st.drift_summary.out_of_sync || 0}</div>
                    <div style="font-size:12px;color:var(--text-secondary);margin-top:2px;">Out of Sync</div>
                </div>
                <div style="background:var(--bg-secondary);border:1px solid var(--border);border-radius:8px;padding:14px;text-align:center;">
                    <div style="font-size:22px;font-weight:700;">${st.connected_nodes ? st.connected_nodes.length : 0}</div>
                    <div style="font-size:12px;color:var(--text-secondary);margin-top:2px;">Connected</div>
                </div>
            </div>` : '';

        return cardWrap('Slurm Module Setup', `
            ${quickStats}
            <table style="border-collapse:collapse;width:100%;max-width:540px;margin-bottom:8px;">
                <tbody>
                    <tr>
                        <td style="color:var(--text-secondary);padding:6px 16px 6px 0;font-size:13px;">Status</td>
                        <td style="padding:6px 0;">${statusBadge}</td>
                    </tr>
                    <tr>
                        <td style="color:var(--text-secondary);padding:6px 16px 6px 0;font-size:13px;">Cluster Name</td>
                        <td style="padding:6px 0;font-family:monospace;font-size:13px;">${st.cluster_name ? escHtml(st.cluster_name) : '<span style="color:var(--text-secondary)">—</span>'}</td>
                    </tr>
                    ${st.slurm_version ? `<tr>
                        <td style="color:var(--text-secondary);padding:6px 16px 6px 0;font-size:13px;">Active Slurm Version</td>
                        <td style="padding:6px 0;font-family:monospace;font-size:13px;">${escHtml(st.slurm_version)}</td>
                    </tr>` : ''}
                    <tr>
                        <td style="color:var(--text-secondary);padding:6px 16px 6px 0;font-size:13px;">Managed Files</td>
                        <td style="padding:6px 0;font-size:13px;">${st.managed_files && st.managed_files.length ? st.managed_files.map(f => `<code style="font-size:12px;background:var(--bg-code,#f1f5f9);padding:1px 4px;border-radius:3px;">${escHtml(f)}</code>`).join(' ') : '—'}</td>
                    </tr>
                    ${st.munge_key_set !== undefined ? `<tr>
                        <td style="color:var(--text-secondary);padding:6px 16px 6px 0;font-size:13px;">Munge Key</td>
                        <td style="padding:6px 0;font-size:13px;">${st.munge_key_set ? '<span style="color:#16a34a;">Set</span>' : '<span style="color:#dc2626;">Not set</span>'}</td>
                    </tr>` : ''}
                    ${connectedHtml}
                    ${driftHtml}
                </tbody>
            </table>
            ${enableForm}
            ${restoreDefaultsBtn}
            ${disableBtn}
        `);
    },

    _bindSettingsEvents(st) {
        const form = document.getElementById('slurm-enable-form');
        if (form) {
            form.addEventListener('submit', async e => {
                e.preventDefault();
                const clusterName = document.getElementById('slurm-cluster-name').value.trim();
                if (!clusterName) { App.toast('Cluster name is required', 'error'); return; }
                try {
                    await API.slurm.enable({ cluster_name: clusterName });
                    App.toast('Slurm module enabled', 'success');
                    await SlurmPages.bootstrapNav();
                    SlurmPages.settings();
                } catch (err) {
                    App.toast('Enable failed: ' + err.message, 'error');
                }
            });
        }

        // B2-3: Restore Defaults button.
        const reseedBtn = document.getElementById('slurm-reseed-btn');
        if (reseedBtn) {
            reseedBtn.addEventListener('click', () => {
                Pages.showConfirmModal({
                    title: 'Restore Default Config Files',
                    message: 'Re-seed clustr built-in Slurm config templates?<br><br>Only files that still contain clustr default content will be updated. Manually edited files will <strong>not</strong> be changed.',
                    confirmText: 'Restore Defaults',
                    onConfirm: async () => {
                        const status = document.getElementById('slurm-reseed-status');
                        if (status) status.textContent = 'Restoring…';
                        try {
                            await API.request('POST', '/slurm/configs/reseed-defaults');
                            App.toast('Default config files restored', 'success');
                            SlurmPages.settings();
                        } catch (err) {
                            App.toast('Restore failed: ' + err.message, 'error');
                            if (status) status.textContent = '';
                        }
                    },
                });
            });
        }

        const disableBtn = document.getElementById('slurm-disable-btn');
        if (disableBtn) {
            disableBtn.addEventListener('click', () => {
                Pages.showConfirmModal({
                    title: 'Disable Slurm Module',
                    message: 'Disable the Slurm module? Configs will remain on deployed nodes.',
                    confirmText: 'Disable',
                    danger: true,
                    onConfirm: async () => {
                        try {
                            await API.slurm.disable();
                            App.toast('Slurm module disabled', 'success');
                            await SlurmPages.bootstrapNav();
                            SlurmPages.settings();
                        } catch (err) {
                            App.toast('Disable failed: ' + err.message, 'error');
                        }
                    },
                });
            });
        }
    },

    // ── Config files page ──────────────────────────────────────────────────

    async configs() {
        App.render(loading('Loading Slurm config files…'));
        try {
            const data = await API.slurm.listConfigs();
            App.render(SlurmPages._configsHtml(data.configs || []));
        } catch (err) {
            App.render(alertBox('Failed to load configs: ' + err.message));
        }
    },

    _configsHtml(configs) {
        if (!configs.length) {
            return cardWrap('Slurm Config Files', emptyState('No config files found. Enable the Slurm module to seed defaults.'));
        }

        const rows = configs.map(c => `
            <tr>
                <td style="padding:10px 12px;font-family:monospace;font-size:13px;">
                    <a href="#/slurm/configs/${encodeURIComponent(c.filename)}" style="color:var(--accent);">${escHtml(c.filename)}</a>
                </td>
                <td style="padding:10px 12px;font-size:13px;">v${c.version}</td>
                <td style="padding:10px 12px;font-size:12px;color:var(--text-secondary);font-family:monospace;">${c.checksum ? c.checksum.substring(0, 12) + '…' : '—'}</td>
                <td style="padding:10px 12px;">
                    <a href="#/slurm/configs/${encodeURIComponent(c.filename)}" class="btn btn-sm" style="font-size:12px;padding:3px 10px;">Edit</a>
                </td>
            </tr>
        `).join('');

        return cardWrap('Slurm Config Files', `
            <div style="overflow-x:auto;">
                <table style="width:100%;border-collapse:collapse;min-width:400px;">
                    <thead>
                        <tr style="border-bottom:1px solid var(--border);">
                            <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">File</th>
                            <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Version</th>
                            <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Checksum</th>
                            <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;"></th>
                        </tr>
                    </thead>
                    <tbody>${rows}</tbody>
                </table>
            </div>
        `);
    },

    // ── Config file editor ─────────────────────────────────────────────────

    async configEditor(filename) {
        App.render(loading('Loading ' + escHtml(filename) + '…'));
        try {
            const config = await API.slurm.getConfig(filename);
            App.render(SlurmPages._configEditorHtml(config));
            SlurmPages._bindEditorEvents(filename);
        } catch (err) {
            App.render(alertBox('Failed to load config: ' + err.message));
        }
    },

    // Template variables available in Slurm config templates.
    _TEMPLATE_VARS: [
        { name: '{{.ClusterName}}',     desc: 'Cluster name from settings' },
        { name: '{{.Hostname}}',        desc: 'Node hostname' },
        { name: '{{.FQDN}}',            desc: 'Node FQDN' },
        { name: '{{.NodeID}}',          desc: 'clustr node UUID' },
        { name: '{{.CPUs}}',            desc: 'Override: CPU count' },
        { name: '{{.Sockets}}',         desc: 'Override: socket count' },
        { name: '{{.CoresPerSocket}}',  desc: 'Override: cores per socket' },
        { name: '{{.ThreadsPerCore}}',  desc: 'Override: threads per core' },
        { name: '{{.RealMemory}}',      desc: 'Override: memory in MB' },
        { name: '{{.Gres}}',            desc: 'Override: GRES string' },
        { name: '{{.ExtraParams}}',     desc: 'Override: extra node params' },
        { name: '{{range .Nodes}}',     desc: 'Iterate over all nodes' },
        { name: '{{end}}',              desc: 'End range/if block' },
        { name: '{{if .IsController}}', desc: 'True when node role=controller' },
        { name: '{{if .IsCompute}}',    desc: 'True when node role=compute' },
    ],

    _configEditorHtml(config) {
        const isSlurmConf = config.filename === 'slurm.conf';
        const varHints = isSlurmConf ? `
            <details style="margin-top:12px;border:1px solid var(--border);border-radius:6px;overflow:hidden;">
                <summary style="padding:8px 12px;font-size:12px;font-weight:600;cursor:pointer;background:var(--bg-secondary);color:var(--text-secondary);user-select:none;">
                    Template Variables (${SlurmPages._TEMPLATE_VARS.length} available)
                </summary>
                <div style="padding:10px 12px;display:flex;flex-wrap:wrap;gap:8px;">
                    ${SlurmPages._TEMPLATE_VARS.map(v => `
                        <div style="background:var(--bg-code,#f1f5f9);border:1px solid var(--border);border-radius:4px;padding:4px 8px;cursor:pointer;font-size:12px;"
                             title="${escHtml(v.desc)}"
                             onclick="SlurmPages._insertTemplateVar('slurm-config-content','${escHtml(v.name)}')">
                            <code style="font-family:monospace;">${escHtml(v.name)}</code>
                            <span style="color:var(--text-secondary);margin-left:4px;">${escHtml(v.desc)}</span>
                        </div>`).join('')}
                </div>
            </details>` : '';

        return cardWrap(`Edit: ${escHtml(config.filename)}`, `
            <div style="margin-bottom:12px;display:flex;align-items:center;gap:12px;flex-wrap:wrap;">
                <span style="font-size:13px;color:var(--text-secondary);">Current version: <strong>v${config.version}</strong></span>
                <span id="slurm-editor-dirty-badge" style="display:none;font-size:12px;color:#f59e0b;font-weight:600;">Unsaved changes</span>
                <a href="#/slurm/configs/${encodeURIComponent(config.filename)}/history" style="font-size:13px;color:var(--accent);">View history</a>
                <a href="#/slurm/configs" style="font-size:13px;color:var(--accent);margin-left:auto;">Back to list</a>
            </div>
            <div style="display:flex;border:1px solid var(--border);border-radius:6px;overflow:hidden;background:var(--bg-code,#f8fafc);">
                <pre id="slurm-config-linenos"
                    style="margin:0;padding:10px 6px 10px 10px;font-family:monospace;font-size:13px;
                           line-height:1.5;color:var(--text-secondary);background:var(--bg-secondary);
                           user-select:none;text-align:right;min-width:40px;border-right:1px solid var(--border);
                           overflow:hidden;flex-shrink:0;">1</pre>
                <textarea id="slurm-config-content"
                    style="flex:1;min-height:400px;font-family:monospace;font-size:13px;padding:10px;
                           border:none;outline:none;background:var(--bg-code,#f8fafc);
                           color:var(--text);resize:vertical;box-sizing:border-box;
                           line-height:1.5;tab-size:4;">${escHtml(config.content)}</textarea>
            </div>
            ${varHints}
            <div style="margin-top:10px;display:flex;align-items:center;gap:10px;flex-wrap:wrap;">
                <input type="text" id="slurm-config-message" placeholder="Version message (optional)"
                    style="flex:1;min-width:200px;padding:6px 10px;border:1px solid var(--border);border-radius:6px;font-size:13px;
                           background:var(--bg-input,#fff);color:var(--text);">
                <button id="slurm-save-config-btn" class="btn btn-primary" style="font-size:13px;padding:6px 16px;">Save New Version</button>
            </div>
            <div id="slurm-validate-result" style="display:none;margin-top:10px;"></div>
            <div style="margin-top:16px;border-top:1px solid var(--border);padding-top:14px;">
                <div style="font-size:13px;font-weight:600;margin-bottom:8px;">Preview rendered output for node</div>
                <div style="display:flex;align-items:center;gap:8px;margin-bottom:8px;flex-wrap:wrap;">
                    <input type="text" id="slurm-preview-node-id" placeholder="Node ID (UUID)"
                        style="flex:1;min-width:200px;padding:6px 10px;border:1px solid var(--border);border-radius:6px;
                               font-size:13px;font-family:monospace;background:var(--bg-input,#fff);color:var(--text);">
                    <button id="slurm-preview-btn" class="btn btn-secondary" style="font-size:13px;padding:6px 14px;">Preview</button>
                </div>
                <div id="slurm-preview-result" style="display:none;"></div>
            </div>
        `);
    },

    _insertTemplateVar(taId, varText) {
        const ta = document.getElementById(taId);
        if (!ta) return;
        const start = ta.selectionStart;
        const end   = ta.selectionEnd;
        ta.value = ta.value.substring(0, start) + varText + ta.value.substring(end);
        ta.selectionStart = ta.selectionEnd = start + varText.length;
        ta.focus();
        ta.dispatchEvent(new Event('input'));
    },

    _updateLineNumbers(taId, lnId) {
        const ta = document.getElementById(taId);
        const ln = document.getElementById(lnId);
        if (!ta || !ln) return;
        const lines = ta.value.split('\n').length;
        ln.textContent = Array.from({ length: lines }, (_, i) => i + 1).join('\n');
        ln.scrollTop = ta.scrollTop;
    },

    _bindEditorEvents(filename) {
        // Dirty state tracking — warn before navigating away.
        SlurmPages._editorDirty = false;
        SlurmPages._editorOriginal = (document.getElementById('slurm-config-content')?.value || '');

        const ta = document.getElementById('slurm-config-content');
        const dirtyBadge = document.getElementById('slurm-editor-dirty-badge');
        if (ta) {
            // Line numbers.
            SlurmPages._updateLineNumbers('slurm-config-content', 'slurm-config-linenos');
            ta.addEventListener('input', () => {
                SlurmPages._updateLineNumbers('slurm-config-content', 'slurm-config-linenos');
                SlurmPages._editorDirty = (ta.value !== SlurmPages._editorOriginal);
                if (dirtyBadge) dirtyBadge.style.display = SlurmPages._editorDirty ? '' : 'none';
            });
            ta.addEventListener('scroll', () => {
                const ln = document.getElementById('slurm-config-linenos');
                if (ln) ln.scrollTop = ta.scrollTop;
            });

            // Tab key — insert 4 spaces.
            ta.addEventListener('keydown', e => {
                if (e.key === 'Tab') {
                    e.preventDefault();
                    const start = ta.selectionStart;
                    const end   = ta.selectionEnd;
                    ta.value = ta.value.substring(0, start) + '    ' + ta.value.substring(end);
                    ta.selectionStart = ta.selectionEnd = start + 4;
                    ta.dispatchEvent(new Event('input'));
                }
            });
        }

        // Navigate-away guard.
        SlurmPages._editorNavGuard = (e) => {
            if (SlurmPages._editorDirty) {
                e.preventDefault();
                return (e.returnValue = 'You have unsaved changes in the config editor. Leave anyway?');
            }
        };
        window.addEventListener('beforeunload', SlurmPages._editorNavGuard);

        const btn = document.getElementById('slurm-save-config-btn');
        if (btn) {
            btn.addEventListener('click', async () => {
                const content = document.getElementById('slurm-config-content').value;
                const message = document.getElementById('slurm-config-message').value.trim();
                const validateEl = document.getElementById('slurm-validate-result');
                if (!content.trim()) { App.toast('Content cannot be empty', 'error'); return; }

                try {
                    btn.disabled = true;
                    btn.textContent = 'Validating…';
                    if (validateEl) { validateEl.style.display = 'none'; validateEl.innerHTML = ''; }

                    // B5-2: validate before save — show inline errors and block save if issues found.
                    let validationPassed = true;
                    try {
                        const vr = await API.slurm.validateConfig(filename, { content });
                        if (vr && !vr.valid && vr.issues && vr.issues.length > 0) {
                            validationPassed = false;
                            if (validateEl) {
                                const issueRows = vr.issues.map(issue => {
                                    const loc = issue.line > 0 ? `<span style="font-family:monospace;font-size:11px;color:var(--text-secondary);">line ${issue.line}</span> ` : '';
                                    const key = issue.key ? `<strong>${escHtml(issue.key)}</strong>: ` : '';
                                    return `<div style="display:flex;gap:6px;align-items:flex-start;padding:4px 0;border-bottom:1px solid rgba(220,38,38,0.12);">
                                        <span style="color:#dc2626;font-size:14px;flex-shrink:0;">&#9888;</span>
                                        <span>${loc}${key}${escHtml(issue.message)}</span>
                                    </div>`;
                                }).join('');
                                validateEl.innerHTML = `
                                    <div style="background:#fef2f2;border:1px solid #fca5a5;border-radius:6px;padding:10px 14px;">
                                        <div style="font-weight:600;font-size:13px;color:#991b1b;margin-bottom:6px;">Validation issues — fix before saving:</div>
                                        ${issueRows}
                                    </div>`;
                                validateEl.style.display = '';
                            }
                        } else if (vr && vr.valid && validateEl) {
                            validateEl.style.display = 'none';
                            validateEl.innerHTML = '';
                        }
                    } catch (_) {
                        // Validation endpoint unreachable or returned an error — allow save to proceed.
                        validationPassed = true;
                    }

                    if (!validationPassed) {
                        btn.disabled = false;
                        btn.textContent = 'Save New Version';
                        return;
                    }

                    btn.textContent = 'Saving…';
                    const result = await API.slurm.saveConfig(filename, { content, message });
                    App.toast(`Saved as version ${result.version}`, 'success');
                    SlurmPages._editorDirty = false;
                    window.removeEventListener('beforeunload', SlurmPages._editorNavGuard);
                    SlurmPages.configEditor(filename);
                } catch (err) {
                    App.toast('Save failed: ' + err.message, 'error');
                } finally {
                    btn.disabled = false;
                    btn.textContent = 'Save New Version';
                }
            });
        }

        // Preview button — renders the saved template for a specific node ID.
        const previewBtn = document.getElementById('slurm-preview-btn');
        const previewResult = document.getElementById('slurm-preview-result');
        if (previewBtn && previewResult) {
            previewBtn.addEventListener('click', async () => {
                const nodeId = (document.getElementById('slurm-preview-node-id')?.value || '').trim();
                if (!nodeId) { App.toast('Enter a Node ID to preview', 'error'); return; }
                previewBtn.disabled = true;
                previewBtn.textContent = 'Loading…';
                previewResult.style.display = '';
                previewResult.innerHTML = '<div class="loading" style="padding:8px 0"><div class="spinner" style="width:14px;height:14px;display:inline-block;margin-right:6px"></div>Rendering…</div>';
                try {
                    const data = await API.slurm.renderPreview(filename, nodeId);
                    previewResult.innerHTML = `
                        <div style="font-size:12px;color:var(--text-secondary);margin-bottom:6px;">
                            Rendered for node <code style="font-family:monospace">${escHtml(nodeId)}</code>
                            &mdash; checksum: <code style="font-family:monospace">${escHtml((data.checksum||'').substring(0,16))}…</code>
                        </div>
                        <pre style="background:var(--bg-code,#f8fafc);border:1px solid var(--border);border-radius:6px;
                                    padding:12px;font-size:12px;overflow:auto;max-height:400px;white-space:pre-wrap;
                                    word-break:break-word;">${escHtml(data.rendered_content || '')}</pre>`;
                } catch (err) {
                    previewResult.innerHTML = `<div class="alert alert-error" style="margin:0;">Preview failed: ${escHtml(err.message)}</div>`;
                } finally {
                    previewBtn.disabled = false;
                    previewBtn.textContent = 'Preview';
                }
            });
        }
    },

    // ── Config history page ────────────────────────────────────────────────

    async configHistory(filename) {
        App.render(loading('Loading history for ' + escHtml(filename) + '…'));
        try {
            const data = await API.slurm.configHistory(filename);
            App.render(SlurmPages._historyHtml(filename, data.history || []));
        } catch (err) {
            App.render(alertBox('Failed to load history: ' + err.message));
        }
    },

    _historyHtml(filename, history) {
        if (!history.length) {
            return cardWrap(`History: ${escHtml(filename)}`, emptyState('No versions found.'));
        }

        const rows = history.map(h => `
            <tr>
                <td style="padding:10px 12px;font-size:13px;">v${h.version}</td>
                <td style="padding:10px 12px;font-size:12px;color:var(--text-secondary);">${h.authored_by ? escHtml(h.authored_by) : '—'}</td>
                <td style="padding:10px 12px;font-size:13px;">${h.message ? escHtml(h.message) : '—'}</td>
                <td style="padding:10px 12px;font-size:12px;color:var(--text-secondary);">${h.created_at ? new Date(h.created_at * 1000).toLocaleString() : '—'}</td>
                <td style="padding:10px 12px;font-family:monospace;font-size:12px;">${h.checksum ? h.checksum.substring(0, 12) + '…' : '—'}</td>
            </tr>
        `).join('');

        return cardWrap(`History: ${escHtml(filename)}`, `
            <div style="margin-bottom:12px;">
                <a href="#/slurm/configs/${encodeURIComponent(filename)}" style="font-size:13px;color:var(--accent);">Back to editor</a>
            </div>
            <table style="width:100%;border-collapse:collapse;">
                <thead>
                    <tr style="border-bottom:1px solid var(--border);">
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Version</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Author</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Message</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Created</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Checksum</th>
                    </tr>
                </thead>
                <tbody>${rows}</tbody>
            </table>
        `);
    },

    // ── Push panel page ────────────────────────────────────────────────────

    async push() {
        App.render(loading('Loading Slurm push panel…'));
        try {
            const [status, configs, scriptsData] = await Promise.all([
                API.slurm.status(),
                API.slurm.listConfigs(),
                API.slurm.listScripts().catch(() => ({ scripts: [] })),
            ]);
            App.render(SlurmPages._pushHtml(status, configs.configs || [], scriptsData.scripts || []));
            SlurmPages._bindPushEvents();
        } catch (err) {
            App.render(alertBox('Failed to load push panel: ' + err.message));
        }
    },

    _pushHtml(status, configs, scripts) {
        const fileCheckboxes = configs.map(c => `
            <label style="display:flex;align-items:center;gap:8px;font-size:13px;cursor:pointer;margin-bottom:6px;">
                <input type="checkbox" name="push-file" value="${escHtml(c.filename)}" checked style="width:14px;height:14px;">
                <span style="font-family:monospace;flex:1;">${escHtml(c.filename)}</span>
                <span style="font-size:11px;color:var(--text-secondary);background:var(--bg-secondary);padding:1px 6px;border-radius:10px;">v${c.version}</span>
            </label>
        `).join('');

        // Only show scripts that have content saved.
        const availableScripts = (scripts || []).filter(s => s.has_content && s.enabled);
        const scriptCheckboxes = availableScripts.length > 0
            ? availableScripts.map(s => `
                <label style="display:flex;align-items:center;gap:8px;font-size:13px;cursor:pointer;margin-bottom:6px;">
                    <input type="checkbox" name="push-script" value="${escHtml(s.script_type)}" checked style="width:14px;height:14px;">
                    <span style="font-family:monospace;flex:1;">${escHtml(s.script_type)}</span>
                    <span style="font-size:11px;color:var(--text-secondary);background:var(--bg-secondary);padding:1px 6px;border-radius:10px;">v${s.version}</span>
                </label>
            `).join('')
            : '<div style="font-size:13px;color:var(--text-secondary);padding:4px 0;">No enabled scripts with saved content</div>';

        const connectedNodes = status.connected_nodes || [];
        const nodeOptions = connectedNodes.map(n =>
            `<option value="${escHtml(n)}">${escHtml(n)}</option>`
        ).join('');

        return cardWrap('Push Slurm Configs', `
            <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:24px;max-width:1100px;">
                <!-- Files -->
                <div>
                    <h3 style="font-size:13px;font-weight:600;margin:0 0 10px;color:var(--text-secondary);">FILES TO PUSH</h3>
                    <div style="border:1px solid var(--border);border-radius:6px;padding:12px;background:var(--bg);">
                        <label style="display:flex;align-items:center;gap:8px;font-size:13px;cursor:pointer;margin-bottom:10px;padding-bottom:10px;border-bottom:1px solid var(--border);">
                            <input type="checkbox" id="push-all-files" checked style="width:14px;height:14px;">
                            <span style="font-weight:600;">All files</span>
                        </label>
                        <div id="push-file-list">${fileCheckboxes}</div>
                    </div>
                </div>
                <!-- Scripts -->
                <div>
                    <h3 style="font-size:13px;font-weight:600;margin:0 0 10px;color:var(--text-secondary);">SCRIPTS TO PUSH</h3>
                    <div style="border:1px solid var(--border);border-radius:6px;padding:12px;background:var(--bg);">
                        <div style="font-size:12px;color:var(--text-secondary);margin-bottom:8px;">Only enabled scripts with saved content are listed</div>
                        <div id="push-script-list">${scriptCheckboxes}</div>
                    </div>
                </div>
                <!-- Options -->
                <div>
                    <h3 style="font-size:13px;font-weight:600;margin:0 0 10px;color:var(--text-secondary);">OPTIONS</h3>
                    <div style="display:flex;flex-direction:column;gap:14px;">
                        <div>
                            <label style="display:block;font-size:13px;font-weight:500;margin-bottom:4px;">Apply Action</label>
                            <select id="push-apply-action" style="width:100%;padding:8px;border:1px solid var(--border);border-radius:6px;background:var(--bg);color:var(--text-primary);font-size:13px;"
                                onchange="SlurmPages._onApplyActionChange(this)">
                                <option value="reconfigure">Reconfigure (scontrol reconfigure)</option>
                                <option value="restart">Restart (systemctl restart slurmd)</option>
                            </select>
                            <div id="push-action-hint" style="font-size:12px;margin-top:4px;color:#16a34a;">
                                Safe — no job disruption
                            </div>
                        </div>
                        <div>
                            <label style="display:block;font-size:13px;font-weight:500;margin-bottom:4px;">
                                Target Nodes
                                <span style="font-weight:400;color:var(--text-secondary);">(${connectedNodes.length} connected)</span>
                            </label>
                            ${connectedNodes.length > 0 ? `
                                <select id="push-target-nodes" multiple
                                    style="width:100%;height:100px;padding:8px;border:1px solid var(--border);border-radius:6px;background:var(--bg);color:var(--text-primary);font-size:13px;">
                                    ${nodeOptions}
                                </select>
                                <div style="font-size:12px;color:var(--text-secondary);margin-top:4px;">Leave all unselected to push to all nodes</div>
                            ` : `
                                <div style="padding:8px 12px;border:1px solid var(--border);border-radius:6px;font-size:13px;color:var(--text-secondary);">
                                    No nodes connected — push will record offline failures
                                </div>
                            `}
                        </div>
                        <button id="push-btn" class="btn btn-primary" style="padding:10px 20px;font-size:14px;">
                            Push Configs
                        </button>
                    </div>
                </div>
            </div>

            <!-- Push operation status -->
            <div id="push-status-area" style="margin-top:24px;display:none;">
                <h3 style="font-size:13px;font-weight:600;margin:0 0 12px;color:var(--text-secondary);">PUSH OPERATION STATUS</h3>
                <div id="push-op-detail"></div>
            </div>
        `);
    },

    _onApplyActionChange(sel) {
        const hint = document.getElementById('push-action-hint');
        if (!hint) return;
        if (sel.value === 'restart') {
            hint.textContent = 'Warning — will restart slurmd/slurmctld. Running jobs may be interrupted.';
            hint.style.color = '#f59e0b';
        } else {
            hint.textContent = 'Safe — no job disruption';
            hint.style.color = '#16a34a';
        }
    },

    _bindPushEvents() {
        // "All files" checkbox toggles individual checkboxes.
        const allChk = document.getElementById('push-all-files');
        if (allChk) {
            allChk.addEventListener('change', () => {
                document.querySelectorAll('input[name="push-file"]').forEach(chk => {
                    chk.checked = allChk.checked;
                });
            });
            document.querySelectorAll('input[name="push-file"]').forEach(chk => {
                chk.addEventListener('change', () => {
                    const all = document.querySelectorAll('input[name="push-file"]');
                    const checked = document.querySelectorAll('input[name="push-file"]:checked');
                    allChk.checked = all.length === checked.length;
                    allChk.indeterminate = checked.length > 0 && checked.length < all.length;
                });
            });
        }

        const pushBtn = document.getElementById('push-btn');
        if (!pushBtn) return;

        pushBtn.addEventListener('click', async () => {
            const filenames = Array.from(
                document.querySelectorAll('input[name="push-file"]:checked')
            ).map(chk => chk.value);

            const scriptTypes = Array.from(
                document.querySelectorAll('input[name="push-script"]:checked')
            ).map(chk => chk.value);

            const applyAction = document.getElementById('push-apply-action')?.value || 'reconfigure';

            const nodeSelect = document.getElementById('push-target-nodes');
            const nodeIds = nodeSelect
                ? Array.from(nodeSelect.selectedOptions).map(o => o.value)
                : [];

            if (applyAction === 'restart') {
                const confirmed = confirm(
                    'Restart action selected.\n\n' +
                    'This will restart slurmd/slurmctld on target nodes. ' +
                    'Running jobs may be interrupted.\n\n' +
                    'Continue?'
                );
                if (!confirmed) return;
            }

            const body = { filenames, apply_action: applyAction };
            if (scriptTypes.length > 0) body.script_types = scriptTypes;
            if (nodeIds.length > 0) body.node_ids = nodeIds;

            pushBtn.disabled = true;
            pushBtn.textContent = 'Pushing…';

            const statusArea = document.getElementById('push-status-area');
            const opDetail = document.getElementById('push-op-detail');
            statusArea.style.display = '';
            opDetail.innerHTML = '<div style="color:var(--text-secondary);font-size:13px;">Starting push operation…</div>';

            try {
                const op = await API.slurm.push(body);
                opDetail.innerHTML = SlurmPages._pushOpDetailHtml(op);
                App.toast('Push operation started: ' + op.id.slice(0, 8));

                // Poll until terminal status.
                SlurmPages._pollPushOp(op.id, opDetail);
            } catch (err) {
                opDetail.innerHTML = alertBox('Push failed: ' + err.message);
                App.toast('Push failed: ' + err.message, 'error');
            } finally {
                pushBtn.disabled = false;
                pushBtn.textContent = 'Push Configs';
            }
        });
    },

    async _pollPushOp(opId, container) {
        const terminalStatuses = new Set(['completed', 'partial', 'failed']);
        let attempts = 0;
        const maxAttempts = 90; // 90 × 2s = 3min polling window

        const poll = async () => {
            if (attempts++ >= maxAttempts) {
                container.innerHTML = alertBox('Push operation polling timed out. Check status manually.');
                return;
            }
            try {
                const op = await API.slurm.pushOpStatus(opId);
                container.innerHTML = SlurmPages._pushOpDetailHtml(op);
                if (!terminalStatuses.has(op.status)) {
                    setTimeout(poll, 2000);
                }
            } catch (err) {
                container.innerHTML = alertBox('Failed to poll push status: ' + err.message);
            }
        };

        setTimeout(poll, 2000);
    },

    _pushOpDetailHtml(op) {
        const statusColors = {
            completed: 'badge-ready',
            partial:   'badge-warning',
            failed:    'badge-error',
            in_progress: 'badge-neutral',
            pending:   'badge-neutral',
        };

        const spinning = (op.status === 'in_progress' || op.status === 'pending')
            ? ' <span style="font-size:12px;color:var(--text-secondary);">polling…</span>'
            : '';

        let html = `
            <div style="background:var(--bg-secondary);border:1px solid var(--border);border-radius:8px;padding:16px;margin-bottom:16px;">
                <div style="display:flex;align-items:center;gap:12px;margin-bottom:12px;flex-wrap:wrap;">
                    <span style="font-size:13px;font-family:monospace;color:var(--text-secondary);">Op ID: ${escHtml(op.id)}</span>
                    <span class="badge ${statusColors[op.status] || 'badge-neutral'}">${escHtml(op.status)}</span>
                    ${spinning}
                </div>
                <div style="display:flex;gap:24px;font-size:13px;margin-bottom:12px;flex-wrap:wrap;">
                    <div><span style="color:var(--text-secondary);">Files: </span>${(op.filenames || []).map(escHtml).join(', ')}</div>
                    <div><span style="color:var(--text-secondary);">Action: </span>${escHtml(op.apply_action)}</div>
                    <div><span style="color:var(--text-secondary);">Nodes: </span>${op.node_count}</div>
                    <div><span style="color:var(--text-secondary);color:#16a34a;">Success: </span><strong style="color:#16a34a;">${op.success_count}</strong></div>
                    <div><span style="color:var(--text-secondary);">Failed: </span><strong style="${op.failure_count > 0 ? 'color:#dc2626;' : ''}">${op.failure_count}</strong></div>
                </div>
        `;

        // Per-node results table.
        const results = op.node_results || {};
        const nodeIds = Object.keys(results);
        if (nodeIds.length > 0) {
            const rows = nodeIds.map(nodeId => {
                const r = results[nodeId];
                const statusBadge = r.ok
                    ? '<span class="badge badge-ready">success</span>'
                    : '<span class="badge badge-error">failed</span>';
                const errText = r.error ? `<div style="font-size:12px;color:#dc2626;margin-top:4px;">${escHtml(r.error)}</div>` : '';
                const fileRow = (r.file_results || []).map(fr =>
                    `<span style="font-family:monospace;font-size:12px;color:${fr.ok ? '#16a34a' : '#dc2626'};">${escHtml(fr.filename)}</span>`
                ).join(' ');
                const applyRow = r.apply_result && r.apply_result.output
                    ? `<details style="margin-top:4px;"><summary style="font-size:12px;cursor:pointer;color:var(--text-secondary);">Apply output</summary><pre style="font-size:11px;margin:4px 0;white-space:pre-wrap;max-height:120px;overflow-y:auto;">${escHtml(r.apply_result.output)}</pre></details>`
                    : '';

                return `
                    <tr>
                        <td style="padding:8px 12px;font-family:monospace;font-size:12px;">${escHtml(nodeId)}</td>
                        <td style="padding:8px 12px;">${statusBadge}</td>
                        <td style="padding:8px 12px;font-size:13px;">${fileRow}</td>
                        <td style="padding:8px 12px;">${errText}${applyRow}</td>
                    </tr>
                `;
            }).join('');

            html += `
                <table style="width:100%;border-collapse:collapse;margin-top:8px;">
                    <thead>
                        <tr style="border-bottom:1px solid var(--border);">
                            <th style="text-align:left;padding:6px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Node</th>
                            <th style="text-align:left;padding:6px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Status</th>
                            <th style="text-align:left;padding:6px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Files</th>
                            <th style="text-align:left;padding:6px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Details</th>
                        </tr>
                    </thead>
                    <tbody>${rows}</tbody>
                </table>
            `;
        } else if (op.status === 'in_progress' || op.status === 'pending') {
            html += `<div style="font-size:13px;color:var(--text-secondary);margin-top:8px;">Waiting for node results…</div>`;
        }

        html += '</div>';
        return html;
    },

    // ── Scripts list page ──────────────────────────────────────────────────

    async scripts() {
        App.render(loading('Loading Slurm scripts…'));
        try {
            const data = await API.slurm.listScripts();
            App.render(SlurmPages._scriptsHtml(data.scripts || []));
        } catch (err) {
            App.render(alertBox('Failed to load scripts: ' + err.message));
        }
    },

    _scriptsHtml(scripts) {
        const rows = scripts.map(s => {
            const enabledBadge = s.enabled
                ? '<span class="badge badge-ready">enabled</span>'
                : '<span class="badge badge-neutral">disabled</span>';
            const versionText = s.has_content
                ? `v${s.version}`
                : '<span style="color:var(--text-secondary)">—</span>';
            const checksumText = s.checksum
                ? `<code style="font-size:11px;">${s.checksum.substring(0, 12)}…</code>`
                : '—';

            return `
                <tr>
                    <td style="padding:10px 12px;font-family:monospace;font-size:13px;">
                        <a href="#/slurm/scripts/${encodeURIComponent(s.script_type)}" style="color:var(--accent);">${escHtml(s.script_type)}</a>
                    </td>
                    <td style="padding:10px 12px;">${enabledBadge}</td>
                    <td style="padding:10px 12px;font-size:13px;">${versionText}</td>
                    <td style="padding:10px 12px;font-size:12px;color:var(--text-secondary);">${checksumText}</td>
                    <td style="padding:10px 12px;font-family:monospace;font-size:12px;">${s.dest_path ? escHtml(s.dest_path) : '<span style="color:var(--text-secondary)">—</span>'}</td>
                    <td style="padding:10px 12px;">
                        <a href="#/slurm/scripts/${encodeURIComponent(s.script_type)}" class="btn btn-sm" style="font-size:12px;padding:3px 10px;">Edit</a>
                    </td>
                </tr>
            `;
        }).join('');

        return cardWrap('Slurm Scripts', `
            <p style="font-size:13px;color:var(--text-secondary);margin:0 0 16px;">
                Slurm hook scripts (Prolog, Epilog, HealthCheckProgram, etc.) managed by clustr.
                Scripts are pushed with 0755 permissions and do not require <code>scontrol reconfigure</code> unless the path in slurm.conf changes.
            </p>
            <table style="width:100%;border-collapse:collapse;">
                <thead>
                    <tr style="border-bottom:1px solid var(--border);">
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Script Type</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Status</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Version</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Checksum</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Dest Path</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;"></th>
                    </tr>
                </thead>
                <tbody>${rows}</tbody>
            </table>
        `);
    },

    // ── Script editor page ─────────────────────────────────────────────────

    async scriptEditor(scriptType) {
        App.render(loading('Loading ' + escHtml(scriptType) + '…'));
        try {
            // Load script content (may 404 if no version saved yet) and config in parallel.
            const [scriptCfgs] = await Promise.all([
                API.slurm.listScriptConfigs(),
            ]);

            // Find this script's config.
            const cfgs = scriptCfgs.configs || [];
            const cfg = cfgs.find(c => c.script_type === scriptType) || { script_type: scriptType, dest_path: '', enabled: false };

            let script = null;
            try {
                script = await API.slurm.getScript(scriptType);
            } catch (_) {
                // No version yet — show empty editor.
            }

            App.render(SlurmPages._scriptEditorHtml(scriptType, script, cfg));
            SlurmPages._bindScriptEditorEvents(scriptType, cfg);
        } catch (err) {
            App.render(alertBox('Failed to load script: ' + err.message));
        }
    },

    _scriptEditorHtml(scriptType, script, cfg) {
        const version = script ? script.version : 0;
        const content = script ? script.content : '#!/bin/bash\n# Slurm ' + scriptType + ' script\n# Exits 0 to allow the job, non-zero to deny.\n';
        const destPath = cfg.dest_path || script?.dest_path || '';
        const enabled = cfg.enabled;

        const historyLink = script
            ? `<a href="#/slurm/scripts/${encodeURIComponent(scriptType)}/history" style="font-size:13px;color:var(--accent);">View history</a>`
            : '';

        const versionNote = version > 0
            ? `<span style="font-size:13px;color:var(--text-secondary);">Current version: <strong>v${version}</strong></span>`
            : `<span style="font-size:13px;color:var(--text-secondary);">No version saved yet</span>`;

        return cardWrap(`Script: ${escHtml(scriptType)}`, `
            <div style="margin-bottom:12px;display:flex;align-items:center;gap:12px;flex-wrap:wrap;">
                ${versionNote}
                ${historyLink}
                <a href="#/slurm/scripts" style="font-size:13px;color:var(--accent);margin-left:auto;">Back to list</a>
            </div>

            <!-- Config section: dest_path + enable/disable -->
            <div style="background:var(--bg-secondary);border:1px solid var(--border);border-radius:8px;padding:14px;margin-bottom:14px;">
                <div style="font-size:13px;font-weight:600;margin-bottom:10px;">Configuration</div>
                <div style="display:flex;align-items:center;gap:12px;flex-wrap:wrap;">
                    <div style="flex:1;min-width:200px;">
                        <label style="display:block;font-size:12px;color:var(--text-secondary);margin-bottom:4px;">Destination Path</label>
                        <input type="text" id="script-dest-path"
                            value="${escHtml(destPath)}"
                            placeholder="e.g. /etc/slurm/prolog.sh"
                            style="width:100%;padding:6px 10px;border:1px solid var(--border);border-radius:6px;font-size:13px;font-family:monospace;background:var(--bg-input,#fff);color:var(--text);box-sizing:border-box;">
                    </div>
                    <div style="padding-top:18px;">
                        <label style="display:flex;align-items:center;gap:8px;font-size:13px;cursor:pointer;">
                            <input type="checkbox" id="script-enabled" ${enabled ? 'checked' : ''} style="width:14px;height:14px;">
                            <span>Enabled</span>
                        </label>
                    </div>
                    <div style="padding-top:18px;">
                        <button id="script-save-config-btn" class="btn btn-secondary" style="font-size:13px;padding:6px 14px;">Save Config</button>
                    </div>
                </div>
            </div>

            <!-- Script content editor -->
            <textarea id="script-content"
                style="width:100%;min-height:400px;font-family:monospace;font-size:13px;padding:10px;
                       border:1px solid var(--border);border-radius:6px;background:var(--bg-code,#f8fafc);
                       color:var(--text);resize:vertical;box-sizing:border-box;
                       tab-size:4;">${escHtml(content)}</textarea>
            <div style="margin-top:10px;display:flex;align-items:center;gap:10px;">
                <input type="text" id="script-message" placeholder="Version message (optional)"
                    style="flex:1;padding:6px 10px;border:1px solid var(--border);border-radius:6px;font-size:13px;
                           background:var(--bg-input,#fff);color:var(--text);">
                <button id="script-save-btn" class="btn btn-primary" style="font-size:13px;padding:6px 16px;">Save New Version</button>
            </div>
            <div style="margin-top:10px;font-size:12px;color:var(--text-secondary);">
                Scripts must start with a shebang line (e.g. <code>#!/bin/bash</code>).
                The script is pushed as an executable file (mode 0755) to the configured destination path.
            </div>
        `);
    },

    _bindScriptEditorEvents(scriptType, initialCfg) {
        // Tab support in textarea.
        const ta = document.getElementById('script-content');
        if (ta) {
            ta.addEventListener('keydown', e => {
                if (e.key === 'Tab') {
                    e.preventDefault();
                    const start = ta.selectionStart;
                    const end = ta.selectionEnd;
                    ta.value = ta.value.substring(0, start) + '\t' + ta.value.substring(end);
                    ta.selectionStart = ta.selectionEnd = start + 1;
                }
            });
        }

        // Save config (dest_path + enabled).
        const cfgBtn = document.getElementById('script-save-config-btn');
        if (cfgBtn) {
            cfgBtn.addEventListener('click', async () => {
                const destPath = (document.getElementById('script-dest-path')?.value || '').trim();
                const enabled = document.getElementById('script-enabled')?.checked ?? false;
                if (!destPath) { App.toast('Destination path is required', 'error'); return; }
                cfgBtn.disabled = true;
                cfgBtn.textContent = 'Saving…';
                try {
                    await API.slurm.setScriptConfig(scriptType, { dest_path: destPath, enabled });
                    App.toast('Script config saved', 'success');
                } catch (err) {
                    App.toast('Failed to save config: ' + err.message, 'error');
                } finally {
                    cfgBtn.disabled = false;
                    cfgBtn.textContent = 'Save Config';
                }
            });
        }

        // Save script content as new version.
        const saveBtn = document.getElementById('script-save-btn');
        if (saveBtn) {
            saveBtn.addEventListener('click', async () => {
                const content = document.getElementById('script-content')?.value || '';
                const destPath = (document.getElementById('script-dest-path')?.value || '').trim();
                const message = (document.getElementById('script-message')?.value || '').trim();
                if (!content.trim()) { App.toast('Script content cannot be empty', 'error'); return; }
                if (!destPath) { App.toast('Destination path is required before saving content', 'error'); return; }
                saveBtn.disabled = true;
                saveBtn.textContent = 'Saving…';
                try {
                    const result = await API.slurm.saveScript(scriptType, { content, dest_path: destPath, message });
                    App.toast(`Saved as version ${result.version}`, 'success');
                    SlurmPages.scriptEditor(scriptType);
                } catch (err) {
                    App.toast('Save failed: ' + err.message, 'error');
                } finally {
                    saveBtn.disabled = false;
                    saveBtn.textContent = 'Save New Version';
                }
            });
        }
    },

    // ── Script history page ────────────────────────────────────────────────

    async scriptHistory(scriptType) {
        App.render(loading('Loading history for ' + escHtml(scriptType) + '…'));
        try {
            const data = await API.slurm.scriptHistory(scriptType);
            App.render(SlurmPages._scriptHistoryHtml(scriptType, data.history || []));
        } catch (err) {
            App.render(alertBox('Failed to load script history: ' + err.message));
        }
    },

    _scriptHistoryHtml(scriptType, history) {
        if (!history.length) {
            return cardWrap(`History: ${escHtml(scriptType)}`, emptyState('No versions found.'));
        }

        const rows = history.map(h => `
            <tr>
                <td style="padding:10px 12px;font-size:13px;">v${h.version}</td>
                <td style="padding:10px 12px;font-size:12px;color:var(--text-secondary);">${h.authored_by ? escHtml(h.authored_by) : '—'}</td>
                <td style="padding:10px 12px;font-size:13px;">${h.message ? escHtml(h.message) : '—'}</td>
                <td style="padding:10px 12px;font-size:12px;color:var(--text-secondary);">${h.created_at ? new Date(h.created_at * 1000).toLocaleString() : '—'}</td>
                <td style="padding:10px 12px;font-family:monospace;font-size:12px;">${h.checksum ? h.checksum.substring(0, 12) + '…' : '—'}</td>
                <td style="padding:10px 12px;font-family:monospace;font-size:12px;">${h.dest_path ? escHtml(h.dest_path) : '—'}</td>
            </tr>
        `).join('');

        return cardWrap(`History: ${escHtml(scriptType)}`, `
            <div style="margin-bottom:12px;">
                <a href="#/slurm/scripts/${encodeURIComponent(scriptType)}" style="font-size:13px;color:var(--accent);">Back to editor</a>
                &nbsp;|&nbsp;
                <a href="#/slurm/scripts" style="font-size:13px;color:var(--accent);">All scripts</a>
            </div>
            <table style="width:100%;border-collapse:collapse;">
                <thead>
                    <tr style="border-bottom:1px solid var(--border);">
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Version</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Author</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Message</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Created</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Checksum</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Dest Path</th>
                    </tr>
                </thead>
                <tbody>${rows}</tbody>
            </table>
        `);
    },

    // ── Builds page ────────────────────────────────────────────────────────

    async builds() {
        App.render(loading('Loading Slurm builds…'));
        try {
            const [buildsResp, matrixResp] = await Promise.all([
                API.slurm.listBuilds().catch(() => ({ builds: [] })),
                API.slurm.listDepMatrix().catch(() => ({ matrix: [] })),
            ]);
            const builds = (buildsResp && buildsResp.builds) ? buildsResp.builds : [];
            const matrix = (matrixResp && matrixResp.matrix) ? matrixResp.matrix : [];
            App.render(SlurmPages._buildsHtml(builds, matrix));
            SlurmPages._bindBuildsEvents();
        } catch (err) {
            App.render(alertBox('Failed to load builds: ' + err.message));
        }
    },

    _buildsHtml(builds, matrix) {
        const statusColors = {
            building:  'badge-neutral',
            completed: 'badge-ready',
            failed:    'badge-error',
        };

        const buildRows = builds.length === 0
            ? `<tr><td colspan="6" style="padding:20px;text-align:center;color:var(--text-secondary);font-size:13px;">No builds yet. Use the form below to start one.</td></tr>`
            : builds.map(b => `
                <tr data-build-id="${escHtml(b.id)}">
                    <td style="padding:10px 12px;font-size:13px;font-family:monospace;">${escHtml(b.version)}</td>
                    <td style="padding:10px 12px;font-size:13px;">${escHtml(b.arch)}</td>
                    <td style="padding:10px 12px;">
                        <span class="badge ${statusColors[b.status] || 'badge-neutral'}">${escHtml(b.status)}</span>
                        ${b.is_active ? '<span class="badge badge-ready" style="margin-left:4px;">Active</span>' : ''}
                    </td>
                    <td style="padding:10px 12px;font-size:12px;color:var(--text-secondary);">${b.started_at ? fmtDate(b.started_at) : '—'}</td>
                    <td style="padding:10px 12px;font-size:12px;color:var(--text-secondary);">${b.artifact_size ? SlurmPages._fmtBytes(b.artifact_size) : '—'}</td>
                    <td style="padding:10px 12px;white-space:nowrap;">
                        ${b.status === 'building'
                            ? `<button class="btn btn-sm" onclick="SlurmPages._viewBuildLogs('${escHtml(b.id)}')">Logs</button>`
                            : ''}
                        ${b.status === 'completed' && !b.is_active
                            ? `<button class="btn btn-primary btn-sm" style="margin-left:4px;" onclick="SlurmPages._setActiveBuild('${escHtml(b.id)}')">Set Active</button>`
                            : ''}
                        ${b.status === 'completed'
                            ? `<button class="btn btn-sm" style="margin-left:4px;" onclick="SlurmPages._viewBuildLogs('${escHtml(b.id)}')">Logs</button>`
                            : ''}
                        ${!b.is_active
                            ? `<button class="btn btn-danger btn-sm" style="margin-left:4px;" onclick="SlurmPages._deleteBuild('${escHtml(b.id)}', '${escHtml(b.version)}')">Delete</button>`
                            : ''}
                    </td>
                </tr>
            `).join('');

        const matrixRows = matrix.length === 0
            ? `<tr><td colspan="6" style="padding:20px;text-align:center;color:var(--text-secondary);font-size:13px;">No compatibility matrix data.</td></tr>`
            : matrix.map(m => `
                <tr>
                    <td style="padding:8px 12px;font-size:13px;font-family:monospace;">${escHtml(m.dep_name)}</td>
                    <td style="padding:8px 12px;font-size:13px;font-family:monospace;">${escHtml(m.dep_version_min)} – ${escHtml(m.dep_version_max)}</td>
                    <td style="padding:8px 12px;font-size:13px;font-family:monospace;">${escHtml(m.slurm_version_min)} – ${escHtml(m.slurm_version_max)}</td>
                    <td style="padding:8px 12px;font-size:12px;color:var(--text-secondary);">${escHtml(m.source || '—')}</td>
                </tr>
            `).join('');

        return `
            ${cardWrap('Slurm Builds', `
                <table style="width:100%;border-collapse:collapse;margin-bottom:20px;">
                    <thead>
                        <tr style="border-bottom:1px solid var(--border);">
                            <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Version</th>
                            <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Arch</th>
                            <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Status</th>
                            <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Started</th>
                            <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Artifact Size</th>
                            <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Actions</th>
                        </tr>
                    </thead>
                    <tbody>${buildRows}</tbody>
                </table>

                <h3 style="font-size:14px;font-weight:600;margin:0 0 12px;">Start New Build</h3>
                <form id="slurm-new-build-form" style="display:flex;flex-direction:column;gap:10px;max-width:480px;">
                    <div>
                        <label style="display:block;font-size:13px;font-weight:500;margin-bottom:4px;">Slurm Version</label>
                        <input id="slurm-build-version" class="input" type="text" placeholder="e.g. 24.05.3" style="width:100%;" required>
                    </div>
                    <div>
                        <label style="display:block;font-size:13px;font-weight:500;margin-bottom:4px;">Architecture</label>
                        <select id="slurm-build-arch" class="input" style="width:100%;">
                            <option value="x86_64">x86_64</option>
                            <option value="aarch64">aarch64</option>
                        </select>
                    </div>
                    <div>
                        <label style="display:block;font-size:13px;font-weight:500;margin-bottom:4px;">Extra Configure Flags (optional)</label>
                        <input id="slurm-build-flags" class="input" type="text" placeholder="e.g. --with-pmix=/opt/pmix" style="width:100%;">
                    </div>
                    <div style="display:flex;align-items:center;gap:10px;margin-top:4px;">
                        <button type="submit" class="btn btn-primary btn-sm" id="slurm-new-build-btn">Start Build</button>
                        <span id="slurm-new-build-status" style="font-size:12px;color:var(--text-secondary);"></span>
                    </div>
                </form>

                <div id="slurm-build-log-section" style="display:none;margin-top:24px;">
                    <h3 style="font-size:14px;font-weight:600;margin:0 0 8px;">Build Log</h3>
                    <pre id="slurm-build-log-output" style="background:var(--bg-secondary);border:1px solid var(--border);border-radius:6px;padding:12px;font-size:12px;max-height:400px;overflow-y:auto;white-space:pre-wrap;word-break:break-all;"></pre>
                    <button class="btn btn-sm" style="margin-top:8px;" id="slurm-build-log-close">Close</button>
                </div>
            `)}
            ${cardWrap('Munge Key Management', `
                <p style="font-size:13px;color:var(--text-secondary);margin:0 0 16px;">
                    Generate or rotate the munge key distributed to all Slurm nodes.
                    The key is encrypted at rest with AES-256-GCM.
                </p>
                <div style="display:flex;gap:10px;align-items:center;flex-wrap:wrap;">
                    <button class="btn btn-primary btn-sm" id="slurm-generate-munge-btn">Generate Munge Key</button>
                    <button class="btn btn-sm" id="slurm-rotate-munge-btn">Rotate Munge Key</button>
                    <span id="slurm-munge-status" style="font-size:12px;color:var(--text-secondary);"></span>
                </div>
            `)}
            ${cardWrap('Dependency Compatibility Matrix', `
                <table style="width:100%;border-collapse:collapse;">
                    <thead>
                        <tr style="border-bottom:1px solid var(--border);">
                            <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Dependency</th>
                            <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Dep Version Range</th>
                            <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Slurm Version Range</th>
                            <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Source</th>
                        </tr>
                    </thead>
                    <tbody>${matrixRows}</tbody>
                </table>
            `)}
        `;
    },

    _bindBuildsEvents() {
        // New build form submission.
        const form = document.getElementById('slurm-new-build-form');
        if (form) {
            form.addEventListener('submit', async (e) => {
                e.preventDefault();
                const btn    = document.getElementById('slurm-new-build-btn');
                const status = document.getElementById('slurm-new-build-status');
                const ver    = (document.getElementById('slurm-build-version').value || '').trim();
                const arch   = (document.getElementById('slurm-build-arch').value || 'x86_64').trim();
                const flags  = (document.getElementById('slurm-build-flags').value || '').trim();

                if (!ver) { status.textContent = 'Version is required.'; return; }

                btn.disabled = true;
                status.textContent = 'Starting build…';
                try {
                    const body = { version: ver, arch };
                    if (flags) body.configure_flags = flags.split(/\s+/).filter(Boolean);
                    const resp = await API.slurm.startBuild(body);
                    status.textContent = `Build started (ID: ${resp.build_id || resp.id}). Refreshing…`;
                    App.toast('Build started successfully.', 'success');
                    setTimeout(() => SlurmPages.builds(), 1500);
                } catch (err) {
                    status.textContent = 'Error: ' + err.message;
                    btn.disabled = false;
                }
            });
        }

        // Munge key generate button.
        const genBtn = document.getElementById('slurm-generate-munge-btn');
        if (genBtn) {
            genBtn.addEventListener('click', async () => {
                const status = document.getElementById('slurm-munge-status');
                genBtn.disabled = true;
                status.textContent = 'Generating…';
                try {
                    await API.slurm.generateMungeKey();
                    status.textContent = 'Munge key generated successfully.';
                    App.toast('Munge key generated.', 'success');
                } catch (err) {
                    status.textContent = 'Error: ' + err.message;
                } finally {
                    genBtn.disabled = false;
                }
            });
        }

        // Munge key rotate button.
        const rotBtn = document.getElementById('slurm-rotate-munge-btn');
        if (rotBtn) {
            rotBtn.addEventListener('click', async () => {
                const status = document.getElementById('slurm-munge-status');
                rotBtn.disabled = true;
                status.textContent = 'Rotating…';
                try {
                    await API.slurm.rotateMungeKey();
                    status.textContent = 'Munge key rotated successfully.';
                    App.toast('Munge key rotated.', 'success');
                } catch (err) {
                    status.textContent = 'Error: ' + err.message;
                } finally {
                    rotBtn.disabled = false;
                }
            });
        }

        // Build log close button.
        const closeBtn = document.getElementById('slurm-build-log-close');
        if (closeBtn) {
            closeBtn.addEventListener('click', () => {
                const section = document.getElementById('slurm-build-log-section');
                if (section) section.style.display = 'none';
            });
        }
    },

    async _viewBuildLogs(buildId) {
        const section = document.getElementById('slurm-build-log-section');
        const output  = document.getElementById('slurm-build-log-output');
        if (!section || !output) return;

        section.style.display = '';
        output.textContent = 'Loading logs…';
        output.scrollTop = output.scrollHeight;

        try {
            const resp = await API.slurm.buildLogs(buildId);
            // The server returns either a `logs` string or a `message` describing where
            // to find logs (e.g. via the SSE log stream).
            const logs = resp && resp.logs
                ? resp.logs
                : (resp && resp.message ? resp.message : '(no log output)');
            output.textContent = logs;
            output.scrollTop = output.scrollHeight;
        } catch (err) {
            output.textContent = 'Error loading logs: ' + err.message;
        }
    },

    async _setActiveBuild(buildId) {
        if (!confirm('Set this build as active? Nodes will be instructed to install it on next push.')) return;
        try {
            await API.slurm.setActiveBuild(buildId);
            App.toast('Active build updated.', 'success');
            SlurmPages.builds();
        } catch (err) {
            App.toast('Failed to set active build: ' + err.message, 'error');
        }
    },

    async _deleteBuild(buildId, version) {
        if (!confirm(`Delete build ${version}? This cannot be undone.`)) return;
        try {
            await API.slurm.deleteBuild(buildId);
            App.toast(`Build ${version} deleted.`, 'success');
            SlurmPages.builds();
        } catch (err) {
            App.toast('Failed to delete build: ' + err.message, 'error');
        }
    },

    _fmtBytes(bytes) {
        if (bytes < 1024) return bytes + ' B';
        if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
        if (bytes < 1024 * 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
        return (bytes / (1024 * 1024 * 1024)).toFixed(2) + ' GB';
    },

    // ── Sync status page ───────────────────────────────────────────────────

    async syncStatus() {
        App.render(loading('Loading sync status…'));
        try {
            const data = await API.slurm.syncStatus();
            App.render(SlurmPages._syncStatusHtml(data.drift || []));
            SlurmPages._bindSyncStatusEvents(data.drift || []);
            // Auto-refresh every 30 seconds.
            App.setAutoRefresh(() => SlurmPages.syncStatus(), 30000);
        } catch (err) {
            App.render(alertBox('Failed to load sync status: ' + err.message));
        }
    },

    _syncStatusHtml(drift) {
        if (!drift.length) {
            return cardWrap('Slurm Sync Status', emptyState('No sync data yet. Push configs to nodes to start tracking.'));
        }

        // Build summary counts.
        const nodeIds   = [...new Set(drift.map(d => d.node_id))];
        const files     = [...new Set(drift.map(d => d.filename))];
        const nodeInSync = nodeIds.filter(n => drift.filter(d => d.node_id === n).every(d => d.in_sync));
        const nodeOut   = nodeIds.filter(n => drift.filter(d => d.node_id === n).some(d => !d.in_sync));
        const totalNodes   = nodeIds.length;
        const inSyncCount  = nodeInSync.length;
        const outSyncCount = nodeOut.length;

        // Summary cards.
        const cards = `
            <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(110px,1fr));gap:12px;margin-bottom:24px;max-width:600px;">
                <div style="background:var(--bg-secondary);border:1px solid var(--border);border-radius:8px;padding:14px;text-align:center;">
                    <div style="font-size:22px;font-weight:700;">${totalNodes}</div>
                    <div style="font-size:12px;color:var(--text-secondary);margin-top:2px;">Nodes Tracked</div>
                </div>
                <div style="background:var(--bg-secondary);border:1px solid var(--border);border-radius:8px;padding:14px;text-align:center;">
                    <div style="font-size:22px;font-weight:700;color:#16a34a;">${inSyncCount}</div>
                    <div style="font-size:12px;color:var(--text-secondary);margin-top:2px;">In Sync</div>
                </div>
                <div style="background:var(--bg-secondary);border:1px solid var(--border);border-radius:8px;padding:14px;text-align:center;">
                    <div style="font-size:22px;font-weight:700;${outSyncCount > 0 ? 'color:#dc2626;' : ''}">${outSyncCount}</div>
                    <div style="font-size:12px;color:var(--text-secondary);margin-top:2px;">Out of Sync</div>
                </div>
                <div style="background:var(--bg-secondary);border:1px solid var(--border);border-radius:8px;padding:14px;text-align:center;">
                    <div style="font-size:22px;font-weight:700;">${files.length}</div>
                    <div style="font-size:12px;color:var(--text-secondary);margin-top:2px;">Managed Files</div>
                </div>
            </div>`;

        // Per-file sync matrix: rows = nodes, cols = files.
        const colHeaders = files.map(f =>
            `<th style="text-align:center;padding:8px 10px;font-size:11px;color:var(--text-secondary);font-weight:600;font-family:monospace;max-width:120px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" title="${escHtml(f)}">${escHtml(f)}</th>`
        ).join('');

        const matrixRows = nodeIds.map(nodeId => {
            const nodeDrift = drift.filter(d => d.node_id === nodeId);
            const nodeSync  = nodeDrift.every(d => d.in_sync);
            const cols = files.map(fn => {
                const entry = nodeDrift.find(d => d.filename === fn);
                if (!entry) return `<td style="text-align:center;padding:8px 10px;"><span style="color:var(--text-secondary);font-size:12px;">—</span></td>`;
                const color = entry.in_sync ? '#16a34a' : '#dc2626';
                const title = entry.in_sync
                    ? `v${entry.deployed_version} (current)`
                    : `deployed v${entry.deployed_version || 0}, current v${entry.current_version}`;
                return `<td style="text-align:center;padding:8px 10px;" title="${escHtml(title)}">
                    <span style="font-size:12px;font-weight:600;color:${color};">
                        ${entry.in_sync ? `v${entry.deployed_version}` : `v${entry.deployed_version||0}<span style="color:var(--text-secondary);">/v${entry.current_version}</span>`}
                    </span>
                </td>`;
            }).join('');
            const rowBadge = nodeSync
                ? `<span class="badge badge-ready" style="font-size:10px;">sync</span>`
                : `<span class="badge badge-error" style="font-size:10px;">drift</span>`;
            return `
                <tr style="border-bottom:1px solid var(--border);">
                    <td style="padding:8px 12px;font-family:monospace;font-size:12px;white-space:nowrap;">
                        ${escHtml(nodeId.substring(0,12))}… ${rowBadge}
                    </td>
                    ${cols}
                </tr>`;
        }).join('');

        const pushAllBtn = outSyncCount > 0 ? `
            <div style="margin-bottom:16px;">
                <button id="sync-push-all-btn" class="btn btn-primary" style="font-size:13px;padding:7px 16px;">
                    Push All Out-of-Sync (${outSyncCount} node${outSyncCount === 1 ? '' : 's'})
                </button>
            </div>` : '';

        const autoRefreshNote = `<div style="font-size:12px;color:var(--text-secondary);margin-bottom:16px;">Auto-refreshing every 30s</div>`;

        return cardWrap('Slurm Sync Status', `
            ${autoRefreshNote}
            ${cards}
            ${pushAllBtn}
            <div style="overflow-x:auto;">
                <table style="width:100%;border-collapse:collapse;min-width:400px;">
                    <thead>
                        <tr style="border-bottom:2px solid var(--border);">
                            <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Node</th>
                            ${colHeaders}
                        </tr>
                    </thead>
                    <tbody>${matrixRows}</tbody>
                </table>
            </div>
        `);
    },

    _bindSyncStatusEvents(drift) {
        const btn = document.getElementById('sync-push-all-btn');
        if (!btn) return;
        btn.addEventListener('click', () => {
            // Navigate to push page — the out-of-sync data is visible there.
            App.navigate('#/slurm/push');
        });
    },

    // ── Upgrades wizard ────────────────────────────────────────────────────

    async upgrades() {
        App.render(loading('Loading Slurm upgrades…'));
        try {
            const [buildsResp, opsResp] = await Promise.all([
                API.slurm.listBuilds().catch(() => ({ builds: [] })),
                API.slurm.listUpgrades().catch(() => ({ operations: [] })),
            ]);
            const builds    = (buildsResp && buildsResp.builds) ? buildsResp.builds : [];
            const completed = builds.filter(b => b.status === 'completed');
            const ops       = (opsResp && opsResp.operations) ? opsResp.operations : [];
            App.render(SlurmPages._upgradesHtml(completed, ops));
            SlurmPages._bindUpgradesEvents(completed);
        } catch (err) {
            App.render(alertBox('Failed to load upgrades: ' + err.message));
        }
    },

    _upgradesHtml(builds, ops) {
        const buildOpts = builds.length === 0
            ? `<option value="">No completed builds available</option>`
            : builds.map(b => `<option value="${escHtml(b.id)}">${escHtml(b.version)} (${escHtml(b.arch)})</option>`).join('');

        const opsRows = ops.length === 0
            ? `<tr><td colspan="7" style="padding:20px;text-align:center;color:var(--text-secondary);font-size:13px;">No upgrade operations yet.</td></tr>`
            : ops.map(op => {
                const statusClass = {
                    completed: 'badge-ready', failed: 'badge-error', in_progress: 'badge-info',
                    paused: 'badge-warning', rolled_back: 'badge-neutral', queued: 'badge-neutral',
                }[op.status] || 'badge-neutral';
                return `
                <tr>
                    <td style="padding:8px 12px;font-size:12px;font-family:monospace;">
                        <a href="#/slurm/upgrades/${encodeURIComponent(op.id)}" style="color:var(--accent);">${escHtml(op.id.slice(0,8))}…</a>
                    </td>
                    <td style="padding:8px 12px;font-size:12px;">${escHtml(op.status === 'in_progress' ? (op.phase || 'starting') : op.status)}</td>
                    <td style="padding:8px 12px;font-size:12px;">
                        <span class="badge ${statusClass}">${escHtml(op.status)}</span>
                    </td>
                    <td style="padding:8px 12px;font-size:12px;">${op.current_batch || 0}/${op.total_batches || 0}</td>
                    <td style="padding:8px 12px;font-size:12px;">${fmtDate(op.started_at)}</td>
                    <td style="padding:8px 12px;font-size:12px;">${op.completed_at ? fmtDate(op.completed_at) : '—'}</td>
                    <td style="padding:8px 12px;">
                        <a href="#/slurm/upgrades/${encodeURIComponent(op.id)}" class="btn btn-sm" style="font-size:11px;padding:3px 10px;">View</a>
                    </td>
                </tr>`;
            }).join('');

        return cardWrap('Slurm Rolling Upgrades', `
            <!-- Upgrade wizard form -->
            <div style="background:var(--surface-alt,var(--bg));border:1px solid var(--border);border-radius:8px;padding:20px;margin-bottom:24px;">
                <h3 style="font-size:14px;font-weight:600;margin:0 0 16px;">Start New Upgrade</h3>
                <div style="display:grid;gap:12px;max-width:520px;">
                    <div>
                        <label style="font-size:12px;color:var(--text-secondary);display:block;margin-bottom:4px;">Target Build</label>
                        <select id="upgrade-build-select" style="width:100%;padding:7px 10px;border:1px solid var(--border);border-radius:6px;background:var(--surface);color:var(--text);font-size:13px;">
                            ${buildOpts}
                        </select>
                    </div>
                    <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px;">
                        <div>
                            <label style="font-size:12px;color:var(--text-secondary);display:block;margin-bottom:4px;">Batch Size</label>
                            <input type="number" id="upgrade-batch-size" value="10" min="1" max="200"
                                style="width:100%;padding:7px 10px;border:1px solid var(--border);border-radius:6px;background:var(--surface);color:var(--text);font-size:13px;">
                        </div>
                        <div>
                            <label style="font-size:12px;color:var(--text-secondary);display:block;margin-bottom:4px;">Drain Timeout (min)</label>
                            <input type="number" id="upgrade-drain-timeout" value="30" min="1" max="240"
                                style="width:100%;padding:7px 10px;border:1px solid var(--border);border-radius:6px;background:var(--surface);color:var(--text);font-size:13px;">
                        </div>
                    </div>
                    <label style="display:flex;align-items:center;gap:8px;font-size:13px;cursor:pointer;">
                        <input type="checkbox" id="upgrade-db-backup">
                        I have confirmed SlurmDB backup before proceeding
                    </label>
                    <div style="display:flex;gap:8px;align-items:center;">
                        <button id="upgrade-validate-btn" class="btn btn-secondary" style="font-size:13px;padding:7px 16px;">Validate Plan</button>
                        <button id="upgrade-start-btn" class="btn btn-primary" style="font-size:13px;padding:7px 16px;" disabled>Start Upgrade</button>
                    </div>
                </div>
                <div id="upgrade-validation-result" style="margin-top:16px;display:none;"></div>
            </div>

            <!-- Upgrade history table -->
            <h3 style="font-size:14px;font-weight:600;margin:0 0 12px;">Upgrade History</h3>
            <table style="width:100%;border-collapse:collapse;">
                <thead>
                    <tr style="border-bottom:1px solid var(--border);">
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">ID</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Phase</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Status</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Batches</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Started</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Completed</th>
                        <th></th>
                    </tr>
                </thead>
                <tbody>${opsRows}</tbody>
            </table>
        `);
    },

    _bindUpgradesEvents(builds) {
        const validateBtn  = document.getElementById('upgrade-validate-btn');
        const startBtn     = document.getElementById('upgrade-start-btn');
        const resultDiv    = document.getElementById('upgrade-validation-result');
        let validationPassed = false;

        const getReq = () => ({
            to_build_id:       (document.getElementById('upgrade-build-select')?.value || '').trim(),
            batch_size:        parseInt(document.getElementById('upgrade-batch-size')?.value || '10', 10),
            drain_timeout_min: parseInt(document.getElementById('upgrade-drain-timeout')?.value || '30', 10),
            confirmed_db_backup: document.getElementById('upgrade-db-backup')?.checked || false,
        });

        if (validateBtn) {
            validateBtn.addEventListener('click', async () => {
                const req = getReq();
                if (!req.to_build_id) { App.toast('Select a target build first.', 'error'); return; }
                validateBtn.disabled = true;
                validateBtn.textContent = 'Validating…';
                validationPassed = false;
                if (startBtn) startBtn.disabled = true;
                try {
                    const v = await API.slurm.validateUpgrade(req);
                    resultDiv.style.display = '';
                    const warnHtml = (v.warnings || []).map(w =>
                        `<div style="font-size:12px;color:var(--warning,#f59e0b);margin-top:4px;">⚠ ${escHtml(w)}</div>`).join('');
                    const errHtml = (v.errors || []).map(e =>
                        `<div style="font-size:12px;color:var(--error,#ef4444);margin-top:4px;">✗ ${escHtml(e)}</div>`).join('');
                    const planHtml = v.upgrade_plan ? SlurmPages._upgradePlanHtml(v.upgrade_plan) : '';
                    const jobInfo  = v.job_count >= 0 ? `<div style="font-size:12px;margin-top:6px;color:var(--text-secondary);">Running jobs: <strong>${v.job_count}</strong></div>` : '';
                    const versionHtml = (v.from_version || v.to_version) ? `
                        <div style="font-size:12px;margin-bottom:8px;">
                            ${v.from_version ? `Current: <code>${escHtml(v.from_version)}</code>` : ''}
                            ${v.from_version && v.to_version ? ' → ' : ''}
                            ${v.to_version ? `Target: <code>${escHtml(v.to_version)}</code>` : ''}
                        </div>` : '';
                    resultDiv.innerHTML = `
                        <div style="padding:14px;border:1px solid var(--border);border-radius:6px;background:var(--surface);">
                            ${versionHtml}
                            <div style="font-size:13px;font-weight:600;color:${v.valid ? 'var(--success,#22c55e)' : 'var(--error,#ef4444)'};">
                                ${v.valid ? 'Validation passed' : 'Validation failed'}
                            </div>
                            ${errHtml}${warnHtml}${jobInfo}${planHtml}
                        </div>`;
                    if (v.valid) {
                        validationPassed = true;
                        if (startBtn) startBtn.disabled = false;
                    }
                } catch (err) {
                    resultDiv.style.display = '';
                    resultDiv.innerHTML = alertBox('Validation error: ' + err.message);
                } finally {
                    validateBtn.disabled = false;
                    validateBtn.textContent = 'Validate Plan';
                }
            });
        }

        if (startBtn) {
            startBtn.addEventListener('click', async () => {
                const req = getReq();
                if (!req.to_build_id) { App.toast('Select a target build first.', 'error'); return; }
                if (!confirm('Start rolling upgrade? This will drain and restart Slurm services across the cluster.')) return;
                startBtn.disabled = true;
                startBtn.textContent = 'Starting…';
                try {
                    const resp = await API.slurm.startUpgrade(req);
                    App.toast('Upgrade started (op: ' + (resp.op_id || '?') + ')', 'success');
                    if (resp.op_id) {
                        App.navigate('#/slurm/upgrades/' + resp.op_id);
                    } else {
                        SlurmPages.upgrades();
                    }
                } catch (err) {
                    App.toast('Failed to start upgrade: ' + err.message, 'error');
                    startBtn.disabled = false;
                    startBtn.textContent = 'Start Upgrade';
                }
            });
        }
    },

    _upgradePlanHtml(plan) {
        if (!plan) return '';
        const phaseRow = (label, nodes) => nodes && nodes.length > 0
            ? `<tr><td style="padding:5px 10px;font-size:12px;font-weight:600;">${label}</td>
               <td style="padding:5px 10px;font-size:12px;font-family:monospace;">${nodes.map(n => escHtml(n)).join(', ')}</td></tr>`
            : '';
        const batchRows = (plan.compute_batches || []).map((batch, i) =>
            `<tr><td style="padding:5px 10px;font-size:12px;">Compute Batch ${i+1}</td>
             <td style="padding:5px 10px;font-size:12px;font-family:monospace;">${batch.map(n => escHtml(n)).join(', ')}</td></tr>`
        ).join('');

        return `
            <div style="margin-top:12px;">
                <div style="font-size:12px;font-weight:600;margin-bottom:6px;">Upgrade Plan</div>
                <table style="border-collapse:collapse;width:100%;border:1px solid var(--border);border-radius:4px;">
                    ${phaseRow('DBD', plan.dbd_nodes)}
                    ${phaseRow('Controller', plan.controller_nodes)}
                    ${batchRows}
                    ${phaseRow('Login', plan.login_nodes)}
                </table>
            </div>`;
    },

    // ── Upgrade detail / progress ───────────────────────────────────────────

    async upgradeDetail(opId) {
        App.render(loading('Loading upgrade…'));
        try {
            const op = await API.slurm.getUpgrade(opId);
            App.render(SlurmPages._upgradeDetailHtml(op));
            SlurmPages._bindUpgradeDetailEvents(op);
            // Auto-refresh every 3s if in progress.
            if (op.status === 'in_progress' || op.status === 'queued') {
                App.setAutoRefresh(() => SlurmPages.upgradeDetail(opId), 3000);
            }
        } catch (err) {
            App.render(alertBox('Failed to load upgrade: ' + err.message));
        }
    },

    _upgradeDetailHtml(op) {
        const PHASES = ['Pre-flight', 'DBD', 'Controller', 'Compute', 'Login', 'Done'];
        const PHASE_KEYS = ['preflight', 'dbd', 'controller', 'compute', 'login', 'done'];
        const phaseIdx = Math.max(0, PHASE_KEYS.indexOf(op.phase || 'dbd'));

        // Animated step indicator.
        const stepBar = PHASES.slice(0, -1).map((label, i) => {
            const done    = i < phaseIdx;
            const current = i === phaseIdx;
            const dotColor = done ? '#22c55e' : (current ? 'var(--accent)' : 'var(--border)');
            const labelStyle = current ? 'font-weight:700;color:var(--text);' : (done ? 'color:#22c55e;' : 'color:var(--text-secondary);');
            const anim = current ? 'animation:pulse 1.5s ease-in-out infinite;' : '';
            return `<div style="display:flex;flex-direction:column;align-items:center;gap:4px;flex:1;min-width:60px;">
                <div style="width:16px;height:16px;border-radius:50%;background:${dotColor};flex-shrink:0;${anim}"></div>
                <span style="font-size:11px;text-align:center;${labelStyle}">${label}</span>
            </div>${i < PHASES.length - 2 ? `<div style="flex:1;height:2px;background:${done ? '#22c55e' : 'var(--border)'};align-self:flex-start;margin-top:7px;"></div>` : ''}`;
        }).join('');

        // Progress bar.
        const total   = op.total_batches || 1;
        const current = op.current_batch || 0;
        const pct     = Math.min(100, Math.round((current / total) * 100));

        // ETA estimate.
        let etaHtml = '';
        if (op.status === 'in_progress' && op.started_at && current > 0) {
            const elapsedMs = Date.now() - new Date(op.started_at).getTime();
            const msPerBatch = elapsedMs / current;
            const remaining = (total - current) * msPerBatch;
            const remainSecs = Math.round(remaining / 1000);
            const remMin = Math.floor(remainSecs / 60);
            const remSec = remainSecs % 60;
            etaHtml = `<span style="font-size:12px;color:var(--text-secondary);">~${remMin}m ${remSec}s remaining</span>`;
        }

        const nodeResults = op.node_results || {};
        const nodeRows = Object.entries(nodeResults).map(([nodeId, r]) => {
            const badgeClass = r.ok ? 'badge-ready' : 'badge-error';
            // Show hostname if available, else truncated UUID.
            const nodeDisplay = r.hostname ? escHtml(r.hostname) : `<span style="font-family:monospace;">${escHtml(nodeId.slice(0,12))}…</span>`;
            const buildLogLink = r.build_id
                ? `<a href="#/slurm/builds" style="font-size:11px;color:var(--accent);margin-left:6px;">View logs</a>`
                : '';
            return `
                <tr>
                    <td style="padding:8px 12px;font-size:12px;">${nodeDisplay}</td>
                    <td style="padding:8px 12px;font-size:12px;">${escHtml(r.phase || '')}</td>
                    <td style="padding:8px 12px;"><span class="badge ${badgeClass}">${r.ok ? 'OK' : 'FAIL'}</span></td>
                    <td style="padding:8px 12px;font-size:12px;font-family:monospace;">${escHtml(r.installed_version || '—')}${buildLogLink}</td>
                    <td style="padding:8px 12px;font-size:12px;color:var(--error,#ef4444);">${escHtml(r.error || '')}</td>
                </tr>`;
        }).join('');

        const statusClass = {
            completed: 'badge-ready', failed: 'badge-error', in_progress: 'badge-info',
            paused: 'badge-warning', rolled_back: 'badge-neutral', queued: 'badge-neutral',
        }[op.status] || 'badge-neutral';

        const canPause    = op.status === 'in_progress';
        const canResume   = op.status === 'paused';
        const canRollback = ['in_progress','paused','failed','completed'].includes(op.status);

        return cardWrap(`Upgrade ${op.id.slice(0,8)}…`, `
            <style>@keyframes pulse{0%,100%{opacity:1}50%{opacity:.4}}</style>
            <a href="#/slurm/upgrades" style="font-size:13px;color:var(--accent);display:block;margin-bottom:16px;">← Back to upgrades</a>

            <!-- Status header -->
            <div style="display:flex;align-items:center;gap:16px;margin-bottom:16px;flex-wrap:wrap;">
                <span class="badge ${statusClass}" style="font-size:13px;padding:4px 12px;">${escHtml(op.status)}</span>
                <span style="font-size:13px;color:var(--text-secondary);">Batch ${current} / ${total}</span>
                <span style="font-size:13px;color:var(--text-secondary);">Started: ${fmtDate(op.started_at)}</span>
                ${op.completed_at ? `<span style="font-size:13px;color:var(--text-secondary);">Completed: ${fmtDate(op.completed_at)}</span>` : ''}
                ${etaHtml}
            </div>

            <!-- Phase step indicator -->
            <div style="display:flex;align-items:flex-start;gap:0;margin-bottom:16px;background:var(--surface);border:1px solid var(--border);border-radius:8px;padding:16px 20px;">
                ${stepBar}
            </div>

            <!-- Progress bar -->
            <div style="margin-bottom:20px;">
                <div style="display:flex;justify-content:space-between;font-size:12px;color:var(--text-secondary);margin-bottom:4px;">
                    <span>${pct}% complete</span>
                    <span>${current}/${total} batches</span>
                </div>
                <div style="height:8px;background:var(--border);border-radius:4px;overflow:hidden;">
                    <div style="height:100%;width:${pct}%;background:${op.status==='failed'?'#dc2626':'var(--accent)'};border-radius:4px;transition:width .3s ease;"></div>
                </div>
            </div>

            <!-- Action buttons -->
            <div style="display:flex;gap:8px;margin-bottom:20px;flex-wrap:wrap;">
                ${canPause    ? `<button id="upgrade-pause-btn"    class="btn btn-secondary" style="font-size:13px;padding:7px 16px;">Pause</button>` : ''}
                ${canResume   ? `<button id="upgrade-resume-btn"   class="btn btn-primary"   style="font-size:13px;padding:7px 16px;">Resume</button>` : ''}
                ${canRollback ? `<button id="upgrade-rollback-btn" class="btn btn-danger"    style="font-size:13px;padding:7px 16px;">Rollback</button>` : ''}
            </div>

            <!-- Node results table -->
            ${Object.keys(nodeResults).length === 0
                ? `<p style="font-size:13px;color:var(--text-secondary);">No node results yet — upgrade is in the early phases.</p>`
                : `<div style="overflow-x:auto;">
                    <table style="width:100%;border-collapse:collapse;min-width:500px;">
                        <thead>
                            <tr style="border-bottom:1px solid var(--border);">
                                <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Node</th>
                                <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Phase</th>
                                <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Status</th>
                                <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Version</th>
                                <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Error</th>
                            </tr>
                        </thead>
                        <tbody>${nodeRows}</tbody>
                    </table>
                </div>`
            }
        `);
    },

    _bindUpgradeDetailEvents(op) {
        const opId = op.id;
        const pauseBtn    = document.getElementById('upgrade-pause-btn');
        const resumeBtn   = document.getElementById('upgrade-resume-btn');
        const rollbackBtn = document.getElementById('upgrade-rollback-btn');

        if (pauseBtn) {
            pauseBtn.addEventListener('click', async () => {
                pauseBtn.disabled = true;
                try {
                    await API.slurm.pauseUpgrade(opId);
                    App.toast('Upgrade paused.', 'success');
                    SlurmPages.upgradeDetail(opId);
                } catch (err) {
                    App.toast('Pause failed: ' + err.message, 'error');
                    pauseBtn.disabled = false;
                }
            });
        }

        if (resumeBtn) {
            resumeBtn.addEventListener('click', async () => {
                resumeBtn.disabled = true;
                try {
                    await API.slurm.resumeUpgrade(opId);
                    App.toast('Upgrade resumed.', 'success');
                    SlurmPages.upgradeDetail(opId);
                } catch (err) {
                    App.toast('Resume failed: ' + err.message, 'error');
                    resumeBtn.disabled = false;
                }
            });
        }

        if (rollbackBtn) {
            rollbackBtn.addEventListener('click', async () => {
                // Explicit red-warning rollback confirmation.
                const confirmed = confirm(
                    '⚠ ROLLBACK UPGRADE?\n\n' +
                    'This will initiate a rollback to the previous Slurm version. ' +
                    'The cluster will be drained and services restarted across all nodes. ' +
                    'Running jobs WILL be interrupted.\n\n' +
                    'Type OK in the next prompt to confirm.'
                );
                if (!confirmed) return;
                const typed = prompt('Type ROLLBACK to confirm:');
                if ((typed || '').trim().toUpperCase() !== 'ROLLBACK') {
                    App.toast('Rollback cancelled', 'info');
                    return;
                }
                rollbackBtn.disabled = true;
                rollbackBtn.textContent = 'Rolling back…';
                try {
                    await API.slurm.rollbackUpgrade(opId);
                    App.toast('Rollback initiated.', 'success');
                    SlurmPages.upgrades();
                } catch (err) {
                    App.toast('Rollback failed: ' + err.message, 'error');
                    rollbackBtn.disabled = false;
                    rollbackBtn.textContent = 'Rollback';
                }
            });
        }
    },

    // ── Node override editor (called from node detail Slurm tab) ──────────

    // renderNodeOverrideEditor renders the override form into a container element.
    async renderNodeOverrideEditor(nodeId, container) {
        if (!container) return;
        container.innerHTML = `<div class="loading" style="padding:8px 0"><div class="spinner" style="width:14px;height:14px;display:inline-block;margin-right:6px"></div>Loading overrides…</div>`;
        try {
            const data = await API.slurm.getNodeOverrides(nodeId);
            const ov   = data.overrides || {};
            container.innerHTML = SlurmPages._nodeOverrideHtml(nodeId, ov);
        } catch (err) {
            container.innerHTML = `<div class="alert alert-error" style="margin:0;">Failed to load overrides: ${escHtml(err.message)}</div>`;
        }
    },

    _nodeOverrideHtml(nodeId, ov) {
        const numField = (id, label, val, placeholder) => `
            <div style="flex:1;min-width:120px;">
                <label style="display:block;font-size:12px;color:var(--text-secondary);margin-bottom:4px;">${label}</label>
                <input type="number" id="ov-${id}" value="${val !== undefined && val !== null ? val : ''}" placeholder="${placeholder}"
                    min="0"
                    style="width:100%;padding:6px 8px;border:1px solid var(--border);border-radius:6px;font-size:13px;background:var(--bg-input,#fff);color:var(--text);box-sizing:border-box;">
            </div>`;

        const customPairs = Object.entries(ov.custom || {});
        const customRows  = customPairs.map(([k, v], i) => SlurmPages._customKVRow(i, k, v)).join('');

        return `
            <div style="display:flex;gap:10px;flex-wrap:wrap;margin-bottom:12px;">
                ${numField('cpus',              'CPUs',            ov.cpus,            'e.g. 32')}
                ${numField('sockets',           'Sockets',         ov.sockets,         'e.g. 2')}
                ${numField('cores-per-socket',  'Cores/Socket',    ov.cores_per_socket,'e.g. 16')}
                ${numField('threads-per-core',  'Threads/Core',    ov.threads_per_core,'e.g. 2')}
                ${numField('real-memory',       'RealMemory (MB)', ov.real_memory,     'e.g. 256000')}
            </div>
            <div style="margin-bottom:12px;">
                <label style="display:block;font-size:12px;color:var(--text-secondary);margin-bottom:4px;">Gres</label>
                <input type="text" id="ov-gres" value="${ov.gres ? escHtml(ov.gres) : ''}" placeholder="e.g. gpu:a100:4"
                    style="width:100%;padding:6px 8px;border:1px solid var(--border);border-radius:6px;font-size:13px;font-family:monospace;background:var(--bg-input,#fff);color:var(--text);box-sizing:border-box;">
            </div>
            <div style="margin-bottom:12px;">
                <label style="display:block;font-size:12px;color:var(--text-secondary);margin-bottom:4px;">gres.conf content for this node</label>
                <textarea id="ov-gres-conf" rows="4"
                    style="width:100%;padding:6px 8px;border:1px solid var(--border);border-radius:6px;font-size:12px;font-family:monospace;background:var(--bg-input,#fff);color:var(--text);resize:vertical;box-sizing:border-box;"
                    placeholder="AutoDetect=nvml&#10;Name=gpu Type=a100 File=/dev/nvidia[0-3]">${ov.gres_conf_content ? escHtml(ov.gres_conf_content) : ''}</textarea>
            </div>
            <div style="margin-bottom:12px;">
                <div style="font-size:12px;font-weight:600;color:var(--text-secondary);margin-bottom:6px;">Custom Parameters</div>
                <div id="ov-custom-rows" style="display:flex;flex-direction:column;gap:6px;">
                    ${customRows}
                </div>
                <button onclick="SlurmPages._addCustomKVRow()" class="btn btn-secondary btn-sm" style="margin-top:8px;font-size:12px;">+ Add Parameter</button>
            </div>
            <div style="display:flex;gap:8px;align-items:center;margin-top:12px;flex-wrap:wrap;">
                <button id="ov-save-btn" class="btn btn-primary btn-sm" onclick="SlurmPages._saveNodeOverrides('${escHtml(nodeId)}')">Save Overrides</button>
                <button id="ov-preview-btn" class="btn btn-secondary btn-sm" onclick="SlurmPages._previewFromOverrides('${escHtml(nodeId)}')">Preview slurm.conf</button>
                <span id="ov-save-status" style="font-size:12px;color:var(--text-secondary);"></span>
            </div>
            <div id="ov-preview-result" style="display:none;margin-top:10px;"></div>
        `;
    },

    _customKVRow(idx, key, val) {
        key = key || ''; val = val || '';
        return `
            <div class="ov-kv-row" data-idx="${idx}" style="display:flex;gap:6px;align-items:center;">
                <input type="text" class="ov-kv-key" value="${escHtml(key)}" placeholder="Key (e.g. Feature)"
                    style="flex:1;padding:5px 8px;border:1px solid var(--border);border-radius:6px;font-size:12px;font-family:monospace;background:var(--bg-input,#fff);color:var(--text);">
                <input type="text" class="ov-kv-val" value="${escHtml(val)}" placeholder="Value"
                    style="flex:2;padding:5px 8px;border:1px solid var(--border);border-radius:6px;font-size:12px;font-family:monospace;background:var(--bg-input,#fff);color:var(--text);">
                <button onclick="this.closest('.ov-kv-row').remove()" class="btn btn-danger btn-sm" style="padding:4px 8px;font-size:12px;">×</button>
            </div>`;
    },

    _addCustomKVRow() {
        const container = document.getElementById('ov-custom-rows');
        if (!container) return;
        const idx = container.querySelectorAll('.ov-kv-row').length;
        container.insertAdjacentHTML('beforeend', SlurmPages._customKVRow(idx, '', ''));
    },

    async _saveNodeOverrides(nodeId) {
        const btn      = document.getElementById('ov-save-btn');
        const statusEl = document.getElementById('ov-save-status');
        const toNum = (id) => {
            const v = (document.getElementById(id)?.value || '').trim();
            return v ? parseInt(v, 10) : null;
        };
        const ov = {
            cpus:              toNum('ov-cpus'),
            sockets:           toNum('ov-sockets'),
            cores_per_socket:  toNum('ov-cores-per-socket'),
            threads_per_core:  toNum('ov-threads-per-core'),
            real_memory:       toNum('ov-real-memory'),
            gres:              (document.getElementById('ov-gres')?.value || '').trim() || null,
            gres_conf_content: (document.getElementById('ov-gres-conf')?.value || '').trim() || null,
        };
        const custom = {};
        document.querySelectorAll('.ov-kv-row').forEach(row => {
            const k = (row.querySelector('.ov-kv-key')?.value || '').trim();
            const v = (row.querySelector('.ov-kv-val')?.value || '').trim();
            if (k) custom[k] = v;
        });
        if (Object.keys(custom).length) ov.custom = custom;

        if (btn) { btn.disabled = true; btn.textContent = 'Saving…'; }
        if (statusEl) statusEl.textContent = '';
        try {
            await API.slurm.setNodeOverrides(nodeId, { overrides: ov });
            if (statusEl) { statusEl.textContent = 'Saved.'; statusEl.style.color = '#16a34a'; }
            App.toast('Overrides saved', 'success');
        } catch (err) {
            if (statusEl) { statusEl.textContent = 'Error: ' + err.message; statusEl.style.color = '#dc2626'; }
            App.toast('Save failed: ' + err.message, 'error');
        } finally {
            if (btn) { btn.disabled = false; btn.textContent = 'Save Overrides'; }
        }
    },

    async _previewFromOverrides(nodeId) {
        const previewDiv = document.getElementById('ov-preview-result');
        const btn        = document.getElementById('ov-preview-btn');
        if (!previewDiv) return;
        previewDiv.style.display = '';
        previewDiv.innerHTML = '<div style="font-size:13px;color:var(--text-secondary);">Loading preview…</div>';
        if (btn) { btn.disabled = true; btn.textContent = 'Loading…'; }
        try {
            const data = await API.slurm.renderPreview('slurm.conf', nodeId);
            previewDiv.innerHTML = `
                <div style="font-size:12px;color:var(--text-secondary);margin-bottom:6px;">
                    Rendered slurm.conf for this node
                    &mdash; checksum: <code style="font-family:monospace;">${escHtml((data.checksum||'').substring(0,16))}…</code>
                </div>
                <pre style="background:var(--bg-code,#f8fafc);border:1px solid var(--border);border-radius:6px;
                            padding:10px;font-size:12px;overflow:auto;max-height:300px;white-space:pre-wrap;word-break:break-word;">${escHtml(data.rendered_content || '')}</pre>
                <button onclick="document.getElementById('ov-preview-result').style.display='none'" class="btn btn-sm" style="margin-top:6px;font-size:12px;">Close</button>`;
        } catch (err) {
            previewDiv.innerHTML = `<div class="alert alert-error" style="margin:0;">Preview failed: ${escHtml(err.message)}</div>`;
        } finally {
            if (btn) { btn.disabled = false; btn.textContent = 'Preview slurm.conf'; }
        }
    },

    // renderNodeSyncStatus renders the per-node sync status into a container element.
    async renderNodeSyncStatus(nodeId, container) {
        if (!container) return;
        container.innerHTML = `<span style="font-size:13px;color:var(--text-secondary);">Loading sync status…</span>`;
        try {
            const data = await API.slurm.nodeSyncStatus(nodeId);
            const drift = data.drift || [];
            if (!drift.length) {
                container.innerHTML = '<span style="font-size:13px;color:var(--text-secondary);">No sync data yet for this node.</span>';
                return;
            }
            const rows = drift.map(d => {
                const color = d.in_sync ? '#16a34a' : '#dc2626';
                return `<div style="display:flex;align-items:center;gap:10px;font-size:13px;margin-bottom:4px;">
                    <code style="font-family:monospace;min-width:120px;">${escHtml(d.filename)}</code>
                    <span style="color:${color};font-weight:600;">${d.in_sync ? 'In Sync' : 'Out of Sync'}</span>
                    <span style="font-size:12px;color:var(--text-secondary);">deployed v${d.deployed_version||0} / current v${d.current_version}</span>
                </div>`;
            }).join('');
            container.innerHTML = rows;
        } catch (_) {
            container.innerHTML = `<span style="font-size:13px;color:var(--text-secondary);">Sync status unavailable</span>`;
        }
    },

};
