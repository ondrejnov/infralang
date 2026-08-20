package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-config-inspect/tfconfig"
	"github.com/ondrejnov/infralang/internal/syntax"
)

type terraformModuleManifest struct {
	Modules []struct {
		Key    string `json:"Key"`
		Source string `json:"Source"`
		Dir    string `json:"Dir"`
	} `json:"Modules"`
}

func (server *server) moduleArgumentCompletions(directory string, context moduleContext, source string, offset int) ([]CompletionItem, bool) {
	if !moduleObjectKeyContext(context.Arguments, offset) || completionFollowsColon(source, offset) {
		return nil, false
	}
	constructor := server.visibleSymbols(directory)[context.Constructor]
	if constructor == nil || constructor.Category != "moduleImport" {
		return nil, true
	}
	moduleDirectory := installedModuleDirectory(directory, context.Label, constructor.Target)
	if moduleDirectory == "" {
		return nil, true
	}
	module, _ := tfconfig.LoadModule(moduleDirectory)
	if module == nil {
		return nil, true
	}

	names := make([]string, 0, len(module.Variables))
	for name := range module.Variables {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]CompletionItem, 0, len(names))
	for _, wireName := range names {
		variable := module.Variables[wireName]
		label := wireToSourceName(wireName)
		if syntax.SourceNameToWire(label) != wireName {
			label = `"` + wireName + `"`
		}
		detail := "optional module input"
		sortText := "1_" + label
		if variable.Required {
			detail = "required module input"
			sortText = "0_" + label
		}
		if variable.Type != "" {
			detail += " (" + variable.Type + ")"
		}
		documentation := variable.Description
		if variable.Deprecated != "" {
			if documentation != "" {
				documentation += "\n\n"
			}
			documentation += "Deprecated: " + variable.Deprecated
		}
		items = append(items, CompletionItem{
			Label: label, InsertText: label, Kind: symbolKindProperty, Detail: detail,
			Documentation: documentation, SortText: sortText,
		})
	}
	return items, true
}

func moduleObjectKeyContext(object *syntax.ObjectExpression, offset int) bool {
	if object == nil {
		return false
	}
	for i := range object.Fields {
		field := &object.Fields[i]
		if field.Condition != nil && spanContains(field.Condition.GetSpan(), offset) {
			return false
		}
		if spanContains(field.Value.GetSpan(), offset) {
			return field.Punned
		}
	}
	for _, item := range object.Items {
		if spanContains(item.GetSpan(), offset) {
			return false
		}
	}
	return true
}

func installedModuleDirectory(directory, label, source string) string {
	manifestPath := filepath.Join(directory, ".terraform", "modules", "modules.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return ""
	}
	var manifest terraformModuleManifest
	if json.Unmarshal(data, &manifest) != nil {
		return ""
	}

	selected := ""
	for _, candidate := range manifest.Modules {
		if label != "" && candidate.Key == label && sameModuleSource(candidate.Source, source) {
			selected = candidate.Dir
			break
		}
		if selected == "" && !strings.Contains(candidate.Key, ".") && sameModuleSource(candidate.Source, source) {
			selected = candidate.Dir
		}
	}
	if selected == "" {
		return ""
	}
	if !filepath.IsAbs(selected) {
		selected = filepath.Join(directory, selected)
	}
	selected = filepath.Clean(selected)
	relative, err := filepath.Rel(directory, selected)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ""
	}
	return selected
}

func sameModuleSource(left, right string) bool {
	left = strings.TrimPrefix(strings.TrimSuffix(left, "/"), "registry.terraform.io/")
	right = strings.TrimPrefix(strings.TrimSuffix(right, "/"), "registry.terraform.io/")
	return left == right
}
