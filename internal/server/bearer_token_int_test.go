package server_test

// bearer_token_int_test.go — SEC-1 integration tests.
//
// Verifies that:
//   1. HTTP endpoints reject ?token= (no Authorization header) with 401.
//   2. HTTP endpoints accept Authorization: Bearer with 200.
//
// WebSocket acceptance via ?token= is not tested here because it requires a
// real WS upgrade (gorilla/websocket client + live server) and depends on
// systemd-nspawn / BMC infra that is absent in CI. The unit test for
// wsTokenLift in bearer_token_test.go covers the middleware logic directly.

import (
	"net/http"
	"testing"
)

// TestHTTPEndpoint_QueryParamToken_Rejected is the SEC-1 integration regression
// guard. It sends a valid admin API key via ?token= on a plain HTTP GET and
// expects 401. Before the fix, the shared auth middleware accepted query-param
// tokens on every endpoint, leaking credentials into access logs and referrers.
func TestHTTPEndpoint_QueryParamToken_Rejected(t *testing.T) {
	_, ts, fullKey := newAuthTestServer(t)

	// Sanity: the key must be valid via the Authorization header.
	headReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/nodes", nil)
	headReq.Header.Set("Authorization", "Bearer "+fullKey)
	headResp, err := http.DefaultClient.Do(headReq)
	if err != nil {
		t.Fatalf("header auth request: %v", err)
	}
	headResp.Body.Close()
	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("pre-condition: header auth on /nodes: got %d, want 200", headResp.StatusCode)
	}

	// Core assertion: the same token sent via ?token= must be rejected.
	queryReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/nodes?token="+fullKey, nil)
	queryResp, err := http.DefaultClient.Do(queryReq)
	if err != nil {
		t.Fatalf("query-param auth request: %v", err)
	}
	queryResp.Body.Close()
	if queryResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("SEC-1 INTEGRATION REGRESSION: ?token= on HTTP endpoint returned %d, want 401", queryResp.StatusCode)
	}
}

// TestHTTPEndpoint_BearerHeader_StillAccepted confirms that the standard
// Authorization: Bearer path continues to work after the SEC-1 hardening.
func TestHTTPEndpoint_BearerHeader_StillAccepted(t *testing.T) {
	_, ts, fullKey := newAuthTestServer(t)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer "+fullKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("nodes request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("bearer header auth on /nodes: got %d, want 200", resp.StatusCode)
	}
}
