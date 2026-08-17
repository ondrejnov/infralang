package syntax

import "testing"

func TestParseProgram(t *testing.T) {
	t.Parallel()

	source := `
terraform { requiredVersion: ">= 1.5.0" }
provider Null from "hashicorp/null" version "3.3.1"
input name: string = "example"
let label = f"resource-{name}"
configure nullEast = Null("east", {})
resource placeholder = nullEast.resource("placeholder", { triggers: { "Name": label } })
data lookup = nullEast.dataSource("lookup", { inputs: { name: label } })
module child "child" from "./child" { marker: label } using { "null.east": nullEast }
output id = placeholder.id
`
	file, diagnostics := Parse("test.infra", source)
	if len(diagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", diagnostics)
	}
	if len(file.Declarations) != 9 {
		t.Fatalf("Parse() declarations = %d, want 9", len(file.Declarations))
	}

	resource, ok := file.Declarations[5].(*ResourceDeclaration)
	if !ok {
		t.Fatalf("declaration 5 is %T, want *ResourceDeclaration", file.Declarations[5])
	}
	if resource.ProviderConfigName != "nullEast" || resource.Kind != "resource" || resource.Label != "placeholder" {
		t.Errorf("resource parsed as %#v", resource)
	}
	dataSource, ok := file.Declarations[6].(*DataDeclaration)
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

func TestParseRejectsDynamicResourceLabel(t *testing.T) {
	t.Parallel()

	_, diagnostics := Parse("test.infra", `
provider Null from "hashicorp/null"
configure null = Null({})
input label: string
resource item = null.resource(label, {})
`)
	if len(diagnostics) == 0 {
		t.Fatal("Parse() returned no diagnostics")
	}
}

func TestParseRejectsInvalidTerraformLabels(t *testing.T) {
	t.Parallel()

	_, diagnostics := Parse("test.infra", `
provider Null from "hashicorp/null"
configure null = Null({})
resource item = null.resource("bad.label", {})
`)
	if len(diagnostics) == 0 {
		t.Fatal("Parse() returned no diagnostics")
	}
}

func TestParseRejectsUnaryPlus(t *testing.T) {
	t.Parallel()

	_, diagnostics := Parse("test.infra", `input value: number = +1`)
	if len(diagnostics) == 0 {
		t.Fatal("Parse() returned no diagnostics")
	}
}
