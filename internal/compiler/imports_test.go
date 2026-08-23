package compiler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ondrejnov/infralang/internal/syntax"
)

func TestCompileExportedTypeIsUsableAndErased(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
export type Config = object { value: string }
input config: Config = { value: "ok" }
output value = config.value
`)
	if strings.Contains(string(result), "export") || strings.Contains(string(result), "Config") {
		t.Fatalf("exported type leaked into Terraform JSON:\n%s", result)
	}
}

func TestCompileStandaloneRejectsUnresolvedTypeImport(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("main.infra", `import type { Config } from "./types.infra"`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Message, "project compilation") {
		t.Fatalf("Compile() diagnostics = %v", diagnostics)
	}
}

func TestCompileTimeEvaluatorDoesNotPerformEffects(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	secretPath := filepath.Join(directory, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("DO_NOT_EXPOSE_SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []string{
		`const invalid = file("` + secretPath + `")`,
		`const invalid = env("TOKEN")`,
		`const invalid = http("https://example.invalid")`,
		`const invalid = provider.read()`,
	}
	for _, source := range tests {
		file, parseDiagnostics := syntax.Parse("effect.infra", source)
		if len(parseDiagnostics) != 0 {
			t.Fatal(parseDiagnostics)
		}
		_, diagnostics := Compile(file)
		joined := ""
		for _, diagnostic := range diagnostics {
			joined += diagnostic.Message + " "
		}
		if !strings.Contains(joined, "effectful operations") || strings.Contains(joined, "DO_NOT_EXPOSE_SECRET") {
			t.Fatalf("effect diagnostic = %v", diagnostics)
		}
	}
}

func TestSensitiveTypeMismatchIsRedacted(t *testing.T) {
	t.Parallel()

	child := collectInterfaceSource(t, "child", `input requiredValue: number`)
	file, parseDiagnostics := syntax.Parse("sensitive.infra", `
import module Child from "./child"
input credentials: object { token: string } = { token: "DO_NOT_EXPOSE_SECRET" } with { sensitive: true }
module child = Child("child", { requiredValue: credentials.token })
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := CompileWithOptions(file, CompileOptions{LocalModules: map[string]ModuleInterface{"./child": child}})
	joined := ""
	for _, diagnostic := range diagnostics {
		joined += diagnostic.Message + " "
	}
	if !strings.Contains(joined, "sensitive string") || strings.Contains(joined, "DO_NOT_EXPOSE_SECRET") {
		t.Fatalf("sensitive diagnostic was not redacted: %v", diagnostics)
	}
}

func TestSensitiveChildOutputRemainsRedactedAcrossInterface(t *testing.T) {
	t.Parallel()

	source := collectInterfaceSource(t, "source", `output secret = "DO_NOT_EXPOSE_SECRET" with { sensitive: true }`)
	sink := collectInterfaceSource(t, "sink", `input requiredValue: number`)
	file, parseDiagnostics := syntax.Parse("sensitive-output.infra", `
import module Source from "./source"
import module Sink from "./sink"
module source = Source("source", {})
module sink = Sink("sink", { requiredValue: source.secret })
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := CompileWithOptions(file, CompileOptions{LocalModules: map[string]ModuleInterface{"./source": source, "./sink": sink}})
	joined := ""
	for _, diagnostic := range diagnostics {
		joined += diagnostic.Message + " "
	}
	if !strings.Contains(joined, "sensitive string") || strings.Contains(joined, "DO_NOT_EXPOSE_SECRET") {
		t.Fatalf("sensitive child output diagnostic was not redacted: %v", diagnostics)
	}
}

func TestPrepareBindsModuleImportAndCompileErasesIt(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("module-import.infra", `
import module Child from "registry.example/child"
module child = Child("child", { value: "ok" })
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	prepared, diagnostics := Prepare(file)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	module := prepared.Declarations[1].(*syntax.ModuleDeclaration)
	if module.ModuleName != "Child" || module.Source != "registry.example/child" {
		t.Fatalf("bound module = %#v", module)
	}
	result, diagnostics := Compile(prepared)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if text := string(result); !strings.Contains(text, `"source": "registry.example/child"`) || strings.Contains(text, "Child") {
		t.Fatalf("compiled module import:\n%s", result)
	}
}

func TestPrepareRejectsInvalidModuleImports(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"unknown": `module child = Missing("child", {})`,
		"duplicate": `
import module Child from "first/child"
import module Child from "second/child"
module child = Child("child", {})
`,
		"nested": `
component Invalid() {
  import module Child from "remote/child"
}
`,
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			file, parseDiagnostics := syntax.Parse(name+".infra", source)
			if len(parseDiagnostics) != 0 {
				t.Fatal(parseDiagnostics)
			}
			_, diagnostics := Prepare(file)
			if len(diagnostics) == 0 {
				t.Fatal("Prepare() returned no diagnostics")
			}
		})
	}
}

func TestPrepareBindsModuleVersionAndCompileEmitsIt(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("module-version.infra", `
import module Vpc from "terraform-aws-modules/vpc/aws" version "~> 5.0"
input region: string = "eu-central-1"
module vpc = Vpc("vpc", { region })
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	prepared, diagnostics := Prepare(file)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	module := prepared.Declarations[2].(*syntax.ModuleDeclaration)
	if module.Source != "terraform-aws-modules/vpc/aws" || module.Version != "~> 5.0" {
		t.Fatalf("bound module = %#v", module)
	}
	result, diagnostics := Compile(prepared)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	text := string(result)
	if !strings.Contains(text, `"source": "terraform-aws-modules/vpc/aws"`) ||
		!strings.Contains(text, `"version": "~\u003e 5.0"`) {
		t.Fatalf("compiled module import:\n%s", text)
	}
}

func TestPrepareRejectsInvalidModuleImportVersions(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"local source with version": `
import module Child from "./child" version "~> 1.0"
module child = Child("child", {})
`,
		"conflicting versions": `
import module First from "registry.example/child" version "~> 1.0"
import module Second from "registry.example/child" version "~> 2.0"
module first = First("first", {})
module second = Second("second", {})
`,
		"version versus unversioned": `
import module First from "registry.example/child" version "~> 1.0"
import module Second from "registry.example/child"
module first = First("first", {})
module second = Second("second", {})
`,
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			file, parseDiagnostics := syntax.Parse(name+".infra", source)
			if len(parseDiagnostics) != 0 {
				t.Fatal(parseDiagnostics)
			}
			_, diagnostics := Prepare(file)
			if len(diagnostics) == 0 {
				t.Fatal("Prepare() returned no diagnostics")
			}
			if name == "local source with version" &&
				!strings.Contains(diagnostics[0].Message, "cannot declare a version constraint") {
				t.Fatalf("diagnostics = %v", diagnostics)
			}
			if strings.Contains(name, "versions") &&
				!strings.Contains(diagnostics[0].Message, "conflicting version constraints") {
				t.Fatalf("diagnostics = %v", diagnostics)
			}
		})
	}
}
