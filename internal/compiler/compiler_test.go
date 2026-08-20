package compiler

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ondrejnov/infralang/internal/syntax"
)

func TestCompileTerraformProviderResourceAndModule(t *testing.T) {
	t.Parallel()

	source := `
output resourceId = placeholder.id
resource placeholder = nullEast.resource("placeholder", {
  triggers: {
    "DisplayName": prefix,
    retryCount: replicas,
  },
})
data lookup = nullEast.dataSource("lookup", {
  inputs: {
    marker: prefix,
  },
})
import module Child from "./child"
module childModule = Child("child", {
  markerValue: prefix,
}) using {
  "null.east": nullEast,
}
configure nullEast = Null("east", {})
let prefix = f"service-{name}"
input replicas: number = 2
input name: string = "api"
provider Null from "hashicorp/null" version "3.3.1"
terraform { requiredVersion: ">= 1.5.0" }
`
	file, parseDiagnostics := syntax.Parse("test.infra", source)
	if len(parseDiagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", parseDiagnostics)
	}
	result, diagnostics := Compile(file)
	if len(diagnostics) != 0 {
		t.Fatalf("Compile() diagnostics = %v", diagnostics)
	}

	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	terraformBlock := document["terraform"].(map[string]any)
	required := terraformBlock["required_providers"].(map[string]any)
	nullRequirement := required["null"].(map[string]any)
	if nullRequirement["source"] != "hashicorp/null" || nullRequirement["version"] != "3.3.1" {
		t.Errorf("required provider = %#v", nullRequirement)
	}

	providers := document["provider"].(map[string]any)["null"].([]any)
	if len(providers) != 1 || providers[0].(map[string]any)["alias"] != "east" {
		t.Errorf("provider configurations = %#v", providers)
	}

	resource := document["resource"].(map[string]any)["null_resource"].(map[string]any)["placeholder"].(map[string]any)
	if resource["provider"] != "null.east" {
		t.Errorf("resource provider = %#v", resource["provider"])
	}
	triggers := resource["triggers"].(map[string]any)
	if _, ok := triggers["DisplayName"]; !ok {
		t.Errorf("quoted provider key was not preserved: %#v", triggers)
	}
	if _, ok := triggers["retry_count"]; !ok {
		t.Errorf("camelCase provider key was not converted: %#v", triggers)
	}
	dataSource := document["data"].(map[string]any)["null_data_source"].(map[string]any)["lookup"].(map[string]any)
	if dataSource["provider"] != "null.east" {
		t.Errorf("data source provider = %#v", dataSource["provider"])
	}

	module := document["module"].(map[string]any)["child"].(map[string]any)
	if module["marker_value"] != "${local.prefix}" {
		t.Errorf("module marker_value = %#v", module["marker_value"])
	}
	moduleProviders := module["providers"].(map[string]any)
	if moduleProviders["null.east"] != "null.east" {
		t.Errorf("module providers = %#v", moduleProviders)
	}

	output := document["output"].(map[string]any)["resourceId"].(map[string]any)
	if output["value"] != "${null_resource.placeholder.id}" {
		t.Errorf("output value = %#v", output["value"])
	}
}

func TestExamplesCompile(t *testing.T) {
	t.Parallel()

	paths := []string{
		"../../examples/aws-s3/main.infra",
		"../../examples/basic/main.infra",
		"../../examples/lvm/main.infra",
		"../../examples/provider-alias/main.infra",
	}
	for _, sourcePath := range paths {
		sourcePath := sourcePath
		t.Run(sourcePath, func(t *testing.T) {
			t.Parallel()
			source, err := os.ReadFile(sourcePath)
			if err != nil {
				t.Fatalf("os.ReadFile() error = %v", err)
			}
			file, parseDiagnostics := syntax.Parse(sourcePath, string(source))
			if len(parseDiagnostics) != 0 {
				t.Fatalf("Parse() diagnostics = %v", parseDiagnostics)
			}
			if _, diagnostics := Compile(file); len(diagnostics) != 0 {
				t.Fatalf("Compile() diagnostics = %v", diagnostics)
			}
		})
	}
}

func TestCompileReportsUnknownName(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("test.infra", `output value = missing.value`)
	if len(parseDiagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Message, `unknown name "missing"`) {
		t.Fatalf("Compile() diagnostics = %v", diagnostics)
	}
}

func TestCompileReportsInputDefaultTypeMismatch(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("test.infra", `input replicas: number = "two"`)
	if len(parseDiagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Message, "incompatible type") {
		t.Fatalf("Compile() diagnostics = %v", diagnostics)
	}
}

func TestCompileForwardReferencesUseTerraformAddresses(t *testing.T) {
	t.Parallel()

	source := `
provider Terraform from "terraform.io/builtin/terraform"
configure terraform = Terraform({})
resource first = terraform.data("first", { input: second.output })
resource second = terraform.data("second", { input: "ready" })
output first = first.output
`
	result := compileSource(t, source)
	if !strings.Contains(string(result), `"input": "${terraform_data.second.output}"`) {
		t.Fatalf("compiled JSON does not contain the forward Terraform address:\n%s", result)
	}
}

func TestCompileProviderForwardReferenceUsesTerraformAddress(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
provider Null from "hashicorp/null" version "3.3.1"
configure defaultNull = Null({})
configure dependent = Null("dependent", { value: later.id })
resource later = defaultNull.resource("later", {})
`)
	if !strings.Contains(string(result), `"value": "${null_resource.later.id}"`) {
		t.Fatalf("provider configuration does not contain the forward Terraform address:\n%s", result)
	}
}

func TestCompilePreservesNumericPrecision(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
input integer: number = 9223372036854775808
input decimal: number = 9007199254740993.0
`)
	if !strings.Contains(string(result), `9223372036854775808`) {
		t.Fatalf("large integer was not preserved:\n%s", result)
	}
	if !strings.Contains(string(result), `9007199254740993.0`) {
		t.Fatalf("large decimal was not preserved:\n%s", result)
	}
}

func TestCompileEscapesTerraformTemplatesByContext(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
input amount: string = "10"
let literal = replace("${missing}", "missing", "present")
let formatted = f"${amount}"
let dollars = f"$${amount}"
`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	locals := document["locals"].(map[string]any)
	if locals["literal"] != `${replace("$${missing}", "missing", "present")}` {
		t.Errorf("literal expression = %q", locals["literal"])
	}
	if locals["formatted"] != `${"$"}${var.amount}` {
		t.Errorf("formatted expression = %q", locals["formatted"])
	}
	if locals["dollars"] != `${"$"}${"$"}${var.amount}` {
		t.Errorf("multiple-dollar expression = %q", locals["dollars"])
	}
}

func TestCompileChecksCompositeDefaults(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("test.infra", `
input ports: list<number> = [80, "https"]
input weights: map<number> = { primary: 1, secondary: "two" }
`)
	if len(parseDiagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) != 2 {
		t.Fatalf("Compile() diagnostics = %v, want 2 errors", diagnostics)
	}
}

func TestCompileOptionalInputDefaultsToNull(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `input token: optional<string>`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	variable := document["variable"].(map[string]any)["token"].(map[string]any)
	if variable["type"] != "string" {
		t.Errorf("optional input type = %#v", variable["type"])
	}
	if value, exists := variable["default"]; !exists || value != nil {
		t.Errorf("optional input default = %#v, exists = %t", value, exists)
	}
}

func TestCompileInfersForwardLocals(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("test.infra", `
let result = value + 1
let value = "not-a-number"
`)
	if len(parseDiagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Message, "arithmetic operators expect numbers") {
		t.Fatalf("Compile() diagnostics = %v", diagnostics)
	}
}

func TestCompileReportsLocalCycles(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("test.infra", `
let first = second
let second = first
`)
	if len(parseDiagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "dependency cycle") {
		t.Fatalf("Compile() diagnostics = %v", diagnostics)
	}
}

func TestCompileRejectsReservedNames(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("test.infra", `input true: string`)
	if len(parseDiagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Message, "reserved") {
		t.Fatalf("Compile() diagnostics = %v", diagnostics)
	}
}

func TestCompileModuleLanguageExtensions(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
terraform { requiredVersion: ">= 1.5.0" }
provider Null from "hashicorp/null" version "3.3.1"
import module Child from "./child"
configure nullProvider = Null
input machines: map<object {
  address: string,
  memory?: number = 1024,
}> with {
  description: "Machines",
  validations: [{ condition: length(machines) > 0, errorMessage: "required" }],
}

module child = Child("child", { name: each.key }) using { null: nullProvider } with {
  forEach: machines,
  dependsOn: [],
}
output selected = {for name, machine in child: name => machine.id if machine.ready} with { description: "Ready machines" }
moved from "module.old" to "module.child"
`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if _, exists := document["provider"]; exists {
		t.Fatal("inherited provider emitted a provider block")
	}
	variable := document["variable"].(map[string]any)["machines"].(map[string]any)
	if variable["type"] != "map(object({address = string, memory = optional(number, 1024)}))" {
		t.Errorf("variable type = %#v", variable["type"])
	}
	module := document["module"].(map[string]any)["child"].(map[string]any)
	if module["for_each"] != "${var.machines}" || module["name"] != "${each.key}" {
		t.Errorf("module = %#v", module)
	}
	output := document["output"].(map[string]any)["selected"].(map[string]any)
	if output["value"] != "${{for name, machine in module.child : name => machine.id if machine.ready}}" {
		t.Errorf("output value = %#v", output["value"])
	}
	if len(document["moved"].([]any)) != 1 {
		t.Errorf("moved = %#v", document["moved"])
	}
}

func TestCompileStaticTerraformTraversals(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
provider Terraform from "terraform.io/builtin/terraform"
configure terraform = Terraform
resource first = terraform.data("first", {})
resource second = terraform.data("second", {}, {
  dependsOn: [first],
  lifecycle: {
    ignoreChanges: [address("input.marker")],
  },
})
`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	second := document["resource"].(map[string]any)["terraform_data"].(map[string]any)["second"].(map[string]any)
	dependsOn := second["depends_on"].([]any)
	if len(dependsOn) != 1 || dependsOn[0] != "terraform_data.first" {
		t.Errorf("depends_on = %#v", dependsOn)
	}
	lifecycle := second["lifecycle"].(map[string]any)
	ignoreChanges := lifecycle["ignore_changes"].([]any)
	if len(ignoreChanges) != 1 || ignoreChanges[0] != "input.marker" {
		t.Errorf("ignore_changes = %#v", ignoreChanges)
	}
}

func TestCompilePreservesCombinedFileProvenanceAndDiagnosticOrder(t *testing.T) {
	t.Parallel()

	first, firstDiagnostics := syntax.Parse("z.infra", `output zed = missingZ.value`)
	second, secondDiagnostics := syntax.Parse("a.infra", `output alpha = missingA.value`)
	if len(firstDiagnostics)+len(secondDiagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v %v", firstDiagnostics, secondDiagnostics)
	}
	combined := &syntax.File{Name: "combined.infra", Declarations: append(first.Declarations, second.Declarations...)}
	_, diagnostics := Compile(combined)
	if len(diagnostics) != 2 {
		t.Fatalf("Compile() diagnostics = %v", diagnostics)
	}
	if diagnostics[0].Filename != "a.infra" || diagnostics[1].Filename != "z.infra" {
		t.Fatalf("diagnostic order/provenance = %v", diagnostics)
	}
	if diagnostics[0].Span.Start.Column != 16 {
		t.Errorf("first diagnostic span = %v", diagnostics[0].Span)
	}
}

func TestSortedDiagnosticsUsesCompleteStableKey(t *testing.T) {
	t.Parallel()

	span := syntax.Span{Start: syntax.Position{Offset: 4}, End: syntax.Position{Offset: 8}}
	diagnostics := []syntax.Diagnostic{
		{Filename: "same.infra", Span: span, Code: "B", Message: "z"},
		{Filename: "same.infra", Span: syntax.Span{Start: span.Start, End: syntax.Position{Offset: 7}}, Code: "Z", Message: "a"},
		{Filename: "same.infra", Span: span, Code: "A", Message: "z"},
		{Filename: "same.infra", Span: span, Code: "A", Message: "a"},
	}
	sorted := SortedDiagnostics(diagnostics)
	if sorted[0].Span.End.Offset != 7 || sorted[1].Message != "a" || sorted[2].Code != "A" || sorted[3].Code != "B" {
		t.Fatalf("SortedDiagnostics() = %#v", sorted)
	}
}

func TestCompileInputWireAliasAndLegacyName(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
input imageId "image_id": string = "ami"
input legacyId: string = "legacy"
output values = { aliased: imageId, legacy: legacyId }
`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatal(err)
	}
	variables := document["variable"].(map[string]any)
	if _, ok := variables["image_id"]; !ok {
		t.Fatalf("aliased variable missing: %#v", variables)
	}
	if _, ok := variables["legacyId"]; !ok {
		t.Fatalf("legacy variable spelling changed: %#v", variables)
	}
	value := document["output"].(map[string]any)["values"].(map[string]any)["value"].(map[string]any)
	if value["aliased"] != `${var.image_id}` || value["legacy"] != `${var.legacyId}` {
		t.Errorf("output value = %#v", value)
	}
}

func TestCompileRejectsDuplicateInputWireNames(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("inputs.infra", `
input first "shared": string
input second "shared": string
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) != 2 {
		t.Fatalf("Compile() diagnostics = %v, want both conflicting declarations", diagnostics)
	}
}

func TestCompileProviderShorthandAndClauseOrder(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
provider Null from "hashicorp/null"
import module Child from "./child"
configure nullProvider = Null
input items: map<string> = {}
module child = Child("child", { marker: each.key }) with { forEach: items } using [nullProvider]
`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatal(err)
	}
	module := document["module"].(map[string]any)["child"].(map[string]any)
	providers := module["providers"].(map[string]any)
	if providers["null"] != "null" {
		t.Errorf("providers = %#v", providers)
	}
}

func TestCompileRejectsAmbiguousProviderShorthand(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("providers.infra", `
provider Null from "hashicorp/null"
import module Child from "./child"
configure first = Null
configure second = Null("second", {})
let notProvider = "value"
module child = Child("child", {}) using [first, first, second, notProvider]
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) != 3 {
		t.Fatalf("Compile() diagnostics = %v, want repeated handle, provider alias, and non-provider", diagnostics)
	}
}

func TestCompileObjectPunning(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
input fieldName: string = "value"
let punned = { fieldName, }
let explicit = { fieldName: fieldName }
`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatal(err)
	}
	locals := document["locals"].(map[string]any)
	if locals["punned"].(map[string]any)["field_name"] != `${var.fieldName}` {
		t.Errorf("punned local = %#v", locals["punned"])
	}
	if !equalJSON(locals["punned"], locals["explicit"]) {
		t.Errorf("punned = %#v, explicit = %#v", locals["punned"], locals["explicit"])
	}
}

func TestCompileRejectsInvalidObjectPuns(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("puns.infra", `
input known: string
let unknownPun = { missing, }
let duplicatePun = { known, known: "duplicate" }
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) != 3 {
		t.Fatalf("Compile() diagnostics = %v, want unknown and both duplicate fields", diagnostics)
	}
}

func TestCompileGroupedMovedAddresses(t *testing.T) {
	t.Parallel()

	source := "moved { `module.old[\"unknown\"]` -> `module.new[\"unknown\"]`, `thing.old` -> `thing.new`, }"
	result := compileSource(t, source)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatal(err)
	}
	moved := document["moved"].([]any)
	if len(moved) != 2 || moved[0].(map[string]any)["from"] != `module.old["unknown"]` || moved[1].(map[string]any)["to"] != "thing.new" {
		t.Fatalf("moved = %#v", moved)
	}
	if strings.Contains(string(result), "${module.old") {
		t.Fatalf("raw moved address was interpolated:\n%s", result)
	}
}

func TestCompileConditionalResource(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
provider Terraform from "terraform.io/builtin/terraform"
configure terraform = Terraform
input enabled: bool = true
resource optional = terraform.data("optional", { input: enabled }) when enabled
output result = optional[0].output
`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatal(err)
	}
	resource := document["resource"].(map[string]any)["terraform_data"].(map[string]any)["optional"].(map[string]any)
	if resource["count"] != `${var.enabled ? 1 : 0}` {
		t.Errorf("conditional count = %#v", resource["count"])
	}
	output := document["output"].(map[string]any)["result"].(map[string]any)["value"]
	if output != `${terraform_data.optional[0].output}` {
		t.Errorf("indexed output = %#v", output)
	}
}

func TestCompileRejectsInvalidConditionalResources(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"condition type": `
provider Terraform from "terraform.io/builtin/terraform"
configure terraform = Terraform
resource optional = terraform.data("optional", {}) when "yes"
`,
		"cardinality": `
provider Terraform from "terraform.io/builtin/terraform"
configure terraform = Terraform
resource optional = terraform.data("optional", {}, { forEach: {} }) when true
`,
		"direct member": `
provider Terraform from "terraform.io/builtin/terraform"
configure terraform = Terraform
resource optional = terraform.data("optional", {}) when true
output result = optional.output
`,
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			file, parseDiagnostics := syntax.Parse(name+".infra", source)
			if len(parseDiagnostics) != 0 {
				t.Fatal(parseDiagnostics)
			}
			_, diagnostics := Compile(file)
			if len(diagnostics) == 0 {
				t.Fatal("Compile() returned no diagnostics")
			}
		})
	}
}

func TestCompileConciseAndLegacyValidationsPreserveOrder(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
input name: string = "value" with {
  validate length(name) > 0 else "first",
  validations: [
    { condition: name != "bad", errorMessage: "second" },
    { condition: name != "worse", errorMessage: "third" },
  ],
  validate name != "last" else "fourth",
}
`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatal(err)
	}
	validations := document["variable"].(map[string]any)["name"].(map[string]any)["validation"].([]any)
	if len(validations) != 4 {
		t.Fatalf("validations = %#v", validations)
	}
	want := []string{"first", "second", "third", "fourth"}
	for index, message := range want {
		if validations[index].(map[string]any)["error_message"] != message {
			t.Errorf("validations[%d] = %#v", index, validations[index])
		}
	}
}

func TestCompileRejectsNonBooleanConciseValidation(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("validation.infra", `
input name: string with { validate name else "must be valid", }
output independent = missing
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) != 2 {
		t.Fatalf("Compile() diagnostics = %v, want validation and independent errors", diagnostics)
	}
}

func TestCompileLexicalIteratorShadowing(t *testing.T) {
	t.Parallel()

	compileSource(t, `
let values = [for item in [1]: {
  nested: [for item in ["inner"]: item],
  outer: item + 1,
}]
`)
}

func TestCompileEachUnavailableInOwnForEach(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("each.infra", `
import module Child from "./child"
module child = Child("child", { value: each.value }) with { forEach: each.value }
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Message, `unknown name "each"`) {
		t.Fatalf("Compile() diagnostics = %v", diagnostics)
	}
}

func TestCompilePhaseOneErrorRecovery(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("errors.infra", `
provider Null from "hashicorp/null"
import module Child from "./child"
configure nullProvider = Null
module child = Child("child", {}) using { missingProvider }
output independent = missingValue
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) != 2 {
		t.Fatalf("Compile() diagnostics = %v, want independent errors", diagnostics)
	}
}

func equalJSON(left, right any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}

func TestCompileConsistentObjectWireNamesAndStructuralAliases(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
type Config = object {
  imageId "image_id": string,
  regionName "region"?: string = "default",
  exactCamel "exactCamel": string,
}
input config: Config = { imageId: "ami", exactCamel: "kept" }
input legacyInput: string = "legacy"
let inlineValue = { imageId: config.imageId }
let quotedValue = { "imageId": config.imageId }
output values = {
  inlineValue: inlineValue,
  directValue: { imageId: config.imageId },
  quotedValue: quotedValue["imageId"],
  legacyInput: legacyInput,
}
`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatal(err)
	}
	variables := document["variable"].(map[string]any)
	config := variables["config"].(map[string]any)
	if config["type"] != "object({image_id = string, region = optional(string, \"default\"), exactCamel = string})" {
		t.Errorf("config type = %#v", config["type"])
	}
	defaultValue := config["default"].(map[string]any)
	if defaultValue["image_id"] != "ami" || defaultValue["region"] != "default" || defaultValue["exactCamel"] != "kept" {
		t.Errorf("completed config default = %#v", defaultValue)
	}
	if _, ok := variables["legacyInput"]; !ok {
		t.Fatalf("legacy input spelling changed: %#v", variables)
	}
	locals := document["locals"].(map[string]any)
	if _, ok := locals["inlineValue"].(map[string]any)["image_id"]; !ok {
		t.Errorf("local wire key = %#v", locals["inlineValue"])
	}
	if _, ok := locals["quotedValue"].(map[string]any)["imageId"]; !ok {
		t.Errorf("quoted key changed = %#v", locals["quotedValue"])
	}
	output := document["output"].(map[string]any)["values"].(map[string]any)["value"].(map[string]any)
	if _, ok := output["inline_value"]; !ok {
		t.Errorf("output keys were not constructed consistently: %#v", output)
	}
}

func TestCompileRejectsQuotedMemberAndStructuralCollisions(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("fields.infra", `
type SourceCollision = object { same: string, same: number }
type WireCollision = object { imageId: string, image_id: string }
let quoted = { "imageId": "value" }
output invalid = quoted.imageId
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) != 5 {
		t.Fatalf("Compile() diagnostics = %v, want both source fields, both wire fields, and quoted-member error", diagnostics)
	}
}

func TestCompileNestedStructuralAssignmentDiagnostics(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("assignment.infra", `
type Nested = object {
  requiredName: string,
  optionalCount?: number,
}
type Config = object {
  nested: Nested,
  enabled: bool,
}
input config: Config = { nested: { requiredName: 42 } }
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) != 2 {
		t.Fatalf("Compile() diagnostics = %v, want nested mismatch and missing required field", diagnostics)
	}
	joined := diagnostics[0].Message + " " + diagnostics[1].Message
	if !strings.Contains(joined, "required_name") || !strings.Contains(joined, "enabled") {
		t.Fatalf("structural diagnostics = %v", diagnostics)
	}
}

func TestCompileStructuralConditionalCommonTypes(t *testing.T) {
	t.Parallel()

	compileSource(t, `
input enabled: bool = true
let compatible = enabled ? { commonValue: 1, optionalValue: "yes" } : { commonValue: 2 }
output common = compatible.commonValue
output optional = compatible.optionalValue
`)

	file, parseDiagnostics := syntax.Parse("conditional-types.infra", `
input enabled: bool = true
let incompatible = enabled ? { value: 1 } : { value: "one" }
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Message, "incompatible types") {
		t.Fatalf("Compile() diagnostics = %v", diagnostics)
	}
}

func TestCompileTypeAliasesAcrossCombinedFilesAndErasure(t *testing.T) {
	t.Parallel()

	usage, usageDiagnostics := syntax.Parse("a-use.infra", `
input config: HostConfig = { hostName: "node" }
output host = config.hostName
`)
	alias, aliasDiagnostics := syntax.Parse("z-types.infra", `
type HostConfig = object { hostName "host_name": string }
`)
	if len(usageDiagnostics)+len(aliasDiagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v %v", usageDiagnostics, aliasDiagnostics)
	}
	combined := &syntax.File{Name: "combined.infra", Declarations: append(usage.Declarations, alias.Declarations...)}
	result, diagnostics := Compile(combined)
	if len(diagnostics) != 0 {
		t.Fatalf("Compile() diagnostics = %v", diagnostics)
	}
	if strings.Contains(string(result), `"type_alias"`) || !strings.Contains(string(result), `"host_name"`) {
		t.Fatalf("alias did not expand and erase:\n%s", result)
	}
}

func TestCompileRejectsInvalidTypeAliasesDeterministically(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("aliases.infra", `
type string = number
type MissingUse = Missing
type First = Second
type Second = First
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) != 4 {
		t.Fatalf("Compile() diagnostics = %v, want builtin, unknown, and complete two-node cycle", diagnostics)
	}
	for index := 1; index < len(diagnostics); index++ {
		if diagnostics[index-1].Span.Start.Offset > diagnostics[index].Span.Start.Offset {
			t.Fatalf("diagnostics are not deterministic: %v", diagnostics)
		}
	}
}

func TestCompileOrderedObjectSpreadLoweringAndTyping(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
let base = { sharedValue: "base", baseOnly: "base" }
let merged = {
  beforeValue: "before",
  ...base,
  sharedValue: "override",
  ...{ afterValue: "after", sharedValue: "last" },
}
output shared = merged.sharedValue
`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatal(err)
	}
	merged := document["locals"].(map[string]any)["merged"].(string)
	if !strings.Contains(merged, "merge(") || !strings.Contains(merged, "local.base") || !strings.Contains(merged, `"shared_value" = "last"`) {
		t.Errorf("ordered merge lowering = %q", merged)
	}
	output := document["output"].(map[string]any)["shared"].(map[string]any)["value"]
	if output != `${local.merged.shared_value}` {
		t.Errorf("spread result member = %#v", output)
	}
}

func TestCompileBlockSpreadBoundaries(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
provider Terraform from "terraform.io/builtin/terraform"
configure terraform = Terraform
let fixedArgs = { inputValue: "fixed" }
resource fixed = terraform.data("fixed", { ...fixedArgs })
`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatal(err)
	}
	fixed := document["resource"].(map[string]any)["terraform_data"].(map[string]any)["fixed"].(map[string]any)
	if fixed["input_value"] != `${local.fixedArgs.input_value}` {
		t.Errorf("fixed block spread = %#v", fixed)
	}

	file, parseDiagnostics := syntax.Parse("dynamic-spread.infra", `
provider Terraform from "terraform.io/builtin/terraform"
configure terraform = Terraform
input dynamicArgs: map<string> = {}
resource invalid = terraform.data("invalid", { ...dynamicArgs })
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Message, "statically known object shape") {
		t.Fatalf("Compile() diagnostics = %v", diagnostics)
	}
}

func TestCompileRejectsInvalidObjectSpreadOperand(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("spread.infra", `let invalid = { ...42 }`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Message, "got number") {
		t.Fatalf("Compile() diagnostics = %v", diagnostics)
	}
}

func TestCompileConditionalObjectFields(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
input enabled: bool = true
let config = {
  description: "enabled" when enabled,
  stableValue: "stable",
}
output description = config.description
`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatal(err)
	}
	config := document["locals"].(map[string]any)["config"].(string)
	if !strings.Contains(config, `var.enabled ? {"description" = "enabled"} : {}`) || !strings.Contains(config, `"stable_value"`) {
		t.Errorf("conditional object lowering = %q", config)
	}

	file, parseDiagnostics := syntax.Parse("conditional-fields.infra", `
provider Terraform from "terraform.io/builtin/terraform"
configure terraform = Terraform
input enabled: bool = true
resource invalid = terraform.data("invalid", { input: "value" when enabled })
let badType = { value: 1 when "yes" }
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := Compile(file)
	if len(diagnostics) != 2 {
		t.Fatalf("Compile() diagnostics = %v, want runtime block shape and condition type errors", diagnostics)
	}
}

func TestCompileTypedEachBindings(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
type HostConfig = object { hostName "host_name": string }
provider Terraform from "terraform.io/builtin/terraform"
import module Child from "./child"
configure terraform = Terraform
input hosts: map<HostConfig> = { first: { hostName: "node" } }
input names: set<string> = ["one"]
module child = Child("child", { name: each.value.hostName }) with { forEach: hosts }
resource item = terraform.data("item", { input: each.key, value: each.value }, { forEach: names })
`)
	if !strings.Contains(string(result), `${each.value.host_name}`) || !strings.Contains(string(result), `${each.key}`) {
		t.Fatalf("typed each lowering:\n%s", result)
	}

	tests := map[string]string{
		"bad member": `
type HostConfig = object { hostName: string }
import module Child from "./child"
input hosts: map<HostConfig> = {}
module child = Child("child", { name: each.value.missing }) with { forEach: hosts }
`,
		"scalar": "import module Child from \"./child\"\nmodule child = Child(\"child\", {}) with { forEach: 1 }",
		"tuple":  "import module Child from \"./child\"\nmodule child = Child(\"child\", {}) with { forEach: [\"one\"] }",
		"non-string set": `input values: set<number> = [1]
import module Child from "./child"
module child = Child("child", {}) with { forEach: values }`,
		"outside each": `output invalid = each.value`,
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			file, parseDiagnostics := syntax.Parse(name+".infra", source)
			if len(parseDiagnostics) != 0 {
				t.Fatal(parseDiagnostics)
			}
			_, diagnostics := Compile(file)
			if len(diagnostics) == 0 {
				t.Fatal("Compile() returned no diagnostics")
			}
		})
	}
}

func TestCompilePhaseTwoDeterminismAndInlineLocalEquality(t *testing.T) {
	t.Parallel()

	typesFile, diagnostics := syntax.Parse("types.infra", `type Config = object { imageId: string }`)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	valuesFile, diagnostics := syntax.Parse("values.infra", `
input config: Config = { imageId: "ami" }
let named = { imageId: config.imageId }
output values = { inline: { imageId: config.imageId }, named: named }
`)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	first := &syntax.File{Name: "combined.infra", Declarations: append(typesFile.Declarations, valuesFile.Declarations...)}
	second := &syntax.File{Name: "combined.infra", Declarations: append(valuesFile.Declarations, typesFile.Declarations...)}
	firstResult, firstDiagnostics := Compile(first)
	secondResult, secondDiagnostics := Compile(second)
	if len(firstDiagnostics)+len(secondDiagnostics) != 0 {
		t.Fatalf("Compile() diagnostics = %v %v", firstDiagnostics, secondDiagnostics)
	}
	if string(firstResult) != string(secondResult) {
		t.Fatalf("reordered compilation differs:\n%s\n%s", firstResult, secondResult)
	}
	var document map[string]any
	if err := json.Unmarshal(firstResult, &document); err != nil {
		t.Fatal(err)
	}
	local := document["locals"].(map[string]any)["named"].(map[string]any)
	output := document["output"].(map[string]any)["values"].(map[string]any)["value"].(map[string]any)
	inline := output["inline"].(map[string]any)
	if !equalJSON(local, inline) {
		t.Fatalf("inline = %#v, local = %#v", inline, local)
	}
}

func TestCollectModuleInterface(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("child.infra", `
provider Null from "hashicorp/null"
configure childNull = Null
input imageId "image_id": string
input retries: number = 3 with { sensitive: true }
output image = imageId
output retryCount = retries with { sensitive: true }
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	contract, diagnostics := CollectInterface(file, CompileOptions{ModuleID: "child"})
	if len(diagnostics) != 0 {
		t.Fatalf("CollectInterface() diagnostics = %v", diagnostics)
	}
	if len(contract.Inputs) != 2 || contract.Inputs[0].Name.Source != "imageId" || contract.Inputs[0].Name.Wire != "image_id" || !contract.Inputs[0].Required {
		t.Fatalf("inputs = %#v", contract.Inputs)
	}
	if contract.Inputs[1].Required || !contract.Inputs[1].Sensitive {
		t.Fatalf("optional/sensitive input = %#v", contract.Inputs[1])
	}
	if len(contract.Outputs) != 2 || contract.Outputs[0].Type.kind != valueString || !contract.Outputs[1].Sensitive {
		t.Fatalf("outputs = %#v", contract.Outputs)
	}
	if len(contract.Providers) != 1 || contract.Providers[0].Name.Source != "childNull" || contract.Providers[0].Source != "hashicorp/null" {
		t.Fatalf("providers = %#v", contract.Providers)
	}
}

func TestCompileLocalModuleForwardingAndOutputTyping(t *testing.T) {
	t.Parallel()

	child := collectInterfaceSource(t, "child", `
input imageId "image_id": string
input retries: number = 1
output image = imageId
`)
	result := compileSourceWithOptions(t, `
import module Child from "./child"
input config: object { imageId "image_id": string, retries: number } = { imageId: "ami", retries: 4 }
module child = Child("child", {
  ...inputs(config),
  retries: 2,
})
output image = child.image
`, CompileOptions{ModuleID: ".", LocalModules: map[string]ModuleInterface{"./child": child}})
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatal(err)
	}
	module := document["module"].(map[string]any)["child"].(map[string]any)
	if module["image_id"] != `${var.config.image_id}` || module["retries"] != float64(2) {
		t.Fatalf("forwarded module arguments = %#v", module)
	}
	output := document["output"].(map[string]any)["image"].(map[string]any)["value"]
	if output != `${module.child.image}` {
		t.Errorf("typed child output = %#v", output)
	}
}

func TestCompileRejectsInvalidInputForwarding(t *testing.T) {
	t.Parallel()

	child := collectInterfaceSource(t, "child", `input shared: string = "default"`)
	options := CompileOptions{ModuleID: ".", LocalModules: map[string]ModuleInterface{"./child": child}}
	tests := []struct {
		name       string
		source     string
		wantErrors int
		contains   string
	}{
		{
			name: "forward conflict",
			source: `
import module Child from "./child"
let first = { shared: "first" }
let second = { shared: "second" }
module child = Child("child", { ...inputs(first), ...inputs(second) })
`, wantErrors: 2, contains: "multiple forwarding",
		},
		{
			name:       "unknown field",
			source:     "import module Child from \"./child\"\nmodule child = Child(\"child\", { ...inputs({ unknown: \"value\" }) })",
			wantErrors: 1, contains: "not an input",
		},
		{
			name: "dynamic keys",
			source: `
import module Child from "./child"
input values: map<string> = {}
module child = Child("child", { ...inputs(values) })
`, wantErrors: 1, contains: "statically structural",
		},
		{
			name:       "remote boundary",
			source:     "import module Child from \"registry.example/child\"\nmodule child = Child(\"child\", { ...inputs({ shared: \"value\" }) })",
			wantErrors: 1, contains: "local InfraLang child",
		},
		{
			name:       "outside module",
			source:     `let invalid = { ...inputs({ shared: "value" }) }`,
			wantErrors: 1, contains: "only valid in a module",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file, parseDiagnostics := syntax.Parse(test.name+".infra", test.source)
			if len(parseDiagnostics) != 0 {
				t.Fatal(parseDiagnostics)
			}
			_, diagnostics := CompileWithOptions(file, options)
			if len(diagnostics) != test.wantErrors {
				t.Fatalf("CompileWithOptions() diagnostics = %v, want %d", diagnostics, test.wantErrors)
			}
			joined := ""
			for _, diagnostic := range diagnostics {
				joined += diagnostic.Message + " "
			}
			if !strings.Contains(joined, test.contains) {
				t.Fatalf("diagnostics = %v, want %q", diagnostics, test.contains)
			}
		})
	}
}

func TestCompileReportsCompleteLocalCallErrors(t *testing.T) {
	t.Parallel()

	child := collectInterfaceSource(t, "child", `
input requiredName: string
input nested: object { value: number }
output result = requiredName
`)
	file, parseDiagnostics := syntax.Parse("parent.infra", `
import module Child from "./child"
module child = Child("child", {
  unknown: true,
  nested: { value: "wrong" },
})
output missing = child.missing
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := CompileWithOptions(file, CompileOptions{ModuleID: ".", LocalModules: map[string]ModuleInterface{"./child": child}})
	if len(diagnostics) != 4 {
		t.Fatalf("CompileWithOptions() diagnostics = %v, want unknown, missing, nested mismatch, and output errors", diagnostics)
	}
	joined := ""
	for _, diagnostic := range diagnostics {
		joined += diagnostic.Message + " "
	}
	for _, expected := range []string{"unknown input", "missing required input", "incompatible type", "no field"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("diagnostics = %v, missing %q", diagnostics, expected)
		}
	}
}

func TestCompileRequiresIndexForIteratedLocalModuleOutputs(t *testing.T) {
	t.Parallel()

	child := collectInterfaceSource(t, "child", `output value = "child"`)
	file, parseDiagnostics := syntax.Parse("iterated.infra", `
import module Child from "./child"
input items: map<string> = {}
module child = Child("child", {}) with { forEach: items }
output invalid = child.value
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := CompileWithOptions(file, CompileOptions{LocalModules: map[string]ModuleInterface{"./child": child}})
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Message, "is a collection") {
		t.Fatalf("CompileWithOptions() diagnostics = %v", diagnostics)
	}
}

func TestCompileChecksLocalProviderSlots(t *testing.T) {
	t.Parallel()

	child := collectInterfaceSource(t, "child", `
provider Null from "hashicorp/null"
configure childNull = Null
`)
	options := CompileOptions{ModuleID: ".", LocalModules: map[string]ModuleInterface{"./child": child}}
	tests := []struct {
		name     string
		source   string
		contains string
	}{
		{
			name:     "missing",
			source:   "import module Child from \"./child\"\nmodule child = Child(\"child\", {})",
			contains: "missing provider slot",
		},
		{
			name: "unknown",
			source: `
provider Null from "hashicorp/null"
import module Child from "./child"
configure parentNull = Null
module child = Child("child", {}) using { other: parentNull }
`, contains: "unknown provider slot",
		},
		{
			name: "source mismatch",
			source: `
provider Random from "hashicorp/random"
import module Child from "./child"
configure random = Random
module child = Child("child", {}) using { childNull: random }
`, contains: "requires source",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file, parseDiagnostics := syntax.Parse(test.name+".infra", test.source)
			if len(parseDiagnostics) != 0 {
				t.Fatal(parseDiagnostics)
			}
			_, diagnostics := CompileWithOptions(file, options)
			joined := ""
			for _, diagnostic := range diagnostics {
				joined += diagnostic.Message + " "
			}
			if !strings.Contains(joined, test.contains) {
				t.Fatalf("diagnostics = %v, want %q", diagnostics, test.contains)
			}
		})
	}
}

func TestCompileConstantsEvaluateExactlyAndErase(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
const huge = 9007199254740992 + 1
const decimal: number = 0.1 + 0.2
const doubled = [for value in [1, 2, 3]: value * 2 if value > 1]
const base = { firstValue: huge }
const composed = {
  ...base,
  decimalValue: decimal when true,
  omitted: "no" when false,
  doubled,
}
output result = composed
`)
	text := string(result)
	if strings.Contains(text, `"locals"`) || strings.Contains(text, `"const"`) || !strings.Contains(text, `9007199254740993`) {
		t.Fatalf("constant erasure or exact integer failed:\n%s", result)
	}
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatal(err)
	}
	value := document["output"].(map[string]any)["result"].(map[string]any)["value"].(map[string]any)
	if value["decimal_value"] != 0.3 || value["omitted"] != nil {
		t.Fatalf("constant object = %#v", value)
	}
	doubled := value["doubled"].([]any)
	if len(doubled) != 2 || doubled[0] != float64(4) || doubled[1] != float64(6) {
		t.Fatalf("constant comprehension = %#v", doubled)
	}
}

func TestCompileTimeSubstitutionRespectsRuntimeComprehensionScope(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
const item = "compile-time"
input items: list<string> = ["runtime"]
output values = [for item in items: item]
`)
	text := string(result)
	if !strings.Contains(text, `for item in var.items : item`) || strings.Contains(text, `: "compile-time"`) {
		t.Fatalf("comprehension iterator was captured by constant substitution:\n%s", result)
	}
}

func TestCompileTimeLabelsAndProviderAliases(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
provider Null from "hashicorp/null"
import module Child from "registry.example/child"
const suffix = "stable"
configure selected = Null(f"west-{suffix}", {})
resource item = selected.resource(f"resource-{suffix}", {})
data lookup = selected.dataSource(f"lookup-{suffix}", {})
module child = Child(f"module-{suffix}", {})
`)
	text := string(result)
	for _, expected := range []string{`"alias": "west-stable"`, `"resource-stable"`, `"lookup-stable"`, `"module-stable"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("compile-time label output missing %s:\n%s", expected, result)
		}
	}
}

func TestCompileConstantDiagnostics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		source   string
		contains []string
	}{
		{name: "annotation", source: `const retries: number = "three"`, contains: []string{"incompatible annotation"}},
		{name: "runtime", source: "input value: string\nconst invalid = value", contains: []string{"runtime or unknown name"}},
		{name: "effectful call", source: `const invalid = file("secret")`, contains: []string{"function calls and effectful operations"}},
		{name: "unknown annotation", source: `const invalid: Missing = 1`, contains: []string{"unknown type"}},
		{name: "non-finite decimal", source: `const invalid = 1 / 3`, contains: []string{"not a finite decimal"}},
		{name: "runtime name conflict", source: "const shared = 1\nlet shared = 2", contains: []string{"conflicts with a runtime declaration"}},
		{name: "cycle", source: "const first = second\nconst second = first", contains: []string{"constant \"first\" is part", "constant \"second\" is part"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file, parseDiagnostics := syntax.Parse(test.name+".infra", test.source)
			if len(parseDiagnostics) != 0 {
				t.Fatal(parseDiagnostics)
			}
			_, diagnostics := Compile(file)
			joined := ""
			for _, diagnostic := range diagnostics {
				joined += diagnostic.Message + " "
			}
			for _, expected := range test.contains {
				if !strings.Contains(joined, expected) {
					t.Fatalf("diagnostics = %v, want %q", diagnostics, expected)
				}
			}
		})
	}
}

func TestCompileStaticIndexedProvidersAndModules(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
provider Null from "hashicorp/null"
import module Child from "registry.example/child"
const regions = {
  west: { label: "west_child" },
  east: { label: "east_child" },
}
static for key, region in regions {
  configure providers[key] = Null({})
  module children[key] = Child(region.label, {}) using { "null": providers[key] }
}
output westId = children["west"].id
`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatal(err)
	}
	providers := document["provider"].(map[string]any)["null"].([]any)
	if len(providers) != 2 || providers[0].(map[string]any)["alias"] != "east" || providers[1].(map[string]any)["alias"] != "west" {
		t.Fatalf("sorted indexed providers = %#v", providers)
	}
	modules := document["module"].(map[string]any)
	if len(modules) != 2 || modules["east_child"] == nil || modules["west_child"] == nil {
		t.Fatalf("indexed modules = %#v", modules)
	}
	west := modules["west_child"].(map[string]any)
	if west["providers"].(map[string]any)["null"] != "null.west" {
		t.Fatalf("indexed provider reference = %#v", west)
	}
	output := document["output"].(map[string]any)["westId"].(map[string]any)["value"]
	if output != `${module.west_child.id}` || strings.Contains(string(result), "for_each") || strings.Contains(string(result), "each.") {
		t.Fatalf("indexed module lowering = %#v\n%s", output, result)
	}
}

func TestCompileNestedStaticLoopsAndNumericModuleIndexes(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
import module Child from "registry.example/child"
const groups = ["a", "b"]
static for group in groups {
  static for index, suffix in ["one", "two"] {
    module children[f"{group}-{index + 9007199254740993}"] = Child(f"{group}-{suffix}", {})
  }
}
output selected = children["b-9007199254740994"].id
module numeric[9007199254740992 + 1] = Child("numeric", {})
output numericId = numeric[9007199254740993].id
`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatal(err)
	}
	modules := document["module"].(map[string]any)
	if len(modules) != 5 {
		t.Fatalf("nested static modules = %#v", modules)
	}
	outputs := document["output"].(map[string]any)
	if outputs["selected"].(map[string]any)["value"] != `${module.b-two.id}` || outputs["numericId"].(map[string]any)["value"] != `${module.numeric.id}` {
		t.Fatalf("numeric indexed outputs = %#v", outputs)
	}
}

func TestCompileRejectsInvalidStaticLabelsAndIndexes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		source   string
		contains string
	}{
		{name: "runtime loop", source: "input values: list<string> = []\nstatic for value in values { output item = value }", contains: "runtime or unknown name"},
		{name: "non iterable", source: `static for value in 1 { output item = value }`, contains: "expects a compile-time list or object"},
		{name: "dynamic label", source: "provider Terraform from \"terraform.io/builtin/terraform\"\nconfigure terraform = Terraform\ninput label: string\nresource item = terraform.data(label, {})", contains: "runtime or unknown name"},
		{name: "invalid label", source: "provider Terraform from \"terraform.io/builtin/terraform\"\nconfigure terraform = Terraform\nresource item = terraform.data(\"bad.label\", {})", contains: "not a valid Terraform identifier"},
		{name: "numeric provider", source: "provider Null from \"hashicorp/null\"\nconfigure providers[1] = Null({})", contains: "indexed provider key"},
		{name: "unknown lookup", source: "import module Child from \"remote/child\"\nmodule children[\"known\"] = Child(\"known\", {})\noutput missing = children[\"missing\"].id", contains: "unknown indexed handle"},
		{name: "duplicate numeric index", source: "import module Child from \"remote/child\"\nmodule children[1.0] = Child(\"first\", {})\nmodule children[1] = Child(\"second\", {})", contains: "already declared"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file, parseDiagnostics := syntax.Parse(test.name+".infra", test.source)
			if len(parseDiagnostics) != 0 {
				t.Fatal(parseDiagnostics)
			}
			_, diagnostics := Compile(file)
			joined := ""
			for _, diagnostic := range diagnostics {
				joined += diagnostic.Message + " "
			}
			if !strings.Contains(joined, test.contains) {
				t.Fatalf("diagnostics = %v, want %q", diagnostics, test.contains)
			}
		})
	}
}

func TestCompileGeneratedCollisionReportsIterationProvenance(t *testing.T) {
	t.Parallel()

	file, parseDiagnostics := syntax.Parse("collision.infra", `
provider Terraform from "terraform.io/builtin/terraform"
configure terraform = Terraform
static for value in ["first", "second"] {
  resource repeated = terraform.data("same", {})
}
`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	prepared, prepareDiagnostics := Prepare(file)
	var expansions []string
	for _, declaration := range prepared.Declarations {
		if resource, ok := declaration.(*syntax.ResourceDeclaration); ok {
			expansions = append(expansions, resource.GetExpansion())
		}
	}
	if len(prepareDiagnostics) == 0 {
		t.Fatalf("Prepare() returned no collision diagnostics; expansions = %#v", expansions)
	}
	_, diagnostics := Compile(file)
	joined := ""
	for _, diagnostic := range diagnostics {
		joined += diagnostic.Message + " "
	}
	if !strings.Contains(joined, "value=0") || !strings.Contains(joined, "value=1") || !strings.Contains(joined, "already declared") {
		t.Fatalf("collision diagnostics lack deterministic provenance: %v", diagnostics)
	}
}

func TestCompileTimeIdentityIgnoresSourceHandleRenames(t *testing.T) {
	t.Parallel()

	first := compileSource(t, `
provider Null from "hashicorp/null"
import module Child from "registry.example/child"
configure providers["west"] = Null({})
resource sourceItem = providers["west"].resource("stable_resource", {})
module children[1] = Child("stable_module", {})
output resourceId = sourceItem.id
output moduleId = children[1].id
`)
	second := compileSource(t, `
import module Child from "registry.example/child"
output moduleId = renamedModules[1.0].id
module renamedModules[1.0] = Child("stable_module", {})
output resourceId = renamedItem.id
resource renamedItem = renamedProviders["west"].resource("stable_resource", {})
configure renamedProviders["west"] = Null({})
provider Null from "hashicorp/null"
`)
	if string(first) != string(second) {
		t.Fatalf("source-only renames or reorder changed Terraform identity:\n%s\n%s", first, second)
	}

	changed := compileSource(t, `
provider Null from "hashicorp/null"
import module Child from "registry.example/child"
configure providers["west"] = Null({})
resource sourceItem = providers["west"].resource("changed_resource", {})
module children[1] = Child("changed_module", {})
`)
	if string(first) == string(changed) || !strings.Contains(string(changed), `"changed_resource"`) || !strings.Contains(string(changed), `"changed_module"`) {
		t.Fatalf("explicit identity change was not reflected:\n%s", changed)
	}
}

func TestGeneratedTerraformValidates(t *testing.T) {
	terraform, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform is not installed")
	}

	result := compileSource(t, `
terraform { requiredVersion: ">= 1.5.0" }
provider Terraform from "terraform.io/builtin/terraform"
const compileTimeInput = 9007199254740992 + 1
input amount: string = "10"
input huge: number = 9223372036854775808
input exact: number = 9007199254740993.0
input offset: number = -1
input token: optional<string>
let formatted = f"${amount}"
let literal = replace("${missing}", "missing", "present")
let control = replace("\x01", "x", "y")
let dollars = f"$${amount}"
let baseObject = { baseValue: amount }
let composedObject = {
  ...baseObject,
  optionalValue: token when token != null,
  finalValue: exact,
}
configure terraform = Terraform({})
component ValidatedComponent(label: string, value: number) using { terraform: Terraform } {
  resource componentItem = terraform.data(label, { input: value })
  export result = componentItem.output
}
instantiate validatedComponent = ValidatedComponent(label: "component", value: compileTimeInput) using { terraform: terraform }
static for label in ["static"] {
  resource staticItem = terraform.data(label, { input: compileTimeInput })
}
resource first = terraform.data("first", { input: second.output })
resource second = terraform.data("second", {
  input: {
    formatted: formatted,
    huge: huge,
    exact: exact,
    literal: literal,
    control: control,
    dollars: dollars,
    offset: offset,
    token: token,
    composed: composedObject,
  },
})
output result = first.output
output componentResult = validatedComponent.result
`)
	directory := t.TempDir()
	configurationPath := filepath.Join(directory, "main.tf.json")
	if err := os.WriteFile(configurationPath, result, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	for _, arguments := range [][]string{
		{"-chdir=" + directory, "init", "-backend=false", "-input=false"},
		{"-chdir=" + directory, "validate"},
		{"-chdir=" + directory, "plan", "-input=false", "-lock=false", "-refresh=false"},
	} {
		command := exec.Command(terraform, arguments...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("terraform %s failed: %v\n%s", strings.Join(arguments, " "), err, output)
		}
	}
}

func compileSource(t *testing.T, source string) []byte {
	t.Helper()
	file, parseDiagnostics := syntax.Parse("test.infra", source)
	if len(parseDiagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", parseDiagnostics)
	}
	result, diagnostics := Compile(file)
	if len(diagnostics) != 0 {
		t.Fatalf("Compile() diagnostics = %v", diagnostics)
	}
	return result
}

func collectInterfaceSource(t *testing.T, moduleID, source string) ModuleInterface {
	t.Helper()
	file, diagnostics := syntax.Parse(moduleID+".infra", source)
	if len(diagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", diagnostics)
	}
	contract, diagnostics := CollectInterface(file, CompileOptions{ModuleID: moduleID})
	if len(diagnostics) != 0 {
		t.Fatalf("CollectInterface() diagnostics = %v", diagnostics)
	}
	return contract
}

func compileSourceWithOptions(t *testing.T, source string, options CompileOptions) []byte {
	t.Helper()
	file, diagnostics := syntax.Parse("test.infra", source)
	if len(diagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", diagnostics)
	}
	result, diagnostics := CompileWithOptions(file, options)
	if len(diagnostics) != 0 {
		t.Fatalf("CompileWithOptions() diagnostics = %v", diagnostics)
	}
	return result
}
