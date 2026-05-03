//go:build !webdist

package web

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeFS is a test FS with configurable per-path behaviour.
type fakeFS struct {
	files map[string]string // path → content (absent = ErrNotExist)
	errs  map[string]error  // path → forced error
}

func (f fakeFS) Open(name string) (fs.File, error) {
	if err, ok := f.errs[name]; ok {
		return nil, err
	}
	if content, ok := f.files[name]; ok {
		return &fakeFile{name: name, r: strings.NewReader(content)}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

type fakeFile struct {
	name string
	r    *strings.Reader
}

func (f *fakeFile) Read(b []byte) (int, error)  { return f.r.Read(b) }
func (f *fakeFile) Close() error                { return nil }
func (f *fakeFile) Stat() (fs.FileInfo, error)  { return &fakeInfo{name: f.name, size: int64(f.r.Len())}, nil }

type fakeInfo struct {
	name string
	size int64
}

func (i *fakeInfo) Name() string      { return i.name }
func (i *fakeInfo) Size() int64       { return i.size }
func (i *fakeInfo) Mode() fs.FileMode { return 0o444 }
func (i *fakeInfo) ModTime() time.Time { return time.Time{} }
func (i *fakeInfo) IsDir() bool       { return false }
func (i *fakeInfo) Sys() any          { return nil }

func newTestHandler(sub fs.FS) *spaHandler {
	return &spaHandler{
		fileServer: http.FileServer(http.FS(sub)),
		sub:        sub,
	}
}

// TestBUG7_ErrNotExistFallsBackToIndex verifies that a missing file causes
// a fallback to index.html rather than a 500 or 404.
func TestBUG7_ErrNotExistFallsBackToIndex(t *testing.T) {
	sub := fakeFS{
		files: map[string]string{
			"index.html": "<html>app</html>",
		},
	}
	h := newTestHandler(sub)

	req := httptest.NewRequest(http.MethodGet, "/some/spa/route", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<html>app</html>") {
		t.Fatalf("expected index.html content, got: %s", body)
	}
}

// TestBUG7_RealFSErrorReturns500 verifies that a genuine FS error (e.g. EIO)
// returns a 500 and does NOT fall back to index.html.
func TestBUG7_RealFSErrorReturns500(t *testing.T) {
	sub := fakeFS{
		files: map[string]string{
			"index.html": "<html>app</html>",
		},
		errs: map[string]error{
			"assets/corrupt.js": &fs.PathError{Op: "open", Path: "assets/corrupt.js", Err: syscall.EIO},
		},
	}
	h := newTestHandler(sub)

	req := httptest.NewRequest(http.MethodGet, "/assets/corrupt.js", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 got %d; body: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "<html>app</html>") {
		t.Fatal("500 path must not serve index.html")
	}
}

// TestPERF1_CacheHeaders validates the three cache header tiers.
func TestPERF1_CacheHeaders(t *testing.T) {
	sub := fakeFS{
		files: map[string]string{
			"index.html":        "<html>app</html>",
			"assets/app.abc.js": "// hashed asset",
			"favicon.svg":       "<svg/>",
		},
	}
	h := newTestHandler(sub)

	tests := []struct {
		path        string
		wantCC      string
		description string
	}{
		{
			path:        "/assets/app.abc.js",
			wantCC:      "public, max-age=31536000, immutable",
			description: "hashed Vite asset → immutable",
		},
		{
			path:        "/favicon.svg",
			wantCC:      "public, max-age=86400, stale-while-revalidate=604800",
			description: "stable top-level asset → 1-day cache",
		},
		{
			path:        "/index.html",
			wantCC:      "no-cache, no-store, must-revalidate",
			description: "SPA shell → no-store",
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			got := w.Header().Get("Cache-Control")
			if got != tc.wantCC {
				t.Errorf("path %s: want Cache-Control %q, got %q", tc.path, tc.wantCC, got)
			}
		})
	}
}

// TestPERF1_IndexServedWithNoStore verifies that the SPA fallback path (/ root)
// also delivers no-store on the index.html response.
func TestPERF1_IndexServedWithNoStore(t *testing.T) {
	sub := fakeFS{
		files: map[string]string{
			"index.html": "<html>app</html>",
		},
	}
	h := newTestHandler(sub)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	cc := w.Header().Get("Cache-Control")
	if !strings.Contains(cc, "no-store") {
		t.Errorf("root path: expected no-store in Cache-Control, got %q", cc)
	}
}

// Compile-time check: emptyFS satisfies fs.FS.
var _ fs.FS = emptyFS{}

// TestEmptyFSIndexHtml verifies that the stub emptyFS returns a valid,
// non-empty file for index.html so that routes falling back to index.html
// return a 200 with visible content — not a silent empty body that could mask
// routing/handler regressions.
func TestEmptyFSIndexHtml(t *testing.T) {
	e := emptyFS{}
	f, err := e.Open("index.html")
	if err != nil {
		t.Fatalf("emptyFS.Open(index.html): %v", err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("emptyFS read: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("emptyFS index.html must not be empty: stub should serve placeholder HTML")
	}
	if !strings.Contains(string(b), "clustr") {
		t.Fatalf("emptyFS index.html should contain 'clustr', got: %s", string(b))
	}
}

// TestEmptyFSMissingFile verifies that emptyFS returns ErrNotExist for unknown files.
func TestEmptyFSMissingFile(t *testing.T) {
	e := emptyFS{}
	_, err := e.Open("nonexistent.txt")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not exist") && err.Error() != "open nonexistent.txt: file does not exist" {
		// Acceptable as long as it wraps ErrNotExist
	}
	if !isNotExist(err) {
		t.Fatalf("expected ErrNotExist-wrapping error, got: %v", err)
	}
}

func isNotExist(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "not exist") ||
		strings.Contains(err.Error(), "no such file"))
}
