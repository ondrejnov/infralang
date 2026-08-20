package main

import (
	"os"
	"path/filepath"
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
