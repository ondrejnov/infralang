package project

import (
	"strings"
	"testing"

	"github.com/ondrejnov/infralang/internal/compiler"
)

func TestCompileLVMExampleWithTypedSSHConfig(t *testing.T) {
	result, err := CompileWithOptions("../../examples/lvm", compiler.CompileOptions{
		ProviderSchemas: compiler.ProviderSchemas{
			"github.com/ondrejnov/lvm": {
				BlockTypes: map[string]compiler.ProviderBlockSchema{
					"ssh": {NestingMode: "single"},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != 0 || len(result.Artifacts) != 1 {
		t.Fatalf("LVM compilation = artifacts %#v, diagnostics %v", result.Artifacts, result.Diagnostics)
	}
	artifact := string(result.Artifacts[0].Data)
	for _, expected := range []string{
		`"ssh": [`,
		`"host": "${var.ssh.host}"`,
		`"known_hosts_file": "${var.ssh.known_hosts_file}"`,
		`object({host = string, user = string`,
	} {
		if !strings.Contains(artifact, expected) {
			t.Fatalf("typed SSH config missing %q:\n%s", expected, artifact)
		}
	}
	if strings.Contains(artifact, `"ssh": "${var.ssh}"`) {
		t.Fatalf("typed SSH config was emitted as a string block:\n%s", artifact)
	}
	if strings.Contains(artifact, "SshConfig") {
		t.Fatalf("type alias leaked into Terraform artifact:\n%s", artifact)
	}
}
