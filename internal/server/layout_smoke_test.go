package server_test

// C2-6: Smoke tests asserting Alpine.js and HTMX are present in the served
// HTML layout pages. These guard against accidental deletion of vendor scripts
// from index.html or portal.html.

import (
	"net/http"
	"strings"
	"testing"
)

// TestLayoutIncludesAlpineAndHTMX verifies that the main SPA layout (index.html)
// references both Alpine.js and HTMX vendor scripts. This is a guard test —
// if either library is accidentally removed from the layout the test fails
// loudly before the change ships to production.
func TestLayoutIncludesAlpineAndHTMX(t *testing.T) {
	_, ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusSeeOther {
		// Redirect to /login is expected when auth is required; follow it.
		loc := resp.Header.Get("Location")
		resp.Body.Close()
		resp, err = http.Get(ts.URL + loc)
		if err != nil {
			t.Fatalf("GET %s: %v", loc, err)
		}
		defer resp.Body.Close()
	}

	// In dev mode the server serves index.html even unauthenticated (or the
	// login page which also includes the vendor scripts). Read the body and
	// check for the script tags.
	var body strings.Builder
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			body.Write(buf[:n])
		}
		if readErr != nil {
			break
		}
	}
	html := body.String()

	// The vendor script filenames are pinned — check exact file names so a
	// version bump without a corresponding CI re-vendor is caught here.
	// F1 (v1.5.0): index.html uses the Alpine CSP build (alpine-csp-*.min.js)
	// instead of the standard build. Accept only the CSP build here.
	checks := []struct {
		name    string
		needle  string
	}{
		{"Alpine.js 3.15.11 CSP build", "alpine-csp-3.15.11.min.js"},
		{"HTMX 2.0.9", "htmx-2.0.9.min.js"},
	}
	for _, c := range checks {
		if !strings.Contains(html, c.needle) {
			t.Errorf("layout smoke: %s script (%s) not found in HTML response (status %d)", c.name, c.needle, resp.StatusCode)
		}
	}
}

// TestPortalLayoutIncludesAlpineAndHTMX verifies the researcher portal page
// (portal.html) also includes both libraries.
func TestPortalLayoutIncludesAlpineAndHTMX(t *testing.T) {
	_, ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/portal")
	if err != nil {
		t.Fatalf("GET /portal: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusSeeOther {
		loc := resp.Header.Get("Location")
		resp.Body.Close()
		resp, err = http.Get(ts.URL + loc)
		if err != nil {
			t.Fatalf("GET %s: %v", loc, err)
		}
		defer resp.Body.Close()
	}

	var body strings.Builder
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			body.Write(buf[:n])
		}
		if readErr != nil {
			break
		}
	}
	html := body.String()

	// F1 (v1.5.0): portal pages use the Alpine CSP build.
	checks := []struct {
		name   string
		needle string
	}{
		{"Alpine.js 3.15.11 CSP build", "alpine-csp-3.15.11.min.js"},
		{"HTMX 2.0.9", "htmx-2.0.9.min.js"},
	}
	for _, c := range checks {
		if !strings.Contains(html, c.needle) {
			t.Errorf("portal layout smoke: %s script (%s) not found in portal HTML (status %d)", c.name, c.needle, resp.StatusCode)
		}
	}
}
