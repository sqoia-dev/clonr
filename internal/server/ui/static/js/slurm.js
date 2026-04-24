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
                badge.textContent  = enabled ? '' : (st.status === 'not_configured' ? 'Not configured' : 'Disabled');
                badge.style.display = enabled ? 'none' : '';
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
            <div style="margin-top:20px;">
                <h3 style="font-size:14px;font-weight:600;margin:0 0 12px;">Enable Slurm Module</h3>
                <form id="slurm-enable-form" style="display:flex;flex-direction:column;gap:10px;max-width:400px;">
                    <div>
                        <label style="display:block;font-size:13px;font-weight:500;margin-bottom:4px;">Cluster Name</label>
                        <input type="text" id="slurm-cluster-name" placeholder="e.g. hpc-prod"
                               style="width:100%;padding:6px 10px;border:1px solid var(--border);border-radius:6px;font-size:13px;background:var(--bg-input,#fff);color:var(--text);"
                               value="${st.cluster_name ? escHtml(st.cluster_name) : ''}">
                    </div>
                    <div>
                        <button type="submit" class="btn btn-primary" style="font-size:13px;padding:6px 16px;">
                            Enable
                        </button>
                    </div>
                </form>
            </div>` : '';

        const disableBtn = st.enabled && isAdmin ? `
            <div style="margin-top:24px;padding-top:16px;border-top:1px solid var(--border);">
                <h3 style="font-size:14px;font-weight:600;margin:0 0 8px;color:var(--text-secondary);">Danger Zone</h3>
                <p style="font-size:13px;color:var(--text-secondary);margin:0 0 12px;">
                    Disabling the Slurm module stops clonr from managing Slurm configs. It does <strong>not</strong> remove configs from deployed nodes.
                </p>
                <button id="slurm-disable-btn" class="btn btn-danger" style="font-size:13px;padding:6px 16px;">
                    Disable Module
                </button>
            </div>` : '';

        return cardWrap('Slurm', `
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
                    <tr>
                        <td style="color:var(--text-secondary);padding:6px 16px 6px 0;font-size:13px;">Managed Files</td>
                        <td style="padding:6px 0;font-size:13px;">${st.managed_files && st.managed_files.length ? st.managed_files.map(f => `<code style="font-size:12px;background:var(--bg-code,#f1f5f9);padding:1px 4px;border-radius:3px;">${escHtml(f)}</code>`).join(' ') : '—'}</td>
                    </tr>
                    ${connectedHtml}
                    ${driftHtml}
                </tbody>
            </table>
            ${enableForm}
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

        const disableBtn = document.getElementById('slurm-disable-btn');
        if (disableBtn) {
            disableBtn.addEventListener('click', async () => {
                if (!confirm('Disable the Slurm module? Configs will remain on deployed nodes.')) return;
                try {
                    await API.slurm.disable();
                    App.toast('Slurm module disabled', 'success');
                    await SlurmPages.bootstrapNav();
                    SlurmPages.settings();
                } catch (err) {
                    App.toast('Disable failed: ' + err.message, 'error');
                }
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
            <table style="width:100%;border-collapse:collapse;">
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

    _configEditorHtml(config) {
        return cardWrap(`Edit: ${escHtml(config.filename)}`, `
            <div style="margin-bottom:12px;display:flex;align-items:center;gap:12px;flex-wrap:wrap;">
                <span style="font-size:13px;color:var(--text-secondary);">Current version: <strong>v${config.version}</strong></span>
                <a href="#/slurm/configs/${encodeURIComponent(config.filename)}/history" style="font-size:13px;color:var(--accent);">View history</a>
                <a href="#/slurm/configs" style="font-size:13px;color:var(--accent);margin-left:auto;">Back to list</a>
            </div>
            <textarea id="slurm-config-content"
                style="width:100%;min-height:400px;font-family:monospace;font-size:13px;padding:10px;
                       border:1px solid var(--border);border-radius:6px;background:var(--bg-code,#f8fafc);
                       color:var(--text);resize:vertical;box-sizing:border-box;">${escHtml(config.content)}</textarea>
            <div style="margin-top:10px;display:flex;align-items:center;gap:10px;">
                <input type="text" id="slurm-config-message" placeholder="Version message (optional)"
                    style="flex:1;padding:6px 10px;border:1px solid var(--border);border-radius:6px;font-size:13px;
                           background:var(--bg-input,#fff);color:var(--text);">
                <button id="slurm-save-config-btn" class="btn btn-primary" style="font-size:13px;padding:6px 16px;">Save New Version</button>
            </div>
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

    _bindEditorEvents(filename) {
        const btn = document.getElementById('slurm-save-config-btn');
        if (btn) {
            btn.addEventListener('click', async () => {
                const content = document.getElementById('slurm-config-content').value;
                const message = document.getElementById('slurm-config-message').value.trim();
                if (!content.trim()) { App.toast('Content cannot be empty', 'error'); return; }
                try {
                    btn.disabled = true;
                    btn.textContent = 'Saving…';
                    const result = await API.slurm.saveConfig(filename, { content, message });
                    App.toast(`Saved as version ${result.version}`, 'success');
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
        const managedFiles = configs.map(c => c.filename);
        const fileCheckboxes = managedFiles.map(fn => `
            <label style="display:flex;align-items:center;gap:8px;font-size:13px;cursor:pointer;margin-bottom:6px;">
                <input type="checkbox" name="push-file" value="${escHtml(fn)}" checked style="width:14px;height:14px;">
                <span style="font-family:monospace;">${escHtml(fn)}</span>
            </label>
        `).join('');

        // Only show scripts that have content saved.
        const availableScripts = (scripts || []).filter(s => s.has_content && s.enabled);
        const scriptCheckboxes = availableScripts.length > 0
            ? availableScripts.map(s => `
                <label style="display:flex;align-items:center;gap:8px;font-size:13px;cursor:pointer;margin-bottom:6px;">
                    <input type="checkbox" name="push-script" value="${escHtml(s.script_type)}" checked style="width:14px;height:14px;">
                    <span style="font-family:monospace;">${escHtml(s.script_type)}</span>
                    <span style="font-size:11px;color:var(--text-secondary);">v${s.version}</span>
                </label>
            `).join('')
            : '<div style="font-size:13px;color:var(--text-secondary);padding:4px 0;">No enabled scripts with saved content</div>';

        const connectedNodes = status.connected_nodes || [];
        const nodeOptions = connectedNodes.map(n =>
            `<option value="${escHtml(n)}">${escHtml(n)}</option>`
        ).join('');

        return cardWrap('Push Slurm Configs', `
            <div style="display:grid;grid-template-columns:1fr 1fr 1fr;gap:24px;max-width:1100px;">
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
                            <select id="push-apply-action" style="width:100%;padding:8px;border:1px solid var(--border);border-radius:6px;background:var(--bg);color:var(--text-primary);font-size:13px;">
                                <option value="reconfigure">Reconfigure — scontrol reconfigure (no job disruption)</option>
                                <option value="restart">Restart — systemctl restart slurmd/slurmctld</option>
                            </select>
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
                Slurm hook scripts (Prolog, Epilog, HealthCheckProgram, etc.) managed by clonr.
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

    // ── Sync status page ───────────────────────────────────────────────────

    async syncStatus() {
        App.render(loading('Loading sync status…'));
        try {
            const data = await API.slurm.syncStatus();
            App.render(SlurmPages._syncStatusHtml(data.drift || []));
        } catch (err) {
            App.render(alertBox('Failed to load sync status: ' + err.message));
        }
    },

    _syncStatusHtml(drift) {
        if (!drift.length) {
            return cardWrap('Slurm Sync Status', emptyState('No sync data yet. Push configs to nodes to start tracking.'));
        }

        const rows = drift.map(d => `
            <tr>
                <td style="padding:10px 12px;font-size:13px;font-family:monospace;">${escHtml(d.node_id)}</td>
                <td style="padding:10px 12px;font-size:13px;font-family:monospace;">${escHtml(d.filename)}</td>
                <td style="padding:10px 12px;font-size:13px;">v${d.current_version}</td>
                <td style="padding:10px 12px;font-size:13px;">v${d.deployed_version || 0}</td>
                <td style="padding:10px 12px;">
                    <span class="badge ${d.in_sync ? 'badge-ready' : 'badge-error'}">${d.in_sync ? 'In Sync' : 'Out of Sync'}</span>
                </td>
            </tr>
        `).join('');

        return cardWrap('Slurm Sync Status', `
            <table style="width:100%;border-collapse:collapse;">
                <thead>
                    <tr style="border-bottom:1px solid var(--border);">
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Node</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">File</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Current</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Deployed</th>
                        <th style="text-align:left;padding:8px 12px;font-size:12px;color:var(--text-secondary);font-weight:600;">Status</th>
                    </tr>
                </thead>
                <tbody>${rows}</tbody>
            </table>
        `);
    },

};
