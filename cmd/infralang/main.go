package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ondrejnov/infralang/internal/compiler"
	"github.com/ondrejnov/infralang/internal/syntax"
)

const version = "0.1.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
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

func runBuild(arguments []string) error {
	flags := flag.NewFlagSet("build", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	output := flags.String("o", "", "output Terraform JSON path")
	stdout := flags.Bool("stdout", false, "write Terraform JSON to stdout")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("build expects exactly one .infra source file")
	}
	if *stdout && *output != "" {
		return errors.New("-o and -stdout cannot be used together")
	}

	sourcePath := flags.Arg(0)
	result, diagnostics, err := compileFile(sourcePath)
	if err != nil {
		return err
	}
	if len(diagnostics) > 0 {
		printDiagnostics(os.Stderr, sourcePath, diagnostics)
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
		return errors.New("check expects exactly one .infra source file")
	}

	sourcePath := flags.Arg(0)
	_, diagnostics, err := compileFile(sourcePath)
	if err != nil {
		return err
	}
	if len(diagnostics) > 0 {
		printDiagnostics(os.Stderr, sourcePath, diagnostics)
		return fmt.Errorf("check failed with %d error(s)", len(diagnostics))
	}
	fmt.Printf("%s: ok\n", sourcePath)
	return nil
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

func printDiagnostics(output *os.File, sourcePath string, diagnostics []syntax.Diagnostic) {
	lines := sourceLines(sourcePath)
	for _, diagnostic := range diagnostics {
		fmt.Fprintf(output, "%s:%d:%d: error: %s\n", diagnostic.Filename, diagnostic.Span.Start.Line, diagnostic.Span.Start.Column, diagnostic.Message)
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
	fmt.Fprintln(output, "  infralang build [-o FILE | -stdout] SOURCE.infra")
	fmt.Fprintln(output, "  infralang check SOURCE.infra")
	fmt.Fprintln(output, "  infralang version")
}
