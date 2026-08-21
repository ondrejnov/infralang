package project

import (
	"strings"
	"testing"
)

func TestCompileLibvirtCompositionErasesDeterministically(t *testing.T) {
	first, err := Compile("../../examples/libvirt")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Compile("../../examples/libvirt")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Diagnostics)+len(second.Diagnostics) != 0 {
		t.Fatalf("libvirt diagnostics = %v %v", first.Diagnostics, second.Diagnostics)
	}
	if len(first.Artifacts) != 3 || len(second.Artifacts) != 3 {
		t.Fatalf("libvirt artifacts = %#v %#v", first.Artifacts, second.Artifacts)
	}
	for index := range first.Artifacts {
		if first.Artifacts[index].ModuleID != second.Artifacts[index].ModuleID || string(first.Artifacts[index].Data) != string(second.Artifacts[index].Data) {
			t.Fatalf("libvirt artifact %d is not byte-stable:\n%s\n%s", index, first.Artifacts[index].Data, second.Artifacts[index].Data)
		}
	}
	root := string(first.Artifacts[0].Data)
	for _, expected := range []string{
		`"vm_defaults"`, `"host_db1"`, `"host_db2"`, `"alias": "db1"`, `"alias": "db2"`,
		`"base_image_path": "/root/debian-13-generic-amd64.raw"`, `"volume_group": "vg_kvm"`,
		`"from": "module.db1"`, `"to": "module.host_db1.module.vm[\"db1\"]"`,
	} {
		if !strings.Contains(root, expected) {
			t.Fatalf("libvirt root lost stable identity %s:\n%s", expected, root)
		}
	}
	for _, erased := range []string{"hostDefinitions", "HostDeployment", "instantiate", "$component$", "$indexed$"} {
		if strings.Contains(root, erased) {
			t.Fatalf("compile-time construct %q leaked into libvirt artifact:\n%s", erased, root)
		}
	}
}
