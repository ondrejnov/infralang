package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompileResolvesExportedTypeImport(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.infra", `
import type { HostConfig as Config } from "./shared/types.infra"
input config: Config = { hostName: "node", retries: 2 }
output hostName = config.hostName
`)
	writeProjectFile(t, root, "shared/types.infra", `
type PrivateConfig = object { hostName: string, retries: RetryCount }
export type HostConfig = PrivateConfig
`)
	writeProjectFile(t, root, "shared/helpers.infra", `type RetryCount = number`)

	result, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != 0 || len(result.Artifacts) != 1 {
		t.Fatalf("Compile() = artifacts %#v, diagnostics %v", result.Artifacts, result.Diagnostics)
	}
	artifact := string(result.Artifacts[0].Data)
	if strings.Contains(artifact, "import") || strings.Contains(artifact, "HostConfig") || !strings.Contains(artifact, `object({host_name = string, retries = number})`) {
		t.Fatalf("resolved type import output:\n%s", artifact)
	}
}

func TestCompileResolvesExportedComposedTypeImport(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.infra", `
import type { Config } from "./shared/types.infra"
input config: Config = { hostName: "node", enabled: true }
output hostName = config.hostName
`)
	writeProjectFile(t, root, "shared/types.infra", `
type Base = object { hostName: string }
export type Config = Base & object { enabled: bool }
`)

	result, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != 0 || len(result.Artifacts) != 1 {
		t.Fatalf("Compile() = artifacts %#v, diagnostics %v", result.Artifacts, result.Diagnostics)
	}
	artifact := string(result.Artifacts[0].Data)
	if !strings.Contains(artifact, `object({host_name = string, enabled = bool})`) {
		t.Fatalf("resolved composed type import output:\n%s", artifact)
	}
}

func TestCompileSharesUnexportedAliasesWithinDirectory(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.infra", `input config: SharedConfig = { value: "ok" }`)
	writeProjectFile(t, root, "types.infra", `type SharedConfig = object { value: string }`)

	result, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != 0 || len(result.Artifacts) != 1 {
		t.Fatalf("same-directory alias compilation = artifacts %#v, diagnostics %v", result.Artifacts, result.Diagnostics)
	}
}

func TestCompileRejectsInvalidTypeImports(t *testing.T) {
	tests := []struct {
		name     string
		main     string
		files    map[string]string
		contains string
	}{
		{
			name: "private", main: `import type { Private } from "./types.infra"`,
			files: map[string]string{"types.infra": `type Private = string`}, contains: "is private",
		},
		{
			name: "missing", main: `import type { Missing } from "./types.infra"`,
			files: map[string]string{"types.infra": `export type Present = string`}, contains: "unknown exported type",
		},
		{
			name: "runtime declaration", main: `import type { Runtime } from "./types.infra"`,
			files: map[string]string{"types.infra": `const Runtime = 1`}, contains: "exported type aliases only",
		},
		{
			name: "duplicate local", main: "type Config = string\nimport type { Exported as Config } from \"./types.infra\"",
			files: map[string]string{"types.infra": `export type Exported = string`}, contains: "conflicts with another type",
		},
		{
			name: "non infra", main: `import type { Config } from "./types.txt"`,
			files: map[string]string{"types.txt": `export type Config = string`}, contains: "must target a .infra file",
		},
		{
			name: "missing file", main: `import type { Config } from "./missing.infra"`,
			files: map[string]string{}, contains: "cannot resolve type import",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeProjectFile(t, root, "main.infra", test.main)
			for path, source := range test.files {
				writeProjectFile(t, root, path, source)
			}
			result, err := Compile(root)
			if err != nil {
				t.Fatal(err)
			}
			joined := diagnosticMessages(result)
			if len(result.Artifacts) != 0 || !strings.Contains(joined, test.contains) {
				t.Fatalf("Compile() = artifacts %#v, diagnostics %v; want %q", result.Artifacts, result.Diagnostics, test.contains)
			}
		})
	}
}

func TestCompileRejectsDuplicateImportedLocalNamesAcrossFiles(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.infra", `import type { First as Shared } from "./types/first.infra"`)
	writeProjectFile(t, root, "other.infra", `import type { Second as Shared } from "./types/second.infra"`)
	writeProjectFile(t, root, "types/first.infra", `export type First = string`)
	writeProjectFile(t, root, "types/second.infra", `export type Second = number`)

	result, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	if joined := diagnosticMessages(result); len(result.Artifacts) != 0 || !strings.Contains(joined, `imported local type name "Shared" conflicts`) {
		t.Fatalf("duplicate import diagnostics = %v", result.Diagnostics)
	}
}

func TestCompileRejectsAbsoluteAndParentEscapingTypeImports(t *testing.T) {
	tests := []struct {
		name       string
		importPath func(string, string) string
		contains   string
	}{
		{name: "absolute", importPath: func(_ string, outside string) string { return filepath.Join(outside, "types.infra") }, contains: "must be relative"},
		{name: "parent escape", importPath: func(root, outside string) string {
			path, err := filepath.Rel(root, filepath.Join(outside, "types.infra"))
			if err != nil {
				t.Fatal(err)
			}
			return filepath.ToSlash(path)
		}, contains: "outside project root"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			writeProjectFile(t, outside, "types.infra", `THIS_EXTERNAL_CONTENT_MUST_NOT_BE_PARSED`)
			path := test.importPath(root, outside)
			writeProjectFile(t, root, "main.infra", `import type { Config } from "`+path+`"`)
			result, err := Compile(root)
			if err != nil {
				t.Fatal(err)
			}
			joined := diagnosticMessages(result)
			if len(result.Artifacts) != 0 || !strings.Contains(joined, test.contains) || strings.Contains(joined, "THIS_EXTERNAL_CONTENT_MUST_NOT_BE_PARSED") {
				t.Fatalf("path diagnostics = %v", result.Diagnostics)
			}
		})
	}
}

func TestCompileRejectsTypeImportFileCycle(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.infra", `import type { First } from "./first.infra"`)
	writeProjectFile(t, root, "first.infra", `
import type { Second } from "./second.infra"
export type First = string
`)
	writeProjectFile(t, root, "second.infra", `
import type { First } from "./first.infra"
export type Second = number
`)

	result, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	joined := diagnosticMessages(result)
	if len(result.Artifacts) != 0 || !strings.Contains(joined, "type import cycle") || !strings.Contains(joined, "first.infra -> second.infra -> first.infra") {
		t.Fatalf("cycle diagnostics = %v", result.Diagnostics)
	}
}

func TestCompileRejectsExpandedImportedTypeCycle(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.infra", `import type { First } from "./shared/types.infra"`)
	writeProjectFile(t, root, "shared/types.infra", `
export type First = Second
type Second = First
`)

	result, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	joined := diagnosticMessages(result)
	if len(result.Artifacts) != 0 || !strings.Contains(joined, "expanded type cycle") || !strings.Contains(joined, `type alias "First"`) || !strings.Contains(joined, `type alias "Second"`) {
		t.Fatalf("expanded type cycle diagnostics = %v", result.Diagnostics)
	}
}

func TestCompileBlocksTypeImportSymlinkEscapeBeforeReading(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeProjectFile(t, root, "main.infra", `import type { Secret } from "./escape.infra"`)
	writeProjectFile(t, outside, "secret.infra", `THIS_MUST_NOT_BE_PARSED_OR_REPORTED`)
	if err := os.Symlink(filepath.Join(outside, "secret.infra"), filepath.Join(root, "escape.infra")); err != nil {
		t.Fatal(err)
	}

	result, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	joined := diagnosticMessages(result)
	if len(result.Artifacts) != 0 || !strings.Contains(joined, "outside project root") || strings.Contains(joined, "THIS_MUST_NOT_BE_PARSED_OR_REPORTED") {
		t.Fatalf("escape diagnostics = %v", result.Diagnostics)
	}
}

func TestCompileRejectsTypeImportSymlinkToNonInfraTarget(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "main.infra", `import type { Config } from "./types.infra"`)
	writeProjectFile(t, root, "types.txt", `export type Config = string`)
	if err := os.Symlink(filepath.Join(root, "types.txt"), filepath.Join(root, "types.infra")); err != nil {
		t.Fatal(err)
	}

	result, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	if joined := diagnosticMessages(result); len(result.Artifacts) != 0 || !strings.Contains(joined, "resolves to a non-.infra file") {
		t.Fatalf("non-.infra symlink diagnostics = %v", result.Diagnostics)
	}
}

func diagnosticMessages(result CompileResult) string {
	var joined strings.Builder
	for _, diagnostic := range result.Diagnostics {
		joined.WriteString(diagnostic.Message)
		joined.WriteByte(' ')
	}
	return joined.String()
}
