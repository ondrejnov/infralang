package syntax

import (
	"fmt"
	"regexp"
)

var terraformIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

func IsTerraformIdentifier(value string) bool {
	return terraformIdentifier.MatchString(value)
}

type Position struct {
	Offset int
	Line   int
	Column int
}

type Span struct {
	Start Position
	End   Position
}

func (s Span) String() string {
	return fmt.Sprintf("%d:%d", s.Start.Line, s.Start.Column)
}

type Diagnostic struct {
	Filename string
	Span     Span
	Message  string
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
	TokenLeftParen
	TokenRightParen
	TokenLeftBrace
	TokenRightBrace
	TokenLeftBracket
	TokenRightBracket
	TokenColon
	TokenComma
	TokenDot
	TokenSemicolon
	TokenAssign
	TokenArrow
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
		TokenLeftParen:    "(",
		TokenRightParen:   ")",
		TokenLeftBrace:    "{",
		TokenRightBrace:   "}",
		TokenLeftBracket:  "[",
		TokenRightBracket: "]",
		TokenColon:        ":",
		TokenComma:        ",",
		TokenDot:          ".",
		TokenSemicolon:    ";",
		TokenAssign:       "=",
		TokenArrow:        "=>",
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
