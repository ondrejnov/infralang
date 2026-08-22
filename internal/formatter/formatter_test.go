package formatter

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatSpacingIndentationCommentsAndDeclarations(t *testing.T) {
	source := `terraform{
requiredVersion:">= 1.5.0",
}
// provider used by the module
provider   AWS from "hashicorp/aws" version "~> 6.0" input region:string="eu-central-1"
resource bucket=aws.s3Bucket("application",{
bucket: f"app-{region}", # stable name
tags:{"Name":region},
})`
	want := `terraform {
  requiredVersion: ">= 1.5.0",
}
// provider used by the module
provider AWS from "hashicorp/aws" version "~> 6.0"
input region: string = "eu-central-1"
resource bucket = aws.s3Bucket("application", {
  bucket: f"app-{region}", # stable name
  tags: { "Name": region },
})
`

	formatted, diagnostics := Format("main.infra", source)
	if len(diagnostics) > 0 {
		t.Fatalf("Format() diagnostics = %v", diagnostics)
	}
	if got := string(formatted); got != want {
		t.Fatalf("Format() =\n%s\nwant:\n%s", got, want)
	}
	formattedAgain, diagnostics := Format("main.infra", string(formatted))
	if len(diagnostics) > 0 {
		t.Fatalf("second Format() diagnostics = %v", diagnostics)
	}
	if got := string(formattedAgain); got != want {
		t.Fatalf("second Format() =\n%s\nwant:\n%s", got, want)
	}
}

func TestFormatPreservesInvalidSource(t *testing.T) {
	formatted, diagnostics := Format("main.infra", "input =")
	if formatted != nil {
		t.Fatalf("Format() = %q, want nil", formatted)
	}
	if len(diagnostics) == 0 {
		t.Fatal("Format() diagnostics are empty")
	}
}

func TestFormatComprehensionsWithoutInnerBracePadding(t *testing.T) {
	source := "output values={for key,value in items:key=>value}\n"
	want := "output values = {for key, value in items: key => value}\n"
	formatted, diagnostics := Format("main.infra", source)
	if len(diagnostics) > 0 {
		t.Fatalf("Format() diagnostics = %v", diagnostics)
	}
	if got := string(formatted); got != want {
		t.Fatalf("Format() = %q, want %q", got, want)
	}
}

func TestFormatKeepsScalarListComprehensionOnOneLine(t *testing.T) {
	source := "output serial_console_enabled_vms = sort([\n  for hostname, vm in vmModule: hostname if vm.serial_console_enabled\n])\n"
	want := "output serial_console_enabled_vms = sort([for hostname, vm in vmModule: hostname if vm.serial_console_enabled])\n"
	formatted, diagnostics := Format("main.infra", source)
	if len(diagnostics) > 0 {
		t.Fatalf("Format() diagnostics = %v", diagnostics)
	}
	if got := string(formatted); got != want {
		t.Fatalf("Format() = %q, want %q", got, want)
	}
}

func TestFormatMultifieldObjectsAcrossLines(t *testing.T) {
	source := "let settings={name:\"api\",port:80,nested:{enabled:true,mode:\"safe\"}}\n"
	want := "let settings = {\n  name: \"api\",\n  port: 80,\n  nested: {\n    enabled: true,\n    mode: \"safe\"\n  }\n}\n"
	formatted, diagnostics := Format("main.infra", source)
	if len(diagnostics) > 0 {
		t.Fatalf("Format() diagnostics = %v", diagnostics)
	}
	if got := string(formatted); got != want {
		t.Fatalf("Format() =\n%s\nwant:\n%s", got, want)
	}
}

func TestFormatKeepsPunnedObjectsOnOneLine(t *testing.T) {
	source := "let settings={name,port,enabled}\n"
	want := "let settings = { name, port, enabled }\n"
	formatted, diagnostics := Format("main.infra", source)
	if len(diagnostics) > 0 {
		t.Fatalf("Format() diagnostics = %v", diagnostics)
	}
	if got := string(formatted); got != want {
		t.Fatalf("Format() = %q, want %q", got, want)
	}
}

func TestFormatConditionalLetAssignments(t *testing.T) {
	source := "let config={stable:true}\nif(enabled){config={...config,password:\"secret\"}}\n"
	want := `let config = { stable: true }
if (enabled) {
  config = {
    ...config,
    password: "secret"
  }
}
`
	formatted, diagnostics := Format("main.infra", source)
	if len(diagnostics) > 0 {
		t.Fatalf("Format() diagnostics = %v", diagnostics)
	}
	if got := string(formatted); got != want {
		t.Fatalf("Format() =\n%s\nwant:\n%s", got, want)
	}
}

func TestFormatAllExamplesIsIdempotentAndParsable(t *testing.T) {
	err := filepath.WalkDir("../../examples", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".infra") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		formatted, diagnostics := Format(path, string(source))
		if len(diagnostics) > 0 {
			t.Errorf("Format(%s) diagnostics = %v", path, diagnostics)
			return nil
		}
		formattedAgain, diagnostics := Format(path, string(formatted))
		if len(diagnostics) > 0 {
			t.Errorf("second Format(%s) diagnostics = %v", path, diagnostics)
			return nil
		}
		if !bytes.Equal(formatted, formattedAgain) {
			t.Errorf("Format(%s) is not idempotent", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
