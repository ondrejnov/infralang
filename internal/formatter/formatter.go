package formatter

import (
	"bytes"
	"reflect"
	"strings"

	"github.com/ondrejnov/infralang/internal/syntax"
)

type sourceItem struct {
	token       syntax.Token
	raw         string
	start       int
	end         int
	startColumn int
	comment     bool
}

type delimiter struct {
	multiline bool
}

// Format returns canonical InfraLang source without changing invalid input.
func Format(filename, source string) ([]byte, []syntax.Diagnostic) {
	file, diagnostics := syntax.Parse(filename, source)
	if len(diagnostics) > 0 {
		return nil, diagnostics
	}
	tokens, diagnostics := syntax.Lex(filename, source)
	if len(diagnostics) > 0 {
		return nil, diagnostics
	}

	declarationStarts := make(map[int]bool)
	collectDeclarationStarts(file.Declarations, declarationStarts)
	forcedMultiline := collectForcedMultilineObjects(file)
	items := sourceItems(source, tokens)
	if len(items) == 0 {
		return []byte{}, nil
	}

	compactComprehensions := collectCompactComprehensionOffsets(file, tokens, forcedMultiline)
	multilineDelimiters := delimiterLayout(tokens, declarationStarts, forcedMultiline, compactComprehensions)
	genericTokens := genericTokenOffsets(tokens)
	unaryTokens := unaryTokenOffsets(tokens)
	comprehensionClosings := comprehensionClosingOffsets(tokens)
	var output formatWriter
	var delimiters []delimiter
	var previous *sourceItem
	var previousToken *syntax.Token

	for index := range items {
		item := &items[index]
		indent := multilineDepth(delimiters)
		if !item.comment && isClosing(item.token.Kind) && len(delimiters) > 0 && delimiters[len(delimiters)-1].multiline {
			indent--
		}

		newlines := 0
		if previous != nil {
			newlines = min(strings.Count(source[previous.end:item.start], "\n"), 2)
			if newlines > 0 && !item.comment && !previous.comment &&
				compactComprehensions[previous.start] && compactComprehensions[item.start] {
				newlines = 0
			}
			if !item.comment {
				if declarationStarts[item.start] || isMultilineClose(item.token.Kind, delimiters) || followsMultilineSeparator(previousToken, delimiters) {
					newlines = max(newlines, 1)
				}
			}
		}
		output.newlines(newlines)
		if previous != nil && newlines == 0 && needsSpace(previous, item, previousToken, genericTokens, unaryTokens, comprehensionClosings, nextToken(items, index)) {
			output.space()
		}
		output.write(item.raw, indent, item.comment, item.startColumn)

		if !item.comment {
			if isOpening(item.token.Kind) {
				delimiters = append(delimiters, delimiter{multiline: multilineDelimiters[item.start]})
			} else if isClosing(item.token.Kind) && len(delimiters) > 0 {
				delimiters = delimiters[:len(delimiters)-1]
			}
			previousToken = &item.token
		}
		previous = item
	}

	output.newlines(1)
	return output.bytes(), nil
}

func collectDeclarationStarts(declarations []syntax.Declaration, starts map[int]bool) {
	for _, declaration := range declarations {
		starts[declaration.GetSpan().Start.Offset] = true
		switch value := declaration.(type) {
		case *syntax.StaticForDeclaration:
			collectDeclarationStarts(value.Declarations, starts)
		case *syntax.ComponentDefinition:
			collectDeclarationStarts(value.Declarations, starts)
		case *syntax.IfDeclaration:
			for index := range value.Assignments {
				starts[value.Assignments[index].GetSpan().Start.Offset] = true
			}
		}
	}
}

func sourceItems(source string, tokens []syntax.Token) []sourceItem {
	items := make([]sourceItem, 0, len(tokens))
	previousEnd := 0
	for _, token := range tokens {
		if token.Kind == syntax.TokenEOF {
			break
		}
		items = append(items, commentsIn(source, previousEnd, token.Span.Start.Offset)...)
		items = append(items, sourceItem{
			token: token, raw: source[token.Span.Start.Offset:token.Span.End.Offset],
			start: token.Span.Start.Offset, end: token.Span.End.Offset,
			startColumn: token.Span.Start.Column,
		})
		previousEnd = token.Span.End.Offset
	}
	items = append(items, commentsIn(source, previousEnd, len(source))...)
	return items
}

func commentsIn(source string, start, end int) []sourceItem {
	var items []sourceItem
	for offset := start; offset < end; {
		commentStart := -1
		commentEnd := -1
		switch {
		case source[offset] == '#':
			commentStart = offset
			commentEnd = offset
			for commentEnd < end && source[commentEnd] != '\n' {
				commentEnd++
			}
		case source[offset] == '/' && offset+1 < end && source[offset+1] == '/':
			commentStart = offset
			commentEnd = offset + 2
			for commentEnd < end && source[commentEnd] != '\n' {
				commentEnd++
			}
		case source[offset] == '/' && offset+1 < end && source[offset+1] == '*':
			commentStart = offset
			commentEnd = offset + 2
			for commentEnd+1 < end && (source[commentEnd] != '*' || source[commentEnd+1] != '/') {
				commentEnd++
			}
			if commentEnd+1 < end {
				commentEnd += 2
			}
		}
		if commentStart < 0 {
			offset++
			continue
		}
		_, column := lineAndColumn(source, commentStart)
		items = append(items, sourceItem{
			raw: source[commentStart:commentEnd], start: commentStart, end: commentEnd,
			startColumn: column, comment: true,
		})
		offset = commentEnd
	}
	return items
}

func lineAndColumn(source string, offset int) (int, int) {
	line := 1
	column := 1
	for index := 0; index < offset; index++ {
		if source[index] == '\n' {
			line++
			column = 1
		} else {
			column++
		}
	}
	return line, column
}

func delimiterLayout(tokens []syntax.Token, declarationStarts, forcedMultiline, compactComprehensions map[int]bool) map[int]bool {
	type opening struct {
		token     syntax.Token
		multiline bool
	}
	result := make(map[int]bool)
	var stack []opening
	var previous *syntax.Token
	for index := range tokens {
		token := tokens[index]
		if len(stack) > 0 && !compactComprehensions[stack[len(stack)-1].token.Span.Start.Offset] &&
			((previous != nil && token.Span.Start.Line > previous.Span.End.Line) || declarationStarts[token.Span.Start.Offset]) {
			stack[len(stack)-1].multiline = true
		}
		if isOpening(token.Kind) {
			forced := forcedMultiline[token.Span.Start.Offset] && !compactComprehensions[token.Span.Start.Offset]
			if token.Kind == syntax.TokenLeftBrace && index > 0 {
				forced = forced || forcedMultiline[tokens[index-1].Span.Start.Offset] || ifBodyOpening(tokens, index)
			}
			stack = append(stack, opening{token: token, multiline: forced})
		} else if isClosing(token.Kind) && len(stack) > 0 {
			open := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			result[open.token.Span.Start.Offset] = open.multiline
		}
		if token.Kind != syntax.TokenEOF {
			previous = &tokens[index]
		}
	}
	return result
}

func ifBodyOpening(tokens []syntax.Token, index int) bool {
	if index < 2 || tokens[index].Kind != syntax.TokenLeftBrace || tokens[index-1].Kind != syntax.TokenRightParen {
		return false
	}
	depth := 0
	for candidate := index - 1; candidate >= 0; candidate-- {
		switch tokens[candidate].Kind {
		case syntax.TokenRightParen:
			depth++
		case syntax.TokenLeftParen:
			depth--
			if depth == 0 {
				return candidate > 0 && tokens[candidate-1].IsIdentifier("if")
			}
		}
	}
	return false
}

func collectCompactComprehensionOffsets(file *syntax.File, tokens []syntax.Token, forcedMultiline map[int]bool) map[int]bool {
	type sourceRange struct {
		start int
		end   int
	}
	var ranges []sourceRange
	var visit func(reflect.Value)
	visit = func(value reflect.Value) {
		if !value.IsValid() {
			return
		}
		if value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
			if value.IsNil() {
				return
			}
			if value.CanInterface() {
				if comprehension, ok := value.Interface().(*syntax.ForExpression); ok && !comprehension.Object {
					span := comprehension.GetSpan()
					multiline := false
					for offset := range forcedMultiline {
						if offset > span.Start.Offset && offset < span.End.Offset {
							multiline = true
							break
						}
					}
					if !multiline {
						ranges = append(ranges, sourceRange{start: span.Start.Offset, end: span.End.Offset})
					}
				}
			}
			visit(value.Elem())
			return
		}
		switch value.Kind() {
		case reflect.Struct:
			for index := 0; index < value.NumField(); index++ {
				visit(value.Field(index))
			}
		case reflect.Slice, reflect.Array:
			for index := 0; index < value.Len(); index++ {
				visit(value.Index(index))
			}
		}
	}
	visit(reflect.ValueOf(file))

	result := make(map[int]bool)
	for _, token := range tokens {
		if token.Kind == syntax.TokenEOF {
			continue
		}
		for _, sourceRange := range ranges {
			if token.Span.Start.Offset >= sourceRange.start && token.Span.End.Offset <= sourceRange.end {
				result[token.Span.Start.Offset] = true
				break
			}
		}
	}
	return result
}

func collectForcedMultilineObjects(file *syntax.File) map[int]bool {
	result := make(map[int]bool)
	var visit func(reflect.Value)
	visit = func(value reflect.Value) {
		if !value.IsValid() {
			return
		}
		if value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
			if value.IsNil() {
				return
			}
			if value.CanInterface() {
				switch node := value.Interface().(type) {
				case *syntax.ObjectExpression:
					if objectNeedsMultiline(node) {
						result[node.GetSpan().Start.Offset] = true
					}
				case *syntax.TypeExpression:
					if node.Name == "object" && len(node.Fields) > 1 {
						result[node.GetSpan().Start.Offset] = true
					}
				}
			}
			visit(value.Elem())
			return
		}
		switch value.Kind() {
		case reflect.Struct:
			for index := 0; index < value.NumField(); index++ {
				visit(value.Field(index))
			}
		case reflect.Slice, reflect.Array:
			for index := 0; index < value.Len(); index++ {
				visit(value.Index(index))
			}
		}
	}
	visit(reflect.ValueOf(file))
	return result
}

func objectNeedsMultiline(object *syntax.ObjectExpression) bool {
	if len(object.Items) <= 1 {
		return false
	}
	for _, item := range object.Items {
		field, ok := item.(syntax.ObjectField)
		if !ok || !field.Punned {
			return true
		}
	}
	return false
}

func genericTokenOffsets(tokens []syntax.Token) map[int]bool {
	result := make(map[int]bool)
	var stack []int
	for index, token := range tokens {
		switch token.Kind {
		case syntax.TokenLess:
			if index > 0 && tokens[index-1].Kind == syntax.TokenIdentifier && isGenericType(tokens[index-1].Lexeme) {
				stack = append(stack, token.Span.Start.Offset)
				result[token.Span.Start.Offset] = true
			}
		case syntax.TokenGreater:
			if len(stack) > 0 {
				result[token.Span.Start.Offset] = true
				stack = stack[:len(stack)-1]
			}
		}
	}
	return result
}

func isGenericType(name string) bool {
	switch name {
	case "list", "set", "map", "optional":
		return true
	default:
		return false
	}
}

func unaryTokenOffsets(tokens []syntax.Token) map[int]bool {
	result := make(map[int]bool)
	var previous *syntax.Token
	for index := range tokens {
		token := &tokens[index]
		if token.Kind == syntax.TokenBang || token.Kind == syntax.TokenMinus {
			if previous == nil || startsExpressionAfter(previous.Kind) {
				result[token.Span.Start.Offset] = true
			}
		}
		if token.Kind != syntax.TokenEOF {
			previous = token
		}
	}
	return result
}

func comprehensionClosingOffsets(tokens []syntax.Token) map[int]bool {
	type opening struct {
		comprehension bool
	}
	result := make(map[int]bool)
	var stack []opening
	for index, token := range tokens {
		if isOpening(token.Kind) {
			comprehension := token.Kind == syntax.TokenLeftBrace && index+1 < len(tokens) && tokens[index+1].IsIdentifier("for")
			stack = append(stack, opening{comprehension: comprehension})
			continue
		}
		if !isClosing(token.Kind) || len(stack) == 0 {
			continue
		}
		open := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if open.comprehension {
			result[token.Span.Start.Offset] = true
		}
	}
	return result
}

func startsExpressionAfter(kind syntax.TokenKind) bool {
	switch kind {
	case syntax.TokenLeftParen, syntax.TokenLeftBracket, syntax.TokenLeftBrace,
		syntax.TokenColon, syntax.TokenComma, syntax.TokenAssign, syntax.TokenFatArrow,
		syntax.TokenPlus, syntax.TokenMinus, syntax.TokenStar, syntax.TokenSlash, syntax.TokenPercent,
		syntax.TokenBang, syntax.TokenEqual, syntax.TokenNotEqual, syntax.TokenLess, syntax.TokenLessEqual,
		syntax.TokenGreater, syntax.TokenGreaterEqual, syntax.TokenAnd, syntax.TokenOr, syntax.TokenCoalesce,
		syntax.TokenQuestion:
		return true
	default:
		return false
	}
}

func isOpening(kind syntax.TokenKind) bool {
	return kind == syntax.TokenLeftParen || kind == syntax.TokenLeftBracket || kind == syntax.TokenLeftBrace
}

func isClosing(kind syntax.TokenKind) bool {
	return kind == syntax.TokenRightParen || kind == syntax.TokenRightBracket || kind == syntax.TokenRightBrace
}

func isMultilineClose(kind syntax.TokenKind, delimiters []delimiter) bool {
	return isClosing(kind) && len(delimiters) > 0 && delimiters[len(delimiters)-1].multiline
}

func followsMultilineSeparator(previous *syntax.Token, delimiters []delimiter) bool {
	if previous == nil || len(delimiters) == 0 || !delimiters[len(delimiters)-1].multiline {
		return false
	}
	return previous.Kind == syntax.TokenComma || previous.Kind == syntax.TokenSemicolon || isOpening(previous.Kind)
}

func multilineDepth(delimiters []delimiter) int {
	depth := 0
	for _, delimiter := range delimiters {
		if delimiter.multiline {
			depth++
		}
	}
	return depth
}

func nextToken(items []sourceItem, index int) *syntax.Token {
	for index++; index < len(items); index++ {
		if !items[index].comment {
			return &items[index].token
		}
	}
	return nil
}

func needsSpace(previous, current *sourceItem, previousToken *syntax.Token, generic, unary, comprehensionClosings map[int]bool, next *syntax.Token) bool {
	if previous.comment || current.comment {
		return true
	}
	if previousToken == nil {
		return false
	}
	left, right := previousToken.Kind, current.token.Kind
	if right == syntax.TokenComma || right == syntax.TokenSemicolon || right == syntax.TokenDot ||
		right == syntax.TokenRightParen || right == syntax.TokenRightBracket || right == syntax.TokenColon {
		return false
	}
	if left == syntax.TokenDot || left == syntax.TokenLeftParen || left == syntax.TokenLeftBracket || left == syntax.TokenEllipsis {
		return false
	}
	if right == syntax.TokenRightBrace {
		return left != syntax.TokenLeftBrace && !comprehensionClosings[current.token.Span.Start.Offset]
	}
	if left == syntax.TokenLeftBrace {
		return right != syntax.TokenRightBrace && !(right == syntax.TokenIdentifier && current.token.Lexeme == "for")
	}
	if right == syntax.TokenLeftParen {
		if previousToken.IsIdentifier("if") {
			return true
		}
		return left != syntax.TokenIdentifier && left != syntax.TokenRightParen && left != syntax.TokenRightBracket
	}
	if right == syntax.TokenLeftBracket {
		if left == syntax.TokenIdentifier {
			return previousToken.Lexeme == "using" || previousToken.Lexeme == "in"
		}
		return left != syntax.TokenRightParen && left != syntax.TokenRightBracket
	}
	if generic[current.token.Span.Start.Offset] || (left == syntax.TokenLess && generic[previousToken.Span.Start.Offset]) {
		return false
	}
	if right == syntax.TokenQuestion {
		return next == nil || next.Kind != syntax.TokenColon
	}
	if left == syntax.TokenQuestion && right == syntax.TokenColon {
		return false
	}
	if unary[previousToken.Span.Start.Offset] {
		return false
	}
	if isOperator(left) || isOperator(right) || left == syntax.TokenColon || left == syntax.TokenComma || left == syntax.TokenSemicolon {
		return true
	}
	return true
}

func isOperator(kind syntax.TokenKind) bool {
	switch kind {
	case syntax.TokenAssign, syntax.TokenArrow, syntax.TokenFatArrow, syntax.TokenPlus, syntax.TokenMinus,
		syntax.TokenStar, syntax.TokenSlash, syntax.TokenPercent, syntax.TokenBang, syntax.TokenEqual,
		syntax.TokenNotEqual, syntax.TokenLess, syntax.TokenLessEqual, syntax.TokenGreater,
		syntax.TokenGreaterEqual, syntax.TokenAnd, syntax.TokenOr, syntax.TokenCoalesce, syntax.TokenQuestion:
		return true
	default:
		return false
	}
}

type formatWriter struct {
	buffer bytes.Buffer
}

func (writer *formatWriter) bytes() []byte {
	return writer.buffer.Bytes()
}

func (writer *formatWriter) space() {
	data := writer.buffer.Bytes()
	if len(data) == 0 || data[len(data)-1] == ' ' || data[len(data)-1] == '\n' {
		return
	}
	writer.buffer.WriteByte(' ')
}

func (writer *formatWriter) newlines(count int) {
	data := writer.buffer.Bytes()
	for len(data) > 0 && (data[len(data)-1] == ' ' || data[len(data)-1] == '\t') {
		data = data[:len(data)-1]
	}
	if len(data) != writer.buffer.Len() {
		writer.buffer.Reset()
		writer.buffer.Write(data)
	}
	existing := 0
	for index := len(data) - 1; index >= 0 && data[index] == '\n'; index-- {
		existing++
	}
	for existing < count {
		writer.buffer.WriteByte('\n')
		existing++
	}
}

func (writer *formatWriter) write(raw string, indent int, comment bool, originalColumn int) {
	writer.indent(indent)
	if !comment || !strings.Contains(raw, "\n") {
		writer.buffer.WriteString(strings.TrimRight(raw, " \t\r"))
		return
	}
	lines := strings.Split(raw, "\n")
	writer.buffer.WriteString(strings.TrimRight(lines[0], " \t\r"))
	for _, line := range lines[1:] {
		writer.newlines(1)
		writer.indent(indent)
		writer.buffer.WriteString(strings.TrimRight(removeIndent(line, originalColumn-1), " \t\r"))
	}
}

func (writer *formatWriter) indent(level int) {
	data := writer.buffer.Bytes()
	if len(data) > 0 && data[len(data)-1] != '\n' {
		return
	}
	writer.buffer.WriteString(strings.Repeat("  ", max(level, 0)))
}

func removeIndent(line string, width int) string {
	for width > 0 && len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
		line = line[1:]
		width--
	}
	return line
}
