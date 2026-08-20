package project

import (
	"strings"
	"testing"
)

func TestCompileExpandsDirectoryWideComponentBeforeGraphDiscovery(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "components.infra", `
import module Child from "./child"
component ChildCall(value: string) {
  module child = Child("child", { value: value })
  export result = child.result
}
`)
	writeProjectFile(t, root, "main.infra", `
input runtimeValue: string = "ready"
instantiate call = ChildCall(value: runtimeValue)
output result = call.result
`)
	writeProjectFile(t, root, "child/main.infra", `
input value: string
output result = value
`)

	result, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != 0 || len(result.Artifacts) != 2 {
		t.Fatalf("component project compilation = artifacts %#v, diagnostics %v", result.Artifacts, result.Diagnostics)
	}
	rootArtifact := string(result.Artifacts[0].Data)
	if !strings.Contains(rootArtifact, `"value": "${var.runtimeValue}"`) || !strings.Contains(rootArtifact, `${module.child.result}`) || strings.Contains(rootArtifact, "component") {
		t.Fatalf("component project artifact:\n%s", rootArtifact)
	}
}

func TestCompileChecksUnusedComponentExportAgainstLocalInterface(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.infra", `
import module Child from "./child"
component ChildCall() {
  module child = Child("child", {})
  export invalid = child.missing
}
instantiate call = ChildCall()
`)
	writeProjectFile(t, root, "child/main.infra", `output present = "value"`)

	result, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	if joined := diagnosticMessages(result); len(result.Artifacts) != 0 || !strings.Contains(joined, `object has no field "missing"`) {
		t.Fatalf("component export interface diagnostics = %v", result.Diagnostics)
	}
}

func TestCompileExpandsNestedComponentsAcrossImmediateFiles(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "a-leaf.infra", `
import module LeafModule from "registry.example/leaf"
component Leaf(value: string) {
  module leaf = LeafModule("leaf", { value: value })
  export result = leaf.result
}
`)
	writeProjectFile(t, root, "b-outer.infra", `
component Outer(value: string) {
  instantiate nested = Leaf(value: value)
  export result = nested.result
}
`)
	writeProjectFile(t, root, "main.infra", `
instantiate outer = Outer(value: "ready")
output result = outer.result
`)

	result, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != 0 || len(result.Artifacts) != 1 || !strings.Contains(string(result.Artifacts[0].Data), `${module.leaf.result}`) {
		t.Fatalf("nested directory component compilation = artifacts %#v, diagnostics %v", result.Artifacts, result.Diagnostics)
	}
}

func TestCompileDoesNotShareComponentsAcrossModules(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.infra", `
component RootOnly() { export value = "root" }
import module Child from "./child"
module child = Child("child", {})
`)
	writeProjectFile(t, root, "child/main.infra", `
instantiate invalid = RootOnly()
output value = invalid.value
`)

	result, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	if joined := diagnosticMessages(result); len(result.Artifacts) != 0 || !strings.Contains(joined, `unknown component "RootOnly"`) {
		t.Fatalf("cross-module component diagnostics = %v", result.Diagnostics)
	}
}

func TestCompileTypeImportCannotImportComponent(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.infra", `import type { Shared } from "./shared/components.infra"`)
	writeProjectFile(t, root, "shared/components.infra", `component Shared() {}`)

	result, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	if joined := diagnosticMessages(result); len(result.Artifacts) != 0 || !strings.Contains(joined, "exported type aliases only") {
		t.Fatalf("component type import diagnostics = %v", result.Diagnostics)
	}
}
