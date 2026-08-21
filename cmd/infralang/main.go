package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ondrejnov/infralang/internal/compiler"
	"github.com/ondrejnov/infralang/internal/project"
	"github.com/ondrejnov/infralang/internal/syntax"
)

const version = "0.1.0"

var terraformBinary = "terraform"

type buildArtifact struct {
	directory string
	result    []byte
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			os.Exit(exitError.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "infralang: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		printUsage(os.Stderr)
		return errors.New("missing command")
	}

	switch arguments[0] {
	case "build":
		return runBuild(arguments[1:])
	case "check":
		return runCheck(arguments[1:])
	case "init", "apply", "validate", "plan", "destroy":
		return runTerraform(arguments[0], arguments[1:])
	case "version", "--version", "-version":
		fmt.Printf("InfraLang %s\n", version)
		return nil
	case "help", "--help", "-h":
		printUsage(os.Stdout)
		return nil
	default:
		printUsage(os.Stderr)
		return fmt.Errorf("unknown command %q", arguments[0])
	}
}

func runTerraform(terraformCommand string, arguments []string) error {
	workingDirectory, err := buildForTerraform(".")
	if err != nil {
		return err
	}

	terraformArguments := append([]string{terraformCommand}, arguments...)
	command := exec.Command(terraformBinary, terraformArguments...)
	command.Dir = workingDirectory
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func buildForTerraform(sourcePath string) (string, error) {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", sourcePath, err)
	}

	if info.IsDir() {
		artifacts, diagnostics, err := compileProject(sourcePath)
		if err != nil {
			return "", err
		}
		if len(diagnostics) > 0 {
			printDiagnostics(os.Stderr, diagnostics)
			return "", fmt.Errorf("compilation failed with %d error(s)", len(diagnostics))
		}
		if err := writeBuildArtifacts(artifacts); err != nil {
			return "", err
		}
		return sourcePath, nil
	}

	if filepath.Ext(sourcePath) != ".infra" {
		return "", fmt.Errorf("source file %q must use the .infra extension", sourcePath)
	}
	module, err := hasSiblingInfraFiles(sourcePath)
	if err != nil {
		return "", err
	}
	if module {
		root := filepath.Dir(sourcePath)
		artifacts, diagnostics, err := compileProject(root)
		if err != nil {
			return "", err
		}
		if len(diagnostics) > 0 {
			printDiagnostics(os.Stderr, diagnostics)
			return "", fmt.Errorf("compilation failed with %d error(s)", len(diagnostics))
		}
		if err := writeBuildArtifacts(artifacts); err != nil {
			return "", err
		}
		return root, nil
	}

	result, diagnostics, err := compileFile(sourcePath)
	if err != nil {
		return "", err
	}
	if len(diagnostics) > 0 {
		printDiagnostics(os.Stderr, diagnostics)
		return "", fmt.Errorf("compilation failed with %d error(s)", len(diagnostics))
	}
	outputPath := strings.TrimSuffix(sourcePath, filepath.Ext(sourcePath)) + ".tf.json"
	if err := writeAtomically(outputPath, result); err != nil {
		return "", err
	}
	return filepath.Dir(sourcePath), nil
}

func writeBuildArtifacts(artifacts []buildArtifact) error {
	for _, artifact := range artifacts {
		outputPath := filepath.Join(artifact.directory, "main.tf.json")
		if err := writeAtomically(outputPath, artifact.result); err != nil {
			return err
		}
	}
	return nil
}

func runBuild(arguments []string) error {
	flags := flag.NewFlagSet("build", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	output := flags.String("o", "", "output Terraform JSON path")
	stdout := flags.Bool("stdout", false, "write Terraform JSON to stdout")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("build expects exactly one .infra source file or module directory")
	}
	if *stdout && *output != "" {
		return errors.New("-o and -stdout cannot be used together")
	}

	sourcePath := flags.Arg(0)
	info, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", sourcePath, err)
	}
	if info.IsDir() {
		if *stdout || *output != "" {
			return errors.New("-o and -stdout are only supported for single-file builds")
		}
		artifacts, diagnostics, err := compileProject(sourcePath)
		if err != nil {
			return err
		}
		if len(diagnostics) > 0 {
			printDiagnostics(os.Stderr, diagnostics)
			return fmt.Errorf("compilation failed with %d error(s)", len(diagnostics))
		}
		if err := writeBuildArtifacts(artifacts); err != nil {
			return err
		}
		for _, artifact := range artifacts {
			outputPath := filepath.Join(artifact.directory, "main.tf.json")
			fmt.Printf("wrote %s\n", outputPath)
		}
		return nil
	}
	result, diagnostics, err := compileFile(sourcePath)
	if err != nil {
		return err
	}
	if len(diagnostics) > 0 {
		printDiagnostics(os.Stderr, diagnostics)
		return fmt.Errorf("compilation failed with %d error(s)", len(diagnostics))
	}
	if *stdout {
		_, err = os.Stdout.Write(result)
		return err
	}

	outputPath := *output
	if outputPath == "" {
		extension := filepath.Ext(sourcePath)
		outputPath = strings.TrimSuffix(sourcePath, extension) + ".tf.json"
	}
	if err := writeAtomically(outputPath, result); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", outputPath)
	return nil
}

func runCheck(arguments []string) error {
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("check expects exactly one .infra source file or module directory")
	}

	sourcePath := flags.Arg(0)
	info, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", sourcePath, err)
	}
	if info.IsDir() {
		_, diagnostics, err := compileProject(sourcePath)
		if err != nil {
			return err
		}
		if len(diagnostics) > 0 {
			printDiagnostics(os.Stderr, diagnostics)
			return fmt.Errorf("check failed with %d error(s)", len(diagnostics))
		}
		fmt.Printf("%s: ok\n", sourcePath)
		return nil
	}
	module, err := hasSiblingInfraFiles(sourcePath)
	if err != nil {
		return err
	}
	if module {
		_, diagnostics, err := compileProject(filepath.Dir(sourcePath))
		if err != nil {
			return err
		}
		if len(diagnostics) > 0 {
			printDiagnostics(os.Stderr, diagnostics)
			return fmt.Errorf("check failed with %d error(s)", len(diagnostics))
		}
		fmt.Printf("%s: ok\n", sourcePath)
		return nil
	}
	_, diagnostics, err := compileFile(sourcePath)
	if err != nil {
		return err
	}
	if len(diagnostics) > 0 {
		printDiagnostics(os.Stderr, diagnostics)
		return fmt.Errorf("check failed with %d error(s)", len(diagnostics))
	}
	fmt.Printf("%s: ok\n", sourcePath)
	return nil
}

func compileProject(root string) ([]buildArtifact, []syntax.Diagnostic, error) {
	schemas, _ := loadProviderSchemas(root)
	result, err := project.CompileWithOptions(root, compiler.CompileOptions{ProviderSchemas: schemas})
	if err != nil {
		return nil, nil, err
	}
	artifacts := make([]buildArtifact, 0, len(result.Artifacts))
	for _, artifact := range result.Artifacts {
		artifacts = append(artifacts, buildArtifact{directory: artifact.Directory, result: artifact.Data})
	}
	return artifacts, result.Diagnostics, nil
}

func compileFile(sourcePath string) ([]byte, []syntax.Diagnostic, error) {
	if filepath.Ext(sourcePath) != ".infra" {
		return nil, nil, fmt.Errorf("source file %q must use the .infra extension", sourcePath)
	}
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", sourcePath, err)
	}
	file, diagnostics := syntax.Parse(sourcePath, string(source))
	if len(diagnostics) > 0 {
		return nil, compiler.SortedDiagnostics(diagnostics), nil
	}
	result, diagnostics := compiler.Compile(file)
	return result, compiler.SortedDiagnostics(diagnostics), nil
}

func hasSiblingInfraFiles(sourcePath string) (bool, error) {
	entries, err := os.ReadDir(filepath.Dir(sourcePath))
	if err != nil {
		return false, fmt.Errorf("inspect module directory %s: %w", filepath.Dir(sourcePath), err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".infra" {
			continue
		}
		count++
		if count > 1 {
			return true, nil
		}
	}
	return false, nil
}

func writeAtomically(outputPath string, data []byte) error {
	directory := filepath.Dir(outputPath)
	temporary, err := os.CreateTemp(directory, ".infralang-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary output: %w", err)
	}
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return fmt.Errorf("set output permissions: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary output: %w", err)
	}
	if err := os.Rename(temporaryPath, outputPath); err != nil {
		return fmt.Errorf("replace %s: %w", outputPath, err)
	}
	return nil
}

func printDiagnostics(output *os.File, diagnostics []syntax.Diagnostic) {
	for _, diagnostic := range diagnostics {
		fmt.Fprintf(output, "%s:%d:%d: error: %s\n", diagnostic.Filename, diagnostic.Span.Start.Line, diagnostic.Span.Start.Column, diagnostic.Message)
		lines := sourceLines(diagnostic.Filename)
		lineIndex := diagnostic.Span.Start.Line - 1
		if lineIndex < 0 || lineIndex >= len(lines) {
			continue
		}
		line := lines[lineIndex]
		fmt.Fprintf(output, "  %s\n", line)
		column := diagnostic.Span.Start.Column
		if column < 1 {
			column = 1
		}
		fmt.Fprintf(output, "  %s^\n", strings.Repeat(" ", column-1))
	}
}

func sourceLines(sourcePath string) []string {
	file, err := os.Open(sourcePath)
	if err != nil {
		return nil
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}

func printUsage(output *os.File) {
	fmt.Fprintln(output, "InfraLang compiles programmer-oriented infrastructure code to Terraform JSON.")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Usage:")
	fmt.Fprintln(output, "  infralang build [-o FILE | -stdout] SOURCE.infra|MODULE_DIR")
	fmt.Fprintln(output, "  infralang check SOURCE.infra|MODULE_DIR")
	fmt.Fprintln(output, "  infralang init|validate|plan|apply|destroy [TERRAFORM_ARGS...]")
	fmt.Fprintln(output, "  infralang version")
}
