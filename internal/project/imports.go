package project

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ondrejnov/infralang/internal/syntax"
)

type importResolver struct {
	root             string
	files            map[string]*importFile
	directories      map[string]*importDirectory
	diagnostics      []syntax.Diagnostic
	importCycles     map[string]bool
	typeCycleSymbols map[*importSymbol]bool
	typeStack        []*importSymbol
}

type importFile struct {
	path      string
	directory *importDirectory
	file      *syntax.File
	aliases   map[string]*importSymbol
	names     map[string]syntax.Node
	edges     []*importBinding
}

type importDirectory struct {
	path      string
	files     []*importFile
	symbols   map[string]*importSymbol
	imports   []*importBinding
	bindState uint8
}

type importSymbol struct {
	name        string
	declaration *syntax.TypeAliasDeclaration
	file        *importFile
	target      *importSymbol
	item        *syntax.TypeImportItem
	state       uint8
	expanded    *syntax.TypeExpression
}

type importBinding struct {
	declaration *syntax.TypeImportDeclaration
	item        *syntax.TypeImportItem
	source      *importFile
	target      *importFile
	symbol      *importSymbol
	valid       bool
}

func newImportResolver(root string) *importResolver {
	return &importResolver{
		root: root, files: make(map[string]*importFile), directories: make(map[string]*importDirectory),
		importCycles: make(map[string]bool), typeCycleSymbols: make(map[*importSymbol]bool),
	}
}

func (resolver *importResolver) resolveDirectory(path string) (*syntax.File, error) {
	directory, err := resolver.loadDirectory(path)
	if err != nil {
		return nil, err
	}
	resolver.bindDirectory(directory)
	resolver.detectImportCycles()

	combined := &syntax.File{Name: directory.files[0].file.Name, ID: directory.files[0].file.ID}
	bindings := make(map[*syntax.TypeImportDeclaration][]*importBinding)
	for _, binding := range directory.imports {
		bindings[binding.declaration] = append(bindings[binding.declaration], binding)
	}
	for _, file := range directory.files {
		for _, declaration := range file.file.Declarations {
			importDeclaration, imported := declaration.(*syntax.TypeImportDeclaration)
			if !imported {
				combined.Declarations = append(combined.Declarations, declaration)
				continue
			}
			for _, binding := range bindings[importDeclaration] {
				if !binding.valid {
					continue
				}
				expanded, ok := resolver.expandSymbol(binding.symbol.target)
				if !ok {
					continue
				}
				combined.Declarations = append(combined.Declarations, &syntax.TypeAliasDeclaration{
					BaseNode: binding.item.BaseNode, Name: binding.item.LocalName, Type: cloneImportedType(expanded),
				})
			}
		}
	}
	return combined, nil
}

func (resolver *importResolver) loadDirectory(path string) (*importDirectory, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("canonicalize type directory %s: %w", path, err)
	}
	canonical = filepath.Clean(canonical)
	if directory := resolver.directories[canonical]; directory != nil {
		return directory, nil
	}
	entries, err := os.ReadDir(canonical)
	if err != nil {
		return nil, fmt.Errorf("read type directory %s: %w", canonical, err)
	}
	directory := &importDirectory{path: canonical, symbols: make(map[string]*importSymbol)}
	resolver.directories[canonical] = directory
	seenFiles := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".infra" {
			continue
		}
		path := filepath.Join(canonical, entry.Name())
		canonicalFile, err := filepath.EvalSymlinks(path)
		if err != nil {
			resolver.addPathDiagnostic(path, fmt.Sprintf("cannot canonicalize module .infra file: %v", err))
			continue
		}
		canonicalFile = filepath.Clean(canonicalFile)
		inside, containmentErr := pathWithin(resolver.root, canonicalFile)
		if containmentErr != nil {
			resolver.addPathDiagnostic(path, containmentErr.Error())
			continue
		}
		if !inside {
			resolver.addPathDiagnostic(path, "module .infra file resolves outside project root")
			continue
		}
		if filepath.Ext(canonicalFile) != ".infra" {
			resolver.addPathDiagnostic(path, "module .infra symlink resolves to a non-.infra file")
			continue
		}
		info, statErr := os.Stat(canonicalFile)
		if statErr != nil || !info.Mode().IsRegular() {
			resolver.addPathDiagnostic(path, "module .infra entry must resolve to a regular file")
			continue
		}
		if seenFiles[canonicalFile] {
			continue
		}
		seenFiles[canonicalFile] = true
		file, err := resolver.loadFile(canonicalFile, directory)
		if err != nil {
			return nil, err
		}
		directory.files = append(directory.files, file)
	}
	if len(directory.files) == 0 {
		return nil, fmt.Errorf("module directory %s contains no .infra files", canonical)
	}
	sort.Slice(directory.files, func(i, j int) bool { return directory.files[i].path < directory.files[j].path })
	for _, file := range directory.files {
		for _, declaration := range file.file.Declarations {
			alias, ok := declaration.(*syntax.TypeAliasDeclaration)
			if !ok {
				continue
			}
			symbol := &importSymbol{name: alias.Name, declaration: alias, file: file}
			if isProjectBuiltinType(alias.Name) {
				resolver.addDiagnostic(alias, fmt.Sprintf("type alias %q cannot shadow a builtin type", alias.Name))
				continue
			}
			if previous := directory.symbols[alias.Name]; previous != nil {
				message := fmt.Sprintf("type alias %q conflicts with another alias", alias.Name)
				resolver.addDiagnostic(previous.declaration, message)
				resolver.addDiagnostic(alias, message)
				continue
			}
			file.aliases[alias.Name] = symbol
			directory.symbols[alias.Name] = symbol
		}
	}
	return directory, nil
}

func (resolver *importResolver) loadFile(path string, directory *importDirectory) (*importFile, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("canonicalize type file %s: %w", path, err)
	}
	canonical = filepath.Clean(canonical)
	if file := resolver.files[canonical]; file != nil {
		return file, nil
	}
	source, err := os.ReadFile(canonical)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", canonical, err)
	}
	parsed, diagnostics := syntax.Parse(canonical, string(source))
	resolver.diagnostics = append(resolver.diagnostics, diagnostics...)
	file := &importFile{
		path: canonical, directory: directory, file: parsed,
		aliases: make(map[string]*importSymbol), names: make(map[string]syntax.Node),
	}
	for _, declaration := range parsed.Declarations {
		if name := importableDeclarationName(declaration); name != "" && file.names[name] == nil {
			file.names[name] = declaration
		}
	}
	resolver.files[canonical] = file
	return file, nil
}

func (resolver *importResolver) bindDirectory(directory *importDirectory) {
	if directory.bindState != 0 {
		return
	}
	directory.bindState = 1
	for _, file := range directory.files {
		for _, declaration := range file.file.Declarations {
			importDeclaration, ok := declaration.(*syntax.TypeImportDeclaration)
			if !ok {
				continue
			}
			targetPath, valid := resolver.resolveImportPath(file, importDeclaration)
			if !valid {
				continue
			}
			targetDirectory, err := resolver.loadDirectory(filepath.Dir(targetPath))
			if err != nil {
				resolver.addDiagnostic(importDeclaration, err.Error())
				continue
			}
			targetFile := resolver.files[targetPath]
			if targetFile == nil {
				resolver.addDiagnostic(importDeclaration, fmt.Sprintf("type import target %q is not an immediate .infra file", importDeclaration.Path))
				continue
			}
			resolver.bindDirectory(targetDirectory)
			seenInDeclaration := make(map[string]bool)
			for index := range importDeclaration.Items {
				item := &importDeclaration.Items[index]
				binding := &importBinding{declaration: importDeclaration, item: item, source: file, target: targetFile}
				file.edges = append(file.edges, binding)
				directory.imports = append(directory.imports, binding)
				if seenInDeclaration[item.LocalName] {
					continue
				}
				seenInDeclaration[item.LocalName] = true
				target := targetFile.aliases[item.ImportedName]
				switch {
				case target == nil && targetFile.names[item.ImportedName] != nil:
					resolver.addDiagnostic(item, fmt.Sprintf("import type accepts exported type aliases only; %q is not a type alias", item.ImportedName))
					continue
				case target == nil:
					resolver.addDiagnostic(item, fmt.Sprintf("unknown exported type %q in %q", item.ImportedName, importDeclaration.Path))
					continue
				case !target.declaration.Exported:
					resolver.addDiagnostic(item, fmt.Sprintf("type alias %q in %q is private", item.ImportedName, importDeclaration.Path))
					continue
				}
				if previous := directory.symbols[item.LocalName]; previous != nil {
					message := fmt.Sprintf("imported local type name %q conflicts with another type", item.LocalName)
					resolver.addDiagnostic(importSymbolNode(previous), message)
					resolver.addDiagnostic(item, message)
					continue
				}
				symbol := &importSymbol{name: item.LocalName, file: file, target: target, item: item}
				binding.symbol = symbol
				binding.valid = true
				directory.symbols[item.LocalName] = symbol
			}
		}
	}
	directory.bindState = 2
}

func (resolver *importResolver) resolveImportPath(file *importFile, declaration *syntax.TypeImportDeclaration) (string, bool) {
	path := declaration.Path
	if filepath.IsAbs(path) {
		resolver.addDiagnostic(declaration, "type import path must be relative")
		return "", false
	}
	if filepath.Ext(path) != ".infra" {
		resolver.addDiagnostic(declaration, "type import path must target a .infra file")
		return "", false
	}
	target := filepath.Join(filepath.Dir(file.path), filepath.FromSlash(path))
	canonical, err := filepath.EvalSymlinks(target)
	if err != nil {
		resolver.addDiagnostic(declaration, fmt.Sprintf("cannot resolve type import %q: %v", path, err))
		return "", false
	}
	canonical = filepath.Clean(canonical)
	inside, err := pathWithin(resolver.root, canonical)
	if err != nil {
		resolver.addDiagnostic(declaration, err.Error())
		return "", false
	}
	if !inside {
		resolver.addDiagnostic(declaration, fmt.Sprintf("type import %q resolves outside project root", path))
		return "", false
	}
	if filepath.Ext(canonical) != ".infra" {
		resolver.addDiagnostic(declaration, fmt.Sprintf("type import %q resolves to a non-.infra file", path))
		return "", false
	}
	info, err := os.Stat(canonical)
	if err != nil {
		resolver.addDiagnostic(declaration, fmt.Sprintf("cannot inspect type import %q: %v", path, err))
		return "", false
	}
	if !info.Mode().IsRegular() {
		resolver.addDiagnostic(declaration, fmt.Sprintf("type import %q must target a regular .infra file", path))
		return "", false
	}
	return canonical, true
}

func (resolver *importResolver) detectImportCycles() {
	paths := make([]string, 0, len(resolver.files))
	for path := range resolver.files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	states := make(map[string]uint8, len(paths))
	var stack []*importFile
	var visit func(*importFile)
	visit = func(file *importFile) {
		if states[file.path] == 2 {
			return
		}
		states[file.path] = 1
		stack = append(stack, file)
		for _, edge := range file.edges {
			if edge.target == nil {
				continue
			}
			if states[edge.target.path] == 1 {
				start := 0
				for index, candidate := range stack {
					if candidate == edge.target {
						start = index
						break
					}
				}
				parts := make([]string, 0, len(stack)-start+1)
				for _, candidate := range stack[start:] {
					parts = append(parts, resolver.relativePath(candidate.path))
				}
				parts = append(parts, resolver.relativePath(edge.target.path))
				cycle := strings.Join(parts, " -> ")
				if !resolver.importCycles[cycle] {
					resolver.importCycles[cycle] = true
					resolver.addDiagnostic(edge.declaration, "type import cycle: "+cycle)
				}
				continue
			}
			if states[edge.target.path] == 0 {
				visit(edge.target)
			}
		}
		stack = stack[:len(stack)-1]
		states[file.path] = 2
	}
	for _, path := range paths {
		if states[path] == 0 {
			visit(resolver.files[path])
		}
	}
}

func (resolver *importResolver) expandSymbol(symbol *importSymbol) (*syntax.TypeExpression, bool) {
	if symbol == nil {
		return nil, false
	}
	if symbol.target != nil {
		return resolver.expandSymbol(symbol.target)
	}
	switch symbol.state {
	case 1:
		start := 0
		for index, candidate := range resolver.typeStack {
			if candidate == symbol {
				start = index
				break
			}
		}
		for _, candidate := range resolver.typeStack[start:] {
			if resolver.typeCycleSymbols[candidate] {
				continue
			}
			resolver.typeCycleSymbols[candidate] = true
			resolver.addDiagnostic(candidate.declaration, fmt.Sprintf("type alias %q is part of an expanded type cycle", candidate.name))
		}
		return nil, false
	case 2:
		return symbol.expanded, symbol.expanded != nil
	}
	symbol.state = 1
	resolver.typeStack = append(resolver.typeStack, symbol)
	expanded, ok := resolver.expandType(symbol.declaration.Type, symbol.file.directory)
	resolver.typeStack = resolver.typeStack[:len(resolver.typeStack)-1]
	symbol.state = 2
	if ok {
		symbol.expanded = expanded
	}
	return symbol.expanded, ok
}

func (resolver *importResolver) expandType(expression *syntax.TypeExpression, directory *importDirectory) (*syntax.TypeExpression, bool) {
	if expression == nil {
		return nil, true
	}
	if !isProjectBuiltinType(expression.Name) {
		symbol := directory.symbols[expression.Name]
		if symbol == nil {
			resolver.addDiagnostic(expression, fmt.Sprintf("unknown type %q while expanding exported type", expression.Name))
			return nil, false
		}
		return resolver.expandSymbol(symbol)
	}
	result := &syntax.TypeExpression{BaseNode: expression.BaseNode, Name: expression.Name}
	valid := true
	for _, argument := range expression.Arguments {
		expanded, ok := resolver.expandType(argument, directory)
		valid = valid && ok
		if expanded != nil {
			result.Arguments = append(result.Arguments, expanded)
		}
	}
	for _, field := range expression.Fields {
		expanded, ok := resolver.expandType(field.Type, directory)
		valid = valid && ok
		field.Type = expanded
		result.Fields = append(result.Fields, field)
	}
	return result, valid
}

func (resolver *importResolver) relativePath(path string) string {
	relative, err := filepath.Rel(resolver.root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(relative)
}

func (resolver *importResolver) addDiagnostic(node syntax.Node, message string) {
	resolver.diagnostics = append(resolver.diagnostics, syntax.NewDiagnostic(node.GetFile(), node.GetSpan(), "PROJECT_ERROR", message))
}

func (resolver *importResolver) addPathDiagnostic(path, message string) {
	resolver.diagnostics = append(resolver.diagnostics, syntax.NewDiagnostic(syntax.FileID(path), syntax.Span{File: syntax.FileID(path)}, "PROJECT_ERROR", message))
}

func importSymbolNode(symbol *importSymbol) syntax.Node {
	if symbol.item != nil {
		return symbol.item
	}
	return symbol.declaration
}

func importableDeclarationName(declaration syntax.Declaration) string {
	switch value := declaration.(type) {
	case *syntax.ProviderDeclaration:
		return value.Name
	case *syntax.TypeAliasDeclaration:
		return value.Name
	case *syntax.ConstDeclaration:
		return value.Name
	case *syntax.InputDeclaration:
		return value.Name
	case *syntax.LetDeclaration:
		return value.Name
	case *syntax.ConfigureDeclaration:
		return value.Name
	case *syntax.ResourceDeclaration:
		return value.Name
	case *syntax.DataDeclaration:
		return value.Name
	case *syntax.ModuleDeclaration:
		return value.Name
	case *syntax.ModuleImportDeclaration:
		return value.Name
	case *syntax.OutputDeclaration:
		return value.Name
	case *syntax.ComponentDefinition:
		return value.Name
	case *syntax.ComponentInstance:
		return value.Name
	default:
		return ""
	}
}

func isProjectBuiltinType(name string) bool {
	switch name {
	case "string", "number", "bool", "dynamic", "any", "object", "list", "set", "map", "optional":
		return true
	default:
		return false
	}
}

func cloneImportedType(expression *syntax.TypeExpression) *syntax.TypeExpression {
	if expression == nil {
		return nil
	}
	result := &syntax.TypeExpression{BaseNode: expression.BaseNode, Name: expression.Name}
	for _, argument := range expression.Arguments {
		result.Arguments = append(result.Arguments, cloneImportedType(argument))
	}
	for _, field := range expression.Fields {
		field.Type = cloneImportedType(field.Type)
		result.Fields = append(result.Fields, field)
	}
	return result
}
