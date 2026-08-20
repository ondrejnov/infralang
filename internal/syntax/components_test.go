package syntax

import "testing"

func TestParseComponentsAndInstances(t *testing.T) {
	t.Parallel()

	file, diagnostics := Parse("components.infra", `
component Host(name: string, config: HostConfig) using {
  libvirt: Libvirt,
  lvm: Lvm,
} {
  module host name from "./modules/host" { commonConfig: config } using { libvirt: libvirt, lvm: lvm }
  export vms = host.vms
}
instantiate hostModules["first"] = Host(name: "first", config: value) using { libvirt: providerHandle, lvm: lvmHandle }
instantiate host = Host(name: "single", config: value) using { libvirt: providerHandle, lvm: lvmHandle }
`)
	if len(diagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", diagnostics)
	}
	if len(file.Declarations) != 3 {
		t.Fatalf("declarations = %#v", file.Declarations)
	}
	definition := file.Declarations[0].(*ComponentDefinition)
	if definition.Name != "Host" || len(definition.Parameters) != 2 || len(definition.Providers) != 2 || len(definition.Declarations) != 2 {
		t.Fatalf("component definition = %#v", definition)
	}
	if export, ok := definition.Declarations[1].(*ComponentExport); !ok || export.Name != "vms" {
		t.Fatalf("component export = %#v", definition.Declarations[1])
	}
	indexed := file.Declarations[1].(*ComponentInstance)
	if indexed.Index == nil || indexed.ComponentName != "Host" || len(indexed.Arguments.Fields) != 2 || len(indexed.Providers.Fields) != 2 {
		t.Fatalf("indexed component instance = %#v", indexed)
	}
	ordinary := file.Declarations[2].(*ComponentInstance)
	if ordinary.Index != nil || ordinary.Name != "host" {
		t.Fatalf("ordinary component instance = %#v", ordinary)
	}
}

func TestParseRejectsMalformedComponents(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"missing parameter type":  `component Invalid(value) { export value = value }`,
		"missing parameter comma": `component Invalid(first: string second: string) {}`,
		"missing provider type":   `component Invalid() using { provider } {}`,
		"positional argument":     `instantiate invalid = Missing("value")`,
		"missing argument comma":  `instantiate invalid = Missing(first: "one" second: "two")`,
		"missing assignment":      `instantiate invalid Missing()`,
		"invalid export":          `component Invalid() { export value value }`,
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, diagnostics := Parse("invalid-component.infra", source)
			if len(diagnostics) == 0 {
				t.Fatal("Parse() returned no diagnostics")
			}
		})
	}
}
