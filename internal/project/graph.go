package project

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ondrejnov/infralang/internal/compiler"
	"github.com/ondrejnov/infralang/internal/syntax"
)

type moduleNode struct {
	id        string
	directory string
	file      *syntax.File
	edges     map[string]*moduleNode
	state     uint8
}

type projectGraph struct {
	root        string
	rootNode    *moduleNode
	nodes       map[string]*moduleNode
	diagnostics []syntax.Diagnostic
	stack       []*moduleNode
	imports     *importResolver
}

func discoverGraph(root string) (*projectGraph, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve project root %s: %w", root, err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("canonicalize project root %s: %w", absolute, err)
	}
	graph := &projectGraph{root: filepath.Clean(canonical), nodes: make(map[string]*moduleNode)}
	graph.imports = newImportResolver(graph.root)
	rootNode, err := graph.loadModule(graph.root)
	if err != nil {
		return nil, err
	}
	graph.rootNode = rootNode
	if err := graph.discover(rootNode); err != nil {
		return nil, err
	}
	graph.diagnostics = append(graph.diagnostics, graph.imports.diagnostics...)
	graph.diagnostics = syntax.SortDiagnostics(graph.diagnostics)
	return graph, nil
}

func (graph *projectGraph) loadModule(directory string) (*moduleNode, error) {
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, fmt.Errorf("canonicalize local module %s: %w", directory, err)
	}
	canonical = filepath.Clean(canonical)
	if node := graph.nodes[canonical]; node != nil {
		return node, nil
	}
	combined, err := graph.imports.resolveDirectory(canonical)
	if err != nil {
		return nil, err
	}
	prepared, diagnostics := compiler.Prepare(combined)
	graph.diagnostics = append(graph.diagnostics, diagnostics...)
	combined = prepared
	relative, err := filepath.Rel(graph.root, canonical)
	if err != nil {
		return nil, fmt.Errorf("identify module %s: %w", canonical, err)
	}
	id := filepath.ToSlash(relative)
	if id == "" {
		id = "."
	}
	node := &moduleNode{id: id, directory: canonical, file: combined, edges: make(map[string]*moduleNode)}
	graph.nodes[canonical] = node
	return node, nil
}

func (graph *projectGraph) discover(node *moduleNode) error {
	if node.state == 2 {
		return nil
	}
	if node.state == 1 {
		return nil
	}
	node.state = 1
	graph.stack = append(graph.stack, node)
	defer func() { graph.stack = graph.stack[:len(graph.stack)-1] }()

	for _, declaration := range node.file.Declarations {
		module, ok := declaration.(*syntax.ModuleDeclaration)
		if !ok || !strings.HasPrefix(module.Source, ".") {
			continue
		}
		targetPath := filepath.Clean(filepath.Join(node.directory, module.Source))
		canonical, err := filepath.EvalSymlinks(targetPath)
		if err != nil {
			return fmt.Errorf("canonicalize local module %s: %w", targetPath, err)
		}
		inside, err := pathWithin(graph.root, canonical)
		if err != nil {
			return err
		}
		if !inside {
			graph.addDiagnostic(module, fmt.Sprintf("local module %q resolves outside project root", module.Source))
			continue
		}
		hasInfra, err := directoryHasInfra(canonical)
		if err != nil {
			return err
		}
		if !hasInfra {
			continue
		}
		child, err := graph.loadModule(canonical)
		if err != nil {
			return err
		}
		node.edges[module.Source] = child
		if child.state == 1 {
			graph.addDiagnostic(module, "local module dependency cycle: "+graph.cycleString(child))
			continue
		}
		if err := graph.discover(child); err != nil {
			return err
		}
	}
	node.state = 2
	return nil
}

func (graph *projectGraph) cycleString(target *moduleNode) string {
	start := 0
	for index, node := range graph.stack {
		if node == target {
			start = index
			break
		}
	}
	parts := make([]string, 0, len(graph.stack)-start+1)
	for _, node := range graph.stack[start:] {
		parts = append(parts, node.id)
	}
	parts = append(parts, target.id)
	return strings.Join(parts, " -> ")
}

func (graph *projectGraph) addDiagnostic(node syntax.Node, message string) {
	graph.diagnostics = append(graph.diagnostics, syntax.NewDiagnostic(node.GetFile(), node.GetSpan(), "PROJECT_ERROR", message))
}

func (graph *projectGraph) orderedNodes() []*moduleNode {
	result := []*moduleNode{graph.rootNode}
	var children []*moduleNode
	for _, node := range graph.nodes {
		if node != graph.rootNode {
			children = append(children, node)
		}
	}
	sort.Slice(children, func(i, j int) bool { return children[i].id < children[j].id })
	return append(result, children...)
}

func directoryHasInfra(directory string) (bool, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return false, fmt.Errorf("read local module %s: %w", directory, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".infra" {
			return true, nil
		}
	}
	return false, nil
}

func pathWithin(root, target string) (bool, error) {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false, fmt.Errorf("compare project path %s: %w", target, err)
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}
