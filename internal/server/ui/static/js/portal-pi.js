// portal-pi.js — Alpine component for the PI portal (/portal/pi/)
// Extracted from portal_pi.html inline <script> block in Sprint F (v1.5.0) to
// satisfy script-src 'self' CSP.  No logic changes.

function piPortalApp() {
    return {
        loading: true,
        error:   null,
        username: '',
        tab:     'groups',

        // H2 — First-project wizard (auto-compute allocation).
        wizard: {
            show:            false,      // display the overlay?
            step:            1,          // 1 = form, 2 = success
            projectName:     '',
            nodegroupTemplate: '',       // leave empty for default
            initialMembers:  '',         // comma-separated LDAP usernames
            slurmPartition:  '',         // leave empty for default
            ldapSyncEnabled: true,
            autoCompute:     true,
            saving:          false,
            error:           null,
            result:          null,       // ProjectCreateResponse.auto_alloc_result
        },

        // H3 — Per-group undo banner state (groupID → {available, hours, deadline, partition}).
        undoBanners: {},

        groups:        [],
        expandedGroups: {},  // groupID → bool
        groupMembers:  {},   // groupID → array of member objects (null = not loaded)

        // Add member modal state.
        addMember: {
            open:      false,
            groupID:   '',
            groupName: '',
            username:  '',
            saving:    false,
            success:   null,
            error:     null,
        },

        // Expansion request modal state.
        expansion: {
            open:          false,
            groupID:       '',
            groupName:     '',
            justification: '',
            saving:        false,
            success:       null,
            error:         null,
        },

        async init() {
            // Auth check.
            try {
                const me = await apiFetch('/api/v1/auth/me');
                if (!me.ok) { window.location.href = '/login'; return; }
                const meData = await me.json();
                if (meData.role !== 'pi' && meData.role !== 'admin') {
                    // Wrong role — dispatch to the right portal.
                    if (meData.role === 'viewer') { window.location.href = '/portal/'; return; }
                    window.location.href = '/';
                    return;
                }
                this.username = meData.username || '';
            } catch (_) {
                window.location.href = '/login';
                return;
            }

            await Promise.all([this.loadGroups(), this.loadFOSOptions()]);

            // H2: Check whether to show the first-project wizard.
            await this.checkOnboardingStatus();

            this.loading = false;

            // H3: Load undo banner state for each owned group.
            this.$nextTick(async () => {
                if (window.htmx) { htmx.process(document.body); }
                for (const g of this.groups) {
                    await this.loadUndoBanner(g.id);
                }
            });
        },

        // Field of Science options (E2, CF-16).
        fosOptions: [],

        async loadGroups() {
            try {
                const r = await apiFetch('/api/v1/portal/pi/groups');
                if (!r.ok) {
                    const e = await r.json().catch(() => ({ error: 'Failed to load groups' }));
                    this.error = e.error || 'Failed to load groups';
                    return;
                }
                const data = await r.json();
                this.groups = data.groups || [];
                // Pre-expand the first group.
                if (this.groups.length === 1) {
                    this.expandedGroups[this.groups[0].id] = true;
                }
            } catch (err) {
                this.error = 'Network error: ' + err.message;
            }
        },

        async loadFOSOptions() {
            try {
                const r = await apiFetch('/api/v1/fields-of-science');
                if (r.ok) {
                    const d = await r.json();
                    this.fosOptions = d.fields_of_science || [];
                }
            } catch (_) {}
        },

        async setGroupFOS(groupID, fosID) {
            try {
                await apiFetch(`/api/v1/portal/pi/groups/${groupID}/field-of-science`, {
                    method: 'PATCH',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ field_of_science_id: fosID }),
                });
                // Update local state.
                const g = this.groups.find(x => x.id === groupID);
                if (g) g.field_of_science_id = fosID;
            } catch (_) {}
        },

        toggleGroup(groupID) {
            this.expandedGroups[groupID] = !this.expandedGroups[groupID];
            if (this.expandedGroups[groupID] && this.groupMembers[groupID] == null) {
                this.loadMembers(groupID);
            }
        },

        async loadMembers(groupID) {
            try {
                const r = await apiFetch(`/api/v1/portal/pi/groups/${groupID}/members`);
                if (r.ok) {
                    const data = await r.json();
                    this.groupMembers[groupID] = data.members || [];
                } else {
                    this.groupMembers[groupID] = [];
                }
            } catch (_) {
                this.groupMembers[groupID] = [];
            }
        },

        openAddMember(group) {
            this.addMember = {
                open:      true,
                groupID:   group.id,
                groupName: group.name,
                username:  '',
                saving:    false,
                success:   null,
                error:     null,
            };
        },

        async submitAddMember() {
            this.addMember.success = null;
            this.addMember.error   = null;
            const username = (this.addMember.username || '').trim().toLowerCase();
            if (!username) {
                this.addMember.error = 'Please enter an LDAP username.';
                return;
            }
            this.addMember.saving = true;
            try {
                const r = await apiFetch(`/api/v1/portal/pi/groups/${this.addMember.groupID}/members`, {
                    method:  'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body:    JSON.stringify({ ldap_username: username }),
                });
                const data = await r.json();
                if (r.ok) {
                    if (data.status === 'approved') {
                        this.addMember.success = `${username} has been added to the group.`;
                    } else {
                        this.addMember.success = `Request submitted for ${username}. An admin will review it shortly.`;
                    }
                    this.addMember.username = '';
                    // Reload members.
                    await this.loadMembers(this.addMember.groupID);
                } else {
                    this.addMember.error = data.error || 'Failed to add member.';
                }
            } catch (err) {
                this.addMember.error = 'Network error: ' + err.message;
            } finally {
                this.addMember.saving = false;
            }
        },

        async removeMember(groupID, username) {
            if (!confirm(`Remove ${username} from this group? This will remove them from the LDAP group.`)) return;
            try {
                const r = await apiFetch(`/api/v1/portal/pi/groups/${groupID}/members/${encodeURIComponent(username)}`, {
                    method: 'DELETE',
                });
                if (r.ok) {
                    await this.loadMembers(groupID);
                } else {
                    const data = await r.json().catch(() => ({}));
                    alert('Failed to remove member: ' + (data.error || 'Unknown error'));
                }
            } catch (err) {
                alert('Network error: ' + err.message);
            }
        },

        openExpansionRequest(group) {
            this.expansion = {
                open:          true,
                groupID:       group.id,
                groupName:     group.name,
                justification: '',
                saving:        false,
                success:       null,
                error:         null,
            };
        },

        async submitExpansion() {
            this.expansion.success = null;
            this.expansion.error   = null;
            const j = (this.expansion.justification || '').trim();
            if (!j) {
                this.expansion.error = 'Please provide a justification.';
                return;
            }
            this.expansion.saving = true;
            try {
                const r = await apiFetch(`/api/v1/portal/pi/groups/${this.expansion.groupID}/expansion-requests`, {
                    method:  'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body:    JSON.stringify({ justification: j }),
                });
                if (r.ok) {
                    this.expansion.success = 'Request submitted. An admin will review it.';
                    this.expansion.justification = '';
                } else {
                    const data = await r.json().catch(() => ({}));
                    this.expansion.error = data.error || 'Failed to submit request.';
                }
            } catch (err) {
                this.expansion.error = 'Network error: ' + err.message;
            } finally {
                this.expansion.saving = false;
            }
        },

        // ── Grants ────────────────────────────────────────────────────────
        grants:        [],
        grantsLoading: false,
        // Active group ID for grants/publications (uses first group by default).
        activeGroupID: null,

        grantModal: {
            open:           false,
            id:             null,
            groupID:        '',
            title:          '',
            funding_agency: '',
            grant_number:   '',
            start_date:     '',
            end_date:       '',
            amount:         '',
            status:         'active',
            notes:          '',
            saving:         false,
            error:          null,
        },

        async loadGrants() {
            const gid = this.activeGroupID || (this.groups.length > 0 ? this.groups[0].id : null);
            if (!gid) return;
            this.activeGroupID = gid;
            this.grantsLoading = true;
            try {
                const r = await apiFetch(`/api/v1/portal/pi/groups/${gid}/grants`);
                if (r.ok) {
                    const d = await r.json();
                    this.grants = d.grants || [];
                } else {
                    this.grants = [];
                }
            } catch (_) {
                this.grants = [];
            } finally {
                this.grantsLoading = false;
            }
        },

        openGrantModal(g) {
            const gid = this.activeGroupID || (this.groups.length > 0 ? this.groups[0].id : '');
            if (g) {
                this.grantModal = {
                    open: true, id: g.id, groupID: g.node_group_id || gid,
                    title: g.title || '', funding_agency: g.funding_agency || '',
                    grant_number: g.grant_number || '', start_date: g.start_date || '',
                    end_date: g.end_date || '', amount: g.amount ? String(g.amount) : '',
                    status: g.status || 'active', notes: g.notes || '',
                    saving: false, error: null,
                };
            } else {
                this.grantModal = {
                    open: true, id: null, groupID: gid,
                    title: '', funding_agency: '', grant_number: '',
                    start_date: '', end_date: '', amount: '',
                    status: 'active', notes: '',
                    saving: false, error: null,
                };
            }
        },

        async submitGrant() {
            this.grantModal.error = null;
            const t = (this.grantModal.title || '').trim();
            if (!t) { this.grantModal.error = 'Title is required.'; return; }
            const gid = this.grantModal.groupID || this.activeGroupID;
            if (!gid) { this.grantModal.error = 'No group selected.'; return; }
            this.grantModal.saving = true;
            const body = {
                title:          this.grantModal.title.trim(),
                funding_agency: this.grantModal.funding_agency.trim(),
                grant_number:   this.grantModal.grant_number.trim(),
                start_date:     this.grantModal.start_date.trim(),
                end_date:       this.grantModal.end_date.trim(),
                amount:         parseFloat(this.grantModal.amount) || 0,
                status:         this.grantModal.status || 'active',
                notes:          this.grantModal.notes.trim(),
            };
            try {
                let r;
                if (this.grantModal.id) {
                    r = await apiFetch(`/api/v1/portal/pi/groups/${gid}/grants/${this.grantModal.id}`, {
                        method: 'PUT', headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify(body),
                    });
                } else {
                    r = await apiFetch(`/api/v1/portal/pi/groups/${gid}/grants`, {
                        method: 'POST', headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify(body),
                    });
                }
                if (r.ok) {
                    this.grantModal.open = false;
                    this.activeGroupID = gid;
                    await this.loadGrants();
                } else {
                    const d = await r.json().catch(() => ({}));
                    this.grantModal.error = d.error || 'Failed to save grant.';
                }
            } catch (err) {
                this.grantModal.error = 'Network error: ' + err.message;
            } finally {
                this.grantModal.saving = false;
            }
        },

        async deleteGrant(id) {
            if (!confirm('Delete this grant?')) return;
            const gid = this.activeGroupID || (this.groups.length > 0 ? this.groups[0].id : null);
            if (!gid) return;
            try {
                const r = await apiFetch(`/api/v1/portal/pi/groups/${gid}/grants/${id}`, { method: 'DELETE' });
                if (r.ok) {
                    await this.loadGrants();
                } else {
                    const d = await r.json().catch(() => ({}));
                    alert('Failed to delete grant: ' + (d.error || 'Unknown error'));
                }
            } catch (err) {
                alert('Network error: ' + err.message);
            }
        },

        // ── Publications ──────────────────────────────────────────────────
        publications: [],
        pubsLoading:  false,

        pubModal: {
            open:        false,
            id:          null,
            groupID:     '',
            title:       '',
            authors:     '',
            journal:     '',
            year:        '',
            doi:         '',
            doiInput:    '',
            doiLooking:  false,
            doiNotFound: false,
            saving:      false,
            error:       null,
        },

        async loadPublications() {
            const gid = this.activeGroupID || (this.groups.length > 0 ? this.groups[0].id : null);
            if (!gid) return;
            this.activeGroupID = gid;
            this.pubsLoading = true;
            try {
                const r = await apiFetch(`/api/v1/portal/pi/groups/${gid}/publications`);
                if (r.ok) {
                    const d = await r.json();
                    this.publications = d.publications || [];
                } else {
                    this.publications = [];
                }
            } catch (_) {
                this.publications = [];
            } finally {
                this.pubsLoading = false;
            }
        },

        openPubModal(p) {
            const gid = this.activeGroupID || (this.groups.length > 0 ? this.groups[0].id : '');
            if (p) {
                this.pubModal = {
                    open: true, id: p.id, groupID: p.node_group_id || gid,
                    title: p.title || '', authors: p.authors || '',
                    journal: p.journal || '', year: p.year ? String(p.year) : '',
                    doi: p.doi || '', doiInput: '', doiLooking: false, doiNotFound: false,
                    saving: false, error: null,
                };
            } else {
                this.pubModal = {
                    open: true, id: null, groupID: gid,
                    title: '', authors: '', journal: '', year: '', doi: '',
                    doiInput: '', doiLooking: false, doiNotFound: false,
                    saving: false, error: null,
                };
            }
        },

        async lookupDOI() {
            const doi = (this.pubModal.doiInput || '').trim();
            if (!doi) return;
            this.pubModal.doiLooking  = true;
            this.pubModal.doiNotFound = false;
            try {
                const r = await apiFetch(`/api/v1/portal/pi/publications/lookup?doi=${encodeURIComponent(doi)}`);
                if (r.ok) {
                    const d = await r.json();
                    if (d.found) {
                        this.pubModal.title   = d.title   || this.pubModal.title;
                        this.pubModal.authors = d.authors || this.pubModal.authors;
                        this.pubModal.journal = d.journal || this.pubModal.journal;
                        this.pubModal.year    = d.year    ? String(d.year) : this.pubModal.year;
                        this.pubModal.doi     = doi;
                    } else {
                        this.pubModal.doiNotFound = true;
                    }
                } else {
                    this.pubModal.doiNotFound = true;
                }
            } catch (_) {
                this.pubModal.doiNotFound = true;
            } finally {
                this.pubModal.doiLooking = false;
            }
        },

        async submitPublication() {
            this.pubModal.error = null;
            const t = (this.pubModal.title || '').trim();
            if (!t) { this.pubModal.error = 'Title is required.'; return; }
            const gid = this.pubModal.groupID || this.activeGroupID;
            if (!gid) { this.pubModal.error = 'No group selected.'; return; }
            this.pubModal.saving = true;
            const body = {
                title:   this.pubModal.title.trim(),
                authors: this.pubModal.authors.trim(),
                journal: this.pubModal.journal.trim(),
                year:    parseInt(this.pubModal.year, 10) || 0,
                doi:     this.pubModal.doi.trim(),
            };
            try {
                let r;
                if (this.pubModal.id) {
                    r = await apiFetch(`/api/v1/portal/pi/groups/${gid}/publications/${this.pubModal.id}`, {
                        method: 'PUT', headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify(body),
                    });
                } else {
                    r = await apiFetch(`/api/v1/portal/pi/groups/${gid}/publications`, {
                        method: 'POST', headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify(body),
                    });
                }
                if (r.ok) {
                    this.pubModal.open = false;
                    this.activeGroupID = gid;
                    await this.loadPublications();
                } else {
                    const d = await r.json().catch(() => ({}));
                    this.pubModal.error = d.error || 'Failed to save publication.';
                }
            } catch (err) {
                this.pubModal.error = 'Network error: ' + err.message;
            } finally {
                this.pubModal.saving = false;
            }
        },

        async deletePublication(id) {
            if (!confirm('Delete this publication?')) return;
            const gid = this.activeGroupID || (this.groups.length > 0 ? this.groups[0].id : null);
            if (!gid) return;
            try {
                const r = await apiFetch(`/api/v1/portal/pi/groups/${gid}/publications/${id}`, { method: 'DELETE' });
                if (r.ok) {
                    await this.loadPublications();
                } else {
                    const d = await r.json().catch(() => ({}));
                    alert('Failed to delete publication: ' + (d.error || 'Unknown error'));
                }
            } catch (err) {
                alert('Network error: ' + err.message);
            }
        },

        // ── Annual Reviews ────────────────────────────────────────────────
        reviews:        [],
        reviewsLoading: false,

        async loadReviews() {
            this.reviewsLoading = true;
            try {
                const r = await apiFetch('/api/v1/portal/pi/review-cycles');
                if (r.ok) {
                    const d = await r.json();
                    this.reviews = d.responses || [];
                } else {
                    this.reviews = [];
                }
            } catch (_) {
                this.reviews = [];
            } finally {
                this.reviewsLoading = false;
            }
        },

        async submitReview(rr, status) {
            const label = status === 'affirmed' ? 'affirm this group as active' : 'request archival of this group';
            if (!confirm(`Are you sure you want to ${label}?`)) return;
            try {
                const r = await apiFetch(
                    `/api/v1/portal/pi/review-cycles/${rr.cycle_id}/groups/${rr.node_group_id}/respond`,
                    {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ status: status, notes: '' }),
                    }
                );
                if (r.ok) {
                    await this.loadReviews();
                } else {
                    const d = await r.json().catch(() => ({}));
                    alert('Failed to submit review: ' + (d.error || 'Unknown error'));
                }
            } catch (err) {
                alert('Network error: ' + err.message);
            }
        },

        // ── Change Requests (E1, CF-20) ───────────────────────────────────
        changeRequests: { loading: false, items: [] },
        changeRequestModal: {
            open: false, groupID: '', requestType: 'increase_resources',
            justification: '', saving: false, success: null, error: null,
        },

        async loadChangeRequests() {
            this.changeRequests.loading = true;
            try {
                // Load requests for all owned groups.
                const allReqs = [];
                for (const g of this.groups) {
                    const r = await apiFetch(`/api/v1/portal/pi/groups/${g.id}/change-requests`);
                    if (r.ok) {
                        const d = await r.json();
                        allReqs.push(...(d.requests || []));
                    }
                }
                // Sort newest first.
                allReqs.sort((a, b) => (b.created_at > a.created_at ? 1 : -1));
                this.changeRequests.items = allReqs;
            } catch (_) {
                this.changeRequests.items = [];
            } finally {
                this.changeRequests.loading = false;
            }
        },

        openChangeRequest() {
            const gid = this.groups.length > 0 ? this.groups[0].id : '';
            this.changeRequestModal = {
                open: true, groupID: gid, requestType: 'increase_resources',
                justification: '', saving: false, success: null, error: null,
            };
        },

        async submitChangeRequest() {
            this.changeRequestModal.error = null;
            this.changeRequestModal.success = null;
            if (!this.changeRequestModal.groupID) {
                this.changeRequestModal.error = 'Please select a group.'; return;
            }
            if (!this.changeRequestModal.justification.trim()) {
                this.changeRequestModal.error = 'Justification is required.'; return;
            }
            this.changeRequestModal.saving = true;
            try {
                const r = await apiFetch(
                    `/api/v1/portal/pi/groups/${this.changeRequestModal.groupID}/change-requests`,
                    {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({
                            request_type: this.changeRequestModal.requestType,
                            justification: this.changeRequestModal.justification,
                            payload: {},
                        }),
                    }
                );
                const d = await r.json().catch(() => ({}));
                if (r.ok) {
                    this.changeRequestModal.success = 'Request submitted. An admin will review and respond.';
                    this.changeRequestModal.justification = '';
                    await this.loadChangeRequests();
                } else {
                    this.changeRequestModal.error = d.error || 'Failed to submit request.';
                }
            } catch (err) {
                this.changeRequestModal.error = 'Network error: ' + err.message;
            } finally {
                this.changeRequestModal.saving = false;
            }
        },

        async withdrawRequest(req) {
            if (!confirm('Withdraw this change request?')) return;
            try {
                const r = await apiFetch(
                    `/api/v1/portal/pi/change-requests/${req.id}/withdraw`,
                    { method: 'POST' }
                );
                if (r.ok) {
                    await this.loadChangeRequests();
                } else {
                    const d = await r.json().catch(() => ({}));
                    alert('Failed to withdraw: ' + (d.error || 'Unknown error'));
                }
            } catch (err) {
                alert('Network error: ' + err.message);
            }
        },

        // ── Attribute Visibility (E3, CF-39) ──────────────────────────────
        visibility: { loading: false, selectedGroupID: '', attributes: [] },

        async loadVisibility() {
            if (!this.visibility.selectedGroupID) return;
            this.visibility.loading = true;
            this.visibility.attributes = [];
            try {
                const r = await apiFetch(`/api/v1/portal/pi/groups/${this.visibility.selectedGroupID}/attribute-visibility`);
                if (r.ok) {
                    const d = await r.json();
                    this.visibility.attributes = d.attributes || [];
                }
            } catch (_) {}
            finally { this.visibility.loading = false; }
        },

        async setVisibilityOverride(attrName, visibility) {
            if (!this.visibility.selectedGroupID) return;
            try {
                await apiFetch(
                    `/api/v1/portal/pi/groups/${this.visibility.selectedGroupID}/attribute-visibility`,
                    {
                        method: 'PATCH',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ attribute_name: attrName, visibility }),
                    }
                );
                // Mark as overridden in local state.
                const attr = this.visibility.attributes.find(a => a.attribute_name === attrName);
                if (attr) {
                    attr.is_overridden = (visibility !== attr.default_visibility);
                }
            } catch (_) {}
        },

        async resetVisibilityOverride(attrName) {
            if (!this.visibility.selectedGroupID) return;
            try {
                const r = await apiFetch(
                    `/api/v1/portal/pi/groups/${this.visibility.selectedGroupID}/attribute-visibility/${encodeURIComponent(attrName)}`,
                    { method: 'DELETE' }
                );
                if (r.ok || r.status === 204) {
                    await this.loadVisibility();
                }
            } catch (_) {}
        },

        async logout() {
            try { await apiFetch('/api/v1/auth/logout', { method: 'POST' }); } catch (_) {}
            window.location.href = '/login';
        },

        // ─── H2: First-project wizard ───────────────────────────────────────────

        async checkOnboardingStatus() {
            try {
                const r = await apiFetch('/api/v1/portal/pi/onboarding-status');
                if (!r.ok) return;
                const data = await r.json();
                if (data.show_wizard) {
                    this.wizard.show = true;
                }
            } catch (_) { /* non-fatal */ }
        },

        async submitWizard() {
            if (!this.wizard.projectName.trim()) {
                this.wizard.error = 'Project name is required.';
                return;
            }
            this.wizard.saving = true;
            this.wizard.error  = null;
            try {
                const members = this.wizard.initialMembers
                    .split(',')
                    .map(s => s.trim())
                    .filter(s => s.length > 0);

                const body = {
                    project_name:      this.wizard.projectName.trim(),
                    auto_compute:      this.wizard.autoCompute,
                    nodegroup_template: this.wizard.nodegroupTemplate.trim() || undefined,
                    initial_members:   members.length ? members : undefined,
                    slurm_partition:   this.wizard.slurmPartition.trim() || undefined,
                    ldap_sync_enabled: this.wizard.ldapSyncEnabled,
                };
                const r = await apiFetch('/api/v1/projects', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(body),
                });
                const data = await r.json();
                if (!r.ok) {
                    this.wizard.error = data.error || 'Failed to create project.';
                    return;
                }
                this.wizard.result = data.auto_alloc_result || null;
                this.wizard.step   = 2;

                // Reload groups so the new one appears.
                await this.loadGroups();
                // Load undo banner for the new group.
                if (this.wizard.result) {
                    await this.loadUndoBanner(this.wizard.result.node_group_id);
                }
            } catch (e) {
                this.wizard.error = e.message || 'Unexpected error.';
            } finally {
                this.wizard.saving = false;
            }
        },

        async dismissWizard() {
            // Mark onboarding as dismissed (skip path).
            try {
                await apiFetch('/api/v1/portal/pi/onboarding-complete', { method: 'POST' });
            } catch (_) {}
            this.wizard.show = false;
        },

        closeWizard() {
            this.wizard.show = false;
        },

        // ─── H3: Undo banner ─────────────────────────────────────────────────────

        async loadUndoBanner(groupID) {
            try {
                const r = await apiFetch(`/api/v1/node-groups/${groupID}/auto-policy-state`);
                if (!r.ok) return;
                const data = await r.json();
                if (data.auto_compute && data.undo_available) {
                    this.undoBanners[groupID] = {
                        available:  true,
                        hours:      Math.ceil(data.hours_remaining),
                        deadline:   data.undo_deadline,
                        partition:  data.slurm_partition_name || '',
                        groupName:  data.node_group_name || groupID,
                    };
                }
            } catch (_) { /* non-fatal */ }
        },

        async undoAutoPolicy(groupID) {
            if (!confirm('Undo the auto-allocation? This will delete the NodeGroup and its Slurm partition entry. This cannot be undone.')) return;
            try {
                const r = await apiFetch(`/api/v1/node-groups/${groupID}/undo-auto-policy`, { method: 'POST' });
                const data = await r.json();
                if (!r.ok) {
                    alert(data.error || 'Undo failed.');
                    return;
                }
                // Remove banner and reload groups.
                delete this.undoBanners[groupID];
                await this.loadGroups();
            } catch (e) {
                alert(e.message || 'Unexpected error during undo.');
            }
        },
    };
}

function apiFetch(url, opts) {
    return fetch(url, Object.assign({ credentials: 'same-origin' }, opts || {}));
}
