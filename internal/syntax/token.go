package syntax

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var terraformIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

func IsTerraformIdentifier(value string) bool {
	return terraformIdentifier.MatchString(value)
}

var nonSourceIdentifier = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

func SourceNameToWire(value string) string {
	value = nonSourceIdentifier.ReplaceAllString(value, "_")
	var result strings.Builder
	for index, current := range value {
		if unicode.IsUpper(current) {
			if index > 0 {
				previous := rune(value[index-1])
				if previous != '_' && (unicode.IsLower(previous) || unicode.IsDigit(previous)) {
					result.WriteByte('_')
				}
			}
			result.WriteRune(unicode.ToLower(current))
		} else {
			result.WriteRune(current)
		}
	}
	return strings.Trim(result.String(), "_")
}

type Position struct {
	Offset int
	Line   int
	Column int
}

type Span struct {
	File  FileID
	Start Position
	End   Position
}

type FileID string

func (s Span) String() string {
	return fmt.Sprintf("%d:%d", s.Start.Line, s.Start.Column)
}

type Diagnostic struct {
	Filename string
	Span     Span
	Code     string
	Message  string
}

func NewDiagnostic(file FileID, span Span, code, message string) Diagnostic {
	if file == "" {
		file = span.File
	}
	return Diagnostic{Filename: string(file), Span: span, Code: code, Message: message}
}

func SortDiagnostics(diagnostics []Diagnostic) []Diagnostic {
	result := append([]Diagnostic(nil), diagnostics...)
	sort.SliceStable(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.Filename != right.Filename {
			return left.Filename < right.Filename
		}
		if left.Span.Start.Offset != right.Span.Start.Offset {
			return left.Span.Start.Offset < right.Span.Start.Offset
		}
		if left.Span.End.Offset != right.Span.End.Offset {
			return left.Span.End.Offset < right.Span.End.Offset
		}
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		return left.Message < right.Message
	})
	return result
}

func (d Diagnostic) Error() string {
	if d.Filename == "" {
		return fmt.Sprintf("%s: %s", d.Span, d.Message)
	}
	return fmt.Sprintf("%s:%s: %s", d.Filename, d.Span, d.Message)
}

type TokenKind int

const (
	TokenIllegal TokenKind = iota
	TokenEOF
	TokenIdentifier
	TokenNumber
	TokenString
	TokenFString
	TokenRawAddress
	TokenLeftParen
	TokenRightParen
	TokenLeftBrace
	TokenRightBrace
	TokenLeftBracket
	TokenRightBracket
	TokenColon
	TokenComma
	TokenDot
	TokenEllipsis
	TokenSemicolon
	TokenAssign
	TokenArrow
	TokenFatArrow
	TokenPlus
	TokenMinus
	TokenStar
	TokenSlash
	TokenPercent
	TokenBang
	TokenEqual
	TokenNotEqual
	TokenLess
	TokenLessEqual
	TokenGreater
	TokenGreaterEqual
	TokenAnd
	TokenOr
	TokenCoalesce
	TokenQuestion
)

type Token struct {
	Kind   TokenKind
	Lexeme string
	Span   Span
}

func (t Token) IsIdentifier(value string) bool {
	return t.Kind == TokenIdentifier && t.Lexeme == value
}

func (k TokenKind) String() string {
	names := map[TokenKind]string{
		TokenIllegal:      "invalid token",
		TokenEOF:          "end of file",
		TokenIdentifier:   "identifier",
		TokenNumber:       "number",
		TokenString:       "string",
		TokenFString:      "interpolated string",
		TokenRawAddress:   "raw address",
		TokenLeftParen:    "(",
		TokenRightParen:   ")",
		TokenLeftBrace:    "{",
		TokenRightBrace:   "}",
		TokenLeftBracket:  "[",
		TokenRightBracket: "]",
		TokenColon:        ":",
		TokenComma:        ",",
		TokenDot:          ".",
		TokenEllipsis:     "...",
		TokenSemicolon:    ";",
		TokenAssign:       "=",
		TokenArrow:        "->",
		TokenFatArrow:     "=>",
		TokenPlus:         "+",
		TokenMinus:        "-",
		TokenStar:         "*",
		TokenSlash:        "/",
		TokenPercent:      "%",
		TokenBang:         "!",
		TokenEqual:        "==",
		TokenNotEqual:     "!=",
		TokenLess:         "<",
		TokenLessEqual:    "<=",
		TokenGreater:      ">",
		TokenGreaterEqual: ">=",
		TokenAnd:          "&&",
		TokenOr:           "||",
		TokenCoalesce:     "??",
		TokenQuestion:     "?",
	}
	if name, ok := names[k]; ok {
		return name
	}
	return fmt.Sprintf("token(%d)", k)
}
