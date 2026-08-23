package compiler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ondrejnov/infralang/internal/syntax"
)

func TestCompileTerraformBackendAndCloud(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
const prefix = "app/prod"

terraform {
  requiredVersion: ">= 1.5.0",
  backend s3 = {
    bucket: "example-tfstate",
    key: f"{prefix}/terraform.tfstate",
    region: "eu-central-1",
    dynamodbTable: "locks",
    "Legacy_Key": "kept",
  }
}
output nothing = "ok"
`)
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	terraformBlock := document["terraform"].(map[string]any)
	if terraformBlock["required_version"] != ">= 1.5.0" {
		t.Errorf("required_version = %#v", terraformBlock["required_version"])
	}
	backend := terraformBlock["backend"].(map[string]any)["s3"].(map[string]any)
	if backend["bucket"] != "example-tfstate" {
		t.Errorf("backend bucket = %#v", backend["bucket"])
	}
	if backend["key"] != "app/prod/terraform.tfstate" {
		t.Errorf("backend key = %#v", backend["key"])
	}
	if backend["region"] != "eu-central-1" {
		t.Errorf("backend region = %#v", backend["region"])
	}
	if backend["dynamodb_table"] != "locks" {
		t.Errorf("backend dynamodb_table = %#v", backend["dynamodb_table"])
	}
	if backend["Legacy_Key"] != "kept" {
		t.Errorf("backend quoted key = %#v", backend["Legacy_Key"])
	}

	cloudResult := compileSource(t, `
terraform {
  cloud = {
    organization: "acme",
    workspaces: { name: "prod" },
  }
}
output nothing = "ok"
`)
	var cloudDocument map[string]any
	if err := json.Unmarshal(cloudResult, &cloudDocument); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	cloud := cloudDocument["terraform"].(map[string]any)["cloud"].(map[string]any)
	if cloud["organization"] != "acme" {
		t.Errorf("cloud organization = %#v", cloud["organization"])
	}
	workspaces := cloud["workspaces"].(map[string]any)
	if workspaces["name"] != "prod" {
		t.Errorf("cloud workspaces = %#v", workspaces)
	}
}

func TestCompileTerraformBackendDiagnostics(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		source   string
		expected string
	}{
		{
			name: "runtime reference",
			source: `
input region: string
terraform { backend s3 = { region: region } }
output nothing = region
`,
			expected: "compile-time expression cannot reference runtime or unknown name",
		},
		{
			name: "second backend",
			source: `
terraform {
  backend s3 = { bucket: "a" },
  backend azurerm = { bucket: "b" }
}
output nothing = "ok"
`,
			expected: "only one terraform backend is allowed",
		},
		{
			name: "duplicate wire key",
			source: `
terraform {
  backend s3 = {
    bucketName: "a",
    "bucket_name": "b",
  }
}
output nothing = "ok"
`,
			expected: "duplicate key",
		},
		{
			name: "spread is not literal field",
			source: `
const extra = { encrypt: true }
terraform { backend s3 = { bucket: "a", ...extra } }
output nothing = "ok"
`,
			expected: "must contain literal fields",
		},
		{
			name: "field form is unsupported",
			source: `
terraform { backend: "s3" }
output nothing = "ok"
`,
			expected: "use the terraform backend clause instead of a field",
		},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			file, parseDiagnostics := syntax.Parse("test.infra", testCase.source)
			diagnostics := append([]syntax.Diagnostic{}, parseDiagnostics...)
			if len(parseDiagnostics) == 0 {
				_, compileDiagnostics := Compile(file)
				diagnostics = compileDiagnostics
			}
			if len(diagnostics) == 0 {
				t.Fatalf("Compile() diagnostics = %v, expected %q", diagnostics, testCase.expected)
			}
			found := false
			for _, diagnostic := range diagnostics {
				if strings.Contains(diagnostic.Message, testCase.expected) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("Compile() diagnostics = %v, expected message containing %q", diagnostics, testCase.expected)
			}
		})
	}
}
