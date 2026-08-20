package main

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/ondrejnov/infralang/internal/syntax"
)

type completionType struct {
	expression *syntax.TypeExpression
	directory  string
}

func (server *server) structuralArgumentCompletions(path, source string, offset int) ([]CompletionItem, bool) {
	index := server.workspace.file(path)
	if index == nil || index.File == nil || completionFollowsColon(source, offset) {
		return nil, false
	}
	directory := filepath.Dir(path)
	for _, declaration := range index.File.Declarations {
		constant, ok := declaration.(*syntax.ConstDeclaration)
		if !ok || constant.Type == nil || !spanContains(constant.Value.GetSpan(), offset) {
			continue
		}
		object, expected := server.expectedObjectAt(constant.Value, completionType{expression: constant.Type, directory: directory}, offset)
		if object == nil || expected.expression == nil || expected.expression.Name != "object" {
			return nil, false
		}
		return structuralCompletionItems(object, expected.expression), true
	}
	return nil, false
}

func (server *server) expectedObjectAt(expression syntax.Expression, expected completionType, offset int) (*syntax.ObjectExpression, completionType) {
	expected = server.resolveCompletionType(expected, make(map[string]bool))
	switch value := expression.(type) {
	case *syntax.ObjectExpression:
		if expected.expression == nil {
			return nil, completionType{}
		}
		switch expected.expression.Name {
		case "map":
			element := completionTypeArgument(expected, 0)
			for i := range value.Fields {
				field := &value.Fields[i]
				if field.Condition != nil && spanContains(field.Condition.GetSpan(), offset) {
					return nil, completionType{}
				}
				if spanContains(field.Value.GetSpan(), offset) {
					return server.expectedObjectAt(field.Value, element, offset)
				}
			}
		case "object":
			for i := range value.Fields {
				field := &value.Fields[i]
				if field.Condition != nil && spanContains(field.Condition.GetSpan(), offset) {
					return nil, completionType{}
				}
				if !spanContains(field.Value.GetSpan(), offset) {
					continue
				}
				if field.Punned {
					return value, expected
				}
				fieldType := completionObjectFieldType(expected, field)
				if fieldType.expression != nil {
					return server.expectedObjectAt(field.Value, fieldType, offset)
				}
				return nil, completionType{}
			}
			for _, item := range value.Items {
				switch spread := item.(type) {
				case syntax.ObjectSpread:
					if spanContains(spread.GetSpan(), offset) {
						return nil, completionType{}
					}
				case syntax.InputsSpread:
					if spanContains(spread.GetSpan(), offset) {
						return nil, completionType{}
					}
				}
			}
			return value, expected
		}
	case *syntax.ArrayExpression:
		if expected.expression == nil || (expected.expression.Name != "list" && expected.expression.Name != "set") {
			return nil, completionType{}
		}
		element := completionTypeArgument(expected, 0)
		for _, item := range value.Items {
			if spanContains(item.GetSpan(), offset) {
				return server.expectedObjectAt(item, element, offset)
			}
		}
	}
	return nil, completionType{}
}

func completionFollowsColon(source string, offset int) bool {
	tokens, _ := syntax.Lex("completion.infra", source)
	var previous syntax.Token
	found := false
	for _, token := range tokens {
		if token.Kind == syntax.TokenEOF || token.Span.End.Offset > offset {
			continue
		}
		previous = token
		found = true
	}
	return found && previous.Kind == syntax.TokenColon
}

func (server *server) resolveCompletionType(value completionType, seen map[string]bool) completionType {
	for value.expression != nil {
		switch value.expression.Name {
		case "optional":
			value = completionTypeArgument(value, 0)
			continue
		case "string", "number", "bool", "dynamic", "list", "set", "map", "object":
			return value
		}

		key := value.directory + "#" + value.expression.Name
		if seen[key] {
			return completionType{}
		}
		seen[key] = true
		item := server.visibleSymbols(value.directory)[value.expression.Name]
		if item == nil {
			return completionType{}
		}
		switch item.Category {
		case "type":
			index := server.workspace.file(item.Path)
			if index == nil || index.File == nil {
				return completionType{}
			}
			var resolved *syntax.TypeExpression
			for _, declaration := range index.File.Declarations {
				alias, ok := declaration.(*syntax.TypeAliasDeclaration)
				if ok && alias.Name == item.Name {
					resolved = alias.Type
					break
				}
			}
			value = completionType{expression: resolved, directory: filepath.Dir(item.Path)}
		case "typeImport":
			separator := strings.LastIndex(item.Target, "#")
			if separator < 0 {
				return completionType{}
			}
			targetPath, targetName := item.Target[:separator], item.Target[separator+1:]
			index := server.workspace.file(targetPath)
			if index == nil || index.File == nil {
				return completionType{}
			}
			var resolved *syntax.TypeExpression
			for _, declaration := range index.File.Declarations {
				alias, ok := declaration.(*syntax.TypeAliasDeclaration)
				if ok && alias.Name == targetName && alias.Exported {
					resolved = alias.Type
					break
				}
			}
			value = completionType{expression: resolved, directory: filepath.Dir(targetPath)}
		default:
			return completionType{}
		}
	}
	return completionType{}
}

func completionTypeArgument(value completionType, index int) completionType {
	if value.expression == nil || index >= len(value.expression.Arguments) {
		return completionType{}
	}
	return completionType{expression: value.expression.Arguments[index], directory: value.directory}
}

func completionObjectFieldType(value completionType, field *syntax.ObjectField) completionType {
	for i := range value.expression.Fields {
		candidate := &value.expression.Fields[i]
		if candidate.WireName == field.WireName {
			return completionType{expression: candidate.Type, directory: value.directory}
		}
	}
	return completionType{}
}

func structuralCompletionItems(object *syntax.ObjectExpression, expected *syntax.TypeExpression) []CompletionItem {
	existing := make(map[string]bool, len(object.Fields))
	for i := range object.Fields {
		existing[object.Fields[i].WireName] = true
	}
	items := make([]CompletionItem, 0, len(expected.Fields))
	for i := range expected.Fields {
		field := &expected.Fields[i]
		if existing[field.WireName] {
			continue
		}
		label, insertText := field.Name, field.Name
		if field.Quoted {
			label = `"` + field.WireName + `"`
			insertText = label
		}
		detail := typeDetail(field.Type)
		if field.Optional {
			detail = "optional " + detail
		} else {
			detail = "required " + detail
		}
		items = append(items, CompletionItem{
			Label: label, InsertText: insertText, Kind: symbolKindProperty,
			Detail: detail, SortText: "0_" + label,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Label < items[j].Label })
	return items
}

func spanContains(span syntax.Span, offset int) bool {
	return offset >= span.Start.Offset && offset <= span.End.Offset
}
