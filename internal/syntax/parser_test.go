package syntax

import "testing"

func TestParseProgram(t *testing.T) {
	t.Parallel()

	source := `
terraform { requiredVersion: ">= 1.5.0" }
provider Null from "hashicorp/null" version "3.3.1"
import module Child from "./child"
input name: string = "example"
let label = f"resource-{name}"
configure nullEast = Null("east", {})
resource placeholder = nullEast.resource("placeholder", { triggers: { "Name": label } }) with {
  dependsOn: [],
}
data lookup = nullEast.dataSource("lookup", { inputs: { name: label } })
module child = Child("child", { marker: label }) using { "null.east": nullEast }
output id = placeholder.id
`
	file, diagnostics := Parse("test.infra", source)
	if len(diagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", diagnostics)
	}
	if len(file.Declarations) != 10 {
		t.Fatalf("Parse() declarations = %d, want 10", len(file.Declarations))
	}

	resource, ok := file.Declarations[6].(*ResourceDeclaration)
	if !ok {
		t.Fatalf("declaration 5 is %T, want *ResourceDeclaration", file.Declarations[5])
	}
	if resource.ProviderConfigName != "nullEast" || resource.Kind != "resource" || resource.Label != "placeholder" {
		t.Errorf("resource parsed as %#v", resource)
	}
	if resource.With == nil {
		t.Fatal("resource with clause was not parsed")
	}
	dataSource, ok := file.Declarations[7].(*DataDeclaration)
	if !ok {
		t.Fatalf("declaration 6 is %T, want *DataDeclaration", file.Declarations[6])
	}
	if dataSource.Kind != "dataSource" || dataSource.Label != "lookup" {
		t.Errorf("data source parsed as %#v", dataSource)
	}
}

func TestParseFormattedStringEscapedBraces(t *testing.T) {
	t.Parallel()

	expression, diagnostics := ParseExpression("test.infra", `f"{{service}}-{name}"`)
	if len(diagnostics) != 0 {
		t.Fatalf("ParseExpression() diagnostics = %v", diagnostics)
	}
	template, ok := expression.(*TemplateExpression)
	if !ok {
		t.Fatalf("expression is %T, want *TemplateExpression", expression)
	}
	if len(template.Parts) != 2 {
		t.Fatalf("template parts = %d, want 2", len(template.Parts))
	}
	if template.Parts[0].Text != "{service}-" {
		t.Errorf("text part = %q", template.Parts[0].Text)
	}
}

func TestParseAcceptsCompileTimeResourceLabelExpression(t *testing.T) {
	t.Parallel()

	file, diagnostics := Parse("test.infra", `
provider Null from "hashicorp/null"
configure nullProvider = Null({})
input label: string
resource item = nullProvider.resource(label, {})
`)
	if len(diagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", diagnostics)
	}
	resource := file.Declarations[3].(*ResourceDeclaration)
	identifier, ok := resource.LabelExpression.(*IdentifierExpression)
	if !ok || identifier.Name != "label" || resource.Label != "" {
		t.Fatalf("resource label expression = %#v", resource.LabelExpression)
	}
}

func TestParseAcceptsCompileTimeForms(t *testing.T) {
	t.Parallel()

	file, diagnostics := Parse("compile-time.infra", `
provider Null from "hashicorp/null"
import module Child from "./child"
const regions: list<string> = ["west"]
static for index, region in regions {
  configure providers[region] = Null({})
  resource items = providers[region].resource(f"item-{region}", {})
  module children[index] = Child(f"child-{region}", {})
}
`)
	if len(diagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", diagnostics)
	}
	if len(file.Declarations) != 4 {
		t.Fatalf("declarations = %#v", file.Declarations)
	}
	constant := file.Declarations[2].(*ConstDeclaration)
	if constant.Name != "regions" || constant.Type == nil || constant.Type.Name != "list" {
		t.Fatalf("constant = %#v", constant)
	}
	loop := file.Declarations[3].(*StaticForDeclaration)
	if loop.KeyVariable != "index" || loop.ValueVariable != "region" || len(loop.Declarations) != 3 {
		t.Fatalf("static loop = %#v", loop)
	}
	configuration := loop.Declarations[0].(*ConfigureDeclaration)
	resource := loop.Declarations[1].(*ResourceDeclaration)
	module := loop.Declarations[2].(*ModuleDeclaration)
	if configuration.Index == nil || resource.ProviderConfig == nil || resource.LabelExpression == nil || module.Index == nil || module.LabelExpression == nil {
		t.Fatalf("indexed declarations were not retained: %#v %#v %#v", configuration, resource, module)
	}
}

func TestParseRejectsUnaryPlus(t *testing.T) {
	t.Parallel()

	_, diagnostics := Parse("test.infra", `input value: number = +1`)
	if len(diagnostics) == 0 {
		t.Fatal("Parse() returned no diagnostics")
	}
}

func TestParseModuleLanguageExtensions(t *testing.T) {
	t.Parallel()

	file, diagnostics := Parse("test.infra", `
provider Null from "hashicorp/null"
import module Child from "./child"
configure nullProvider = Null
input machines: map<object {
  address: string,
  memory?: number = 1024,
}> with {
  description: "Machines",
  validations: [{ condition: length(machines) > 0, errorMessage: "required" }],
}
module child = Child("child", { name: each.key }) using { null: nullProvider } with { forEach: machines }
output selected = {for name, machine in child: name => machine.id if machine.ready}
moved from "module.old" to "module.child"
`)
	if len(diagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", diagnostics)
	}
	if len(file.Declarations) != 7 {
		t.Fatalf("Parse() declarations = %d, want 7", len(file.Declarations))
	}
	input := file.Declarations[3].(*InputDeclaration)
	object := input.Type.Arguments[0]
	if len(object.Fields) != 2 || !object.Fields[1].Optional || object.Fields[1].Default == nil {
		t.Fatalf("object type fields = %#v", object.Fields)
	}
	module := file.Declarations[4].(*ModuleDeclaration)
	if module.MetaArguments == nil {
		t.Fatal("module meta-arguments were not parsed")
	}
	if _, ok := file.Declarations[6].(*MovedDeclaration); !ok {
		t.Fatalf("last declaration is %T, want *MovedDeclaration", file.Declarations[6])
	}
}

func TestParsePhaseOneForms(t *testing.T) {
	t.Parallel()

	source := `
provider Null from "hashicorp/null"
import module Child from "./child"
input imageId "image_id": string with {
  validate length(imageId) > 0 else "required",
  validations: [{ condition: imageId != "", errorMessage: "legacy" }],
}
let config = { imageId, }
configure nullProvider = Null
resource conditional = nullProvider.resource("conditional", { imageId, }) when imageId != ""
module child = Child("child", {}) with { dependsOn: [conditional] } using { nullProvider }
` + "moved { `module.old[\"x\"]` -> `module.child[\"x\"]`, `null_resource.old` -> `null_resource.new`, }\n"
	file, diagnostics := Parse("phase.infra", source)
	if len(diagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", diagnostics)
	}
	input := file.Declarations[2].(*InputDeclaration)
	if input.WireName != "image_id" || !input.ExplicitWire || len(input.MetadataItems) != 2 {
		t.Fatalf("input = %#v", input)
	}
	automatic, automaticDiagnostics := Parse("automatic.infra", `input instanceType: string`)
	if len(automaticDiagnostics) != 0 {
		t.Fatalf("automatic input diagnostics = %v", automaticDiagnostics)
	}
	automaticInput := automatic.Declarations[0].(*InputDeclaration)
	if automaticInput.WireName != "instance_type" || automaticInput.ExplicitWire {
		t.Fatalf("automatic input = %#v", automaticInput)
	}
	local := file.Declarations[3].(*LetDeclaration)
	if !local.Value.(*ObjectExpression).Fields[0].Punned {
		t.Fatal("object field was not parsed as a pun")
	}
	resource := file.Declarations[5].(*ResourceDeclaration)
	if resource.Condition == nil {
		t.Fatal("resource condition was not parsed")
	}
	module := file.Declarations[6].(*ModuleDeclaration)
	if module.Providers == nil || len(module.Providers.Explicit.Fields) != 1 || module.MetaArguments == nil {
		t.Fatalf("module clauses = %#v", module)
	}
	moved := file.Declarations[7].(*MovedDeclaration)
	if len(moved.Items) != 2 || moved.Items[0].From.Raw != `module.old["x"]` {
		t.Fatalf("moved items = %#v", moved.Items)
	}
	for _, declaration := range file.Declarations {
		if declaration.GetFile() != FileID("phase.infra") || declaration.GetSpan().File != FileID("phase.infra") {
			t.Errorf("declaration %T lost provenance: file=%q span=%q", declaration, declaration.GetFile(), declaration.GetSpan().File)
		}
	}
}

func TestParsePhaseOneFocusedErrors(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"invalid alias":               `input value "bad.name": string`,
		"missing pun comma":           `let value = { first second: 2 }`,
		"empty moved address":         "moved { `` -> `module.next`, }",
		"missing moved comma":         "moved { `a` -> `b` `c` -> `d`, }",
		"missing validation comma":    `input value: string with { validate value != "" else "required" }`,
		"computed validation message": `input value: string with { validate value != "" else f"{value}", }`,
		"duplicate using":             `module child = Child("child", {}) using {} using {}`,
		"inline module source":        `module child "child" from "./child" {}`,
		"missing module arguments":    `module child = Child("child")`,
		"extra module argument":       `module child = Child("child", {}, true)`,
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, diagnostics := Parse("invalid.infra", source)
			if len(diagnostics) == 0 {
				t.Fatal("Parse() returned no diagnostics")
			}
			for _, diagnostic := range diagnostics {
				if diagnostic.Filename != "invalid.infra" || diagnostic.Code == "" {
					t.Errorf("diagnostic lacks identity: %#v", diagnostic)
				}
			}
		})
	}
}

func TestParsePhaseTwoObjectCompositionAndAliases(t *testing.T) {
	t.Parallel()

	file, diagnostics := Parse("phase2.infra", `
type HostConfig = object {
  imageId "image_id": string,
  regionName "region"?: string = "default",
}
input enabled: bool = true
let baseConfig = { imageId: "ami", "exactCamel": "kept" }
let config = {
  beforeValue: "before",
  ...baseConfig,
  description: "enabled" when enabled,
  afterValue: "after",
}
`)
	if len(diagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", diagnostics)
	}
	alias, ok := file.Declarations[0].(*TypeAliasDeclaration)
	if !ok || alias.Name != "HostConfig" {
		t.Fatalf("alias = %#v", file.Declarations[0])
	}
	if alias.Type.Fields[0].WireName != "image_id" || alias.Type.Fields[1].WireName != "region" {
		t.Fatalf("aliased fields = %#v", alias.Type.Fields)
	}
	base := file.Declarations[2].(*LetDeclaration).Value.(*ObjectExpression)
	if base.Fields[0].WireName != "image_id" || base.Fields[1].WireName != "exactCamel" {
		t.Fatalf("construction wire names = %#v", base.Fields)
	}
	composed := file.Declarations[3].(*LetDeclaration).Value.(*ObjectExpression)
	if len(composed.Items) != 4 {
		t.Fatalf("object items = %#v", composed.Items)
	}
	if _, ok := composed.Items[1].(ObjectSpread); !ok {
		t.Fatalf("item 1 is %T, want ObjectSpread", composed.Items[1])
	}
	conditional := composed.Items[2].(ObjectField)
	if conditional.Condition == nil || conditional.WireName != "description" {
		t.Fatalf("conditional field = %#v", conditional)
	}
}

func TestParseRejectsMalformedPhaseTwoItems(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"missing spread operand":   `let value = { ..., }`,
		"missing spread separator": `let value = { ...first second: 2 }`,
		"missing field condition":  `let value = { field: 1 when, }`,
		"missing alias assignment": `type Config object { value: string }`,
		"conditional pun":          `let value = { field when true, }`,
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, diagnostics := Parse("invalid-phase2.infra", source)
			if len(diagnostics) == 0 {
				t.Fatal("Parse() returned no diagnostics")
			}
		})
	}
}

func TestParseInputsSpread(t *testing.T) {
	t.Parallel()

	file, diagnostics := Parse("forwarding.infra", `
module child = Child("child", {
  explicitValue: "explicit",
  ...inputs(config),
})
`)
	if len(diagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", diagnostics)
	}
	module := file.Declarations[0].(*ModuleDeclaration)
	if len(module.Arguments.Items) != 2 {
		t.Fatalf("module arguments = %#v", module.Arguments.Items)
	}
	forwarding, ok := module.Arguments.Items[1].(InputsSpread)
	if !ok {
		t.Fatalf("argument item is %T, want InputsSpread", module.Arguments.Items[1])
	}
	identifier, ok := forwarding.Value.(*IdentifierExpression)
	if !ok || identifier.Name != "config" {
		t.Fatalf("forwarding value = %#v", forwarding.Value)
	}

	_, diagnostics = Parse("invalid-forwarding.infra", `module child = Child("child", { ...inputs(first, second) })`)
	if len(diagnostics) == 0 {
		t.Fatal("Parse() accepted forwarding with multiple arguments")
	}
}
