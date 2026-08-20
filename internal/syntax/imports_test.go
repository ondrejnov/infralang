package syntax

import "testing"

func TestParseTypeExportsAndImports(t *testing.T) {
	t.Parallel()

	file, diagnostics := Parse("types.infra", `
export type HostConfig = object { hostName: string }
import type { HostConfig, HostConfig as Config, } from "./shared.infra"
`)
	if len(diagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", diagnostics)
	}
	if len(file.Declarations) != 2 {
		t.Fatalf("declarations = %#v", file.Declarations)
	}
	exported := file.Declarations[0].(*TypeAliasDeclaration)
	if !exported.Exported || exported.Name != "HostConfig" {
		t.Fatalf("exported alias = %#v", exported)
	}
	imported := file.Declarations[1].(*TypeImportDeclaration)
	if imported.Path != "./shared.infra" || len(imported.Items) != 2 {
		t.Fatalf("type import = %#v", imported)
	}
	if imported.Items[0].ImportedName != "HostConfig" || imported.Items[0].LocalName != "HostConfig" || imported.Items[1].LocalName != "Config" {
		t.Fatalf("type import items = %#v", imported.Items)
	}
}

func TestParseRejectsMalformedTypeImports(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"value import":    `import { Value } from "./types.infra"`,
		"value export":    `export const Value = 1`,
		"missing comma":   `import type { First Second } from "./types.infra"`,
		"duplicate local": `import type { First as Shared, Second as Shared } from "./types.infra"`,
		"empty import":    `import type {} from "./types.infra"`,
		"missing path":    `import type { First } from First`,
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, diagnostics := Parse("invalid-import.infra", source)
			if len(diagnostics) == 0 {
				t.Fatal("Parse() returned no diagnostics")
			}
		})
	}
}
