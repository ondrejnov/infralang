package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ondrejnov/infralang/internal/compiler"
	"github.com/ondrejnov/infralang/internal/formatter"
	"github.com/ondrejnov/infralang/internal/syntax"
)

const (
	errParseError     = -32700
	errInvalidRequest = -32600
	errMethodNotFound = -32601
	errInvalidParams  = -32602
	errInternal       = -32603
)

type server struct {
	reader    *framedReader
	writer    *framedWriter
	workspace *workspace
	shutdown  bool
}

func newServer(input io.Reader, output io.Writer) *server {
	return &server{reader: newFramedReader(input), writer: newFramedWriter(output), workspace: newWorkspace()}
}

func (server *server) run() int {
	for {
		payload, err := server.reader.Read()
		if errors.Is(err, io.EOF) {
			return 0
		}
		if err != nil {
			server.writeError(nil, errParseError, err.Error())
			return 1
		}
		var message rpcMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			server.writeError(nil, errParseError, err.Error())
			continue
		}
		if message.JSONRPC != "2.0" || message.Method == "" {
			server.writeError(message.ID, errInvalidRequest, "invalid JSON-RPC request")
			continue
		}
		if message.Method == "exit" {
			if server.shutdown {
				return 0
			}
			return 1
		}
		server.handle(message)
	}
}

func (server *server) handle(message rpcMessage) {
	request := message.ID != nil
	if server.shutdown && message.Method != "shutdown" {
		if request {
			server.writeError(message.ID, errInvalidRequest, "server has shut down")
		}
		return
	}

	switch message.Method {
	case "initialize":
		var params struct {
			RootURI          string `json:"rootUri"`
			RootPath         string `json:"rootPath"`
			WorkspaceFolders []struct {
				URI string `json:"uri"`
			} `json:"workspaceFolders"`
			InitializationOptions struct {
				ProviderSchemas *bool `json:"providerSchemas"`
			} `json:"initializationOptions"`
		}
		if !decodeParams(message.Params, &params) {
			server.writeError(message.ID, errInvalidParams, "invalid initialize parameters")
			return
		}
		var roots []string
		for _, folder := range params.WorkspaceFolders {
			if path, err := uriToPath(folder.URI); err == nil {
				roots = append(roots, path)
			}
		}
		if len(roots) == 0 && params.RootURI != "" {
			if path, err := uriToPath(params.RootURI); err == nil {
				roots = append(roots, path)
			}
		}
		if len(roots) == 0 && params.RootPath != "" {
			roots = append(roots, params.RootPath)
		}
		server.workspace.setRoots(roots)
		if params.InitializationOptions.ProviderSchemas != nil {
			server.workspace.schemas.setEnabled(*params.InitializationOptions.ProviderSchemas)
		}
		if err := server.workspace.scan(); err != nil {
			server.writeError(message.ID, errInternal, err.Error())
			return
		}
		server.writeResult(message.ID, map[string]any{
			"capabilities": map[string]any{
				"positionEncoding":           "utf-16",
				"textDocumentSync":           map[string]any{"openClose": true, "change": 1, "save": map[string]any{"includeText": true}},
				"completionProvider":         map[string]any{"triggerCharacters": []string{".", ":", "{"}},
				"documentFormattingProvider": true,
				"definitionProvider":         true, "hoverProvider": true, "documentSymbolProvider": true,
				"workspaceSymbolProvider": true,
			},
			"serverInfo": map[string]any{"name": "infralang-lsp", "version": "0.1.0"},
		})
	case "initialized", "$/cancelRequest", "setTrace", "$/setTrace":
		return
	case "shutdown":
		server.shutdown = true
		server.writeResult(message.ID, nil)
	case "textDocument/didOpen":
		var params struct {
			TextDocument struct {
				URI     string `json:"uri"`
				Version int    `json:"version"`
				Text    string `json:"text"`
			} `json:"textDocument"`
		}
		if decodeParams(message.Params, &params) {
			if index, err := server.workspace.open(params.TextDocument.URI, params.TextDocument.Text, params.TextDocument.Version); err == nil {
				server.workspace.schemas.ensure(filepath.Dir(index.Path))
				server.publishDirectoryDiagnostics(filepath.Dir(index.Path))
			}
		}
	case "textDocument/didChange":
		var params struct {
			TextDocument   VersionedTextDocumentIdentifier `json:"textDocument"`
			ContentChanges []struct {
				Text string `json:"text"`
			} `json:"contentChanges"`
		}
		if decodeParams(message.Params, &params) && len(params.ContentChanges) > 0 {
			text := params.ContentChanges[len(params.ContentChanges)-1].Text
			if index, err := server.workspace.change(params.TextDocument.URI, text, params.TextDocument.Version); err == nil {
				server.publishDirectoryDiagnostics(filepath.Dir(index.Path))
			}
		}
	case "textDocument/didSave":
		var params struct {
			TextDocument TextDocumentIdentifier `json:"textDocument"`
			Text         *string                `json:"text,omitempty"`
		}
		if decodeParams(message.Params, &params) {
			path, source, ok := server.workspace.source(params.TextDocument.URI)
			if params.Text != nil {
				source, ok = *params.Text, true
				_, _ = server.workspace.change(params.TextDocument.URI, source, 0)
			}
			if ok {
				server.publishDirectoryDiagnostics(filepath.Dir(path))
			}
		}
	case "textDocument/didClose":
		var params struct {
			TextDocument TextDocumentIdentifier `json:"textDocument"`
		}
		if decodeParams(message.Params, &params) {
			path, _ := server.workspace.close(params.TextDocument.URI)
			if server.workspace.file(path) == nil {
				server.notify("textDocument/publishDiagnostics", map[string]any{"uri": params.TextDocument.URI, "diagnostics": []Diagnostic{}})
			} else {
				server.publishDirectoryDiagnostics(filepath.Dir(path))
			}
		}
	case "workspace/didChangeWatchedFiles":
		var params struct {
			Changes []struct {
				URI  string `json:"uri"`
				Type int    `json:"type"`
			} `json:"changes"`
		}
		if !decodeParams(message.Params, &params) {
			return
		}
		directories := make(map[string]bool)
		for _, change := range params.Changes {
			if path, err := uriToPath(change.URI); err == nil {
				directories[filepath.Dir(path)] = true
				if change.Type == 3 {
					server.notify("textDocument/publishDiagnostics", map[string]any{"uri": change.URI, "diagnostics": []Diagnostic{}})
				}
			}
		}
		if err := server.workspace.scan(); err != nil {
			return
		}
		for directory := range directories {
			server.publishDirectoryDiagnostics(directory)
		}
	case "textDocument/completion":
		var params TextDocumentPositionParams
		if !decodeParams(message.Params, &params) {
			server.writeError(message.ID, errInvalidParams, "invalid completion parameters")
			return
		}
		server.writeResult(message.ID, server.completions(params))
	case "textDocument/definition":
		var params TextDocumentPositionParams
		if !decodeParams(message.Params, &params) {
			server.writeError(message.ID, errInvalidParams, "invalid definition parameters")
			return
		}
		server.writeResult(message.ID, server.definition(params))
	case "textDocument/hover":
		var params TextDocumentPositionParams
		if !decodeParams(message.Params, &params) {
			server.writeError(message.ID, errInvalidParams, "invalid hover parameters")
			return
		}
		server.writeResult(message.ID, server.hover(params))
	case "textDocument/documentSymbol":
		var params struct {
			TextDocument TextDocumentIdentifier `json:"textDocument"`
		}
		if !decodeParams(message.Params, &params) {
			server.writeError(message.ID, errInvalidParams, "invalid document symbol parameters")
			return
		}
		server.writeResult(message.ID, server.documentSymbols(params.TextDocument.URI))
	case "textDocument/formatting":
		var params struct {
			TextDocument TextDocumentIdentifier `json:"textDocument"`
		}
		if !decodeParams(message.Params, &params) {
			server.writeError(message.ID, errInvalidParams, "invalid document formatting parameters")
			return
		}
		server.writeResult(message.ID, server.formatDocument(params.TextDocument.URI))
	case "workspace/symbol":
		var params struct {
			Query string `json:"query"`
		}
		if !decodeParams(message.Params, &params) {
			server.writeError(message.ID, errInvalidParams, "invalid workspace symbol parameters")
			return
		}
		server.writeResult(message.ID, server.workspaceSymbols(params.Query))
	default:
		if request {
			server.writeError(message.ID, errMethodNotFound, "method not found: "+message.Method)
		}
	}
}

func (server *server) formatDocument(uri string) []TextEdit {
	path, source, ok := server.workspace.source(uri)
	if !ok {
		return []TextEdit{}
	}
	formatted, diagnostics := formatter.Format(path, source)
	if len(diagnostics) > 0 || string(formatted) == source {
		return []TextEdit{}
	}
	return []TextEdit{{
		Range:   Range{Start: Position{}, End: positionAt(source, len(source))},
		NewText: string(formatted),
	}}
}

func decodeParams(raw json.RawMessage, target any) bool {
	return len(raw) == 0 || json.Unmarshal(raw, target) == nil
}

func (server *server) writeResult(id json.RawMessage, result any) {
	_ = server.writer.Write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (server *server) writeError(id json.RawMessage, code int, message string) {
	if id == nil {
		id = json.RawMessage("null")
	}
	_ = server.writer.Write(map[string]any{"jsonrpc": "2.0", "id": id, "error": &rpcError{Code: code, Message: message}})
}

func (server *server) notify(method string, params any) {
	_ = server.writer.Write(rpcNotification{JSONRPC: "2.0", Method: method, Params: params})
}

func (server *server) visibleSymbols(directory string) map[string]*symbol {
	result := make(map[string]*symbol)
	for _, index := range server.workspace.directoryFiles(directory) {
		for name, item := range index.Top {
			if result[name] == nil {
				result[name] = item
			}
		}
	}
	return result
}

func (server *server) scopedSymbols(path string, offset int) map[string]*symbol {
	result := server.visibleSymbols(filepath.Dir(path))
	index := server.workspace.file(path)
	if index == nil {
		return result
	}
	for _, component := range index.Components {
		if offset < component.Span.Start.Offset || offset > component.Span.End.Offset {
			continue
		}
		for _, item := range index.Symbols {
			if item.Container == component.Name {
				result[item.Name] = item
			}
		}
		break
	}
	return result
}

func (server *server) findComponent(directory, name string) *symbol {
	for _, index := range server.workspace.directoryFiles(directory) {
		if item := index.Components[name]; item != nil {
			return item
		}
	}
	return nil
}

func (server *server) moduleOutputs(directory string, instance *symbol) map[string]*symbol {
	constructor := server.visibleSymbols(directory)[instance.Target]
	if constructor == nil || constructor.Category != "moduleImport" || !strings.HasPrefix(constructor.Target, ".") {
		return nil
	}
	target := filepath.Clean(filepath.Join(directory, constructor.Target))
	result := make(map[string]*symbol)
	for _, index := range server.workspace.directoryFiles(target) {
		for _, item := range index.Top {
			if item.Category == "output" {
				result[item.Name] = item
			}
		}
	}
	return result
}

func (server *server) members(directory string, root *symbol) map[string]*symbol {
	if len(root.Fields) > 0 {
		return root.Fields
	}
	switch root.Category {
	case "componentInstance":
		if component := server.findComponent(directory, root.Target); component != nil {
			return component.Fields
		}
	case "module":
		return server.moduleOutputs(directory, root)
	}
	return nil
}

func (server *server) resolveMemberPath(directory string, visible map[string]*symbol, path []string) *symbol {
	if len(path) == 0 {
		return nil
	}
	current := server.resolveCompletionSymbol(directory, visible[path[0]], make(map[*symbol]bool))
	for _, name := range path[1:] {
		if current == nil {
			return nil
		}
		current = server.resolveCompletionSymbol(directory, current, make(map[*symbol]bool))
		current = server.members(directory, current)[name]
	}
	return current
}

func (server *server) resolveCompletionSymbol(directory string, root *symbol, seen map[*symbol]bool) *symbol {
	for root != nil && root.Expression != nil && len(root.Fields) == 0 {
		if seen[root] {
			return root
		}
		seen[root] = true
		switch expression := root.Expression.(type) {
		case *syntax.IdentifierExpression:
			root = server.visibleSymbols(directory)[expression.Name]
		case *syntax.IndexExpression:
			root = server.resolveCompletionExpression(directory, expression.Target, seen)
		case *syntax.ConditionalExpression:
			root = server.resolveCompletionExpression(directory, expression.Then, seen)
			if root == nil {
				root = server.resolveCompletionExpression(directory, expression.Else, seen)
			}
		default:
			return root
		}
	}
	return root
}

func (server *server) resolveCompletionExpression(directory string, expression syntax.Expression, seen map[*symbol]bool) *symbol {
	switch value := expression.(type) {
	case *syntax.IdentifierExpression:
		return server.resolveCompletionSymbol(directory, server.visibleSymbols(directory)[value.Name], seen)
	case *syntax.IndexExpression:
		return server.resolveCompletionExpression(directory, value.Target, seen)
	case *syntax.ConditionalExpression:
		if root := server.resolveCompletionExpression(directory, value.Then, seen); root != nil {
			return root
		}
		return server.resolveCompletionExpression(directory, value.Else, seen)
	default:
		return nil
	}
}

func (server *server) completions(params TextDocumentPositionParams) []CompletionItem {
	path, source, ok := server.workspace.source(params.TextDocument.URI)
	if !ok {
		return []CompletionItem{}
	}
	directory := filepath.Dir(path)
	offset := offsetAt(source, params.Position)
	visible := server.scopedSymbols(path, offset)
	if memberPath, member := memberPathAt(source, offset); member {
		root := server.resolveMemberPath(directory, visible, memberPath)
		if root == nil {
			return []CompletionItem{}
		}
		root = server.resolveCompletionSymbol(directory, root, make(map[*symbol]bool))
		if root.Category == "providerConfig" {
			return server.providerMethodCompletions(directory, root, declarationKindAt(source, offset))
		}
		if root.Category == "resource" || root.Category == "data" {
			return server.schemaMemberCompletions(directory, root)
		}
		items := make([]CompletionItem, 0)
		for _, item := range server.members(directory, root) {
			items = append(items, CompletionItem{Label: item.Name, Kind: item.Kind, Detail: item.Detail, SortText: "0_" + item.Name})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Label < items[j].Label })
		return items
	}

	items := make(map[string]CompletionItem)
	keyContext := false
	structuralItems, structuralKeyContext := server.structuralArgumentCompletions(path, source, offset)
	keyContext = keyContext || structuralKeyContext
	for _, item := range structuralItems {
		items[item.Label] = item
	}
	if index := server.workspace.file(path); index != nil {
		for _, context := range index.Modules {
			if offset >= context.Span.Start.Offset && offset <= context.Span.End.Offset {
				moduleItems, moduleKeyContext := server.moduleArgumentCompletions(directory, context, source, offset)
				keyContext = keyContext || moduleKeyContext
				for _, moduleItem := range moduleItems {
					items[moduleItem.Label] = moduleItem
				}
			}
		}
		for _, context := range index.Contexts {
			if offset >= context.Span.Start.Offset && offset <= context.Span.End.Offset {
				server.workspace.schemas.ensure(directory)
				schemaItems, schemaKeyContext := server.schemaArgumentCompletions(directory, context, source, offset)
				keyContext = keyContext || schemaKeyContext
				for _, schemaItem := range schemaItems {
					items[schemaItem.Label] = schemaItem
				}
			}
		}
	}
	if keyContext {
		return sortedCompletionItems(items)
	}
	keywords := []string{
		"terraform", "provider", "input", "type", "export", "import", "const", "static", "component", "instantiate",
		"let", "configure", "resource", "data", "module", "output", "moved", "from", "using", "with", "when", "for", "in",
		"true", "false", "null",
	}
	for _, keyword := range keywords {
		items[keyword] = CompletionItem{Label: keyword, Kind: 14, Detail: "InfraLang keyword", SortText: "2_" + keyword}
	}
	for _, typeName := range []string{"string", "number", "bool", "dynamic", "list", "set", "map", "optional", "object"} {
		items[typeName] = CompletionItem{Label: typeName, Kind: symbolKindStruct, Detail: "InfraLang type", SortText: "1_" + typeName}
	}
	for name, item := range visible {
		items[name] = CompletionItem{Label: name, Kind: item.Kind, Detail: item.Detail, SortText: "0_" + name}
	}
	return sortedCompletionItems(items)
}

func sortedCompletionItems(items map[string]CompletionItem) []CompletionItem {
	result := make([]CompletionItem, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].SortText != result[j].SortText {
			return result[i].SortText < result[j].SortText
		}
		return result[i].Label < result[j].Label
	})
	return result
}

func (server *server) providerSource(directory string, config *symbol) string {
	provider := server.visibleSymbols(directory)[config.Target]
	if provider != nil && provider.Category == "provider" {
		return provider.Target
	}
	return ""
}

func (server *server) providerMethodCompletions(directory string, config *symbol, declarationKind string) []CompletionItem {
	source := server.providerSource(directory, config)
	schema := server.workspace.schemas.provider(directory, source)
	if schema == nil {
		return []CompletionItem{}
	}
	localName := providerLocalName(source)
	items := make(map[string]CompletionItem)
	add := func(wire, detail string) {
		kind := strings.TrimPrefix(wire, localName+"_")
		name := wireToSourceName(kind)
		items[name] = CompletionItem{Label: name, Kind: 2, Detail: detail + " " + wire, SortText: "0_" + name}
	}
	if declarationKind != "data" {
		for wire := range schema.Resources {
			add(wire, "Terraform resource")
		}
	}
	if declarationKind != "resource" {
		for wire := range schema.Data {
			add(wire, "Terraform data source")
		}
	}
	result := make([]CompletionItem, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Label < result[j].Label })
	return result
}

func (server *server) schemaArgumentCompletions(directory string, context schemaContext, sourceText string, offset int) ([]CompletionItem, bool) {
	if completionFollowsColon(sourceText, offset) {
		return nil, false
	}
	if context.ResourceMeta {
		selected, keyContext := schemaBlockAt(context.Arguments, resourceMetaSchema(), offset)
		return schemaCompletionItems(selected, true), keyContext
	}
	config := server.visibleSymbols(directory)[context.ProviderConfig]
	if config == nil {
		return nil, false
	}
	source := server.providerSource(directory, config)
	schema := server.workspace.schemas.provider(directory, source)
	if schema == nil {
		return nil, false
	}
	var block *schemaBlock
	if context.Provider {
		block = schema.Provider
	} else {
		wire := syntax.SourceNameToWire(context.TerraformKind)
		localName := providerLocalName(source)
		if !strings.HasPrefix(wire, localName+"_") {
			wire = localName + "_" + wire
		}
		if context.Data {
			block = schema.Data[wire]
		} else {
			block = schema.Resources[wire]
		}
	}
	selected, keyContext := schemaBlockAt(context.Arguments, block, offset)
	return schemaCompletionItems(selected, true), keyContext
}

func schemaBlockAt(object *syntax.ObjectExpression, block *schemaBlock, offset int) (*schemaBlock, bool) {
	if object == nil || block == nil {
		return nil, false
	}
	for i := range object.Fields {
		field := &object.Fields[i]
		if field.Condition != nil && spanContains(field.Condition.GetSpan(), offset) {
			return nil, false
		}
		if !spanContains(field.Value.GetSpan(), offset) {
			continue
		}
		if field.Punned {
			return block, true
		}
		nestedObject, ok := field.Value.(*syntax.ObjectExpression)
		if !ok {
			return nil, false
		}
		for _, schemaField := range block.Fields {
			if schemaField.WireName == field.WireName && schemaField.Block != nil {
				return schemaBlockAt(nestedObject, schemaField.Block, offset)
			}
		}
		return nil, false
	}
	for _, item := range object.Items {
		switch spread := item.(type) {
		case syntax.ObjectSpread:
			if spanContains(spread.GetSpan(), offset) {
				return nil, false
			}
		case syntax.InputsSpread:
			if spanContains(spread.GetSpan(), offset) {
				return nil, false
			}
		}
	}
	return block, true
}

func (server *server) schemaMemberCompletions(directory string, root *symbol) []CompletionItem {
	config := server.visibleSymbols(directory)[root.Provider]
	if config == nil {
		return nil
	}
	source := server.providerSource(directory, config)
	schema := server.workspace.schemas.provider(directory, source)
	if schema == nil {
		return nil
	}
	wire := syntax.SourceNameToWire(root.TerraformKind)
	localName := providerLocalName(source)
	if !strings.HasPrefix(wire, localName+"_") {
		wire = localName + "_" + wire
	}
	if root.Category == "data" {
		return schemaCompletionItems(schema.Data[wire], false)
	}
	return schemaCompletionItems(schema.Resources[wire], false)
}

func (server *server) symbolAt(params TextDocumentPositionParams) (*symbol, string, int, bool) {
	path, source, ok := server.workspace.source(params.TextDocument.URI)
	if !ok {
		return nil, "", 0, false
	}
	offset := offsetAt(source, params.Position)
	name, _, _ := identifierAt(source, offset)
	if name == "" {
		return nil, path, offset, false
	}
	directory := filepath.Dir(path)
	visible := server.scopedSymbols(path, offset)
	if memberPath, member := memberPathAt(source, offset); member {
		if root := server.resolveMemberPath(directory, visible, memberPath); root != nil {
			return server.members(directory, root)[name], path, offset, true
		}
	}
	return visible[name], path, offset, true
}

func (server *server) definition(params TextDocumentPositionParams) []Location {
	item, path, offset, ok := server.symbolAt(params)
	if path != "" {
		if location := server.moduleImportDefinition(path, offset); location != nil {
			return []Location{*location}
		}
	}
	if !ok || item == nil {
		return []Location{}
	}
	if item.Category == "typeImport" {
		targetPath, targetName, found := strings.Cut(item.Target, "#")
		if found {
			if index := server.workspace.file(targetPath); index != nil {
				if target := index.Top[targetName]; target != nil && target.Category == "type" {
					return []Location{target.location()}
				}
			}
		}
	}
	if item.Category == "moduleImport" && strings.HasPrefix(item.Target, ".") {
		target := filepath.Clean(filepath.Join(filepath.Dir(item.Path), item.Target))
		files := server.workspace.directoryFiles(target)
		if len(files) > 0 {
			return []Location{{URI: files[0].URI, Range: Range{}}}
		}
	}
	_ = path
	return []Location{item.location()}
}

func (server *server) moduleImportDefinition(path string, offset int) *Location {
	index := server.workspace.file(path)
	if index == nil || index.File == nil {
		return nil
	}
	for _, declaration := range index.File.Declarations {
		imported, ok := declaration.(*syntax.ModuleImportDeclaration)
		if !ok || !spanContains(imported.GetSpan(), offset) || !strings.HasPrefix(imported.Source, ".") {
			continue
		}
		target := filepath.Clean(filepath.Join(filepath.Dir(path), imported.Source))
		files := server.workspace.directoryFiles(target)
		if len(files) == 0 {
			return nil
		}
		return &Location{URI: files[0].URI, Range: Range{}}
	}
	return nil
}

func (server *server) hover(params TextDocumentPositionParams) any {
	item, _, _, ok := server.symbolAt(params)
	if !ok || item == nil {
		return nil
	}
	value := "**" + item.Name + "**\n\n`" + item.Detail + "`"
	return map[string]any{"contents": map[string]any{"kind": "markdown", "value": value}}
}

func (server *server) documentSymbols(uri string) []map[string]any {
	path, err := uriToPath(uri)
	if err != nil {
		return []map[string]any{}
	}
	index := server.workspace.file(path)
	if index == nil {
		return []map[string]any{}
	}
	result := make([]map[string]any, 0, len(index.Symbols))
	for _, item := range index.Symbols {
		entry := map[string]any{
			"name": item.Name, "kind": item.Kind, "range": item.Range, "selectionRange": item.SelectionRange,
			"detail": item.Detail,
		}
		if item.Container != "" {
			entry["detail"] = item.Detail + " in " + item.Container
		}
		result = append(result, entry)
	}
	return result
}

func (server *server) workspaceSymbols(query string) []map[string]any {
	query = strings.ToLower(query)
	server.workspace.mu.RLock()
	var symbols []*symbol
	for _, index := range server.workspace.files {
		symbols = append(symbols, index.Symbols...)
	}
	server.workspace.mu.RUnlock()
	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].Name != symbols[j].Name {
			return symbols[i].Name < symbols[j].Name
		}
		return symbols[i].Path < symbols[j].Path
	})
	result := make([]map[string]any, 0)
	for _, item := range symbols {
		if query != "" && !strings.Contains(strings.ToLower(item.Name), query) {
			continue
		}
		entry := map[string]any{"name": item.Name, "kind": item.Kind, "location": item.location()}
		if item.Container != "" {
			entry["containerName"] = item.Container
		}
		result = append(result, entry)
	}
	return result
}

func (server *server) publishDirectoryDiagnostics(directory string) {
	files := server.workspace.directoryFiles(directory)
	diagnostics := compileDiagnostics(files, server.workspace)
	for _, index := range files {
		items := diagnostics[index.Path]
		if items == nil {
			items = []Diagnostic{}
		}
		server.notify("textDocument/publishDiagnostics", map[string]any{"uri": index.URI, "diagnostics": items})
	}
}

func compileDiagnostics(files []*fileIndex, workspace *workspace) map[string][]Diagnostic {
	result := make(map[string][]Diagnostic)
	if len(files) == 0 {
		return result
	}
	sources := make(map[string]string, len(files))
	parsedFiles := make(map[string]*syntax.File, len(files))
	var allDiagnostics []syntax.Diagnostic
	for _, index := range files {
		sources[index.Path] = index.Source
		parsed, diagnostics := syntax.Parse(index.Path, index.Source)
		parsedFiles[index.Path] = parsed
		allDiagnostics = append(allDiagnostics, diagnostics...)
	}
	if len(allDiagnostics) == 0 {
		combined := &syntax.File{Name: files[0].Path, ID: syntax.FileID(files[0].Path)}
		addedAliases := make(map[string]bool)
		for _, index := range files {
			for _, declaration := range parsedFiles[index.Path].Declarations {
				imported, ok := declaration.(*syntax.TypeImportDeclaration)
				if !ok {
					combined.Declarations = append(combined.Declarations, declaration)
					continue
				}
				targetPath := filepath.Clean(filepath.Join(filepath.Dir(index.Path), imported.Path))
				target := parsedFiles[targetPath]
				if target == nil {
					if targetIndex := workspace.file(targetPath); targetIndex != nil {
						target, _ = syntax.Parse(targetPath, targetIndex.Source)
					}
				}
				if target == nil {
					combined.Declarations = append(combined.Declarations, declaration)
					continue
				}
				aliases := make(map[string]*syntax.TypeAliasDeclaration)
				for _, targetDeclaration := range target.Declarations {
					if alias, ok := targetDeclaration.(*syntax.TypeAliasDeclaration); ok {
						aliases[alias.Name] = alias
						key := targetPath + "#" + alias.Name
						if !addedAliases[key] && parsedFiles[targetPath] == nil {
							combined.Declarations = append(combined.Declarations, alias)
							addedAliases[key] = true
						}
					}
				}
				for _, importItem := range imported.Items {
					alias := aliases[importItem.ImportedName]
					if alias == nil || importItem.LocalName == alias.Name {
						continue
					}
					clone := *alias
					clone.Name = importItem.LocalName
					combined.Declarations = append(combined.Declarations, &clone)
				}
			}
		}
		_, compilerDiagnostics := compiler.Compile(combined)
		allDiagnostics = append(allDiagnostics, compilerDiagnostics...)
	}
	for _, diagnostic := range syntax.SortDiagnostics(allDiagnostics) {
		path := filepath.Clean(diagnostic.Filename)
		source, ok := sources[path]
		if !ok {
			continue
		}
		result[path] = append(result[path], Diagnostic{
			Range: rangeForSpan(source, diagnostic.Span), Severity: 1, Code: diagnostic.Code, Source: "InfraLang", Message: diagnostic.Message,
		})
	}
	return result
}

func (server *server) String() string {
	return fmt.Sprintf("InfraLang LSP (%d roots)", len(server.workspace.roots))
}
