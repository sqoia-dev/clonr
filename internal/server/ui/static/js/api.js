// api.js — thin fetch wrapper around the clustr-serverd REST API.
// All methods return parsed JSON or throw an Error with a message from the API.

const API = {
    BASE: '/api/v1',

    // _token returns the legacy localStorage key if present (backwards compat
    // for any CLI/scripted use that injected a Bearer token via the old modal).
    // Session-cookie auth does not need this — the browser sends the cookie automatically.
    _token() {
        try { return localStorage.getItem('clustr_admin_key') || ''; } catch (_) { return ''; }
    },

    _headers(extra = {}) {
        const h = { 'Content-Type': 'application/json', ...extra };
        const tok = this._token();
        if (tok) h['Authorization'] = `Bearer ${tok}`;
        return h;
    },

    // _redirectToLogin navigates to /login if not already there.
    // Preserves the current hash as a ?next= param so the user lands back
    // on the right page after re-authenticating.
    _redirectToLogin() {
        if (window.location.pathname !== '/login') {
            const hash = window.location.hash || '';
            const next = hash ? encodeURIComponent(hash) : '';
            window.location.href = '/login' + (next ? '?next=' + next : '');
        }
    },

    async _parse(resp) {
        const ct = resp.headers.get('Content-Type') || '';
        if (!resp.ok) {
            // 401/403 — session expired or no auth. Redirect to login page.
            if (resp.status === 401 || resp.status === 403) {
                try { localStorage.removeItem('clustr_admin_key'); } catch (_) {}
                API._redirectToLogin();
                // Throw so callers don't proceed with undefined data.
                throw new Error('session expired — redirecting to login');
            }
            let msg = `HTTP ${resp.status}`;
            if (ct.includes('application/json')) {
                const body = await resp.json().catch(() => null);
                if (body && body.error) msg = body.error;
            }
            throw new Error(msg);
        }
        if (resp.status === 204) return null;
        if (ct.includes('application/json')) return resp.json();
        return null;
    },

    async get(path, params = {}) {
        const url = new URL(this.BASE + path, window.location.origin);
        Object.entries(params).forEach(([k, v]) => { if (v !== '' && v != null) url.searchParams.set(k, v); });
        const resp = await fetch(url.toString(), {
            headers: this._headers({ 'Content-Type': undefined }),
            credentials: 'same-origin',
        });
        return this._parse(resp);
    },

    async post(path, body) {
        const resp = await fetch(this.BASE + path, {
            method: 'POST',
            headers: this._headers(),
            body: JSON.stringify(body),
            credentials: 'same-origin',
        });
        return this._parse(resp);
    },

    async put(path, body) {
        const resp = await fetch(this.BASE + path, {
            method: 'PUT',
            headers: this._headers(),
            body: JSON.stringify(body),
            credentials: 'same-origin',
        });
        return this._parse(resp);
    },

    async del(path) {
        const resp = await fetch(this.BASE + path, {
            method: 'DELETE',
            headers: this._headers({ 'Content-Type': undefined }),
            credentials: 'same-origin',
        });
        return this._parse(resp);
    },

    // Generic request helper used by dynamic endpoints (layout, groups, etc.).
    async request(method, path, body) {
        const opts = {
            method,
            headers: method === 'GET' || method === 'DELETE'
                ? this._headers({ 'Content-Type': undefined })
                : this._headers(),
            credentials: 'same-origin',
        };
        if (body !== undefined && method !== 'GET' && method !== 'DELETE') {
            opts.body = JSON.stringify(body);
        }
        const resp = await fetch(this.BASE + path, opts);
        return this._parse(resp);
    },

    // Convenience methods.
    images: {
        list(status = '', tag = '') {
            const params = {};
            if (status) params.status = status;
            if (tag)    params.tag = tag;
            return API.get('/images', params);
        },
        get(id)                     { return API.get(`/images/${id}`); },
        archive(id)                 { return API.del(`/images/${id}`); },
        // delete sends a real DELETE that removes blobs + DB record.
        // opts.force=true unassigns nodes and deletes anyway.
        delete(id, opts = {})       {
            const path = opts.force ? `/images/${id}?force=true` : `/images/${id}`;
            return API.del(path);
        },
        // C3-5: cancel an in-progress ISO build without deleting the image record.
        cancelBuild(id)             { return API.post(`/images/${id}/cancel`, {}); },
        diskLayout(id)              { return API.get(`/images/${id}/disklayout`); },
        metadata(id)                { return API.get(`/images/${id}/metadata`); },
        updateTags(id, tags)        { return API.put(`/images/${id}/tags`, { tags }); },
        activeDeploys(id)           { return API.get(`/images/${id}/active-deploys`); },
        openShellSession(id)        { return API.post(`/images/${id}/shell-session`, {}); },
        closeShellSession(id, sid)  { return API.del(`/images/${id}/shell-session/${sid}`); },
        shellWsUrl(id, sid) {
            const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
            const tok = API._token();
            const base = `${proto}//${location.host}/api/v1/images/${id}/shell-session/${sid}/ws`;
            return tok ? `${base}?token=${encodeURIComponent(tok)}` : base;
        },
    },
    nodes: {
        list(params = {})     { return API.get('/nodes', params); },
        get(id)               { return API.get(`/nodes/${id}`); },
        create(body)          { return API.post('/nodes', body); },
        update(id, body)      { return API.put(`/nodes/${id}`, body); },
        del(id)               { return API.del(`/nodes/${id}`); },
        // S5-12: config change history for a node.
        configHistory(id, page = 1, perPage = 30) {
            return API.get(`/nodes/${id}/config-history`, { page, per_page: perPage });
        },
        power: {
            status(id)              { return API.get(`/nodes/${id}/power`); },
            on(id)                  { return API.post(`/nodes/${id}/power/on`); },
            off(id)                 { return API.post(`/nodes/${id}/power/off`); },
            cycle(id)               { return API.post(`/nodes/${id}/power/cycle`); },
            reset(id)               { return API.post(`/nodes/${id}/power/reset`); },
            pxeBoot(id)             { return API.post(`/nodes/${id}/power/pxe`); },
            diskBoot(id)            { return API.post(`/nodes/${id}/power/disk`); },
            // flipToDisk calls the provider-abstracted boot-flip endpoint.
            // When cycle=true the server also power-cycles the node after flipping.
            flipToDisk(id, cycle)   { return API.post(`/nodes/${id}/power/flip-to-disk${cycle ? '?cycle=true' : ''}`); },
        },
        sensors(id)           { return API.get(`/nodes/${id}/sensors`); },
        exec(id, command, args) { return API.post(`/nodes/${id}/exec`, { command, args: args || [] }); },
    },
    logs: {
        query(params = {})    { return API.get('/logs', params); },
    },
    factory: {
        pull(body)            { return API.post('/factory/pull', body); },
        importISO(body)       { return API.post('/factory/import-iso', body); },
        capture(body)         { return API.post('/factory/capture', body); },
        // buildFromISO submits an installer ISO URL for automated VM-based install.
        // The server downloads the ISO, runs it in QEMU, captures the rootfs,
        // and returns a building BaseImage record. Poll GET /images/:id for status.
        buildFromISO(body)    { return API.post('/factory/build-from-iso', body); },

        // uploadISO — browser file upload with real progress.
        // file     : File object from <input type="file"> or drag-and-drop.
        // metadata : { name, version }
        // onProgress(pct, bytesLoaded, bytesTotal, speedBps, etaSecs)
        // Returns a Promise that resolves to the created BaseImage record.
        uploadISO(file, metadata, onProgress) {
            return new Promise((resolve, reject) => {
                const fd = new FormData();
                fd.append('file', file);
                fd.append('name', metadata.name || '');
                if (metadata.version) fd.append('version', metadata.version);

                const xhr = new XMLHttpRequest();
                let startTime = null;

                xhr.upload.addEventListener('loadstart', () => { startTime = Date.now(); });
                xhr.upload.addEventListener('progress', (e) => {
                    if (!e.lengthComputable || !onProgress) return;
                    const pct  = Math.round((e.loaded / e.total) * 100);
                    const secs = (Date.now() - startTime) / 1000 || 0.001;
                    const bps  = e.loaded / secs;
                    const eta  = bps > 0 ? (e.total - e.loaded) / bps : 0;
                    onProgress(pct, e.loaded, e.total, bps, eta);
                });

                xhr.addEventListener('load', () => {
                    if (xhr.status >= 200 && xhr.status < 300) {
                        try { resolve(JSON.parse(xhr.responseText)); }
                        catch { resolve(null); }
                    } else {
                        let msg = `HTTP ${xhr.status}`;
                        try {
                            const body = JSON.parse(xhr.responseText);
                            if (body && body.error) msg = body.error;
                        } catch {}
                        reject(new Error(msg));
                    }
                });
                xhr.addEventListener('error', () => reject(new Error('Network error during upload')));
                xhr.addEventListener('abort', () => reject(new Error('Upload cancelled')));

                const tok = API._token();
                xhr.open('POST', `${API.BASE}/factory/import`);
                if (tok) xhr.setRequestHeader('Authorization', `Bearer ${tok}`);
                xhr.send(fd);
            });
        },
    },
    buildProgress: {
        // get returns the current BuildState snapshot for an image.
        get(imageId)        { return API.get(`/images/${imageId}/build-progress`); },
        // sseUrl returns the URL for the SSE stream endpoint for a given image.
        sseUrl(imageId) {
            const tok = API._token();
            const base = `${window.location.origin}/api/v1/images/${imageId}/build-progress/stream`;
            return tok ? `${base}?token=${encodeURIComponent(tok)}` : base;
        },
        // buildLogUrl returns the URL for the full build log download.
        buildLogUrl(imageId) {
            const tok = API._token();
            const base = `${window.location.origin}/api/v1/images/${imageId}/build-log`;
            return tok ? `${base}?token=${encodeURIComponent(tok)}` : base;
        },
        // manifest returns the JSON build summary written after a completed build.
        manifest(imageId)   { return API.get(`/images/${imageId}/build-manifest`); },
    },
    imageRoles: {
        list()              { return API.get('/image-roles'); },
    },
    nodeGroups: {
        list()              { return API.get('/node-groups'); },
        get(id)             { return API.get(`/node-groups/${id}`); },
        create(body)        { return API.post('/node-groups', body); },
        update(id, body)    { return API.put(`/node-groups/${id}`, body); },
        del(id)             { return API.del(`/node-groups/${id}`); },
        // Group membership management.
        addMembers(id, nodeIds)  { return API.post(`/node-groups/${id}/members`, { node_ids: nodeIds }); },
        removeMember(id, nodeId) { return API.del(`/node-groups/${id}/members/${encodeURIComponent(nodeId)}`); },
        // Rolling group reimage.
        reimage(id, body)   { return API.post(`/node-groups/${id}/reimage`, body); },
        // Job status polling.
        jobStatus(jobId)    { return API.get(`/reimages/jobs/${encodeURIComponent(jobId)}`); },
        resumeJob(jobId)    { return API.post(`/reimages/jobs/${encodeURIComponent(jobId)}/resume`, {}); },
        // F3: Allocation expiration (v1.5.0).
        setExpiration(id, expiresAt)  { return API.put(`/node-groups/${id}/expiration`, { expires_at: expiresAt }); },
        clearExpiration(id)           { return API.del(`/node-groups/${id}/expiration`); },
    },
    reimages: {
        // listForNode fetches reimage history for a single node.
        listForNode(nodeId)                 { return API.get(`/nodes/${nodeId}/reimage`); },
        // list fetches all reimage records with optional filters. Pass { limit } for pagination.
        list(params = {})                   { return API.get('/reimages', params); },
        get(id)                             { return API.get(`/reimage/${id}`); },
        cancel(id)                          { return API.del(`/reimage/${id}`); },
        retry(id)                           { return API.post(`/reimage/${id}/retry`, {}); },
        // cancelAllActive cancels every non-terminal reimage (pending, triggered, in_progress).
        cancelAllActive()                   { return API.post('/reimage/cancel-all-active', {}); },
    },
    health: {
        get()                 { return API.get('/health'); },
        ready()               { return API.get('/healthz/ready'); },
    },
    auth: {
        me()                  { return API.get('/auth/me'); },
    },
    apiKeys: {
        list()                { return API.get('/admin/api-keys'); },
        create(body)          { return API.post('/admin/api-keys', body); },
        revoke(id)            { return API.del(`/admin/api-keys/${id}`); },
        rotate(id)            { return API.post(`/admin/api-keys/${id}/rotate`, {}); },
    },
    users: {
        list()                             { return API.get('/admin/users'); },
        create(body)                       { return API.post('/admin/users', body); },
        update(id, body)                   { return API.put(`/admin/users/${id}`, body); },
        resetPassword(id, password)        { return API.post(`/admin/users/${id}/reset-password`, { password }); },
        disable(id)                        { return API.del(`/admin/users/${id}`); },
        getGroupMemberships(id)            { return API.get(`/users/${id}/group-memberships`); },
        setGroupMemberships(id, groupIDs)  { return API.put(`/users/${id}/group-memberships`, { group_ids: groupIDs }); },
    },
    audit: {
        query(params = {})  { return API.get('/audit', params); },
        // F2: export URL builder — returns the fetch URL for a JSONL export.
        // Callers open this URL directly (window.open) rather than using API.get
        // because the response is streamed JSONL, not JSON.
        exportURL(params = {}) {
            const q = new URLSearchParams();
            if (params.since)         q.set('since',         params.since);
            if (params.until)         q.set('until',         params.until);
            if (params.actor)         q.set('actor',         params.actor);
            if (params.action)        q.set('action',        params.action);
            if (params.resource_type) q.set('resource_type', params.resource_type);
            const qs = q.toString();
            return '/api/v1/audit/export' + (qs ? '?' + qs : '');
        },
    },
    system: {
        // initramfs — GET current status + history, POST to rebuild, DELETE history entry.
        initramfs()               { return API.get('/system/initramfs'); },
        rebuildInitramfs()        { return API.post('/system/initramfs/rebuild', {}); },
        deleteInitramfsHistory(id){ return API.del(`/system/initramfs/history/${encodeURIComponent(id)}`); },
    },
    sysaccounts: {
        // Groups
        listGroups()            { return API.get('/system/groups'); },
        createGroup(body)       { return API.post('/system/groups', body); },
        updateGroup(id, body)   { return API.put(`/system/groups/${encodeURIComponent(id)}`, body); },
        deleteGroup(id)         { return API.del(`/system/groups/${encodeURIComponent(id)}`); },
        // Accounts
        listAccounts()          { return API.get('/system/accounts'); },
        createAccount(body)     { return API.post('/system/accounts', body); },
        updateAccount(id, body) { return API.put(`/system/accounts/${encodeURIComponent(id)}`, body); },
        deleteAccount(id)       { return API.del(`/system/accounts/${encodeURIComponent(id)}`); },
    },
    ldap: {
        status()                        { return API.get('/ldap/status'); },
        enable(body)                    { return API.post('/ldap/enable', body); },
        disable(body)                   { return API.post('/ldap/disable', body); },
        backup()                        { return API.post('/ldap/backup', {}); },
        // Users
        listUsers()                     { return API.get('/ldap/users'); },
        createUser(body)                { return API.post('/ldap/users', body); },
        updateUser(uid, body)           { return API.put(`/ldap/users/${encodeURIComponent(uid)}`, body); },
        deleteUser(uid)                 { return API.del(`/ldap/users/${encodeURIComponent(uid)}`); },
        setPassword(uid, password, forceChange) { return API.post(`/ldap/users/${encodeURIComponent(uid)}/password`, { password, force_change: !!forceChange }); },
        lockUser(uid)                   { return API.post(`/ldap/users/${encodeURIComponent(uid)}/lock`, {}); },
        unlockUser(uid)                 { return API.post(`/ldap/users/${encodeURIComponent(uid)}/unlock`, {}); },
        // Groups
        listGroups()                    { return API.get('/ldap/groups'); },
        createGroup(body)               { return API.post('/ldap/groups', body); },
        updateGroup(cn, body)           { return API.put(`/ldap/groups/${encodeURIComponent(cn)}`, body); },
        deleteGroup(cn)                 { return API.del(`/ldap/groups/${encodeURIComponent(cn)}`); },
        addMember(cn, uid)              { return API.post(`/ldap/groups/${encodeURIComponent(cn)}/members`, { uid }); },
        removeMember(cn, uid)           { return API.del(`/ldap/groups/${encodeURIComponent(cn)}/members/${encodeURIComponent(uid)}`); },
        // Logs
        logs(params = {})               { return API.get('/ldap/logs', params); },
        // Admin repair — verifies the admin password and self-heals node-reader bind.
        repairAdminBind(body)           { return API.post('/ldap/admin/repair', body); },
        // Sudoers — LDAP group-based sudo management.
        sudoersStatus()                 { return API.get('/ldap/sudoers/status'); },
        sudoersEnable()                 { return API.post('/ldap/sudoers/enable', {}); },
        sudoersDisable()                { return API.post('/ldap/sudoers/disable', {}); },
        sudoersMembers()                { return API.get('/ldap/sudoers/members'); },
        sudoersGrant(uid)               { return API.post('/ldap/sudoers/members', { uid }); },
        sudoersRevoke(uid)              { return API.del(`/ldap/sudoers/members/${encodeURIComponent(uid)}`); },
        sudoersPush()                   { return API.post('/ldap/sudoers/push', {}); },
    },
    resume: {
        // resume — POST to resume an interrupted image build.
        image(id)             { return API.post(`/images/${id}/resume`, {}); },
    },
    progress: {
        list()                { return API.get('/deploy/progress'); },
        get(mac)              { return API.get(`/deploy/progress/${encodeURIComponent(mac)}`); },

        // sseUrl returns the URL for the SSE stream endpoint.
        sseUrl() {
            const tok = API._token();
            const base = `${window.location.origin}/api/v1/deploy/progress/stream`;
            return tok ? `${base}?token=${encodeURIComponent(tok)}` : base;
        },
    },
    slurm: {
        status()                            { return API.get('/slurm/status'); },
        enable(body)                        { return API.post('/slurm/enable', body); },
        disable()                           { return API.post('/slurm/disable', {}); },
        // Config files
        listConfigs()                       { return API.get('/slurm/configs'); },
        getConfig(filename)                 { return API.get(`/slurm/configs/${encodeURIComponent(filename)}`); },
        saveConfig(filename, body)          { return API.put(`/slurm/configs/${encodeURIComponent(filename)}`, body); },
        validateConfig(filename, body)      { return API.post(`/slurm/configs/${encodeURIComponent(filename)}/validate`, body); },
        configHistory(filename)             { return API.get(`/slurm/configs/${encodeURIComponent(filename)}/history`); },
        // Sync / drift
        syncStatus()                        { return API.get('/slurm/sync-status'); },
        nodeSyncStatus(nodeId)              { return API.get(`/nodes/${encodeURIComponent(nodeId)}/slurm/sync-status`); },
        // Push operations
        push(body)                          { return API.post('/slurm/push', body); },
        pushOpStatus(opId)                  { return API.get(`/slurm/push-ops/${encodeURIComponent(opId)}`); },
        // Node roles
        getNodeRole(nodeId)                 { return API.get(`/nodes/${encodeURIComponent(nodeId)}/slurm/role`); },
        setNodeRole(nodeId, body)           { return API.put(`/nodes/${encodeURIComponent(nodeId)}/slurm/role`, body); },
        nodesByRole(role)                   { return API.get(`/slurm/nodes/by-role/${encodeURIComponent(role)}`); },
        rolesSummary()                      { return API.get('/slurm/roles/summary'); },
        // Node overrides
        getNodeOverrides(nodeId)            { return API.get(`/nodes/${encodeURIComponent(nodeId)}/slurm/overrides`); },
        setNodeOverrides(nodeId, body)      { return API.put(`/nodes/${encodeURIComponent(nodeId)}/slurm/overrides`, body); },
        // Scripts
        listScripts()                       { return API.get('/slurm/scripts'); },
        getScript(scriptType)               { return API.get(`/slurm/scripts/${encodeURIComponent(scriptType)}`); },
        saveScript(scriptType, body)        { return API.put(`/slurm/scripts/${encodeURIComponent(scriptType)}`, body); },
        scriptHistory(scriptType)           { return API.get(`/slurm/scripts/${encodeURIComponent(scriptType)}/history`); },
        listScriptConfigs()                 { return API.get('/slurm/scripts/configs'); },
        setScriptConfig(scriptType, body)   { return API.put(`/slurm/scripts/${encodeURIComponent(scriptType)}/config`, body); },
        // Render preview (dry-run)
        renderPreview(filename, nodeId)     { return API.get(`/slurm/configs/${encodeURIComponent(filename)}/render/${encodeURIComponent(nodeId)}`); },

        // Builds
        listBuilds()                        { return API.get('/slurm/builds'); },
        getBuild(buildId)                   { return API.get(`/slurm/builds/${encodeURIComponent(buildId)}`); },
        startBuild(body)                    { return API.post('/slurm/builds', body); },
        deleteBuild(buildId)                { return API.del(`/slurm/builds/${encodeURIComponent(buildId)}`); },
        buildLogs(buildId)                  { return API.get(`/slurm/builds/${encodeURIComponent(buildId)}/logs`); },
        setActiveBuild(buildId)             { return API.post(`/slurm/builds/${encodeURIComponent(buildId)}/set-active`, {}); },

        // Dependency matrix
        listDepMatrix()                     { return API.get('/slurm/deps/matrix'); },

        // Munge key management
        generateMungeKey()                  { return API.post('/slurm/munge-key/generate', {}); },
        rotateMungeKey()                    { return API.post('/slurm/munge-key/rotate', {}); },

        // Rolling upgrade operations (Sprint 9)
        validateUpgrade(body)               { return API.post('/slurm/upgrades/validate', body); },
        startUpgrade(body)                  { return API.post('/slurm/upgrades', body); },
        listUpgrades()                      { return API.get('/slurm/upgrades'); },
        getUpgrade(opId)                    { return API.get(`/slurm/upgrades/${encodeURIComponent(opId)}`); },
        pauseUpgrade(opId)                  { return API.post(`/slurm/upgrades/${encodeURIComponent(opId)}/pause`, {}); },
        resumeUpgrade(opId)                 { return API.post(`/slurm/upgrades/${encodeURIComponent(opId)}/resume`, {}); },
        rollbackUpgrade(opId)               { return API.post(`/slurm/upgrades/${encodeURIComponent(opId)}/rollback`, {}); },
    },
    network: {
        // Switches
        listSwitches()                      { return API.get('/network/switches'); },
        createSwitch(data)                  { return API.post('/network/switches', data); },
        updateSwitch(id, data)              { return API.put(`/network/switches/${encodeURIComponent(id)}`, data); },
        deleteSwitch(id)                    { return API.del(`/network/switches/${encodeURIComponent(id)}`); },
        // Profiles
        listProfiles()                      { return API.get('/network/profiles'); },
        getProfile(id)                      { return API.get(`/network/profiles/${encodeURIComponent(id)}`); },
        createProfile(data)                 { return API.post('/network/profiles', data); },
        updateProfile(id, data)             { return API.put(`/network/profiles/${encodeURIComponent(id)}`, data); },
        deleteProfile(id)                   { return API.del(`/network/profiles/${encodeURIComponent(id)}`); },
        // Group assignments
        getGroupProfile(groupId)            { return API.get(`/node-groups/${encodeURIComponent(groupId)}/network-profile`); },
        assignProfileToGroup(groupId, profileId) { return API.put(`/node-groups/${encodeURIComponent(groupId)}/network-profile`, { profile_id: profileId }); },
        unassignProfileFromGroup(groupId)   { return API.del(`/node-groups/${encodeURIComponent(groupId)}/network-profile`); },
        // OpenSM
        getOpenSM()                         { return API.get('/network/opensm'); },
        setOpenSM(data)                     { return API.put('/network/opensm', data); },
        // IB status
        getIBStatus()                       { return API.get('/network/ib-status'); },
    },
    dhcp: {
        // leases returns all DHCP allocations derived from the node table.
        // Optional params.role filters by HPC role tag.
        leases(params = {})  { return API.get('/dhcp/leases', params); },
    },
    pi: {
        // Admin-facing PI request management (C.5 — CF-08 approval workflow).
        listMemberRequests(status)          { return API.get('/admin/pi/member-requests', status ? { status } : {}); },
        resolveMemberRequest(id, action)    { return API.post(`/admin/pi/member-requests/${encodeURIComponent(id)}/resolve`, { action }); },
        listExpansionRequests(status)       { return API.get('/admin/pi/expansion-requests', status ? { status } : {}); },
        resolveExpansionRequest(id, action) { return API.post(`/admin/pi/expansion-requests/${encodeURIComponent(id)}/resolve`, { action }); },
        // Node group PI ownership management.
        setGroupPI(groupId, piUserId)       { return API.put(`/node-groups/${encodeURIComponent(groupId)}/pi`, { pi_user_id: piUserId }); },
    },
};
