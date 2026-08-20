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
import module Remote from "registry.example/remote"
import module TerraformOnly from "./terraform-only"
import module Zed from "./z-child"
import module Alpha from "./a-child"
module remote = Remote("remote", {})
module terraformOnly = TerraformOnly("terraform_only", {})
module zed = Zed("zed", { name: "zed" })
module alpha = Alpha("alpha", { name: "alpha" })
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
	writeProjectFile(t, root, "main.infra", "import module Child from \"./child\"\nmodule child = Child(\"child\", {})")
	writeProjectFile(t, root, "child/main.infra", "import module Root from \"..\"\nmodule root = Root(\"root\", {})")

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
import module Direct from "./child"
import module Linked from "./child-link"
module direct = Direct("direct", {})
module linked = Linked("linked", {})
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
	writeProjectFile(t, root, "main.infra", "import module Escaped from \"./escaped\"\nmodule escaped = Escaped(\"escaped\", {})")
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
import module Child from "./child"
input forwarded: object { imageId "image_id": string } = { imageId: "ami" }
module child = Child("child", { ...inputs(forwarded) })
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
	writeProjectFile(t, root, "main.infra", "import module Child from \"./child\"\nmodule child = Child(\"child\", { unknown: true })")
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
	writeProjectFile(t, root, "imports.infra", `import module Child from "./child"`)
	writeProjectFile(t, root, "a.infra", `module child = Child("child", { value: "root" })`)
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
import module Child from "./child"
const calls = { second: "two", first: "one" }
static for key, value in calls {
  module children[key] = Child(value, { name: value })
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

func TestCompileUnusedModuleImportDoesNotDiscoverChild(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.infra", `import module Child from "./child"`)
	writeProjectFile(t, root, "child/main.infra", `output value = "unused"`)

	result, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != 0 || len(result.Artifacts) != 1 || result.Artifacts[0].ModuleID != "." {
		t.Fatalf("unused module import discovered child: %#v", result)
	}
}

func TestCompileDoesNotShareModuleImportsAcrossDirectories(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.infra", `
import module Child from "./child"
module child = Child("child", {})
`)
	writeProjectFile(t, root, "child/main.infra", `module nested = Child("nested", {})`)

	result, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	if joined := diagnosticMessages(result); len(result.Artifacts) != 0 || !strings.Contains(joined, `unknown imported module "Child"`) {
		t.Fatalf("module import leaked into child directory: %v", result.Diagnostics)
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
