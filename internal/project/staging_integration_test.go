package project

import (
	"strings"
	"testing"
)

func TestCompileStagingCompositionErasesDeterministically(t *testing.T) {
	first, err := Compile("../../examples/staging")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Compile("../../examples/staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Diagnostics)+len(second.Diagnostics) != 0 {
		t.Fatalf("staging diagnostics = %v %v", first.Diagnostics, second.Diagnostics)
	}
	if len(first.Artifacts) != 3 || len(second.Artifacts) != 3 {
		t.Fatalf("staging artifacts = %#v %#v", first.Artifacts, second.Artifacts)
	}
	for index := range first.Artifacts {
		if first.Artifacts[index].ModuleID != second.Artifacts[index].ModuleID || string(first.Artifacts[index].Data) != string(second.Artifacts[index].Data) {
			t.Fatalf("staging artifact %d is not byte-stable:\n%s\n%s", index, first.Artifacts[index].Data, second.Artifacts[index].Data)
		}
	}
	root := string(first.Artifacts[0].Data)
	for _, expected := range []string{
		`"vm_defaults"`, `"host_db1"`, `"host_db2"`, `"alias": "db1"`, `"alias": "db2"`,
		`"from": "module.db1"`, `"to": "module.host_db1.module.vm[\"db1\"]"`,
	} {
		if !strings.Contains(root, expected) {
			t.Fatalf("staging root lost stable identity %s:\n%s", expected, root)
		}
	}
	for _, erased := range []string{"hostDefinitions", "HostDeployment", "instantiate", "$component$", "$indexed$"} {
		if strings.Contains(root, erased) {
			t.Fatalf("compile-time construct %q leaked into staging artifact:\n%s", erased, root)
		}
	}
}
