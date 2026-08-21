package compiler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ondrejnov/infralang/internal/syntax"
)

func TestCompileComponentExpandsProvidersAndVirtualExports(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
provider Terraform from "terraform.io/builtin/terraform"
configure terraform = Terraform
component Item(label: string, value: string) using { terraform: Terraform } {
  resource item = terraform.data(label, { input: value })
  export result = item.output
}
instantiate items["first"] = Item(label: "first", value: "one") using { terraform: terraform }
instantiate items["second"] = Item(label: "second", value: "two") using { terraform: terraform }
output first = items["first"].result
output second = items["second"].result
`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatal(err)
	}
	resources := document["resource"].(map[string]any)["terraform_data"].(map[string]any)
	if len(resources) != 2 || resources["first"].(map[string]any)["input"] != "one" || resources["second"].(map[string]any)["input"] != "two" {
		t.Fatalf("expanded resources = %#v", resources)
	}
	outputs := document["output"].(map[string]any)
	if outputs["first"].(map[string]any)["value"] != `${terraform_data.first.output}` || outputs["second"].(map[string]any)["value"] != `${terraform_data.second.output}` {
		t.Fatalf("virtual exports = %#v", outputs)
	}
	text := string(result)
	if strings.Contains(text, "component") || strings.Contains(text, "instantiate") || strings.Contains(text, "for_each") || strings.Contains(text, "each.") {
		t.Fatalf("component constructs did not erase:\n%s", result)
	}
}

func TestCompileComponentAcceptsRuntimeArguments(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
import module Child from "registry.example/child"
input runtimeValue: string = "runtime"
component Wrapper(value: string) {
  module child = Child("stable_child", { value: value })
  export result = child.result
}
instantiate wrapper = Wrapper(value: runtimeValue)
output result = wrapper.result
`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatal(err)
	}
	module := document["module"].(map[string]any)["stable_child"].(map[string]any)
	if module["value"] != `${var.runtime_value}` {
		t.Fatalf("runtime component argument = %#v", module)
	}
	if document["output"].(map[string]any)["result"].(map[string]any)["value"] != `${module.stable_child.result}` {
		t.Fatalf("runtime component export = %#v", document["output"])
	}
}

func TestCompileComponentExpansionAvoidsCallerCapture(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
let internal = "caller"
component Capture(value: string) {
  let internal = value
  export result = internal
}
instantiate capture = Capture(value: internal)
output result = capture.result
`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatal(err)
	}
	locals := document["locals"].(map[string]any)
	if len(locals) != 2 || locals["internal"] != "caller" {
		t.Fatalf("component locals = %#v", locals)
	}
	generated := ""
	for name, value := range locals {
		if name != "internal" {
			generated = name
			if value != `${local.internal}` {
				t.Fatalf("component argument captured internal local: %#v", locals)
			}
		}
	}
	output := document["output"].(map[string]any)["result"].(map[string]any)["value"]
	if generated == "" || output != "${local."+generated+"}" {
		t.Fatalf("hygienic export = %#v, locals = %#v", output, locals)
	}
}

func TestCompileComponentParameterShadowsGlobalConstant(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
import module Child from "registry.example/child"
const value = "global"
component Value(value: string) {
  module child = Child("child", { value: value })
}
instantiate selected = Value(value: "argument")
`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatal(err)
	}
	module := document["module"].(map[string]any)["child"].(map[string]any)
	if module["value"] != "argument" {
		t.Fatalf("component parameter was captured by global constant: %#v", module)
	}
}

func TestComponentHandleDoesNotCaptureComprehensionIterator(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
component Item() { export value = "component" }
instantiate item = Item()
output values = [for item in ["runtime"]: item]
`)
	if !strings.Contains(string(result), `for item in [\"runtime\"] : item`) || strings.Contains(string(result), `: \"component\"`) {
		t.Fatalf("component handle captured comprehension iterator:\n%s", result)
	}
}

func TestCompileNestedComponentsExpandAcyclically(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
import module LeafModule from "registry.example/leaf"
component Leaf(value: string) {
  module leaf = LeafModule("leaf", { value: value })
  export result = leaf.result
}
component Outer(value: string) {
  instantiate nested = Leaf(value: value)
  export result = nested.result
}
instantiate outer = Outer(value: "ready")
output result = outer.result
`)
	text := string(result)
	if !strings.Contains(text, `"leaf"`) || !strings.Contains(text, `"value": "ready"`) || !strings.Contains(text, `${module.leaf.result}`) {
		t.Fatalf("nested component expansion:\n%s", result)
	}
}

func TestCompileNestedComponentsForwardStaticProviders(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
provider Null from "hashicorp/null"
configure nullProvider = Null({})
component Leaf(label: string) using { provider: Null } {
  resource item = provider.resource(label, {})
  export id = item.id
}
component Outer(label: string) using { provider: Null } {
  instantiate nested = Leaf(label: label) using { provider: provider }
  export id = nested.id
}
instantiate outer = Outer(label: "nested") using { provider: nullProvider }
output id = outer.id
`)
	if !strings.Contains(string(result), `"null_resource"`) || !strings.Contains(string(result), `${null_resource.nested.id}`) {
		t.Fatalf("nested provider forwarding:\n%s", result)
	}
}

func TestCompileComponentExportAloneEmitsNothing(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
component Value() { export value = "virtual" }
instantiate value = Value()
`)
	if string(result) != "{}\n" {
		t.Fatalf("virtual-only component emitted Terraform configuration:\n%s", result)
	}
}

func TestCompileComponentNumericIndexIsExact(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
import module ChildModule from "registry.example/child"
component Child(label: string) {
  module child = ChildModule(label, {})
  export id = child.id
}
instantiate children[9007199254740992 + 1] = Child(label: "exact_child")
output id = children[9007199254740993].id
`)
	if !strings.Contains(string(result), `"exact_child"`) || !strings.Contains(string(result), `${module.exact_child.id}`) {
		t.Fatalf("exact component index:\n%s", result)
	}
}

func TestCompileComponentSourceRenamesPreserveExplicitIdentity(t *testing.T) {
	t.Parallel()

	first := compileSource(t, `
provider Terraform from "terraform.io/builtin/terraform"
configure terraform = Terraform
component Item(value: string) using { provider: Terraform } {
  resource internal = provider.data("stable", { input: value })
  export result = internal.output
}
instantiate instance = Item(value: "same") using { provider: terraform }
output result = instance.result
`)
	second := compileSource(t, `
provider Terraform from "terraform.io/builtin/terraform"
configure terraform = Terraform
component Renamed(argument: string) using { selectedProvider: Terraform } {
  resource renamedInternal = selectedProvider.data("stable", { input: argument })
  export renamedResult = renamedInternal.output
}
instantiate renamedInstance = Renamed(argument: "same") using { selectedProvider: terraform }
output result = renamedInstance.renamedResult
`)
	if string(first) != string(second) {
		t.Fatalf("source-only component renames changed explicit identity:\n%s\n%s", first, second)
	}
}

func TestCompileRejectsComponentDependencyCycles(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("component-cycle.infra", `
component First() { instantiate second = Second() }
component Second() { instantiate first = First() }
component Direct() { instantiate direct = Direct() }
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	joined := componentDiagnosticMessages(diagnostics)
	if !strings.Contains(joined, "First -> Second -> First") || !strings.Contains(joined, "Direct -> Direct") || !strings.Contains(joined, `component dependency cycle`) {
		t.Fatalf("component cycle diagnostics = %v", diagnostics)
	}
}

func TestSensitiveComponentArgumentMismatchIsRedacted(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("sensitive-component.infra", `
input credentials: object { token: string } = { token: "DO_NOT_EXPOSE_SECRET" } with { sensitive: true }
component Numeric(value: number) {}
instantiate numeric = Numeric(value: credentials.token)
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	joined := componentDiagnosticMessages(diagnostics)
	if !strings.Contains(joined, "sensitive string") || strings.Contains(joined, "DO_NOT_EXPOSE_SECRET") {
		t.Fatalf("sensitive component diagnostic was not redacted: %v", diagnostics)
	}
}

func TestCompileRejectsInvalidComponentArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		source   string
		contains string
	}{
		{name: "missing", source: "component Value(value: string) {}\ninstantiate value = Value()", contains: "missing argument"},
		{name: "unknown", source: "component Value() {}\ninstantiate value = Value(extra: true)", contains: "unknown argument"},
		{name: "duplicate", source: "component Value(value: string) {}\ninstantiate value = Value(value: \"one\", value: \"two\")", contains: "supplied more than once"},
		{name: "type mismatch", source: "component Value(value: string) {}\ninstantiate value = Value(value: 1)", contains: "expected string, got number"},
		{name: "unknown component", source: `instantiate value = Missing()`, contains: `unknown component "Missing"`},
		{name: "unknown parameter type", source: `component Value(value: Missing) {}`, contains: `unknown type "Missing"`},
		{name: "duplicate signature name", source: `component Value(value: string, value: number) {}`, contains: "conflicts within component"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file, parseDiagnostics := syntax.Parse(test.name+".infra", test.source)
			if len(parseDiagnostics) != 0 {
				t.Fatal(parseDiagnostics)
			}
			_, diagnostics := Compile(file)
			if joined := componentDiagnosticMessages(diagnostics); !strings.Contains(joined, test.contains) {
				t.Fatalf("diagnostics = %v, want %q", diagnostics, test.contains)
			}
		})
	}
}

func TestCompileRejectsInvalidComponentProviders(t *testing.T) {
	t.Parallel()

	prefix := `
provider Null from "hashicorp/null"
provider Random from "hashicorp/random"
configure nullProvider = Null({})
configure randomProvider = Random({})
input runtimeProvider: string = "not-provider"
component WithProvider() using { provider: Null } {
  resource item = provider.resource("item", {})
}
`
	tests := []struct {
		name     string
		instance string
		contains string
	}{
		{name: "missing", instance: `instantiate item = WithProvider()`, contains: "missing provider argument"},
		{name: "unknown mapping", instance: `instantiate item = WithProvider() using { provider: nullProvider, extra: nullProvider }`, contains: "unknown provider argument"},
		{name: "source mismatch", instance: `instantiate item = WithProvider() using { provider: randomProvider }`, contains: `requires source "hashicorp/null", got "hashicorp/random"`},
		{name: "runtime handle", instance: `instantiate item = WithProvider() using { provider: runtimeProvider }`, contains: "unknown provider configuration"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file, parseDiagnostics := syntax.Parse(test.name+".infra", prefix+test.instance)
			if len(parseDiagnostics) != 0 {
				t.Fatal(parseDiagnostics)
			}
			_, diagnostics := Compile(file)
			if joined := componentDiagnosticMessages(diagnostics); !strings.Contains(joined, test.contains) {
				t.Fatalf("diagnostics = %v, want %q", diagnostics, test.contains)
			}
		})
	}
}

func TestCompileRejectsInvalidComponentIdentitiesAndExports(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		source   string
		contains string
	}{
		{
			name: "duplicate address",
			source: `
provider Terraform from "terraform.io/builtin/terraform"
configure terraform = Terraform
component Item() { resource item = terraform.data("same", {}) }
instantiate first = Item()
instantiate second = Item()
`, contains: "Terraform resource terraform_data.same is already declared",
		},
		{name: "duplicate indexed key", source: "component Item() {}\ninstantiate items[1.0] = Item()\ninstantiate items[1] = Item()", contains: "already declared"},
		{name: "duplicate ordinary handle", source: "component Item() {}\ninstantiate item = Item()\ninstantiate item = Item()", contains: "already declared"},
		{name: "runtime index", source: "input key: string\ncomponent Item() {}\ninstantiate items[key] = Item()", contains: "runtime or unknown name"},
		{name: "unknown export", source: "component Item() {}\ninstantiate item = Item()\noutput invalid = item.missing", contains: "has no export"},
		{name: "invalid unused export", source: "component Item() { export invalid = missing }\ninstantiate item = Item()", contains: `unknown name "missing"`},
		{name: "unknown indexed lookup", source: "component Item() { export value = 1 }\ninstantiate items[\"known\"] = Item()\noutput invalid = items[\"missing\"].value", contains: "unknown indexed component instance"},
		{name: "invalid lookup index", source: "input key: string\ncomponent Item() { export value = 1 }\ninstantiate items[\"known\"] = Item()\noutput invalid = items[key].value", contains: "component lookup index"},
		{name: "direct indexed handle", source: "component Item() { export value = 1 }\ninstantiate items[\"known\"] = Item()\noutput invalid = items[\"known\"]", contains: "only through a named export"},
		{name: "constant conflict", source: "const item = 1\ncomponent Item() { export value = 1 }\ninstantiate item = Item()", contains: "conflicts with constant"},
		{name: "indexed namespace conflict", source: "import module Remote from \"remote/module\"\nmodule items[\"module\"] = Remote(\"module\", {})\ncomponent Item() {}\ninstantiate items[\"component\"] = Item()", contains: "conflicts with an indexed provider or module namespace"},
		{name: "export dependency cycle", source: "component Pass(value: dynamic) { export result = value }\ninstantiate first = Pass(value: second.result)\ninstantiate second = Pass(value: first.result)", contains: "component export dependency cycle"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file, parseDiagnostics := syntax.Parse(test.name+".infra", test.source)
			if len(parseDiagnostics) != 0 {
				t.Fatal(parseDiagnostics)
			}
			_, diagnostics := Compile(file)
			if joined := componentDiagnosticMessages(diagnostics); !strings.Contains(joined, test.contains) {
				t.Fatalf("diagnostics = %v, want %q", diagnostics, test.contains)
			}
		})
	}
}

func componentDiagnosticMessages(diagnostics []syntax.Diagnostic) string {
	var result strings.Builder
	for _, diagnostic := range diagnostics {
		result.WriteString(diagnostic.Message)
		result.WriteByte(' ')
	}
	return result.String()
}
