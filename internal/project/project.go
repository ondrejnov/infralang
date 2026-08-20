package project

import (
	"sort"

	"github.com/ondrejnov/infralang/internal/compiler"
	"github.com/ondrejnov/infralang/internal/syntax"
)

type Artifact struct {
	ModuleID  string
	Directory string
	Data      []byte
}

type CompileResult struct {
	Artifacts   []Artifact
	Diagnostics []syntax.Diagnostic
	Interfaces  map[string]compiler.ModuleInterface
}

func Compile(root string) (CompileResult, error) {
	return CompileWithOptions(root, compiler.CompileOptions{})
}

func CompileWithOptions(root string, baseOptions compiler.CompileOptions) (CompileResult, error) {
	graph, err := discoverGraph(root)
	if err != nil {
		return CompileResult{}, err
	}
	result := CompileResult{Interfaces: make(map[string]compiler.ModuleInterface)}
	result.Diagnostics = append(result.Diagnostics, graph.diagnostics...)
	if len(result.Diagnostics) != 0 {
		result.Diagnostics = syntax.SortDiagnostics(result.Diagnostics)
		return result, nil
	}

	var collect func(*moduleNode)
	collected := make(map[*moduleNode]bool)
	collect = func(node *moduleNode) {
		if collected[node] {
			return
		}
		children := make([]*moduleNode, 0, len(node.edges))
		for _, child := range node.edges {
			children = append(children, child)
		}
		sort.Slice(children, func(i, j int) bool { return children[i].id < children[j].id })
		for _, child := range children {
			collect(child)
		}
		options := baseOptions
		options.ProjectRoot = graph.root
		options.ModuleID = node.id
		options.LocalModules = localContracts(node, result.Interfaces)
		contract, _ := compiler.CollectInterface(node.file, options)
		result.Interfaces[node.id] = contract
		collected[node] = true
	}
	collect(graph.rootNode)

	for _, node := range graph.orderedNodes() {
		options := baseOptions
		options.ProjectRoot = graph.root
		options.ModuleID = node.id
		options.LocalModules = localContracts(node, result.Interfaces)
		data, diagnostics := compiler.CompileWithOptions(node.file, options)
		result.Diagnostics = append(result.Diagnostics, diagnostics...)
		if len(diagnostics) == 0 {
			result.Artifacts = append(result.Artifacts, Artifact{ModuleID: node.id, Directory: node.directory, Data: data})
		}
	}
	result.Diagnostics = syntax.SortDiagnostics(result.Diagnostics)
	if len(result.Diagnostics) != 0 {
		result.Artifacts = nil
	}
	return result, nil
}

func localContracts(node *moduleNode, interfaces map[string]compiler.ModuleInterface) map[string]compiler.ModuleInterface {
	result := make(map[string]compiler.ModuleInterface, len(node.edges))
	for source, child := range node.edges {
		if contract, exists := interfaces[child.id]; exists {
			result[source] = contract
		}
	}
	return result
}
