package compiler

import "github.com/ondrejnov/infralang/internal/syntax"

type BindingName struct {
	Source       string
	Wire         string
	ExplicitWire bool
	Quoted       bool
}

func unaliasedInputName(source string) BindingName {
	return BindingName{Source: source, Wire: syntax.SourceNameToWire(source)}
}

func aliasedInputName(source, wire string) BindingName {
	return BindingName{Source: source, Wire: wire, ExplicitWire: true}
}

func objectFieldName(field syntax.ObjectField) BindingName {
	wire := field.WireName
	if wire == "" {
		wire = syntax.SourceNameToWire(field.Name)
	}
	if field.Quoted {
		return BindingName{Wire: wire, Quoted: true}
	}
	return BindingName{Source: field.Name, Wire: wire}
}

type valueKind uint8

const (
	valueDynamic valueKind = iota
	valueNull
	valueString
	valueNumber
	valueBool
	valueList
	valueSet
	valueMap
	valueObject
	valueTuple
)

type valueType struct {
	kind      valueKind
	element   *valueType
	tuple     []valueType
	fields    []valueField
	sensitive bool
	open      bool
}

type valueField struct {
	name         BindingName
	typeInfo     valueType
	optional     bool
	conditional  bool
	defaulted    bool
	defaultValue syntax.Expression
	sensitive    bool
	span         syntax.Span
}

type typeAliasInfo struct {
	declaration *syntax.TypeAliasDeclaration
	state       uint8
	typeInfo    valueType
}

type scopeFrame struct {
	parent   *scopeFrame
	bindings map[string]valueType
}

func newScope(parent *scopeFrame) *scopeFrame {
	return &scopeFrame{parent: parent, bindings: make(map[string]valueType)}
}

func (scope *scopeFrame) lookup(name string) (valueType, bool) {
	for current := scope; current != nil; current = current.parent {
		if value, ok := current.bindings[name]; ok {
			return value, true
		}
	}
	return valueType{}, false
}

func commonType(left, right valueType) (valueType, bool) {
	if left.kind == valueDynamic {
		result := right
		result.sensitive = result.sensitive || left.sensitive
		return result, true
	}
	if right.kind == valueDynamic || right.kind == valueNull {
		result := left
		result.sensitive = result.sensitive || right.sensitive
		return result, true
	}
	if left.kind == valueNull {
		result := right
		result.sensitive = result.sensitive || left.sensitive
		return result, true
	}
	if left.kind == valueList && right.kind == valueTuple {
		return commonListAndTuple(left, right)
	}
	if left.kind == valueTuple && right.kind == valueList {
		return commonListAndTuple(right, left)
	}
	if left.kind != right.kind {
		return valueType{}, false
	}
	result := left
	result.sensitive = left.sensitive || right.sensitive
	switch left.kind {
	case valueList, valueSet, valueMap:
		if left.element == nil || right.element == nil {
			return result, true
		}
		element, ok := commonType(*left.element, *right.element)
		if !ok {
			return valueType{}, false
		}
		result.element = &element
	case valueTuple:
		if len(left.tuple) != len(right.tuple) {
			leftElement, leftOK := tupleElementType(left.tuple)
			rightElement, rightOK := tupleElementType(right.tuple)
			switch {
			case !leftOK && !rightOK:
				return valueType{kind: valueList, element: &valueType{kind: valueDynamic}}, true
			case !leftOK:
				return valueType{kind: valueList, element: &rightElement}, true
			case !rightOK:
				return valueType{kind: valueList, element: &leftElement}, true
			default:
				element, ok := commonType(leftElement, rightElement)
				if !ok {
					return valueType{}, false
				}
				return valueType{kind: valueList, element: &element}, true
			}
		}
		result.tuple = make([]valueType, len(left.tuple))
		for index := range left.tuple {
			item, ok := commonType(left.tuple[index], right.tuple[index])
			if !ok {
				return valueType{}, false
			}
			result.tuple[index] = item
		}
	case valueObject:
		rightFields := make(map[string]valueField, len(right.fields))
		for _, field := range right.fields {
			rightFields[field.name.Wire] = field
		}
		result.fields = nil
		seen := make(map[string]bool, len(left.fields)+len(right.fields))
		for _, leftField := range left.fields {
			rightField, ok := rightFields[leftField.name.Wire]
			if !ok {
				leftField.optional = true
				result.fields = append(result.fields, leftField)
				seen[leftField.name.Wire] = true
				continue
			}
			if leftField.name.Source != rightField.name.Source {
				return valueType{}, false
			}
			fieldType, ok := commonType(leftField.typeInfo, rightField.typeInfo)
			if !ok {
				return valueType{}, false
			}
			leftField.typeInfo = fieldType
			leftField.optional = leftField.optional || rightField.optional
			leftField.conditional = leftField.conditional || rightField.conditional
			result.fields = append(result.fields, leftField)
			seen[leftField.name.Wire] = true
		}
		for _, rightField := range right.fields {
			if seen[rightField.name.Wire] {
				continue
			}
			rightField.optional = true
			result.fields = append(result.fields, rightField)
		}
		result.open = left.open || right.open
	}
	return result, true
}

func commonListAndTuple(list, tuple valueType) (valueType, bool) {
	if list.element == nil {
		return list, true
	}
	element, ok := tupleElementType(tuple.tuple)
	if !ok {
		return list, true
	}
	common, ok := commonType(*list.element, element)
	if !ok {
		return valueType{}, false
	}
	return valueType{kind: valueList, element: &common, sensitive: list.sensitive || tuple.sensitive}, true
}

func tupleElementType(items []valueType) (valueType, bool) {
	if len(items) == 0 {
		return valueType{}, false
	}
	result := items[0]
	for _, item := range items[1:] {
		common, ok := commonType(result, item)
		if !ok {
			return valueType{kind: valueDynamic}, true
		}
		result = common
	}
	return result, true
}

func isAssignable(expected, actual valueType) bool {
	if expected.kind == valueDynamic || actual.kind == valueDynamic || actual.kind == valueNull {
		return true
	}
	if expected.kind == valueList && actual.kind == valueTuple {
		if expected.element == nil {
			return true
		}
		for _, item := range actual.tuple {
			if !isAssignable(*expected.element, item) {
				return false
			}
		}
		return true
	}
	if expected.kind == valueSet && actual.kind == valueTuple {
		if expected.element == nil {
			return true
		}
		for _, item := range actual.tuple {
			if !isAssignable(*expected.element, item) {
				return false
			}
		}
		return true
	}
	if expected.kind == valueMap && actual.kind == valueObject {
		if expected.element == nil {
			return true
		}
		for _, field := range actual.fields {
			if !isAssignable(*expected.element, field.typeInfo) {
				return false
			}
		}
		return true
	}
	if expected.kind != actual.kind {
		return false
	}
	switch expected.kind {
	case valueList, valueSet, valueMap:
		return expected.element == nil || actual.element == nil || isAssignable(*expected.element, *actual.element)
	case valueTuple:
		if len(expected.tuple) != len(actual.tuple) {
			return false
		}
		for index := range expected.tuple {
			if !isAssignable(expected.tuple[index], actual.tuple[index]) {
				return false
			}
		}
		return true
	case valueObject:
		actualFields := make(map[string]valueField, len(actual.fields))
		actualSources := make(map[string]valueField, len(actual.fields))
		for _, field := range actual.fields {
			actualFields[field.name.Wire] = field
			if field.name.Source != "" {
				actualSources[field.name.Source] = field
			}
		}
		for _, field := range expected.fields {
			actualField, ok := actualFields[field.name.Wire]
			if !ok && field.name.Source != "" {
				actualField, ok = actualSources[field.name.Source]
			}
			if !ok {
				if field.optional || field.defaulted || actual.open {
					continue
				}
				return false
			}
			if !isAssignable(field.typeInfo, actualField.typeInfo) {
				return false
			}
		}
		return true
	default:
		return true
	}
}

func valueTypeName(value valueType) string {
	switch value.kind {
	case valueNull:
		return "null"
	case valueString:
		return "string"
	case valueNumber:
		return "number"
	case valueBool:
		return "bool"
	case valueList:
		return "list"
	case valueSet:
		return "set"
	case valueMap:
		return "map"
	case valueObject:
		return "object"
	case valueTuple:
		return "tuple"
	default:
		return "dynamic"
	}
}

func valueTypeDescription(value valueType) string {
	switch value.kind {
	case valueList, valueSet, valueMap:
		name := valueTypeName(value)
		if value.element == nil {
			return name + "<dynamic>"
		}
		return name + "<" + valueTypeDescription(*value.element) + ">"
	case valueTuple:
		return "tuple"
	default:
		return valueTypeName(value)
	}
}

func diagnosticTypeDescription(value valueType) string {
	description := valueTypeDescription(value)
	if value.sensitive {
		return "sensitive " + description
	}
	return description
}
