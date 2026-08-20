package compiler

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/ondrejnov/infralang/internal/syntax"
)

type constKind uint8

const (
	constNull constKind = iota
	constBool
	constString
	constNumber
	constList
	constObject
)

type exactNumber struct {
	value *big.Rat
	raw   string
}

type constField struct {
	name  BindingName
	value constValue
}

type constValue struct {
	kind   constKind
	bool   bool
	text   string
	number exactNumber
	list   []constValue
	object []constField
}

type constBinding struct {
	declaration *syntax.ConstDeclaration
	state       uint8
	value       constValue
}

type staticEnv struct {
	parent  *staticEnv
	values  map[string]constValue
	blocked map[string]bool
}

func (environment *staticEnv) lookup(name string) (constValue, bool) {
	for current := environment; current != nil; current = current.parent {
		if current.blocked[name] {
			return constValue{}, false
		}
		if value, exists := current.values[name]; exists {
			return value, true
		}
	}
	return constValue{}, false
}

func parseExactNumber(text string) (exactNumber, bool) {
	original := text
	sign := 1
	if strings.HasPrefix(text, "-") {
		sign = -1
		text = text[1:]
	}
	exponent := 0
	if index := strings.IndexAny(text, "eE"); index >= 0 {
		parsed, err := strconv.Atoi(text[index+1:])
		if err != nil {
			return exactNumber{}, false
		}
		exponent = parsed
		text = text[:index]
	}
	fractionDigits := 0
	if index := strings.IndexByte(text, '.'); index >= 0 {
		fractionDigits = len(text) - index - 1
		text = text[:index] + text[index+1:]
	}
	if text == "" {
		return exactNumber{}, false
	}
	numerator := new(big.Int)
	if _, ok := numerator.SetString(text, 10); !ok {
		return exactNumber{}, false
	}
	if sign < 0 {
		numerator.Neg(numerator)
	}
	scale := fractionDigits - exponent
	denominator := big.NewInt(1)
	if scale > 0 {
		denominator.Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	} else if scale < 0 {
		multiplier := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-scale)), nil)
		numerator.Mul(numerator, multiplier)
	}
	return exactNumber{value: new(big.Rat).SetFrac(numerator, denominator), raw: original}, true
}

func (number exactNumber) canonical() (string, bool) {
	if number.value == nil {
		return "", false
	}
	denominator := new(big.Int).Set(number.value.Denom())
	twos, fives := 0, 0
	two, five := big.NewInt(2), big.NewInt(5)
	remainder := new(big.Int)
	for {
		remainder.Mod(denominator, two)
		if remainder.Sign() != 0 {
			break
		}
		denominator.Div(denominator, two)
		twos++
	}
	for {
		remainder.Mod(denominator, five)
		if remainder.Sign() != 0 {
			break
		}
		denominator.Div(denominator, five)
		fives++
	}
	if denominator.Cmp(big.NewInt(1)) != 0 {
		return "", false
	}
	scale := twos
	if fives > scale {
		scale = fives
	}
	text := number.value.FloatString(scale)
	if strings.Contains(text, ".") {
		text = strings.TrimRight(text, "0")
		text = strings.TrimRight(text, ".")
	}
	if text == "-0" {
		text = "0"
	}
	return text, true
}

func (number exactNumber) jsonText() (string, bool) {
	if number.raw != "" {
		return number.raw, true
	}
	return number.canonical()
}

func (value constValue) fieldByWire(name string) (constValue, bool) {
	for _, field := range value.object {
		if field.name.Wire == name {
			return field.value, true
		}
	}
	return constValue{}, false
}

func (value constValue) fieldBySource(name string) (constValue, bool) {
	for _, field := range value.object {
		if field.name.Source == name && name != "" {
			return field.value, true
		}
	}
	return constValue{}, false
}

func mergeConstField(fields []constField, next constField) []constField {
	for index, field := range fields {
		if field.name.Wire == next.name.Wire {
			fields[index] = next
			return fields
		}
	}
	return append(fields, next)
}

func constEqual(left, right constValue) bool {
	if left.kind != right.kind {
		return false
	}
	switch left.kind {
	case constNull:
		return true
	case constBool:
		return left.bool == right.bool
	case constString:
		return left.text == right.text
	case constNumber:
		return left.number.value.Cmp(right.number.value) == 0
	case constList:
		if len(left.list) != len(right.list) {
			return false
		}
		for index := range left.list {
			if !constEqual(left.list[index], right.list[index]) {
				return false
			}
		}
		return true
	case constObject:
		if len(left.object) != len(right.object) {
			return false
		}
		for _, field := range left.object {
			other, ok := right.fieldByWire(field.name.Wire)
			if !ok || !constEqual(field.value, other) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func constValueType(value constValue) valueType {
	switch value.kind {
	case constNull:
		return valueType{kind: valueNull}
	case constBool:
		return valueType{kind: valueBool}
	case constString:
		return valueType{kind: valueString}
	case constNumber:
		return valueType{kind: valueNumber}
	case constList:
		tuple := make([]valueType, 0, len(value.list))
		for _, item := range value.list {
			tuple = append(tuple, constValueType(item))
		}
		return valueType{kind: valueTuple, tuple: tuple}
	case constObject:
		result := valueType{kind: valueObject}
		for _, field := range value.object {
			result.fields = append(result.fields, valueField{name: field.name, typeInfo: constValueType(field.value)})
		}
		return result
	default:
		return valueType{kind: valueDynamic}
	}
}

func constIterationItems(value constValue) ([]constValue, []constValue, bool) {
	switch value.kind {
	case constList:
		keys := make([]constValue, len(value.list))
		for index := range value.list {
			number, _ := parseExactNumber(strconv.Itoa(index))
			keys[index] = constValue{kind: constNumber, number: number}
		}
		return keys, value.list, true
	case constObject:
		fields := append([]constField(nil), value.object...)
		sort.Slice(fields, func(i, j int) bool { return fields[i].name.Wire < fields[j].name.Wire })
		keys := make([]constValue, 0, len(fields))
		values := make([]constValue, 0, len(fields))
		for _, field := range fields {
			keys = append(keys, constValue{kind: constString, text: field.name.Wire})
			values = append(values, field.value)
		}
		return keys, values, true
	default:
		return nil, nil, false
	}
}

func constValueString(value constValue) (string, bool) {
	switch value.kind {
	case constString:
		return value.text, true
	case constNumber:
		return value.number.canonical()
	case constBool:
		return strconv.FormatBool(value.bool), true
	case constNull:
		return "null", true
	default:
		return "", false
	}
}

func constValueExpression(value constValue, origin syntax.BaseNode) syntax.Expression {
	switch value.kind {
	case constNull:
		return &syntax.LiteralExpression{BaseNode: origin, Value: nil}
	case constBool:
		return &syntax.LiteralExpression{BaseNode: origin, Value: value.bool}
	case constString:
		return &syntax.LiteralExpression{BaseNode: origin, Value: value.text}
	case constNumber:
		text, ok := value.number.jsonText()
		if !ok {
			text = "0"
		}
		return &syntax.LiteralExpression{BaseNode: origin, Value: json.Number(text)}
	case constList:
		items := make([]syntax.Expression, 0, len(value.list))
		for _, item := range value.list {
			items = append(items, constValueExpression(item, origin))
		}
		return &syntax.ArrayExpression{BaseNode: origin, Items: items}
	case constObject:
		object := &syntax.ObjectExpression{BaseNode: origin}
		for _, field := range value.object {
			item := syntax.ObjectField{
				BaseNode: origin, Name: field.name.Source, WireName: field.name.Wire,
				Quoted: field.name.Quoted, Value: constValueExpression(field.value, origin),
			}
			if item.Name == "" {
				item.Name = field.name.Wire
			}
			object.Fields = append(object.Fields, item)
			object.Items = append(object.Items, item)
		}
		return object
	default:
		return &syntax.LiteralExpression{BaseNode: origin, Value: nil}
	}
}

func constJSON(value constValue) (any, bool) {
	switch value.kind {
	case constNull:
		return nil, true
	case constBool:
		return value.bool, true
	case constString:
		return value.text, true
	case constNumber:
		text, ok := value.number.jsonText()
		if !ok {
			return nil, false
		}
		return json.Number(text), true
	case constList:
		result := make([]any, 0, len(value.list))
		for _, item := range value.list {
			encoded, ok := constJSON(item)
			if !ok {
				return nil, false
			}
			result = append(result, encoded)
		}
		return result, true
	case constObject:
		result := make(map[string]any, len(value.object))
		for _, field := range value.object {
			encoded, ok := constJSON(field.value)
			if !ok {
				return nil, false
			}
			result[field.name.Wire] = encoded
		}
		return result, true
	default:
		return nil, false
	}
}

func constOperationError(operator syntax.TokenKind) error {
	return fmt.Errorf("operator %s is not valid for these compile-time values", operator)
}
