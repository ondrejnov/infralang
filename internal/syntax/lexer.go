package syntax

import (
	"fmt"
	"strconv"
	"unicode"
)

type lexer struct {
	filename    string
	source      string
	offset      int
	line        int
	column      int
	tokens      []Token
	diagnostics []Diagnostic
}

func Lex(filename, source string) ([]Token, []Diagnostic) {
	l := &lexer{
		filename: filename,
		source:   source,
		line:     1,
		column:   1,
	}
	l.run()
	return l.tokens, SortDiagnostics(l.diagnostics)
}

func (l *lexer) run() {
	for !l.atEnd() {
		l.skipWhitespaceAndComments()
		if l.atEnd() {
			break
		}

		start := l.position()
		ch := l.peek(0)

		if ch == 'f' && l.peek(1) == '"' {
			l.advance()
			l.scanString(start, TokenFString)
			continue
		}
		if isIdentifierStart(ch) {
			l.scanIdentifier(start)
			continue
		}
		if ch >= '0' && ch <= '9' {
			l.scanNumber(start)
			continue
		}
		if ch == '"' {
			l.scanString(start, TokenString)
			continue
		}
		if ch == '`' {
			l.scanRawAddress(start)
			continue
		}

		if l.scanOperator(start) {
			continue
		}

		l.advance()
		span := l.span(start, l.position())
		l.tokens = append(l.tokens, Token{Kind: TokenIllegal, Lexeme: string(ch), Span: span})
		l.diagnostics = append(l.diagnostics, NewDiagnostic(FileID(l.filename), span, "LEX_UNEXPECTED_CHARACTER", fmt.Sprintf("unexpected character %q", ch)))
	}

	pos := l.position()
	l.tokens = append(l.tokens, Token{Kind: TokenEOF, Span: l.span(pos, pos)})
}

func (l *lexer) skipWhitespaceAndComments() {
	for !l.atEnd() {
		switch {
		case unicode.IsSpace(rune(l.peek(0))):
			l.advance()
		case l.peek(0) == '#':
			l.skipLineComment()
		case l.peek(0) == '/' && l.peek(1) == '/':
			l.advance()
			l.advance()
			l.skipLineComment()
		case l.peek(0) == '/' && l.peek(1) == '*':
			l.skipBlockComment()
		default:
			return
		}
	}
}

func (l *lexer) skipLineComment() {
	for !l.atEnd() && l.peek(0) != '\n' {
		l.advance()
	}
}

func (l *lexer) skipBlockComment() {
	start := l.position()
	l.advance()
	l.advance()
	for !l.atEnd() {
		if l.peek(0) == '*' && l.peek(1) == '/' {
			l.advance()
			l.advance()
			return
		}
		l.advance()
	}
	l.diagnostics = append(l.diagnostics, NewDiagnostic(FileID(l.filename), l.span(start, l.position()), "LEX_UNTERMINATED_COMMENT", "unterminated block comment"))
}

func (l *lexer) scanIdentifier(start Position) {
	begin := l.offset
	for isIdentifierPart(l.peek(0)) {
		l.advance()
	}
	l.tokens = append(l.tokens, Token{
		Kind:   TokenIdentifier,
		Lexeme: l.source[begin:l.offset],
		Span:   l.span(start, l.position()),
	})
}

func (l *lexer) scanNumber(start Position) {
	begin := l.offset
	for isDigit(l.peek(0)) {
		l.advance()
	}
	if l.peek(0) == '.' && isDigit(l.peek(1)) {
		l.advance()
		for isDigit(l.peek(0)) {
			l.advance()
		}
	}
	if l.peek(0) == 'e' || l.peek(0) == 'E' {
		exponentOffset := l.offset
		exponentColumn := l.column
		l.advance()
		if l.peek(0) == '+' || l.peek(0) == '-' {
			l.advance()
		}
		if !isDigit(l.peek(0)) {
			l.offset = exponentOffset
			l.column = exponentColumn
		} else {
			for isDigit(l.peek(0)) {
				l.advance()
			}
		}
	}
	l.tokens = append(l.tokens, Token{
		Kind:   TokenNumber,
		Lexeme: l.source[begin:l.offset],
		Span:   l.span(start, l.position()),
	})
}

func (l *lexer) scanString(start Position, kind TokenKind) {
	quoteStart := l.offset
	if l.peek(0) != '"' {
		panic("scanString called outside a string")
	}
	l.advance()
	escaped := false
	for !l.atEnd() {
		ch := l.advance()
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '"' {
			raw := l.source[quoteStart:l.offset]
			value, err := strconv.Unquote(raw)
			if err != nil {
				l.diagnostics = append(l.diagnostics, NewDiagnostic(FileID(l.filename), l.span(start, l.position()), "LEX_INVALID_STRING", fmt.Sprintf("invalid string: %v", err)))
				value = raw[1 : len(raw)-1]
			}
			l.tokens = append(l.tokens, Token{
				Kind:   kind,
				Lexeme: value,
				Span:   l.span(start, l.position()),
			})
			return
		}
		if ch == '\n' {
			break
		}
	}

	span := l.span(start, l.position())
	l.tokens = append(l.tokens, Token{Kind: TokenIllegal, Span: span})
	l.diagnostics = append(l.diagnostics, NewDiagnostic(FileID(l.filename), span, "LEX_UNTERMINATED_STRING", "unterminated string"))
}

func (l *lexer) scanRawAddress(start Position) {
	l.advance()
	begin := l.offset
	for !l.atEnd() && l.peek(0) != '`' {
		l.advance()
	}
	if l.atEnd() {
		span := l.span(start, l.position())
		l.tokens = append(l.tokens, Token{Kind: TokenIllegal, Span: span})
		l.diagnostics = append(l.diagnostics, NewDiagnostic(FileID(l.filename), span, "LEX_UNTERMINATED_RAW_ADDRESS", "unterminated raw address"))
		return
	}
	value := l.source[begin:l.offset]
	l.advance()
	l.tokens = append(l.tokens, Token{Kind: TokenRawAddress, Lexeme: value, Span: l.span(start, l.position())})
}

func (l *lexer) scanOperator(start Position) bool {
	if l.sourceSlice(3) == "..." {
		l.advance()
		l.advance()
		l.advance()
		l.tokens = append(l.tokens, Token{Kind: TokenEllipsis, Lexeme: "...", Span: l.span(start, l.position())})
		return true
	}
	twoCharacter := map[string]TokenKind{
		"->": TokenArrow,
		"=>": TokenFatArrow,
		"==": TokenEqual,
		"!=": TokenNotEqual,
		"<=": TokenLessEqual,
		">=": TokenGreaterEqual,
		"&&": TokenAnd,
		"||": TokenOr,
		"??": TokenCoalesce,
	}
	if kind, ok := twoCharacter[l.sourceSlice(2)]; ok {
		lexeme := l.sourceSlice(2)
		l.advance()
		l.advance()
		l.tokens = append(l.tokens, Token{Kind: kind, Lexeme: lexeme, Span: l.span(start, l.position())})
		return true
	}

	oneCharacter := map[byte]TokenKind{
		'(': TokenLeftParen,
		')': TokenRightParen,
		'{': TokenLeftBrace,
		'}': TokenRightBrace,
		'[': TokenLeftBracket,
		']': TokenRightBracket,
		':': TokenColon,
		',': TokenComma,
		'.': TokenDot,
		';': TokenSemicolon,
		'=': TokenAssign,
		'+': TokenPlus,
		'-': TokenMinus,
		'*': TokenStar,
		'/': TokenSlash,
		'%': TokenPercent,
		'!': TokenBang,
		'<': TokenLess,
		'>': TokenGreater,
		'?': TokenQuestion,
	}
	if kind, ok := oneCharacter[l.peek(0)]; ok {
		lexeme := string(l.advance())
		l.tokens = append(l.tokens, Token{Kind: kind, Lexeme: lexeme, Span: l.span(start, l.position())})
		return true
	}
	return false
}

func (l *lexer) atEnd() bool {
	return l.offset >= len(l.source)
}

func (l *lexer) peek(ahead int) byte {
	index := l.offset + ahead
	if index < 0 || index >= len(l.source) {
		return 0
	}
	return l.source[index]
}

func (l *lexer) sourceSlice(length int) string {
	end := l.offset + length
	if end > len(l.source) {
		return ""
	}
	return l.source[l.offset:end]
}

func (l *lexer) advance() byte {
	if l.atEnd() {
		return 0
	}
	ch := l.source[l.offset]
	l.offset++
	if ch == '\n' {
		l.line++
		l.column = 1
	} else {
		l.column++
	}
	return ch
}

func (l *lexer) position() Position {
	return Position{Offset: l.offset, Line: l.line, Column: l.column}
}

func (l *lexer) span(start, end Position) Span {
	return Span{File: FileID(l.filename), Start: start, End: end}
}

func isIdentifierStart(ch byte) bool {
	return ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}

func isIdentifierPart(ch byte) bool {
	return isIdentifierStart(ch) || isDigit(ch)
}

func isDigit(ch byte) bool {
	return ch >= '0' && ch <= '9'
}
