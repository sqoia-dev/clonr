// internal/server/alerts/handler_test.go — HTTP handler regression tests
// for Codex post-ship review issues #8 and #9.
//
//   - #8: writeStoreError previously matched on the literal
//     "system_alerts:" prefix in err.Error(), so DB errors wrapped via
//     fmt.Errorf("system_alerts: ...") were classified as 4xx
//     validation errors instead of 5xx server errors.  We now use
//     errors.Is against the typed ErrValidation sentinel.
//
//   - #9: chi does NOT decode percent-encoded slashes inside path
//     segments, so a device=ctrl0%2Fvd1 was persisted with the %2F
//     intact, mismatching subsequent lookups.  normaliseDeviceParam
//     now url.PathUnescapes the device.

package alerts

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// ─── Issue #8: validation vs DB error classification ─────────────────────────

// fakeStore replaces the real Store for the validation test.  We
// implement only the methods writeStoreError funnels through.  The
// handler hits Push/Set/Unset/List which call into the *Store, so for
// this test we deliberately route through writeStoreError directly.
//
// Simpler approach: invoke writeStoreError with crafted errors and
// inspect the response.  No DB needed.
func TestWriteStoreError_DBErrorReturns500(t *testing.T) {
	w := httptest.NewRecorder()

	// Mimic exactly what the store wraps DB errors with — same
	// "system_alerts:" prefix that used to confuse the classifier.
	dbErr := fmt.Errorf("system_alerts: upsert update: %w", sql.ErrConnDone)
	writeStoreError(w, dbErr)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 for DB error wrapped with system_alerts: prefix", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Body should be the generic "internal error" string, NOT the raw
	// DB error message — DB internals don't leak to clients.
	if body["error"] != "internal error" {
		t.Errorf("error body = %q, want %q", body["error"], "internal error")
	}
}

// TestWriteStoreError_ValidationError400 verifies the happy path —
// validation errors still return 400.
func TestWriteStoreError_ValidationError400(t *testing.T) {
	w := httptest.NewRecorder()
	writeStoreError(w, ErrEmptyKey)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for ErrEmptyKey", w.Code)
	}

	w2 := httptest.NewRecorder()
	wrapped := fmt.Errorf("%w: bogus level", ErrInvalidLevel)
	writeStoreError(w2, wrapped)
	if w2.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for ErrInvalidLevel chain", w2.Code)
	}

	// Sanity: errors.Is must see the sentinel through the chain.
	if !errors.Is(wrapped, ErrValidation) {
		t.Error("errors.Is(wrapped, ErrValidation) = false; sentinel chain broken")
	}
	if !errors.Is(ErrInvalidLevel, ErrValidation) {
		t.Error("ErrInvalidLevel does not wrap ErrValidation")
	}
	if !errors.Is(ErrEmptyKey, ErrValidation) {
		t.Error("ErrEmptyKey does not wrap ErrValidation")
	}
}

// ─── Issue #9: device path URL-decoding ──────────────────────────────────────

// TestNormaliseDeviceParam_DecodesPercentEncodedSlashes is the unit-level
// guard.
func TestNormaliseDeviceParam_DecodesPercentEncodedSlashes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ctrl0/vd1", "ctrl0/vd1"},     // already-decoded passthrough
		{"ctrl0%2Fvd1", "ctrl0/vd1"},   // percent-encoded slash
		{"ctrl0%2fvd1", "ctrl0/vd1"},   // lowercase hex too
		{"-", ""},                      // sentinel
		{"vd%2520x", "vd%20x"},         // double-escape: only one round of decode
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := normaliseDeviceParam(tc.in); got != tc.want {
				t.Errorf("normaliseDeviceParam(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestHandleSet_PercentEncodedDeviceStoredDecoded is the end-to-end
// guard: an HTTP POST with device=ctrl0%2Fvd1 must result in the
// alert being persisted with device="ctrl0/vd1" so a subsequent
// lookup with the decoded text (or the same encoded URL) finds it.
func TestHandleSet_PercentEncodedDeviceStoredDecoded(t *testing.T) {
	d := openTestDB(t)
	s := NewStore(d)
	h := &Handler{Store: s}

	r := chi.NewRouter()
	h.Mount(r)

	body := strings.NewReader(`{"level":"warn","message":"vd1 degraded"}`)
	// Use url.PathEscape just to make explicit what the wire shape is.
	encoded := url.PathEscape("ctrl0/vd1")
	if encoded != "ctrl0%2Fvd1" {
		t.Fatalf("PathEscape sanity: got %q", encoded)
	}
	target := "/system_alerts/set/raid_degraded/" + encoded

	req := httptest.NewRequest("POST", target, body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	// The persisted row must carry the decoded device.  List directly
	// from the store (bypassing HTTP) to confirm what's on disk.
	list, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1: %+v", len(list), list)
	}
	if list[0].Device != "ctrl0/vd1" {
		t.Errorf("stored device = %q, want %q (percent-encoded slash should be decoded)", list[0].Device, "ctrl0/vd1")
	}

	// And a subsequent unset using the same encoded path must hit the
	// row.  This is the round-trip the operator UI exercises.
	unsetReq := httptest.NewRequest("POST", "/system_alerts/unset/raid_degraded/"+encoded, bytes.NewReader(nil))
	unsetW := httptest.NewRecorder()
	r.ServeHTTP(unsetW, unsetReq)
	if unsetW.Code != http.StatusOK {
		t.Fatalf("unset status = %d, body=%s", unsetW.Code, unsetW.Body.String())
	}
	var unsetResp map[string]any
	_ = json.Unmarshal(unsetW.Body.Bytes(), &unsetResp)
	if cleared, _ := unsetResp["cleared"].(bool); !cleared {
		t.Errorf("unset cleared=false — encoded device did not match decoded stored device")
	}
}

