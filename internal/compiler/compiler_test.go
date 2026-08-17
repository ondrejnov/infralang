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
module childModule "child" from "./child" {
  markerValue: prefix,
} using {
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
		"../../examples/basic/main.infra",
		"../../examples/lvm/main.infra",
		"../../examples/provider-alias/main.infra",
		"../../examples/staging/main.infra",
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

func TestGeneratedTerraformValidates(t *testing.T) {
	terraform, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform is not installed")
	}

	result := compileSource(t, `
terraform { requiredVersion: ">= 1.5.0" }
provider Terraform from "terraform.io/builtin/terraform"
input amount: string = "10"
input huge: number = 9223372036854775808
input exact: number = 9007199254740993.0
input offset: number = -1
input token: optional<string>
let formatted = f"${amount}"
let literal = replace("${missing}", "missing", "present")
let control = replace("\x01", "x", "y")
let dollars = f"$${amount}"
configure terraform = Terraform({})
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
  },
})
output result = first.output
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
