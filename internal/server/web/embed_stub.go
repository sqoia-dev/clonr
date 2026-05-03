//go:build !webdist

// Package web provides a stub FS when built without the web dist directory.
// The default build uses this stub; pass -tags webdist for production builds
// that embed the real SPA.
package web

import (
	"io/fs"
	"strings"
	"time"
)

// stubIndexHTML is served by the stub FS so that routes that fall back to
// index.html return a 200 with visible content rather than an empty body.
// An empty body can mask routing/handler regressions during tests and local
// manual validation.
const stubIndexHTML = `<!DOCTYPE html><html><head><title>clustr (dev stub)</title></head><body><div id="root"></div><p>Web bundle not built. Run <code>make web</code> or build with <code>-tags webdist</code>.</p></body></html>`

// distFS returns a stub fs.FS. The real dist is embedded only in production
// builds (go build -tags webdist).
func distFS() (fs.FS, error) {
	return emptyFS{}, nil
}

// emptyFS is a minimal fs.FS that contains only a placeholder index.html.
type emptyFS struct{}

func (emptyFS) Open(name string) (fs.File, error) {
	if name == "index.html" || name == "." {
		content := stubIndexHTML
		return &emptyFile{
			name:   "index.html",
			reader: strings.NewReader(content),
			size:   int64(len(content)),
		}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

type emptyFile struct {
	name   string
	reader *strings.Reader
	size   int64
}

func (f *emptyFile) Read(b []byte) (int, error) {
	return f.reader.Read(b)
}

func (f *emptyFile) Close() error { return nil }

func (f *emptyFile) Stat() (fs.FileInfo, error) {
	return &emptyFileInfo{name: f.name, size: f.size}, nil
}

type emptyFileInfo struct {
	name string
	size int64
}

func (i *emptyFileInfo) Name() string       { return i.name }
func (i *emptyFileInfo) Size() int64        { return i.size }
func (i *emptyFileInfo) Mode() fs.FileMode  { return 0o444 }
func (i *emptyFileInfo) ModTime() time.Time { return time.Time{} }
func (i *emptyFileInfo) IsDir() bool        { return false }
func (i *emptyFileInfo) Sys() any           { return nil }
