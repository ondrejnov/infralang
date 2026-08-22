package compiler

import (
	"fmt"

	"github.com/ondrejnov/infralang/internal/syntax"
)

// lowerIfDeclarations folds staged let assignments into their original
// declaration so Terraform never sees duplicate or self-referential locals.
func (p *preparer) lowerIfDeclarations(declarations []syntax.Declaration) []syntax.Declaration {
	assignedNames := make(map[string]bool)
	lets := make(map[string]*syntax.LetDeclaration)
	for _, declaration := range declarations {
		switch value := declaration.(type) {
		case *syntax.LetDeclaration:
			if lets[value.Name] == nil {
				lets[value.Name] = value
			}
		case *syntax.IfDeclaration:
			for _, assignment := range value.Assignments {
				assignedNames[assignment.Name] = true
			}
		}
	}

	states := make(map[string]syntax.Expression)
	for name := range assignedNames {
		if declaration := lets[name]; declaration != nil {
			states[name] = declaration.Value
		}
	}

	seenLets := make(map[string]bool)
	result := make([]syntax.Declaration, 0, len(declarations))
	for _, declaration := range declarations {
		switch value := declaration.(type) {
		case *syntax.LetDeclaration:
			seenLets[value.Name] = true
			result = append(result, declaration)
		case *syntax.IfDeclaration:
			entryStates := copyExpressionMap(states)
			branchStates := copyExpressionMap(states)
			condition := substituteStagedLets(value.Condition, entryStates, nil)
			var touched []string
			touchedSet := make(map[string]bool)
			for index := range value.Assignments {
				assignment := &value.Assignments[index]
				if !seenLets[assignment.Name] || states[assignment.Name] == nil {
					p.addDiagnostic(assignment, fmt.Sprintf("if assignment target %q must refer to a previously declared let", assignment.Name))
					continue
				}
				branchStates[assignment.Name] = substituteStagedLets(assignment.Value, branchStates, nil)
				if !touchedSet[assignment.Name] {
					touchedSet[assignment.Name] = true
					touched = append(touched, assignment.Name)
				}
			}
			for _, name := range touched {
				if included, constant := literalBool(condition); constant {
					if included {
						states[name] = branchStates[name]
					}
					continue
				}
				states[name] = &syntax.ConditionalExpression{
					BaseNode:  value.BaseNode,
					Condition: condition,
					Then:      branchStates[name],
					Else:      entryStates[name],
				}
			}
		default:
			result = append(result, declaration)
		}
	}
	for name, state := range states {
		if declaration := lets[name]; declaration != nil {
			declaration.Value = state
		}
	}
	return result
}

func copyExpressionMap(source map[string]syntax.Expression) map[string]syntax.Expression {
	result := make(map[string]syntax.Expression, len(source))
	for name, expression := range source {
		result[name] = expression
	}
	return result
}

func substituteStagedLets(expression syntax.Expression, states map[string]syntax.Expression, blocked map[string]bool) syntax.Expression {
	if expression == nil {
		return nil
	}
	if identifier, ok := expression.(*syntax.IdentifierExpression); ok {
		if !blocked[identifier.Name] {
			if replacement := states[identifier.Name]; replacement != nil {
				return cloneExpression(replacement, nil, "")
			}
		}
		return cloneExpression(identifier, nil, "")
	}
	substitute := func(value syntax.Expression, scope map[string]bool) syntax.Expression {
		return substituteStagedLets(value, states, scope)
	}
	switch value := expression.(type) {
	case *syntax.LiteralExpression:
		return cloneExpression(value, nil, "")
	case *syntax.ArrayExpression:
		result := &syntax.ArrayExpression{BaseNode: value.BaseNode}
		for _, item := range value.Items {
			result.Items = append(result.Items, substitute(item, blocked))
		}
		return result
	case *syntax.ObjectExpression:
		result := &syntax.ObjectExpression{BaseNode: value.BaseNode}
		for _, item := range objectItems(value) {
			switch item := item.(type) {
			case syntax.ObjectField:
				item.Value = substitute(item.Value, blocked)
				item.Condition = substitute(item.Condition, blocked)
				result.Items = append(result.Items, item)
				result.Fields = append(result.Fields, item)
			case syntax.ObjectSpread:
				item.Value = substitute(item.Value, blocked)
				result.Items = append(result.Items, item)
			case syntax.InputsSpread:
				item.Value = substitute(item.Value, blocked)
				result.Items = append(result.Items, item)
			}
		}
		return result
	case *syntax.ForExpression:
		inner := copyBlockedNames(blocked)
		inner[value.ValueVariable] = true
		if value.KeyVariable != "" {
			inner[value.KeyVariable] = true
		}
		return &syntax.ForExpression{
			BaseNode: value.BaseNode, KeyVariable: value.KeyVariable, ValueVariable: value.ValueVariable,
			Collection: substitute(value.Collection, blocked), Key: substitute(value.Key, inner),
			Value: substitute(value.Value, inner), Condition: substitute(value.Condition, inner), Object: value.Object,
		}
	case *syntax.TemplateExpression:
		result := &syntax.TemplateExpression{BaseNode: value.BaseNode}
		for _, part := range value.Parts {
			result.Parts = append(result.Parts, syntax.TemplatePart{Text: part.Text, Expression: substitute(part.Expression, blocked)})
		}
		return result
	case *syntax.UnaryExpression:
		return &syntax.UnaryExpression{BaseNode: value.BaseNode, Operator: value.Operator, Operand: substitute(value.Operand, blocked)}
	case *syntax.BinaryExpression:
		return &syntax.BinaryExpression{BaseNode: value.BaseNode, Left: substitute(value.Left, blocked), Operator: value.Operator, Right: substitute(value.Right, blocked)}
	case *syntax.ConditionalExpression:
		return &syntax.ConditionalExpression{BaseNode: value.BaseNode, Condition: substitute(value.Condition, blocked), Then: substitute(value.Then, blocked), Else: substitute(value.Else, blocked)}
	case *syntax.MemberExpression:
		return &syntax.MemberExpression{BaseNode: value.BaseNode, Target: substitute(value.Target, blocked), Name: value.Name}
	case *syntax.IndexExpression:
		return &syntax.IndexExpression{BaseNode: value.BaseNode, Target: substitute(value.Target, blocked), Index: substitute(value.Index, blocked)}
	case *syntax.CallExpression:
		result := &syntax.CallExpression{BaseNode: value.BaseNode, Callee: substitute(value.Callee, blocked)}
		for _, argument := range value.Arguments {
			result.Arguments = append(result.Arguments, substitute(argument, blocked))
		}
		return result
	default:
		return cloneExpression(expression, nil, "")
	}
}
