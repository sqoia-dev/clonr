// manager_test.go — unit tests for URL resolution and sentinel handling.
package slurm

import (
	"testing"
)

// TestResolveRepoURL covers the three resolution cases for resolveRepoURL:
//   - empty string → clustr-builtin URL
//   - "clustr-builtin" sentinel → clustr-builtin URL
//   - custom URL → returned unchanged
func TestResolveRepoURL(t *testing.T) {
	const serverURL = "http://10.99.0.1:8080"

	tests := []struct {
		name      string
		serverURL string
		stored    string
		wantURL   string
	}{
		{
			name:      "empty resolves to builtin",
			serverURL: serverURL,
			stored:    "",
			wantURL:   "http://10.99.0.1:8080/repo/el9-x86_64/",
		},
		{
			name:      "sentinel resolves to builtin",
			serverURL: serverURL,
			stored:    RepoSentinelBuiltin,
			wantURL:   "http://10.99.0.1:8080/repo/el9-x86_64/",
		},
		{
			name:      "custom OpenHPC URL unchanged",
			serverURL: serverURL,
			stored:    "https://repos.openhpc.community/OpenHPC/3/EL_9",
			wantURL:   "https://repos.openhpc.community/OpenHPC/3/EL_9",
		},
		{
			name:      "custom schedmd URL unchanged",
			serverURL: serverURL,
			stored:    "https://download.schedmd.com/slurm/rhel9/",
			wantURL:   "https://download.schedmd.com/slurm/rhel9/",
		},
		{
			name:      "trailing slash on ServerURL is stripped",
			serverURL: serverURL + "/",
			stored:    RepoSentinelBuiltin,
			wantURL:   "http://10.99.0.1:8080/repo/el9-x86_64/",
		},
		{
			name:      "empty ServerURL falls back to localhost",
			serverURL: "",
			stored:    RepoSentinelBuiltin,
			wantURL:   "http://localhost:8080/repo/el9-x86_64/",
		},
		{
			name:      "alternate port in ServerURL",
			serverURL: "http://10.99.0.1:9090",
			stored:    "",
			wantURL:   "http://10.99.0.1:9090/repo/el9-x86_64/",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manager{ServerURL: tc.serverURL}
			got := m.resolveRepoURL(tc.stored)
			if got != tc.wantURL {
				t.Errorf("resolveRepoURL(%q) with ServerURL=%q\n  got  %q\n  want %q",
					tc.stored, tc.serverURL, got, tc.wantURL)
			}
		})
	}
}

// TestRepoSentinelBuiltinValue guards that the sentinel string is not
// accidentally changed — it is irreversible once written to DB rows.
func TestRepoSentinelBuiltinValue(t *testing.T) {
	const want = "clustr-builtin"
	if RepoSentinelBuiltin != want {
		t.Errorf("RepoSentinelBuiltin changed: got %q, want %q — this is irreversible, do not rename", RepoSentinelBuiltin, want)
	}
}
