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
input credentials: object { token: string } = { token: "DO_NOT_EXPOSE_SECRET" } with { sensitive: true }
module child "child" from "./child" { "requiredValue": credentials.token }
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
module source "source" from "./source" {}
module sink "sink" from "./sink" { "requiredValue": source.secret }
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
