package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompileProjectIncludesLocalInfraModules(t *testing.T) {
	directory := t.TempDir()
	child := filepath.Join(directory, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "main.infra"), []byte("import module Child from \"./child\"\nmodule child = Child(\"child\", {})"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "main.infra"), []byte(`input name: string = "child"`), 0o600); err != nil {
		t.Fatal(err)
	}

	artifacts, diagnostics, err := compileProject(directory)
	if err != nil {
		t.Fatalf("compileProject() error = %v", err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("compileProject() diagnostics = %v", diagnostics)
	}
	if len(artifacts) != 2 {
		t.Fatalf("compileProject() artifacts = %d, want 2", len(artifacts))
	}
}

func TestCompileProjectDelegatesLocalValidationAndReturnsNoPartialArtifacts(t *testing.T) {
	directory := t.TempDir()
	child := filepath.Join(directory, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "main.infra"), []byte("import module Child from \"./child\"\nmodule child = Child(\"child\", { unknown: true })"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "main.infra"), []byte(`input required: string`), 0o600); err != nil {
		t.Fatal(err)
	}

	artifacts, diagnostics, err := compileProject(directory)
	if err != nil {
		t.Fatalf("compileProject() error = %v", err)
	}
	if len(artifacts) != 0 {
		t.Fatalf("compileProject() artifacts = %#v, want none", artifacts)
	}
	if len(diagnostics) != 2 {
		t.Fatalf("compileProject() diagnostics = %v, want unknown and missing input", diagnostics)
	}
}

func TestRunCheckSourceFileIncludesSiblingInfraFiles(t *testing.T) {
	directory := t.TempDir()
	mainPath := filepath.Join(directory, "main.infra")
	if err := os.WriteFile(mainPath, []byte("output value = ssh"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "ssh.infra"), []byte(`input ssh: string = "ok"`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runCheck([]string{mainPath}); err != nil {
		t.Fatalf("runCheck() error = %v", err)
	}
}

func TestRunTerraformBuildsAndPassesArguments(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	sourcePath := filepath.Join(directory, "main.infra")
	if err := os.WriteFile(sourcePath, []byte(`input value: string = "ok"`), 0o600); err != nil {
		t.Fatal(err)
	}

	argumentsPath := filepath.Join(directory, "arguments")
	workingDirectoryPath := filepath.Join(directory, "working-directory")
	terraformPath := filepath.Join(directory, "terraform")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argumentsPath + "\"\npwd > \"" + workingDirectoryPath + "\"\n"
	if err := os.WriteFile(terraformPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	previousTerraformBinary := terraformBinary
	terraformBinary = terraformPath
	t.Cleanup(func() { terraformBinary = previousTerraformBinary })

	if err := runTerraform("plan", []string{"-input=false", "-out=plan.tfplan"}); err != nil {
		t.Fatalf("runTerraform() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(directory, "main.tf.json")); err != nil {
		t.Fatalf("generated Terraform JSON: %v", err)
	}
	arguments, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(arguments), "plan\n-input=false\n-out=plan.tfplan\n"; got != want {
		t.Fatalf("Terraform arguments = %q, want %q", got, want)
	}
	workingDirectory, err := os.ReadFile(workingDirectoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(workingDirectory)); got != directory {
		t.Fatalf("Terraform working directory = %q, want %q", got, directory)
	}
}

func TestRunTerraformDoesNotRunAfterCompilationFailure(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	sourcePath := filepath.Join(directory, "main.infra")
	if err := os.WriteFile(sourcePath, []byte(`input =`), 0o600); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(directory, "ran")
	terraformPath := filepath.Join(directory, "terraform")
	script := "#!/bin/sh\nif [ \"$1\" = validate ]; then touch \"" + markerPath + "\"; fi\n"
	if err := os.WriteFile(terraformPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	previousTerraformBinary := terraformBinary
	terraformBinary = terraformPath
	t.Cleanup(func() { terraformBinary = previousTerraformBinary })

	if err := runTerraform("validate", nil); err == nil {
		t.Fatal("runTerraform() error = nil, want compilation error")
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("Terraform marker stat error = %v, want marker to be absent", err)
	}
}
