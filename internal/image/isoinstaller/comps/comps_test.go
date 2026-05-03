package comps

import (
	"testing"
)

const testCompsXML = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE comps PUBLIC "-//Red Hat, Inc.//DTD Comps info//EN" "comps.dtd">
<comps>
  <environment>
    <id>minimal-environment</id>
    <_name>Minimal Install</_name>
    <_description>Basic functionality.</_description>
    <display_order>99</display_order>
    <grouplist><groupid>core</groupid></grouplist>
  </environment>
  <environment>
    <id>server-product-environment</id>
    <_name>Server</_name>
    <_description>An integrated, easy-to-manage server.</_description>
    <display_order>1</display_order>
    <grouplist><groupid>core</groupid></grouplist>
  </environment>
  <environment>
    <id>workstation-product-environment</id>
    <_name>Workstation</_name>
    <_description>A graphical desktop for workstation use.</_description>
    <display_order>10</display_order>
    <grouplist><groupid>core</groupid></grouplist>
  </environment>
</comps>`

const testCompsXMLNoMinimal = `<?xml version="1.0" encoding="UTF-8"?>
<comps>
  <environment>
    <id>server-product-environment</id>
    <_name>Server</_name>
    <_description>An integrated server.</_description>
    <display_order>1</display_order>
  </environment>
  <environment>
    <id>workstation-product-environment</id>
    <_name>Workstation</_name>
    <_description>A graphical desktop.</_description>
    <display_order>10</display_order>
  </environment>
</comps>`

const testCompsXMLEmpty = `<?xml version="1.0" encoding="UTF-8"?>
<comps>
</comps>`

func TestParseCompsXML_WithMinimal(t *testing.T) {
	groups, err := ParseCompsXML([]byte(testCompsXML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}

	// Verify sort order: display_order 1, 10, 99.
	if groups[0].ID != "server-product-environment" {
		t.Errorf("expected first group to be server-product-environment, got %s", groups[0].ID)
	}
	if groups[1].ID != "workstation-product-environment" {
		t.Errorf("expected second group to be workstation-product-environment, got %s", groups[1].ID)
	}
	if groups[2].ID != "minimal-environment" {
		t.Errorf("expected third group to be minimal-environment, got %s", groups[2].ID)
	}

	// Only minimal-environment should be the default.
	defaultCount := 0
	for _, g := range groups {
		if g.IsDefault {
			defaultCount++
			if g.ID != "minimal-environment" {
				t.Errorf("IsDefault=true on non-minimal group %q", g.ID)
			}
		}
	}
	if defaultCount != 1 {
		t.Errorf("expected exactly 1 default group, got %d", defaultCount)
	}
}

func TestParseCompsXML_NoMinimal_FallsBackToLowestOrder(t *testing.T) {
	groups, err := ParseCompsXML([]byte(testCompsXMLNoMinimal))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}

	// No minimal-environment: lowest display_order (1 = server) should be default.
	if !groups[0].IsDefault {
		t.Errorf("expected groups[0] (server-product-environment, order=1) to be default")
	}
	if groups[1].IsDefault {
		t.Errorf("expected groups[1] (workstation-product-environment) to NOT be default")
	}
}

func TestParseCompsXML_Empty(t *testing.T) {
	groups, err := ParseCompsXML([]byte(testCompsXMLEmpty))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if groups == nil {
		t.Error("expected non-nil empty slice, got nil")
	}
	if len(groups) != 0 {
		t.Errorf("expected 0 groups, got %d", len(groups))
	}
}

func TestParseCompsXML_InvalidXML(t *testing.T) {
	_, err := ParseCompsXML([]byte(`<not valid xml`))
	if err == nil {
		t.Error("expected error for invalid XML, got nil")
	}
}

func TestParseCompsXML_FieldValues(t *testing.T) {
	groups, err := ParseCompsXML([]byte(testCompsXML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find minimal-environment by ID regardless of position.
	var minimal *EnvironmentGroup
	for i := range groups {
		if groups[i].ID == "minimal-environment" {
			minimal = &groups[i]
			break
		}
	}
	if minimal == nil {
		t.Fatal("minimal-environment not found in parsed groups")
	}
	if minimal.Name != "Minimal Install" {
		t.Errorf("Name: want %q, got %q", "Minimal Install", minimal.Name)
	}
	if minimal.Description != "Basic functionality." {
		t.Errorf("Description: want %q, got %q", "Basic functionality.", minimal.Description)
	}
	if minimal.DisplayOrder != 99 {
		t.Errorf("DisplayOrder: want 99, got %d", minimal.DisplayOrder)
	}
}
