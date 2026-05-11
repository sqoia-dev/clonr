package plugins

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// TestSSSDPlugin_RendersOneInstructionPerNode verifies that Render returns
// exactly one InstallInstruction for a node with a non-nil LDAPConfig, and
// that the instruction targets /etc/sssd/sssd.conf with the expected content.
func TestSSSDPlugin_RendersOneInstructionPerNode(t *testing.T) {
	p := SSSDPlugin{}

	nodes := []struct {
		id   string
		ldap *api.LDAPNodeConfig
	}{
		{
			"node-a",
			&api.LDAPNodeConfig{
				ServerURI:         "ldaps://clustr-server:636",
				BaseDN:            "dc=cluster,dc=local",
				ServiceBindDN:     "cn=node-reader,ou=services,dc=cluster,dc=local",
				ServiceBindPasswd: "s3cr3t",
			},
		},
		{
			"node-b",
			&api.LDAPNodeConfig{
				ServerURI:         "ldaps://10.0.0.1:636",
				BaseDN:            "dc=hpc,dc=example,dc=com",
				ServiceBindDN:     "cn=bind,ou=svc,dc=hpc,dc=example,dc=com",
				ServiceBindPasswd: "bind-passwd",
			},
		},
	}

	for _, n := range nodes {
		state := config.ClusterState{
			NodeID: n.id,
			NodeConfig: api.NodeConfig{
				ID:         n.id,
				LDAPConfig: n.ldap,
			},
		}

		instrs, err := p.Render(state)
		if err != nil {
			t.Errorf("node %s: Render returned unexpected error: %v", n.id, err)
			continue
		}
		if len(instrs) != 1 {
			t.Errorf("node %s: len(instrs) = %d, want 1", n.id, len(instrs))
			continue
		}

		instr := instrs[0]
		if instr.Opcode != "overwrite" {
			t.Errorf("node %s: Opcode = %q, want \"overwrite\"", n.id, instr.Opcode)
		}
		if instr.Target != "/etc/sssd/sssd.conf" {
			t.Errorf("node %s: Target = %q, want \"/etc/sssd/sssd.conf\"", n.id, instr.Target)
		}
		if instr.Anchors != nil {
			t.Errorf("node %s: Anchors should be nil (sssd.conf is full-file overwrite), got %+v", n.id, instr.Anchors)
		}
		// Content must contain the domain extracted from BaseDN.
		domain := ldapDomainFromBaseDN(n.ldap.BaseDN)
		if !strings.Contains(instr.Payload, "domains = "+domain) {
			t.Errorf("node %s: Payload does not contain 'domains = %s'\npayload:\n%s", n.id, domain, instr.Payload)
		}
		if !strings.Contains(instr.Payload, n.ldap.ServerURI) {
			t.Errorf("node %s: Payload does not contain ServerURI %q", n.id, n.ldap.ServerURI)
		}
		if !strings.Contains(instr.Payload, n.ldap.ServiceBindDN) {
			t.Errorf("node %s: Payload does not contain ServiceBindDN %q", n.id, n.ldap.ServiceBindDN)
		}
	}
}

// TestSSSDPlugin_NilLDAPConfigReturnsNil verifies that a node without
// LDAPConfig produces a nil slice and no error.
func TestSSSDPlugin_NilLDAPConfigReturnsNil(t *testing.T) {
	p := SSSDPlugin{}
	state := config.ClusterState{
		NodeID: "node-no-ldap",
		NodeConfig: api.NodeConfig{
			ID:         "node-no-ldap",
			LDAPConfig: nil,
		},
	}

	instrs, err := p.Render(state)
	if err != nil {
		t.Errorf("Render returned unexpected error for nil LDAPConfig: %v", err)
	}
	if instrs != nil {
		t.Errorf("expected nil instructions for nil LDAPConfig, got %+v", instrs)
	}
}

// TestSSSDPlugin_IdempotentSameStateSameOutput verifies that calling Render
// twice with identical ClusterState produces byte-identical output.
func TestSSSDPlugin_IdempotentSameStateSameOutput(t *testing.T) {
	p := SSSDPlugin{}
	state := config.ClusterState{
		NodeID: "node-idem",
		NodeConfig: api.NodeConfig{
			ID: "node-idem",
			LDAPConfig: &api.LDAPNodeConfig{
				ServerURI:         "ldaps://clustr-server:636",
				BaseDN:            "dc=cluster,dc=local",
				ServiceBindDN:     "cn=node-reader,ou=services,dc=cluster,dc=local",
				ServiceBindPasswd: "idempotent-test-pass",
			},
		},
	}

	first, err := p.Render(state)
	if err != nil {
		t.Fatalf("first Render: %v", err)
	}
	second, err := p.Render(state)
	if err != nil {
		t.Fatalf("second Render: %v", err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Errorf("Render is not idempotent:\n  first  = %+v\n  second = %+v", first, second)
	}
}

// TestSSSDPlugin_HashStableAcrossCalls verifies that HashInstructions produces
// the same digest across repeated calls with identical state.
func TestSSSDPlugin_HashStableAcrossCalls(t *testing.T) {
	p := SSSDPlugin{}
	state := config.ClusterState{
		NodeID: "node-hash",
		NodeConfig: api.NodeConfig{
			ID: "node-hash",
			LDAPConfig: &api.LDAPNodeConfig{
				ServerURI:         "ldaps://clustr-server:636",
				BaseDN:            "dc=cluster,dc=local",
				ServiceBindDN:     "cn=node-reader,ou=services,dc=cluster,dc=local",
				ServiceBindPasswd: "hash-test-pass",
			},
		},
	}

	instrs, err := p.Render(state)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	hash1, err := config.HashInstructions(instrs)
	if err != nil {
		t.Fatalf("HashInstructions (first): %v", err)
	}
	hash2, err := config.HashInstructions(instrs)
	if err != nil {
		t.Fatalf("HashInstructions (second): %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("hash is not stable: first=%q second=%q", hash1, hash2)
	}

	// Different bind password must produce a different hash.
	altState := state
	altLDAP := *state.NodeConfig.LDAPConfig
	altLDAP.ServiceBindPasswd = "different-pass"
	altState.NodeConfig.LDAPConfig = &altLDAP
	altInstrs, err := p.Render(altState)
	if err != nil {
		t.Fatalf("Render (alt): %v", err)
	}
	altHash, err := config.HashInstructions(altInstrs)
	if err != nil {
		t.Fatalf("HashInstructions (alt): %v", err)
	}
	if hash1 == altHash {
		t.Error("different ServiceBindPasswd produced identical hashes — hash function is broken")
	}
}

// TestSSSDPlugin_Name verifies the stable plugin name used in DB rows.
func TestSSSDPlugin_Name(t *testing.T) {
	p := SSSDPlugin{}
	if got := p.Name(); got != "sssd" {
		t.Errorf("Name() = %q, want \"sssd\"", got)
	}
}

// TestSSSDPlugin_WatchedKeys verifies that the plugin subscribes to the
// correct literal watch-key and that SSSDWatchKey() returns the same value.
func TestSSSDPlugin_WatchedKeys(t *testing.T) {
	p := SSSDPlugin{}
	keys := p.WatchedKeys()
	if len(keys) != 1 {
		t.Fatalf("WatchedKeys() returned %d keys, want 1: %v", len(keys), keys)
	}
	if keys[0] != sssdWatchKey {
		t.Errorf("WatchedKeys()[0] = %q, want %q", keys[0], sssdWatchKey)
	}
	if SSSDWatchKey() != sssdWatchKey {
		t.Errorf("SSSDWatchKey() = %q, want %q", SSSDWatchKey(), sssdWatchKey)
	}
}

// ─── ValidatePayload tests (Sprint 42 Day 3) ─────────────────────────────────

// TestSSSDPlugin_ValidatePayload_HappyPath verifies that a well-formed payload
// with valid ldap_uri and base_dn produces no violations.
func TestSSSDPlugin_ValidatePayload_HappyPath(t *testing.T) {
	p := SSSDPlugin{}
	cases := []struct {
		name    string
		payload string
	}{
		{
			name:    "valid ldap URI and base_dn",
			payload: `{"config":{"ldap_uri":"ldap://clustr.local:389","base_dn":"dc=cluster,dc=local"}}`,
		},
		{
			name:    "valid ldaps URI",
			payload: `{"config":{"ldap_uri":"ldaps://clustr.local:636","base_dn":"dc=hpc,dc=example,dc=com"}}`,
		},
		{
			name:    "no config sub-object (stage request with only node_id+plugin_name)",
			payload: `{"node_id":"node-1","plugin_name":"sssd"}`,
		},
		{
			name:    "empty payload",
			payload: ``,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			violations := p.ValidatePayload([]byte(tc.payload))
			if len(violations) != 0 {
				t.Errorf("expected no violations, got %+v", violations)
			}
		})
	}
}

// TestSSSDPlugin_ValidatePayload_BadURI verifies that an invalid LDAP URI is rejected.
func TestSSSDPlugin_ValidatePayload_BadURI(t *testing.T) {
	p := SSSDPlugin{}
	cases := []struct {
		name    string
		payload string
		wantCode string
	}{
		{
			name:     "http scheme instead of ldap",
			payload:  `{"config":{"ldap_uri":"http://clustr.local:80","base_dn":"dc=cluster,dc=local"}}`,
			wantCode: "invalid_uri",
		},
		{
			name:     "empty host",
			payload:  `{"config":{"ldap_uri":"ldap://","base_dn":"dc=cluster,dc=local"}}`,
			wantCode: "invalid_uri",
		},
		{
			name:     "no scheme",
			payload:  `{"config":{"ldap_uri":"clustr.local:389","base_dn":"dc=cluster,dc=local"}}`,
			wantCode: "invalid_uri",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			violations := p.ValidatePayload([]byte(tc.payload))
			if len(violations) == 0 {
				t.Fatalf("expected violations for %q, got none", tc.payload)
			}
			found := false
			for _, v := range violations {
				if v.Code == tc.wantCode && v.Path == "config.ldap_uri" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected violation with code=%q path=config.ldap_uri, got %+v", tc.wantCode, violations)
			}
		})
	}
}

// TestSSSDPlugin_ValidatePayload_BadBaseDN verifies that a base_dn without dc= is rejected.
func TestSSSDPlugin_ValidatePayload_BadBaseDN(t *testing.T) {
	p := SSSDPlugin{}
	payload := `{"config":{"ldap_uri":"ldap://clustr.local:389","base_dn":"ou=people,o=example"}}`
	violations := p.ValidatePayload([]byte(payload))
	if len(violations) == 0 {
		t.Fatal("expected violations for base_dn without dc=, got none")
	}
	found := false
	for _, v := range violations {
		if v.Code == "invalid_base_dn" && v.Path == "config.base_dn" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected violation with code=invalid_base_dn path=config.base_dn, got %+v", violations)
	}
}

// TestSSSDPlugin_ImplementsPayloadValidator is a compile-time + runtime
// assertion that SSSDPlugin satisfies the config.PayloadValidator interface.
func TestSSSDPlugin_ImplementsPayloadValidator(t *testing.T) {
	var _ config.PayloadValidator = SSSDPlugin{}
}

// TestSSSDPlugin_ContentMatchesImperativePath verifies that the rendered
// sssd.conf content is structurally identical to what the imperative deploy
// path (deploy.renderSSSDConf) would produce. We compare the key structural
// elements rather than byte-for-byte since the functions are separate copies.
func TestSSSDPlugin_ContentMatchesImperativePath(t *testing.T) {
	ldapCfg := &api.LDAPNodeConfig{
		ServerURI:         "ldaps://clustr-server:636",
		BaseDN:            "dc=cluster,dc=local",
		ServiceBindDN:     "cn=node-reader,ou=services,dc=cluster,dc=local",
		ServiceBindPasswd: "node-secret",
	}

	p := SSSDPlugin{}
	state := config.ClusterState{
		NodeID: "node-match",
		NodeConfig: api.NodeConfig{
			ID:         "node-match",
			LDAPConfig: ldapCfg,
		},
	}

	instrs, err := p.Render(state)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(instrs) != 1 {
		t.Fatalf("expected 1 instruction, got %d", len(instrs))
	}

	content := instrs[0].Payload

	// Structural assertions matching what deploy.renderSSSDConf emits.
	wantLines := []string{
		"# sssd.conf — generated by clustr-serverd",
		"# DO NOT EDIT — managed by clustr. Regenerated on each reimage.",
		"[sssd]",
		"services = nss, pam, ssh",
		"domains = cluster",
		"[nss]",
		"[pam]",
		"[domain/cluster]",
		"id_provider = ldap",
		"auth_provider = ldap",
		"ldap_uri = ldaps://clustr-server:636",
		"ldap_search_base = dc=cluster,dc=local",
		"ldap_default_bind_dn = cn=node-reader,ou=services,dc=cluster,dc=local",
		"ldap_default_authtok = node-secret",
		"ldap_user_search_base = ou=people,dc=cluster,dc=local",
		"ldap_group_search_base = ou=groups,dc=cluster,dc=local",
		"ldap_tls_cacert = /etc/pki/ca-trust/source/anchors/clustr-ca.crt",
		"cache_credentials = true",
	}

	for _, want := range wantLines {
		if !strings.Contains(content, want) {
			t.Errorf("rendered sssd.conf missing line %q\nfull content:\n%s", want, content)
		}
	}
}
