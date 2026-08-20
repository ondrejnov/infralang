package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompileDiscoversCanonicalGraphAndOrdersArtifacts(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.infra", `
module remote "remote" from "registry.example/remote" {}
module terraformOnly "terraform_only" from "./terraform-only" {}
module zed "zed" from "./z-child" { name: "zed" }
module alpha "alpha" from "./a-child" { name: "alpha" }
`)
	writeProjectFile(t, root, "a-child/main.infra", `input name: string
output value = name`)
	writeProjectFile(t, root, "z-child/main.infra", `input name: string
output value = name`)
	writeProjectFile(t, root, "terraform-only/main.tf", `variable "name" { type = string }`)

	result, err := Compile(root)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("Compile() diagnostics = %v", result.Diagnostics)
	}
	if len(result.Artifacts) != 3 {
		t.Fatalf("Compile() artifacts = %#v", result.Artifacts)
	}
	want := []string{".", "a-child", "z-child"}
	for index, moduleID := range want {
		if result.Artifacts[index].ModuleID != moduleID {
			t.Errorf("artifacts[%d].ModuleID = %q, want %q", index, result.Artifacts[index].ModuleID, moduleID)
		}
	}
	if len(result.Interfaces) != 3 {
		t.Fatalf("interfaces = %#v", result.Interfaces)
	}
}

func TestCompileRejectsCompleteLocalModuleCycle(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.infra", `module child "child" from "./child" {}`)
	writeProjectFile(t, root, "child/main.infra", `module root "root" from ".." {}`)

	result, err := Compile(root)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if len(result.Artifacts) != 0 || len(result.Diagnostics) != 1 {
		t.Fatalf("Compile() = artifacts %#v, diagnostics %v", result.Artifacts, result.Diagnostics)
	}
	if !strings.Contains(result.Diagnostics[0].Message, ". -> child -> .") {
		t.Errorf("cycle diagnostic = %q", result.Diagnostics[0].Message)
	}
}

func TestCompileCanonicalSymlinkIdentityProducesOneChild(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.infra", `
module direct "direct" from "./child" {}
module linked "linked" from "./child-link" {}
`)
	writeProjectFile(t, root, "child/main.infra", `output value = "child"`)
	if err := os.Symlink(filepath.Join(root, "child"), filepath.Join(root, "child-link")); err != nil {
		t.Fatal(err)
	}

	result, err := Compile(root)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("Compile() diagnostics = %v", result.Diagnostics)
	}
	if len(result.Artifacts) != 2 || len(result.Interfaces) != 2 {
		t.Fatalf("canonical graph duplicated child: artifacts=%#v interfaces=%#v", result.Artifacts, result.Interfaces)
	}
}

func TestCompileRejectsCanonicalLocalModuleEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeProjectFile(t, root, "main.infra", `module escaped "escaped" from "./escaped" {}`)
	writeProjectFile(t, outside, "main.infra", `output value = "outside"`)
	if err := os.Symlink(outside, filepath.Join(root, "escaped")); err != nil {
		t.Fatal(err)
	}

	result, err := Compile(root)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if len(result.Artifacts) != 0 || len(result.Diagnostics) != 1 {
		t.Fatalf("Compile() = artifacts %#v, diagnostics %v", result.Artifacts, result.Diagnostics)
	}
	if !strings.Contains(result.Diagnostics[0].Message, "outside project root") {
		t.Errorf("escape diagnostic = %q", result.Diagnostics[0].Message)
	}
}

func TestCompileChecksLocalCallsAfterInterfaceCollection(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.infra", `
input forwarded: object { imageId "image_id": string } = { imageId: "ami" }
module child "child" from "./child" { ...inputs(forwarded) }
output image = child.image
`)
	writeProjectFile(t, root, "child/outputs.infra", `output image = imageId`)
	writeProjectFile(t, root, "child/inputs.infra", `input imageId "image_id": string`)

	result, err := Compile(root)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if len(result.Diagnostics) != 0 || len(result.Artifacts) != 2 {
		t.Fatalf("Compile() = artifacts %#v, diagnostics %v", result.Artifacts, result.Diagnostics)
	}
	if !strings.Contains(string(result.Artifacts[0].Data), `"image_id": "${var.forwarded.image_id}"`) {
		t.Fatalf("forwarded root artifact:\n%s", result.Artifacts[0].Data)
	}
}

func TestCompileLocalCallReportsIndependentErrorsWithoutArtifacts(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.infra", `module child "child" from "./child" { unknown: true }`)
	writeProjectFile(t, root, "child/main.infra", `input required: string`)

	result, err := Compile(root)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if len(result.Artifacts) != 0 || len(result.Diagnostics) != 2 {
		t.Fatalf("Compile() = artifacts %#v, diagnostics %v", result.Artifacts, result.Diagnostics)
	}
}

func TestCompileProjectDeterministic(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "z.infra", `output childValue = child.value`)
	writeProjectFile(t, root, "a.infra", `module child "child" from "./child" { value: "root" }`)
	writeProjectFile(t, root, "child/z.infra", `output value = value`)
	writeProjectFile(t, root, "child/a.infra", `input value: string`)

	first, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Diagnostics)+len(second.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %v %v", first.Diagnostics, second.Diagnostics)
	}
	if len(first.Artifacts) != len(second.Artifacts) {
		t.Fatalf("artifact counts differ: %d != %d", len(first.Artifacts), len(second.Artifacts))
	}
	for index := range first.Artifacts {
		if first.Artifacts[index].ModuleID != second.Artifacts[index].ModuleID || string(first.Artifacts[index].Data) != string(second.Artifacts[index].Data) {
			t.Fatalf("artifacts[%d] differ:\n%#v\n%#v", index, first.Artifacts[index], second.Artifacts[index])
		}
	}
}

func TestCompileDiscoversStaticIndexedLocalModules(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.infra", `
const calls = { second: "two", first: "one" }
static for key, value in calls {
  module children[key] value from "./child" { name: value }
}
output selected = children["second"].value
`)
	writeProjectFile(t, root, "child/main.infra", `
input name: string
output value = name
`)

	first, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Diagnostics)+len(second.Diagnostics) != 0 || len(first.Artifacts) != 2 || len(second.Artifacts) != 2 {
		t.Fatalf("static project compilation = %#v %#v", first, second)
	}
	if string(first.Artifacts[0].Data) != string(second.Artifacts[0].Data) {
		t.Fatalf("static project output is not deterministic:\n%s\n%s", first.Artifacts[0].Data, second.Artifacts[0].Data)
	}
	rootJSON := string(first.Artifacts[0].Data)
	if !strings.Contains(rootJSON, `"one"`) || !strings.Contains(rootJSON, `"two"`) || !strings.Contains(rootJSON, `${module.two.value}`) {
		t.Fatalf("static child modules were not discovered/lowered:\n%s", rootJSON)
	}
}

func writeProjectFile(t *testing.T, root, relative, source string) {
	t.Helper()
	filename := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
}
