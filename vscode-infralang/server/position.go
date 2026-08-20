package main

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ondrejnov/infralang/internal/syntax"
)

func positionAt(source string, offset int) Position {
	if offset < 0 {
		offset = 0
	}
	if offset > len(source) {
		offset = len(source)
	}
	position := Position{}
	for index := 0; index < offset; {
		if source[index] == '\n' {
			position.Line++
			position.Character = 0
			index++
			continue
		}
		r, size := utf8.DecodeRuneInString(source[index:])
		if size == 0 || index+size > offset {
			break
		}
		position.Character += utf16RuneLen(r)
		index += size
	}
	return position
}

func offsetAt(source string, position Position) int {
	if position.Line < 0 || position.Character < 0 {
		return 0
	}
	line, units := 0, 0
	for index := 0; index < len(source); {
		if line == position.Line {
			if source[index] == '\n' || units >= position.Character {
				return index
			}
			r, size := utf8.DecodeRuneInString(source[index:])
			width := utf16RuneLen(r)
			if units+width > position.Character {
				return index
			}
			units += width
			index += size
			continue
		}
		if source[index] == '\n' {
			line++
			units = 0
		}
		index++
	}
	return len(source)
}

func utf16RuneLen(r rune) int {
	if r == utf8.RuneError || r <= 0xffff {
		return 1
	}
	return len(utf16.Encode([]rune{r}))
}

func rangeForSpan(source string, span syntax.Span) Range {
	start, end := span.Start.Offset, span.End.Offset
	if start < 0 || start > len(source) {
		start = 0
	}
	if end < start || end > len(source) {
		end = start
	}
	return Range{Start: positionAt(source, start), End: positionAt(source, end)}
}

func identifierAt(source string, offset int) (string, int, int) {
	if offset > len(source) {
		offset = len(source)
	}
	start := offset
	for start > 0 && isIdentifierByte(source[start-1]) {
		start--
	}
	end := offset
	for end < len(source) && isIdentifierByte(source[end]) {
		end++
	}
	return source[start:end], start, end
}

func isIdentifierByte(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func memberRootAt(source string, offset int) (string, bool) {
	path, ok := memberPathAt(source, offset)
	if !ok || len(path) == 0 {
		return "", false
	}
	return path[0], true
}

func memberPathAt(source string, offset int) ([]string, bool) {
	_, currentStart, _ := identifierAt(source, offset)
	dot := currentStart
	for dot > 0 && (source[dot-1] == ' ' || source[dot-1] == '\t') {
		dot--
	}
	if dot == 0 || source[dot-1] != '.' {
		return nil, false
	}
	expressionEnd := dot - 1
	expressionStart := expressionEnd
	depth := 0
	for expressionStart > 0 {
		value := source[expressionStart-1]
		switch value {
		case ']', ')':
			depth++
		case '[', '(':
			if depth > 0 {
				depth--
			} else {
				return parseMemberPath(source[expressionStart:expressionEnd]), true
			}
		default:
			if depth == 0 && (value == '\n' || value == '\r' || value == '=' || value == ',' || value == ':' || value == '{' || strings.ContainsRune("+-*/%!?<>&|", rune(value))) {
				return parseMemberPath(source[expressionStart:expressionEnd]), true
			}
		}
		expressionStart--
	}
	path := parseMemberPath(source[expressionStart:expressionEnd])
	return path, len(path) > 0
}

func parseMemberPath(expression string) []string {
	var result []string
	depth := 0
	for index := 0; index < len(expression); {
		switch expression[index] {
		case '[', '(':
			depth++
			index++
			continue
		case ']', ')':
			if depth > 0 {
				depth--
			}
			index++
			continue
		}
		if depth == 0 && (expression[index] == '_' || expression[index] >= 'A' && expression[index] <= 'Z' || expression[index] >= 'a' && expression[index] <= 'z') {
			end := index + 1
			for end < len(expression) && isIdentifierByte(expression[end]) {
				end++
			}
			result = append(result, expression[index:end])
			index = end
			continue
		}
		index++
	}
	return result
}

func declarationKindAt(source string, offset int) string {
	if offset > len(source) {
		offset = len(source)
	}
	lineStart := strings.LastIndexByte(source[:offset], '\n') + 1
	line := strings.TrimSpace(source[lineStart:offset])
	for _, kind := range []string{"resource", "data"} {
		if strings.HasPrefix(line, kind+" ") {
			return kind
		}
	}
	return ""
}
