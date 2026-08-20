package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ondrejnov/infralang/internal/syntax"
)

const (
	symbolKindFile        = 1
	symbolKindModule      = 2
	symbolKindClass       = 5
	symbolKindProperty    = 7
	symbolKindConstructor = 9
	symbolKindInterface   = 11
	symbolKindVariable    = 13
	symbolKindConstant    = 14
	symbolKindObject      = 19
	symbolKindStruct      = 23
)

type symbol struct {
	Name           string
	Kind           int
	Category       string
	Detail         string
	URI            string
	Path           string
	Range          Range
	SelectionRange Range
	Span           syntax.Span
	Container      string
	Target         string
	Provider       string
	TerraformKind  string
	Fields         map[string]*symbol
	Expression     syntax.Expression
}

func (item *symbol) location() Location {
	return Location{URI: item.URI, Range: item.SelectionRange}
}

type schemaContext struct {
	Span           syntax.Span
	Arguments      *syntax.ObjectExpression
	ProviderConfig string
	TerraformKind  string
	Data           bool
	Provider       bool
	ResourceMeta   bool
}

type moduleContext struct {
	Span        syntax.Span
	Arguments   *syntax.ObjectExpression
	Constructor string
	Label       string
}

type fileIndex struct {
	Path          string
	URI           string
	Source        string
	File          *syntax.File
	ParseErrors   []syntax.Diagnostic
	Symbols       []*symbol
	Top           map[string]*symbol
	Components    map[string]*symbol
	ModuleImports map[string]*symbol
	TypeImports   map[string]*symbol
	Contexts      []schemaContext
	Modules       []moduleContext
}

type document struct {
	URI     string
	Path    string
	Source  string
	Version int
}

type workspace struct {
	mu        sync.RWMutex
	roots     []string
	documents map[string]document
	files     map[string]*fileIndex
	schemas   *schemaManager
}

func newWorkspace() *workspace {
	return &workspace{
		documents: make(map[string]document),
		files:     make(map[string]*fileIndex),
		schemas:   newSchemaManager(),
	}
}

func (workspace *workspace) setRoots(roots []string) {
	seen := make(map[string]bool)
	clean := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" {
			continue
		}
		absolute, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		absolute = filepath.Clean(absolute)
		if !seen[absolute] {
			seen[absolute] = true
			clean = append(clean, absolute)
		}
	}
	sort.Strings(clean)
	workspace.mu.Lock()
	workspace.roots = clean
	workspace.mu.Unlock()
}

func (workspace *workspace) scan() error {
	workspace.mu.RLock()
	roots := append([]string(nil), workspace.roots...)
	overlays := make(map[string]document, len(workspace.documents))
	for path, item := range workspace.documents {
		overlays[path] = item
	}
	workspace.mu.RUnlock()

	indexed := make(map[string]*fileIndex)
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.IsDir() {
				if path != root && shouldSkipDirectory(entry.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(entry.Name()) != ".infra" {
				return nil
			}
			path = filepath.Clean(path)
			if overlay, ok := overlays[path]; ok {
				indexed[path] = buildFileIndex(path, overlay.URI, overlay.Source)
				return nil
			}
			source, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			indexed[path] = buildFileIndex(path, pathToURI(path), string(source))
			return nil
		})
		if err != nil {
			return fmt.Errorf("scan workspace %s: %w", root, err)
		}
	}
	for path, overlay := range overlays {
		indexed[path] = buildFileIndex(path, overlay.URI, overlay.Source)
	}
	workspace.mu.Lock()
	workspace.files = indexed
	workspace.mu.Unlock()
	return nil
}

func shouldSkipDirectory(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "vendor", "node_modules", "vscode-infralang":
		return true
	default:
		return false
	}
}

func (workspace *workspace) open(uri, source string, version int) (*fileIndex, error) {
	path, err := uriToPath(uri)
	if err != nil {
		return nil, err
	}
	doc := document{URI: uri, Path: path, Source: source, Version: version}
	index := buildFileIndex(path, uri, source)
	workspace.mu.Lock()
	workspace.documents[path] = doc
	workspace.files[path] = index
	workspace.mu.Unlock()
	return index, nil
}

func (workspace *workspace) change(uri, source string, version int) (*fileIndex, error) {
	return workspace.open(uri, source, version)
}

func (workspace *workspace) close(uri string) (string, error) {
	path, err := uriToPath(uri)
	if err != nil {
		return "", err
	}
	workspace.mu.Lock()
	delete(workspace.documents, path)
	workspace.mu.Unlock()
	if source, err := os.ReadFile(path); err == nil {
		index := buildFileIndex(path, pathToURI(path), string(source))
		workspace.mu.Lock()
		workspace.files[path] = index
		workspace.mu.Unlock()
	} else {
		workspace.mu.Lock()
		delete(workspace.files, path)
		workspace.mu.Unlock()
	}
	return path, nil
}

func (workspace *workspace) source(uri string) (string, string, bool) {
	path, err := uriToPath(uri)
	if err != nil {
		return "", "", false
	}
	workspace.mu.RLock()
	defer workspace.mu.RUnlock()
	index := workspace.files[path]
	if index == nil {
		return path, "", false
	}
	return path, index.Source, true
}

func (workspace *workspace) file(path string) *fileIndex {
	workspace.mu.RLock()
	defer workspace.mu.RUnlock()
	return workspace.files[filepath.Clean(path)]
}

func (workspace *workspace) directoryFiles(directory string) []*fileIndex {
	workspace.mu.RLock()
	defer workspace.mu.RUnlock()
	var result []*fileIndex
	for path, index := range workspace.files {
		if filepath.Dir(path) == directory {
			result = append(result, index)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}

func buildFileIndex(path, uri, source string) *fileIndex {
	parsed, diagnostics := syntax.Parse(path, source)
	index := &fileIndex{
		Path: path, URI: uri, Source: source, File: parsed, ParseErrors: diagnostics,
		Top: make(map[string]*symbol), Components: make(map[string]*symbol),
		ModuleImports: make(map[string]*symbol), TypeImports: make(map[string]*symbol),
	}
	indexDeclarations(index, parsed.Declarations, "", true)
	return index
}

func indexDeclarations(index *fileIndex, declarations []syntax.Declaration, container string, visible bool) {
	for _, declaration := range declarations {
		var item *symbol
		switch value := declaration.(type) {
		case *syntax.TerraformDeclaration:
			item = newSymbol(index, "terraform", symbolKindObject, "terraform", "Terraform settings", value, container)
		case *syntax.ProviderDeclaration:
			item = newSymbol(index, value.Name, symbolKindInterface, "provider", "provider "+value.Source, value, container)
			item.Target = value.Source
		case *syntax.TypeAliasDeclaration:
			item = newSymbol(index, value.Name, symbolKindStruct, "type", typeDetail(value.Type), value, container)
			item.Fields = fieldsFromType(index, value.Type, item.Name)
		case *syntax.TypeImportDeclaration:
			for i := range value.Items {
				imported := &value.Items[i]
				item := newNodeSymbol(index, imported.LocalName, symbolKindStruct, "typeImport", "imported type "+imported.ImportedName, imported, container)
				item.Target = filepath.Clean(filepath.Join(filepath.Dir(index.Path), value.Path)) + "#" + imported.ImportedName
				index.Symbols = append(index.Symbols, item)
				index.TypeImports[item.Name] = item
				if visible {
					index.Top[item.Name] = item
				}
			}
			continue
		case *syntax.ModuleImportDeclaration:
			item = newSymbol(index, value.Name, symbolKindConstructor, "moduleImport", "module from "+value.Source, value, container)
			item.Target = value.Source
			index.ModuleImports[value.Name] = item
		case *syntax.ConstDeclaration:
			item = newSymbol(index, value.Name, symbolKindConstant, "const", typeDetail(value.Type), value, container)
			item.Fields = fieldsFromExpression(index, value.Value, item.Name)
			item.Expression = value.Value
		case *syntax.StaticForDeclaration:
			loopContainer := "static for " + value.ValueVariable
			if container != "" {
				loopContainer = container + "." + loopContainer
			}
			indexDeclarations(index, value.Declarations, loopContainer, visible)
			continue
		case *syntax.ComponentDefinition:
			item = newSymbol(index, value.Name, symbolKindClass, "component", componentDetail(value), value, container)
			item.Fields = make(map[string]*symbol)
			index.Components[value.Name] = item
			for i := range value.Parameters {
				parameter := &value.Parameters[i]
				child := newNodeSymbol(index, parameter.Name, symbolKindVariable, "parameter", typeDetail(parameter.Type), parameter, value.Name)
				child.Fields = fieldsFromType(index, parameter.Type, parameter.Name)
				index.Symbols = append(index.Symbols, child)
			}
			for i := range value.Providers {
				provider := &value.Providers[i]
				child := newNodeSymbol(index, provider.Name, symbolKindObject, "providerParameter", "provider "+provider.ProviderName, provider, value.Name)
				child.Target = provider.ProviderName
				index.Symbols = append(index.Symbols, child)
			}
			indexDeclarations(index, value.Declarations, value.Name, false)
			for _, child := range index.Symbols {
				if child.Container == value.Name && child.Category == "componentExport" {
					item.Fields[child.Name] = child
				}
			}
		case *syntax.ComponentInstance:
			item = newSymbol(index, value.Name, symbolKindObject, "componentInstance", "instance of "+value.ComponentName, value, container)
			item.Target = value.ComponentName
		case *syntax.ComponentExport:
			item = newSymbol(index, value.Name, symbolKindProperty, "componentExport", "component export", value, container)
			item.Fields = fieldsFromExpression(index, value.Value, item.Name)
		case *syntax.InputDeclaration:
			item = newSymbol(index, value.Name, symbolKindVariable, "input", "input "+typeDetail(value.Type), value, container)
			item.Fields = fieldsFromType(index, value.Type, item.Name)
		case *syntax.LetDeclaration:
			item = newSymbol(index, value.Name, symbolKindVariable, "let", "local value", value, container)
			item.Fields = fieldsFromExpression(index, value.Value, item.Name)
			item.Expression = value.Value
		case *syntax.ConfigureDeclaration:
			item = newSymbol(index, value.Name, symbolKindObject, "providerConfig", "provider configuration "+value.ProviderName, value, container)
			item.Target = value.ProviderName
			if value.Config != nil {
				index.Contexts = append(index.Contexts, schemaContext{Span: value.Config.GetSpan(), Arguments: value.Config, ProviderConfig: value.Name, Provider: true})
			}
		case *syntax.ResourceDeclaration:
			item = newSymbol(index, value.Name, symbolKindObject, "resource", "resource "+value.ProviderConfigName+"."+value.Kind, value, container)
			item.Provider, item.TerraformKind = value.ProviderConfigName, value.Kind
			index.Contexts = append(index.Contexts, schemaContext{Span: value.Arguments.GetSpan(), Arguments: value.Arguments, ProviderConfig: value.ProviderConfigName, TerraformKind: value.Kind})
			if value.With != nil {
				index.Contexts = append(index.Contexts, schemaContext{Span: value.With.GetSpan(), Arguments: value.With, ResourceMeta: true})
			}
		case *syntax.DataDeclaration:
			item = newSymbol(index, value.Name, symbolKindObject, "data", "data "+value.ProviderConfigName+"."+value.Kind, value, container)
			item.Provider, item.TerraformKind = value.ProviderConfigName, value.Kind
			index.Contexts = append(index.Contexts, schemaContext{Span: value.Arguments.GetSpan(), Arguments: value.Arguments, ProviderConfig: value.ProviderConfigName, TerraformKind: value.Kind, Data: true})
		case *syntax.ModuleDeclaration:
			item = newSymbol(index, value.Name, symbolKindModule, "module", "module instance of "+value.ModuleName, value, container)
			item.Target = value.ModuleName
			index.Modules = append(index.Modules, moduleContext{
				Span: value.Arguments.GetSpan(), Arguments: value.Arguments, Constructor: value.ModuleName, Label: value.Label,
			})
		case *syntax.OutputDeclaration:
			item = newSymbol(index, value.Name, symbolKindProperty, "output", "module output", value, container)
			item.Fields = fieldsFromExpression(index, value.Value, item.Name)
		}
		if item == nil {
			continue
		}
		index.Symbols = append(index.Symbols, item)
		if visible && item.Name != "terraform" {
			index.Top[item.Name] = item
		}
	}
}

func newSymbol(index *fileIndex, name string, kind int, category, detail string, declaration syntax.Declaration, container string) *symbol {
	return newNodeSymbol(index, name, kind, category, detail, declaration, container)
}

func newNodeSymbol(index *fileIndex, name string, kind int, category, detail string, node syntax.Node, container string) *symbol {
	span := node.GetSpan()
	fullRange := rangeForSpan(index.Source, span)
	selection := selectionRange(index.Source, span, name)
	return &symbol{
		Name: name, Kind: kind, Category: category, Detail: detail, URI: index.URI, Path: index.Path,
		Range: fullRange, SelectionRange: selection, Span: span, Container: container,
	}
}

func selectionRange(source string, span syntax.Span, name string) Range {
	tokens, _ := syntax.Lex(string(span.File), source)
	for _, token := range tokens {
		if token.Kind == syntax.TokenIdentifier && token.Lexeme == name && token.Span.Start.Offset >= span.Start.Offset && token.Span.End.Offset <= span.End.Offset {
			return rangeForSpan(source, token.Span)
		}
	}
	return rangeForSpan(source, span)
}

func fieldsFromType(index *fileIndex, expression *syntax.TypeExpression, container string) map[string]*symbol {
	if expression == nil || expression.Name != "object" {
		return nil
	}
	result := make(map[string]*symbol)
	for i := range expression.Fields {
		field := &expression.Fields[i]
		if field.Quoted {
			continue
		}
		item := newNodeSymbol(index, field.Name, symbolKindProperty, "field", typeDetail(field.Type), field, container)
		item.Fields = fieldsFromType(index, field.Type, container+"."+field.Name)
		result[field.Name] = item
	}
	return result
}

func fieldsFromExpression(index *fileIndex, expression syntax.Expression, container string) map[string]*symbol {
	object, ok := expression.(*syntax.ObjectExpression)
	if !ok {
		return nil
	}
	result := make(map[string]*symbol)
	for i := range object.Fields {
		field := &object.Fields[i]
		if field.Quoted {
			continue
		}
		item := newNodeSymbol(index, field.Name, symbolKindProperty, "field", "object field", field, container)
		item.Fields = fieldsFromExpression(index, field.Value, container+"."+field.Name)
		result[field.Name] = item
	}
	return result
}

func typeDetail(expression *syntax.TypeExpression) string {
	if expression == nil {
		return "dynamic"
	}
	if len(expression.Arguments) == 0 {
		return expression.Name
	}
	parts := make([]string, 0, len(expression.Arguments))
	for _, argument := range expression.Arguments {
		parts = append(parts, typeDetail(argument))
	}
	return expression.Name + "<" + strings.Join(parts, ", ") + ">"
}

func componentDetail(definition *syntax.ComponentDefinition) string {
	parts := make([]string, 0, len(definition.Parameters))
	for _, parameter := range definition.Parameters {
		parts = append(parts, parameter.Name+": "+typeDetail(parameter.Type))
	}
	return "component " + definition.Name + "(" + strings.Join(parts, ", ") + ")"
}

func uriToPath(uri string) (string, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" {
		return filepath.Abs(uri)
	}
	if parsed.Scheme != "file" {
		return "", fmt.Errorf("unsupported document URI scheme %q", parsed.Scheme)
	}
	path, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil {
		return "", err
	}
	return filepath.Clean(filepath.FromSlash(path)), nil
}

func pathToURI(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}).String()
}
