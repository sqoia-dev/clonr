package handlers_test

// auth_no_ldap_test.go — architecture boundary test.
//
// RULE (feedback_web_auth_not_ldap.md): The web auth handler (auth.go) MUST NOT
// import anything from internal/ldap or any ldap-parent package. Web login is
// backed exclusively by the SQLite users table + bcrypt; LDAP is a node-provisioning
// concern, not a UI auth concern.
//
// If this test fails, someone introduced a direct LDAP import into the login path.
// Remove the import and use the DB-backed LoginWithPassword function instead.

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestAuthHandler_NoLDAPImport parses auth.go's AST and fails if any import path
// contains "/ldap" or starts with a known ldap package prefix. This enforces the
// architecture rule that web auth never depends on the LDAP module.
func TestAuthHandler_NoLDAPImport(t *testing.T) {
	// Locate auth.go relative to this test file.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed — cannot determine test file path")
	}
	authFile := filepath.Join(filepath.Dir(thisFile), "auth.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, authFile, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", authFile, err)
	}

	// Banned import patterns — any import path containing these strings is a violation.
	// "internal/ldap" covers direct imports of the ldap package or any sub-package.
	// "go-ldap" covers the go-ldap/ldap third-party library sneaking in directly.
	banned := []string{
		"/internal/ldap",
		"go-ldap/ldap",
	}

	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		for _, b := range banned {
			if strings.Contains(path, b) {
				t.Errorf(
					"ARCHITECTURE VIOLATION: auth.go imports %q which contains banned pattern %q.\n"+
						"  Web auth MUST NOT depend on the LDAP module (feedback_web_auth_not_ldap.md).\n"+
						"  Login is backed by internal/db users table + bcrypt only.\n"+
						"  Remove this import and use the LoginWithPassword function field instead.",
					path, b,
				)
			}
		}
	}
}
