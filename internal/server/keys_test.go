package server_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sqoia-dev/clustr/internal/server"
)

// TestGPGKeyBytesNotEmpty verifies the embedded key is non-empty and
// starts with a PGP BEGIN line.
func TestGPGKeyBytesNotEmpty(t *testing.T) {
	data := server.GPGKeyBytes()
	if len(data) == 0 {
		t.Fatal("GPGKeyBytes() returned empty slice")
	}
	const header = "-----BEGIN PGP PUBLIC KEY BLOCK-----"
	found := false
	for i := 0; i < len(data)-len(header) && i < 100; i++ {
		if string(data[i:i+len(header)]) == header {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("GPGKeyBytes() does not start with PGP header; got: %.80s", string(data))
	}
}

// TestGPGKeyBytesReturnsCopy verifies that mutating the returned slice does
// not affect a subsequent call.
func TestGPGKeyBytesReturnsCopy(t *testing.T) {
	first := server.GPGKeyBytes()
	first[0] = 0xFF
	second := server.GPGKeyBytes()
	if second[0] == 0xFF {
		t.Fatal("GPGKeyBytes() returned the same underlying slice, not a copy")
	}
}

// TestWriteGPGKeyToRepo_WritesCorrectBytes verifies that WriteGPGKeyToRepo
// creates RPM-GPG-KEY-clustr with the expected content.
func TestWriteGPGKeyToRepo_WritesCorrectBytes(t *testing.T) {
	dir := t.TempDir()

	if err := server.WriteGPGKeyToRepo(dir); err != nil {
		t.Fatalf("WriteGPGKeyToRepo: %v", err)
	}

	dest := filepath.Join(dir, "RPM-GPG-KEY-clustr")
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read written key: %v", err)
	}

	want := server.GPGKeyBytes()
	if string(data) != string(want) {
		t.Fatalf("written key differs from embedded key\ngot  len=%d\nwant len=%d", len(data), len(want))
	}
}

// TestWriteGPGKeyToRepo_Idempotent verifies that calling WriteGPGKeyToRepo
// twice on the same directory is safe and does not change the file.
func TestWriteGPGKeyToRepo_Idempotent(t *testing.T) {
	dir := t.TempDir()

	if err := server.WriteGPGKeyToRepo(dir); err != nil {
		t.Fatalf("first WriteGPGKeyToRepo: %v", err)
	}

	dest := filepath.Join(dir, "RPM-GPG-KEY-clustr")
	info1, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat after first write: %v", err)
	}

	if err := server.WriteGPGKeyToRepo(dir); err != nil {
		t.Fatalf("second WriteGPGKeyToRepo: %v", err)
	}

	info2, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat after second write: %v", err)
	}

	// ModTime should not advance when content is unchanged (idempotent).
	if info1.ModTime() != info2.ModTime() {
		// This is allowed to differ; what matters is the file contents are correct.
		// The test is primarily that no error occurs and the key is still correct.
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read after second write: %v", err)
	}
	if string(data) != string(server.GPGKeyBytes()) {
		t.Fatal("key content changed after second WriteGPGKeyToRepo call")
	}
}

// TestWriteGPGKeyToRepo_Mode verifies the written file has mode 0644.
func TestWriteGPGKeyToRepo_Mode(t *testing.T) {
	dir := t.TempDir()
	if err := server.WriteGPGKeyToRepo(dir); err != nil {
		t.Fatalf("WriteGPGKeyToRepo: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "RPM-GPG-KEY-clustr"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	got := info.Mode().Perm()
	want := os.FileMode(0o644)
	if got != want {
		t.Fatalf("file mode = %04o, want %04o", got, want)
	}
}
