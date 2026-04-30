package handlers

// users_search.go — unified user search across local + LDAP sources (Sprint 7, NODE-SUDO-5).
//
// GET /api/v1/users/search?q=<query>&source=all|local|ldap
//
// Returns a merged list of user entries from both local DB users and LDAP directory.
// LDAP results are only included when the LDAP module is ready; if not ready,
// only local users are returned (with source="local").

import (
	"net/http"
	"strings"

	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/ldap"
)

// UserSearchResult is a single entry in the unified search response.
type UserSearchResult struct {
	Identifier  string `json:"identifier"`   // uid (LDAP) or username (local)
	DisplayName string `json:"display_name"` // full name or username
	Email       string `json:"email,omitempty"`
	Source      string `json:"source"` // "ldap" | "local"
}

// UsersSearchHandler handles GET /api/v1/users/search.
type UsersSearchHandler struct {
	DB      *db.DB
	LDAPMgr *ldap.Manager // may be nil; LDAP search is skipped if nil or not ready
}

// HandleSearch handles GET /api/v1/users/search?q=<query>&source=all|local|ldap.
func (h *UsersSearchHandler) HandleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "all"
	}

	var results []UserSearchResult

	// ── Local users ──────────────────────────────────────────────────────────
	if source == "all" || source == "local" {
		users, err := h.DB.ListUsers(r.Context())
		if err == nil {
			for _, u := range users {
				if u.IsDisabled() {
					continue
				}
				if q != "" && !strings.Contains(strings.ToLower(u.Username), q) {
					continue
				}
				results = append(results, UserSearchResult{
					Identifier:  u.Username,
					DisplayName: u.Username,
					Source:      "local",
				})
			}
		}
	}

	// ── LDAP users ───────────────────────────────────────────────────────────
	if (source == "all" || source == "ldap") && h.LDAPMgr != nil {
		dit, err := h.LDAPMgr.ReaderDIT(r.Context())
		if err == nil {
			ldapUsers, err := dit.ListUsers()
			if err == nil {
				for _, u := range ldapUsers {
					if q != "" &&
						!strings.Contains(strings.ToLower(u.UID), q) &&
						!strings.Contains(strings.ToLower(u.GivenName+" "+u.SN), q) &&
						!strings.Contains(strings.ToLower(u.Mail), q) {
						continue
					}
					displayName := strings.TrimSpace(u.GivenName + " " + u.SN)
					if displayName == "" {
						displayName = u.UID
					}
					results = append(results, UserSearchResult{
						Identifier:  u.UID,
						DisplayName: displayName,
						Email:       u.Mail,
						Source:      "ldap",
					})
				}
			}
		}
	}

	if results == nil {
		results = []UserSearchResult{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"users": results,
		"total": len(results),
	})
}
