package compiler

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ondrejnov/infralang/internal/syntax"
)

type indexedHandleKind uint8

const (
	indexedProvider indexedHandleKind = iota
	indexedModule
)

type indexedHandle struct {
	kind      indexedHandleKind
	name      string
	span      syntax.Span
	expansion string
}

type preparer struct {
	file                 *syntax.File
	diagnostics          []syntax.Diagnostic
	constants            map[string]*constBinding
	typeAliases          map[string]*syntax.TypeAliasDeclaration
	moduleImports        map[string]*syntax.ModuleImportDeclaration
	moduleSourceVersions map[string]*moduleVersionBinding
	constantStack        []string
	cycleReported        map[string]bool
	indexed              map[string]map[string]indexedHandle
}

type moduleVersionBinding struct {
	declaration *syntax.ModuleImportDeclaration
	Version     string
}

func Prepare(file *syntax.File) (*syntax.File, []syntax.Diagnostic) {
	p := &preparer{
		file: file, constants: make(map[string]*constBinding),
		typeAliases:          make(map[string]*syntax.TypeAliasDeclaration),
		moduleImports:        make(map[string]*syntax.ModuleImportDeclaration),
		moduleSourceVersions: make(map[string]*moduleVersionBinding),
		cycleReported:        make(map[string]bool), indexed: make(map[string]map[string]indexedHandle),
	}
	p.collectCompileTimeDeclarations()
	p.bindModuleImports(file.Declarations, true)
	for _, declaration := range file.Declarations {
		if constant, ok := declaration.(*syntax.ConstDeclaration); ok {
			p.evalConstant(constant.Name)
		}
	}
	expanded := p.expandDeclarations(file.Declarations, nil, "")
	p.resolveIdentities(expanded)
	for _, declaration := range expanded {
		p.rewriteDeclaration(declaration)
	}
	expanded, argumentChecks, providerChecks, exportChecks := p.expandComponents(expanded)
	p.resolveIdentities(expanded)
	for _, declaration := range expanded {
		p.rewriteDeclaration(declaration)
	}
	for index := range argumentChecks {
		argumentChecks[index].Expected = p.rewriteType(argumentChecks[index].Expected)
		argumentChecks[index].Actual = p.rewriteExpression(argumentChecks[index].Actual)
	}
	for index := range providerChecks {
		providerChecks[index].Actual = p.rewriteExpression(providerChecks[index].Actual)
	}
	for index := range exportChecks {
		exportChecks[index].Value = p.rewriteExpression(exportChecks[index].Value)
	}
	expanded = p.lowerIfDeclarations(expanded)
	for _, declaration := range expanded {
		if terraform, ok := declaration.(*syntax.TerraformDeclaration); ok {
			for _, block := range terraform.Blocks {
				p.resolveTerraformBlock(block)
			}
		}
	}
	p.checkFinalIdentities(expanded)
	result := &syntax.File{Name: file.Name, ID: file.ID, Source: file.Source, Declarations: expanded}
	result.ComponentArgumentChecks = append(result.ComponentArgumentChecks, file.ComponentArgumentChecks...)
	result.ComponentArgumentChecks = append(result.ComponentArgumentChecks, argumentChecks...)
	result.ComponentProviderChecks = append(result.ComponentProviderChecks, file.ComponentProviderChecks...)
	result.ComponentProviderChecks = append(result.ComponentProviderChecks, providerChecks...)
	result.ComponentExportChecks = append(result.ComponentExportChecks, file.ComponentExportChecks...)
	result.ComponentExportChecks = append(result.ComponentExportChecks, exportChecks...)
	return result, syntax.SortDiagnostics(p.diagnostics)
}

func (p *preparer) collectCompileTimeDeclarations() {
	for _, declaration := range p.file.Declarations {
		switch value := declaration.(type) {
		case *syntax.ConstDeclaration:
			if previous := p.constants[value.Name]; previous != nil {
				p.addDiagnostic(previous.declaration, fmt.Sprintf("constant %q conflicts with another constant", value.Name))
				p.addDiagnostic(value, fmt.Sprintf("constant %q conflicts with another constant", value.Name))
				continue
			}
			p.constants[value.Name] = &constBinding{declaration: value}
		case *syntax.TypeAliasDeclaration:
			if p.typeAliases[value.Name] == nil {
				p.typeAliases[value.Name] = value
			}
		case *syntax.ModuleImportDeclaration:
			if previous := p.moduleImports[value.Name]; previous != nil {
				message := fmt.Sprintf("module import %q conflicts with another import", value.Name)
				p.addDiagnostic(previous, message)
				p.addDiagnostic(value, message)
				continue
			}
			if value.Version != "" && isLocalModuleSource(value.Source) {
				p.addDiagnostic(value, fmt.Sprintf("local module source %q cannot declare a version constraint", value.Source))
				continue
			}
			if previous := p.moduleSourceVersions[value.Source]; previous != nil && previous.Version != value.Version {
				message := fmt.Sprintf("module source %q is imported with conflicting version constraints %q and %q", value.Source, previous.Version, value.Version)
				p.addDiagnostic(previous.declaration, message)
				p.addDiagnostic(value, message)
				continue
			}
			if value.Version != "" {
				p.moduleSourceVersions[value.Source] = &moduleVersionBinding{declaration: value, Version: value.Version}
			}
			p.moduleImports[value.Name] = value
		}
	}
}

func (p *preparer) bindModuleImports(declarations []syntax.Declaration, topLevel bool) {
	for _, declaration := range declarations {
		switch value := declaration.(type) {
		case *syntax.ModuleImportDeclaration:
			if !topLevel {
				p.addDiagnostic(value, "module imports are directory-scoped and must be top-level")
			}
		case *syntax.ModuleDeclaration:
			imported := p.moduleImports[value.ModuleName]
			if imported == nil {
				p.addDiagnostic(value, fmt.Sprintf("unknown imported module %q", value.ModuleName))
				continue
			}
			value.Source = imported.Source
			value.Version = imported.Version
		case *syntax.StaticForDeclaration:
			p.bindModuleImports(value.Declarations, false)
		case *syntax.ComponentDefinition:
			p.bindModuleImports(value.Declarations, false)
		}
	}
}

func (p *preparer) evalConstant(name string) (constValue, bool) {
	binding := p.constants[name]
	if binding == nil {
		return constValue{}, false
	}
	switch binding.state {
	case 1:
		start := 0
		for index, item := range p.constantStack {
			if item == name {
				start = index
				break
			}
		}
		for _, item := range p.constantStack[start:] {
			if p.cycleReported[item] {
				continue
			}
			p.cycleReported[item] = true
			p.addDiagnostic(p.constants[item].declaration, fmt.Sprintf("constant %q is part of a dependency cycle", item))
		}
		return constValue{}, false
	case 2:
		return binding.value, true
	case 3:
		return constValue{}, false
	}
	binding.state = 1
	p.constantStack = append(p.constantStack, name)
	value, ok := p.eval(binding.declaration.Value, nil)
	p.constantStack = p.constantStack[:len(p.constantStack)-1]
	if !ok {
		binding.state = 3
		return constValue{}, false
	}
	if binding.declaration.Type != nil {
		expected := p.constType(binding.declaration.Type, make(map[string]bool))
		actual := constValueType(value)
		if !isAssignable(expected, actual) {
			p.addDiagnosticAt(binding.declaration.Value, fmt.Sprintf("constant %q has incompatible annotation: expected %s, got %s", name, valueTypeDescription(expected), valueTypeDescription(actual)))
			binding.state = 3
			return constValue{}, false
		}
	}
	binding.value = value
	binding.state = 2
	return value, true
}

func (p *preparer) eval(expression syntax.Expression, environment *staticEnv) (constValue, bool) {
	if expression == nil {
		return constValue{}, false
	}
	switch value := expression.(type) {
	case *syntax.LiteralExpression:
		switch literal := value.Value.(type) {
		case nil:
			return constValue{kind: constNull}, true
		case bool:
			return constValue{kind: constBool, bool: literal}, true
		case string:
			return constValue{kind: constString, text: literal}, true
		case json.Number:
			number, ok := parseExactNumber(literal.String())
			if !ok {
				p.addDiagnosticAt(value, "invalid exact compile-time number")
				return constValue{}, false
			}
			return constValue{kind: constNumber, number: number}, true
		}
	case *syntax.IdentifierExpression:
		if environment != nil {
			if result, ok := environment.lookup(value.Name); ok {
				return result, true
			}
		}
		if result, ok := p.evalConstant(value.Name); ok {
			return result, true
		}
		p.addDiagnosticAt(value, fmt.Sprintf("compile-time expression cannot reference runtime or unknown name %q", value.Name))
		return constValue{}, false
	case *syntax.ArrayExpression:
		result := constValue{kind: constList}
		for _, item := range value.Items {
			itemValue, ok := p.eval(item, environment)
			if !ok {
				return constValue{}, false
			}
			result.list = append(result.list, itemValue)
		}
		return result, true
	case *syntax.ObjectExpression:
		result := constValue{kind: constObject}
		for _, item := range objectItems(value) {
			switch item := item.(type) {
			case syntax.ObjectField:
				if item.Condition != nil {
					condition, ok := p.eval(item.Condition, environment)
					if !ok {
						return constValue{}, false
					}
					if condition.kind != constBool {
						p.addDiagnosticAt(item.Condition, "compile-time object field condition expects bool")
						return constValue{}, false
					}
					if !condition.bool {
						continue
					}
				}
				fieldValue, ok := p.eval(item.Value, environment)
				if !ok {
					return constValue{}, false
				}
				result.object = mergeConstField(result.object, constField{name: objectFieldName(item), value: fieldValue})
			case syntax.ObjectSpread:
				spread, ok := p.eval(item.Value, environment)
				if !ok {
					return constValue{}, false
				}
				if spread.kind != constObject {
					p.addDiagnosticAt(item.Value, "compile-time object spread expects object")
					return constValue{}, false
				}
				for _, field := range spread.object {
					result.object = mergeConstField(result.object, field)
				}
			case syntax.InputsSpread:
				p.addDiagnosticAt(item, "inputs forwarding is not a compile-time value")
				return constValue{}, false
			}
		}
		return result, true
	case *syntax.TemplateExpression:
		var result strings.Builder
		for _, part := range value.Parts {
			if part.Expression == nil {
				result.WriteString(part.Text)
				continue
			}
			partValue, ok := p.eval(part.Expression, environment)
			if !ok {
				return constValue{}, false
			}
			text, ok := constValueString(partValue)
			if !ok {
				p.addDiagnosticAt(part.Expression, "formatted compile-time value must be scalar")
				return constValue{}, false
			}
			result.WriteString(text)
		}
		return constValue{kind: constString, text: result.String()}, true
	case *syntax.UnaryExpression:
		operand, ok := p.eval(value.Operand, environment)
		if !ok {
			return constValue{}, false
		}
		switch value.Operator {
		case syntax.TokenBang:
			if operand.kind != constBool {
				p.addDiagnosticAt(value, "compile-time '!' expects bool")
				return constValue{}, false
			}
			return constValue{kind: constBool, bool: !operand.bool}, true
		case syntax.TokenMinus:
			if operand.kind != constNumber {
				p.addDiagnosticAt(value, "compile-time unary '-' expects number")
				return constValue{}, false
			}
			return constValue{kind: constNumber, number: exactNumber{value: new(big.Rat).Neg(operand.number.value)}}, true
		}
	case *syntax.BinaryExpression:
		return p.evalBinary(value, environment)
	case *syntax.ConditionalExpression:
		condition, ok := p.eval(value.Condition, environment)
		if !ok {
			return constValue{}, false
		}
		if condition.kind != constBool {
			p.addDiagnosticAt(value.Condition, "compile-time conditional expects bool")
			return constValue{}, false
		}
		if condition.bool {
			return p.eval(value.Then, environment)
		}
		return p.eval(value.Else, environment)
	case *syntax.MemberExpression:
		target, ok := p.eval(value.Target, environment)
		if !ok {
			return constValue{}, false
		}
		if target.kind != constObject {
			p.addDiagnosticAt(value, "compile-time member access expects object")
			return constValue{}, false
		}
		result, ok := target.fieldBySource(value.Name)
		if !ok {
			p.addDiagnosticAt(value, fmt.Sprintf("compile-time object has no field %q", value.Name))
		}
		return result, ok
	case *syntax.IndexExpression:
		target, ok := p.eval(value.Target, environment)
		if !ok {
			return constValue{}, false
		}
		index, ok := p.eval(value.Index, environment)
		if !ok {
			return constValue{}, false
		}
		switch target.kind {
		case constList:
			if index.kind != constNumber || !index.number.value.IsInt() {
				p.addDiagnosticAt(value.Index, "compile-time list index expects integer")
				return constValue{}, false
			}
			position := index.number.value.Num().Int64()
			if position < 0 || position >= int64(len(target.list)) {
				p.addDiagnosticAt(value.Index, "compile-time list index is out of range")
				return constValue{}, false
			}
			return target.list[position], true
		case constObject:
			if index.kind != constString {
				p.addDiagnosticAt(value.Index, "compile-time object index expects string")
				return constValue{}, false
			}
			result, exists := target.fieldByWire(index.text)
			if !exists {
				p.addDiagnosticAt(value.Index, fmt.Sprintf("compile-time object has no wire field %q", index.text))
			}
			return result, exists
		}
		p.addDiagnosticAt(value.Target, "compile-time index target must be list or object")
		return constValue{}, false
	case *syntax.ForExpression:
		collection, ok := p.eval(value.Collection, environment)
		if !ok {
			return constValue{}, false
		}
		keys, values, iterable := constIterationItems(collection)
		if !iterable {
			p.addDiagnosticAt(value.Collection, "compile-time comprehension expects list or object")
			return constValue{}, false
		}
		result := constValue{kind: constList}
		if value.Object {
			result.kind = constObject
		}
		for index := range values {
			frame := &staticEnv{parent: environment, values: map[string]constValue{value.ValueVariable: values[index]}}
			if value.KeyVariable != "" {
				frame.values[value.KeyVariable] = keys[index]
			}
			if value.Condition != nil {
				condition, ok := p.eval(value.Condition, frame)
				if !ok {
					return constValue{}, false
				}
				if condition.kind != constBool {
					p.addDiagnosticAt(value.Condition, "compile-time comprehension filter expects bool")
					return constValue{}, false
				}
				if !condition.bool {
					continue
				}
			}
			item, ok := p.eval(value.Value, frame)
			if !ok {
				return constValue{}, false
			}
			if !value.Object {
				result.list = append(result.list, item)
				continue
			}
			key, ok := p.eval(value.Key, frame)
			if !ok || key.kind != constString {
				p.addDiagnosticAt(value.Key, "compile-time object comprehension key expects string")
				return constValue{}, false
			}
			result.object = mergeConstField(result.object, constField{name: BindingName{Wire: key.text, Quoted: true}, value: item})
		}
		return result, true
	case *syntax.CallExpression:
		p.addDiagnosticAt(value, "function calls and effectful operations are not allowed in compile-time expressions")
		return constValue{}, false
	}
	p.addDiagnosticAt(expression, "unsupported compile-time expression")
	return constValue{}, false
}

func (p *preparer) evalBinary(expression *syntax.BinaryExpression, environment *staticEnv) (constValue, bool) {
	left, ok := p.eval(expression.Left, environment)
	if !ok {
		return constValue{}, false
	}
	if expression.Operator == syntax.TokenAnd && left.kind == constBool && !left.bool {
		return constValue{kind: constBool}, true
	}
	if expression.Operator == syntax.TokenOr && left.kind == constBool && left.bool {
		return constValue{kind: constBool, bool: true}, true
	}
	if expression.Operator == syntax.TokenCoalesce && left.kind != constNull {
		return left, true
	}
	right, ok := p.eval(expression.Right, environment)
	if !ok {
		return constValue{}, false
	}
	switch expression.Operator {
	case syntax.TokenEqual:
		return constValue{kind: constBool, bool: constEqual(left, right)}, true
	case syntax.TokenNotEqual:
		return constValue{kind: constBool, bool: !constEqual(left, right)}, true
	case syntax.TokenAnd, syntax.TokenOr:
		if left.kind != constBool || right.kind != constBool {
			p.addDiagnosticAt(expression, "compile-time boolean operator expects bool operands")
			return constValue{}, false
		}
		if expression.Operator == syntax.TokenAnd {
			return constValue{kind: constBool, bool: left.bool && right.bool}, true
		}
		return constValue{kind: constBool, bool: left.bool || right.bool}, true
	case syntax.TokenCoalesce:
		return right, true
	}
	if left.kind != constNumber || right.kind != constNumber {
		p.addDiagnosticAt(expression, constOperationError(expression.Operator).Error())
		return constValue{}, false
	}
	switch expression.Operator {
	case syntax.TokenPlus, syntax.TokenMinus, syntax.TokenStar, syntax.TokenSlash, syntax.TokenPercent:
		if (expression.Operator == syntax.TokenSlash || expression.Operator == syntax.TokenPercent) && right.number.value.Sign() == 0 {
			p.addDiagnosticAt(expression.Right, "compile-time division by zero")
			return constValue{}, false
		}
		result := new(big.Rat)
		switch expression.Operator {
		case syntax.TokenPlus:
			result.Add(left.number.value, right.number.value)
		case syntax.TokenMinus:
			result.Sub(left.number.value, right.number.value)
		case syntax.TokenStar:
			result.Mul(left.number.value, right.number.value)
		case syntax.TokenSlash:
			result.Quo(left.number.value, right.number.value)
		case syntax.TokenPercent:
			if !left.number.value.IsInt() || !right.number.value.IsInt() {
				p.addDiagnosticAt(expression, "compile-time remainder expects integers")
				return constValue{}, false
			}
			integer := new(big.Int).Rem(left.number.value.Num(), right.number.value.Num())
			result.SetInt(integer)
		}
		if _, finite := (exactNumber{value: result}).canonical(); !finite {
			p.addDiagnosticAt(expression, "compile-time numeric result is not a finite decimal")
			return constValue{}, false
		}
		return constValue{kind: constNumber, number: exactNumber{value: result}}, true
	case syntax.TokenLess, syntax.TokenLessEqual, syntax.TokenGreater, syntax.TokenGreaterEqual:
		comparison := left.number.value.Cmp(right.number.value)
		result := false
		switch expression.Operator {
		case syntax.TokenLess:
			result = comparison < 0
		case syntax.TokenLessEqual:
			result = comparison <= 0
		case syntax.TokenGreater:
			result = comparison > 0
		case syntax.TokenGreaterEqual:
			result = comparison >= 0
		}
		return constValue{kind: constBool, bool: result}, true
	}
	p.addDiagnosticAt(expression, constOperationError(expression.Operator).Error())
	return constValue{}, false
}

func (p *preparer) constType(expression *syntax.TypeExpression, visiting map[string]bool) valueType {
	if expression == nil {
		return valueType{kind: valueDynamic}
	}
	if len(expression.Operands) > 0 {
		result := valueType{kind: valueObject}
		type fieldOwner struct {
			field   valueField
			operand int
		}
		sourceFields := make(map[string]fieldOwner)
		wireFields := make(map[string]fieldOwner)
		for operandIndex, operand := range expression.Operands {
			operandType := p.constType(operand, visiting)
			objectOperand, resolvedName := p.isObjectCompositionOperand(operand, make(map[string]bool))
			if !objectOperand {
				p.addDiagnosticAt(operand, fmt.Sprintf("type composition operand must resolve directly to an object type, got %s", resolvedName))
				continue
			}
			for _, field := range operandType.fields {
				sourceConflict := false
				if field.name.Source != "" {
					if previous, exists := sourceFields[field.name.Source]; exists && previous.operand != operandIndex {
						p.addDiagnosticSpan(previous.field.span, fmt.Sprintf("composed object type source field %q conflicts with another field", field.name.Source))
						p.addDiagnosticSpan(field.span, fmt.Sprintf("composed object type source field %q conflicts with another field", field.name.Source))
						sourceConflict = true
					}
					sourceFields[field.name.Source] = fieldOwner{field: field, operand: operandIndex}
				}
				if previous, exists := wireFields[field.name.Wire]; exists && previous.operand != operandIndex && !sourceConflict {
					p.addDiagnosticSpan(previous.field.span, fmt.Sprintf("composed object type wire field %q conflicts with another field", field.name.Wire))
					p.addDiagnosticSpan(field.span, fmt.Sprintf("composed object type wire field %q conflicts with another field", field.name.Wire))
				}
				wireFields[field.name.Wire] = fieldOwner{field: field, operand: operandIndex}
			}
			result.fields = append(result.fields, operandType.fields...)
		}
		return result
	}
	switch expression.Name {
	case "string":
		return valueType{kind: valueString}
	case "number":
		return valueType{kind: valueNumber}
	case "bool":
		return valueType{kind: valueBool}
	case "dynamic", "any":
		return valueType{kind: valueDynamic}
	case "list", "set", "map":
		element := valueType{kind: valueDynamic}
		if len(expression.Arguments) == 1 {
			element = p.constType(expression.Arguments[0], visiting)
		}
		kind := valueList
		if expression.Name == "set" {
			kind = valueSet
		} else if expression.Name == "map" {
			kind = valueMap
		}
		return valueType{kind: kind, element: &element}
	case "optional":
		if len(expression.Arguments) == 1 {
			return p.constType(expression.Arguments[0], visiting)
		}
		return valueType{kind: valueDynamic}
	case "object":
		result := valueType{kind: valueObject}
		for _, field := range expression.Fields {
			name := BindingName{Source: field.Name, Wire: field.WireName, ExplicitWire: field.ExplicitWire, Quoted: field.Quoted}
			if name.Wire == "" {
				name.Wire = syntax.SourceNameToWire(field.Name)
			}
			if field.Quoted {
				name.Source = ""
			}
			result.fields = append(result.fields, valueField{name: name, typeInfo: p.constType(field.Type, visiting), optional: field.Optional, defaulted: field.Default != nil, span: field.GetSpan()})
		}
		return result
	default:
		alias := p.typeAliases[expression.Name]
		if alias == nil {
			p.addDiagnosticAt(expression, fmt.Sprintf("unknown type %q in constant annotation", expression.Name))
			return valueType{kind: valueDynamic}
		}
		if visiting[expression.Name] {
			return valueType{kind: valueDynamic}
		}
		visiting[expression.Name] = true
		result := p.constType(alias.Type, visiting)
		delete(visiting, expression.Name)
		return result
	}
}

func (p *preparer) isObjectCompositionOperand(expression *syntax.TypeExpression, visiting map[string]bool) (bool, string) {
	if len(expression.Operands) > 0 || expression.Name == "object" {
		return true, "object"
	}
	if alias := p.typeAliases[expression.Name]; alias != nil && !visiting[expression.Name] {
		visiting[expression.Name] = true
		valid, name := p.isObjectCompositionOperand(alias.Type, visiting)
		delete(visiting, expression.Name)
		return valid, name
	}
	return false, expression.Name
}

func (p *preparer) addDiagnostic(node syntax.Node, message string) {
	p.addDiagnosticAt(node, message)
}

func (p *preparer) addDiagnosticAt(node syntax.Node, message string) {
	if expansion := node.GetExpansion(); expansion != "" {
		message += " [" + expansion + "]"
	}
	p.diagnostics = append(p.diagnostics, syntax.NewDiagnostic(node.GetFile(), node.GetSpan(), "COMPILE_TIME_ERROR", message))
}

func (p *preparer) addDiagnosticSpan(span syntax.Span, message string) {
	file := span.File
	if file == "" {
		file = p.file.ID
	}
	if file == "" {
		file = syntax.FileID(p.file.Name)
	}
	p.diagnostics = append(p.diagnostics, syntax.NewDiagnostic(file, span, "COMPILE_TIME_ERROR", message))
}

func canonicalIndex(value constValue, allowNumber bool) (string, bool) {
	if value.kind == constString {
		return value.text, value.text != ""
	}
	if allowNumber && value.kind == constNumber {
		return value.number.canonical()
	}
	return "", false
}

func isLocalModuleSource(source string) bool {
	return strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") ||
		strings.HasPrefix(source, "/") || filepath.IsAbs(source)
}

func syntheticHandleName(namespace, key string) string {
	return "$indexed$" + namespace + "$" + hex.EncodeToString([]byte(key))
}

func sortedObjectFields(value constValue) []constField {
	fields := append([]constField(nil), value.object...)
	sort.Slice(fields, func(i, j int) bool { return fields[i].name.Wire < fields[j].name.Wire })
	return fields
}
