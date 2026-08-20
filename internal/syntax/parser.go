package syntax

import (
	"encoding/json"
	"fmt"
	"strings"
)

type parser struct {
	filename    string
	source      string
	tokens      []Token
	current     int
	diagnostics []Diagnostic
}

func Parse(filename, source string) (*File, []Diagnostic) {
	tokens, diagnostics := Lex(filename, source)
	p := &parser{
		filename:    filename,
		source:      source,
		tokens:      tokens,
		diagnostics: diagnostics,
	}

	file := &File{Name: filename, Source: source}
	for !p.atEnd() {
		if p.match(TokenSemicolon) {
			continue
		}
		declaration := p.parseDeclaration()
		if declaration != nil {
			file.Declarations = append(file.Declarations, declaration)
		} else {
			p.synchronize()
		}
		p.match(TokenSemicolon)
	}
	return file, p.diagnostics
}

func ParseExpression(filename, source string) (Expression, []Diagnostic) {
	tokens, diagnostics := Lex(filename, source)
	p := &parser{
		filename:    filename,
		source:      source,
		tokens:      tokens,
		diagnostics: diagnostics,
	}
	expression := p.parseExpression()
	if !p.atEnd() {
		p.report(p.peek(), fmt.Sprintf("unexpected %q after expression", p.peek().Lexeme))
	}
	return expression, p.diagnostics
}

func (p *parser) parseDeclaration() Declaration {
	switch {
	case p.checkIdentifier("terraform"):
		return p.parseTerraformDeclaration()
	case p.checkIdentifier("provider"):
		return p.parseProviderDeclaration()
	case p.checkIdentifier("input"):
		return p.parseInputDeclaration()
	case p.checkIdentifier("let"):
		return p.parseLetDeclaration()
	case p.checkIdentifier("configure"):
		return p.parseConfigureDeclaration()
	case p.checkIdentifier("resource"):
		return p.parseResourceDeclaration()
	case p.checkIdentifier("data"):
		return p.parseDataDeclaration()
	case p.checkIdentifier("module"):
		return p.parseModuleDeclaration()
	case p.checkIdentifier("output"):
		return p.parseOutputDeclaration()
	case p.checkIdentifier("moved"):
		return p.parseMovedDeclaration()
	default:
		token := p.peek()
		p.report(token, fmt.Sprintf("expected a declaration, found %q", token.Lexeme))
		return nil
	}
}

func (p *parser) parseTerraformDeclaration() Declaration {
	start := p.advance().Span.Start
	config := p.parseObjectExpression()
	if config == nil {
		return nil
	}
	return &TerraformDeclaration{
		BaseNode: BaseNode{Span: Span{Start: start, End: config.GetSpan().End}},
		Config:   config,
	}
}

func (p *parser) parseProviderDeclaration() Declaration {
	start := p.advance().Span.Start
	name := p.expect(TokenIdentifier, "expected provider name")
	if !p.expectIdentifier("from") {
		return nil
	}
	source := p.expect(TokenString, "expected provider source string")
	version := ""
	end := source.Span.End
	if p.matchIdentifier("version") {
		token := p.expect(TokenString, "expected provider version constraint")
		version = token.Lexeme
		end = token.Span.End
	}
	return &ProviderDeclaration{
		BaseNode: BaseNode{Span: Span{Start: start, End: end}},
		Name:     name.Lexeme,
		Source:   source.Lexeme,
		Version:  version,
	}
}

func (p *parser) parseInputDeclaration() Declaration {
	start := p.advance().Span.Start
	name := p.expect(TokenIdentifier, "expected input name")
	p.expect(TokenColon, "expected ':' after input name")
	typeExpression := p.parseTypeExpression()
	if typeExpression == nil {
		return nil
	}
	var defaultValue Expression
	end := typeExpression.GetSpan().End
	if p.match(TokenAssign) {
		defaultValue = p.parseExpression()
		if defaultValue == nil {
			return nil
		}
		end = defaultValue.GetSpan().End
	}
	var metadata *ObjectExpression
	if p.matchIdentifier("with") {
		metadata = p.parseObjectExpression()
		if metadata == nil {
			return nil
		}
		end = metadata.GetSpan().End
	}
	return &InputDeclaration{
		BaseNode: BaseNode{Span: Span{Start: start, End: end}},
		Name:     name.Lexeme,
		Type:     typeExpression,
		Default:  defaultValue,
		Metadata: metadata,
	}
}

func (p *parser) parseTypeExpression() *TypeExpression {
	name := p.expect(TokenIdentifier, "expected type name")
	if name.Kind != TokenIdentifier {
		return nil
	}
	typeExpression := &TypeExpression{
		BaseNode: BaseNode{Span: name.Span},
		Name:     name.Lexeme,
	}
	if name.Lexeme == "object" && p.check(TokenLeftBrace) {
		start := p.advance()
		for !p.check(TokenRightBrace) && !p.atEnd() {
			fieldName := p.expect(TokenIdentifier, "expected object type field name")
			optional := p.match(TokenQuestion)
			p.expect(TokenColon, "expected ':' after object type field name")
			fieldType := p.parseTypeExpression()
			if fieldType == nil {
				break
			}
			var defaultValue Expression
			fieldEnd := fieldType.GetSpan().End
			if p.match(TokenAssign) {
				defaultValue = p.parseExpression()
				if defaultValue == nil {
					break
				}
				fieldEnd = defaultValue.GetSpan().End
			}
			typeExpression.Fields = append(typeExpression.Fields, TypeField{
				BaseNode: BaseNode{Span: Span{Start: fieldName.Span.Start, End: fieldEnd}},
				Name:     fieldName.Lexeme, Type: fieldType, Optional: optional, Default: defaultValue,
			})
			p.match(TokenComma, TokenSemicolon)
		}
		end := p.expect(TokenRightBrace, "expected '}' after object type")
		typeExpression.Span = Span{Start: name.Span.Start, End: end.Span.End}
		_ = start
		return typeExpression
	}
	if !p.match(TokenLess) {
		return typeExpression
	}
	for !p.check(TokenGreater) && !p.atEnd() {
		argument := p.parseTypeExpression()
		if argument == nil {
			return typeExpression
		}
		typeExpression.Arguments = append(typeExpression.Arguments, argument)
		if !p.match(TokenComma) {
			break
		}
	}
	end := p.expect(TokenGreater, "expected '>' after type arguments")
	typeExpression.Span.End = end.Span.End
	return typeExpression
}

func (p *parser) parseLetDeclaration() Declaration {
	start := p.advance().Span.Start
	name := p.expect(TokenIdentifier, "expected local name")
	p.expect(TokenAssign, "expected '=' after local name")
	value := p.parseExpression()
	if value == nil {
		return nil
	}
	return &LetDeclaration{
		BaseNode: BaseNode{Span: Span{Start: start, End: value.GetSpan().End}},
		Name:     name.Lexeme,
		Value:    value,
	}
}

func (p *parser) parseConfigureDeclaration() Declaration {
	start := p.advance().Span.Start
	name := p.expect(TokenIdentifier, "expected provider configuration name")
	p.expect(TokenAssign, "expected '=' after provider configuration name")
	providerName := p.expect(TokenIdentifier, "expected provider name")
	if !p.check(TokenLeftParen) {
		return &ConfigureDeclaration{
			BaseNode: BaseNode{Span: Span{Start: start, End: providerName.Span.End}},
			Name:     name.Lexeme, ProviderName: providerName.Lexeme, Inherited: true,
		}
	}
	p.expect(TokenLeftParen, "expected '(' after provider name")
	arguments := p.parseCallArguments()
	end := p.expect(TokenRightParen, "expected ')' after provider configuration")

	var alias Expression
	var config *ObjectExpression
	switch len(arguments) {
	case 1:
		config, _ = arguments[0].(*ObjectExpression)
	case 2:
		alias = arguments[0]
		config, _ = arguments[1].(*ObjectExpression)
	default:
		p.report(name, "provider configuration expects config or alias and config")
	}
	if config == nil {
		p.report(name, "provider configuration must end with an object")
		return nil
	}
	return &ConfigureDeclaration{
		BaseNode:     BaseNode{Span: Span{Start: start, End: end.Span.End}},
		Name:         name.Lexeme,
		ProviderName: providerName.Lexeme,
		Alias:        alias,
		Config:       config,
	}
}

func (p *parser) parseResourceDeclaration() Declaration {
	start := p.advance().Span.Start
	name := p.expect(TokenIdentifier, "expected resource name")
	p.expect(TokenAssign, "expected '=' after resource name")
	value := p.parseExpression()
	call, ok := value.(*CallExpression)
	if !ok {
		p.report(name, "resource value must be a provider resource call")
		return nil
	}
	member, ok := call.Callee.(*MemberExpression)
	if !ok {
		p.report(name, "resource call must use providerConfig.resourceKind(...)")
		return nil
	}
	provider, ok := member.Target.(*IdentifierExpression)
	if !ok {
		p.report(name, "resource call must start with a provider configuration name")
		return nil
	}
	if len(call.Arguments) < 2 || len(call.Arguments) > 3 {
		p.report(name, "resource call expects label, arguments, and optional meta-arguments")
		return nil
	}
	label, ok := stringLiteral(call.Arguments[0])
	if !ok || label == "" {
		p.report(name, "resource label must be a non-empty literal string")
		return nil
	}
	if !IsTerraformIdentifier(label) {
		p.report(name, fmt.Sprintf("resource label %q is not a valid Terraform identifier", label))
		return nil
	}
	arguments, ok := call.Arguments[1].(*ObjectExpression)
	if !ok {
		p.report(name, "resource arguments must be an object")
		return nil
	}
	var meta *ObjectExpression
	if len(call.Arguments) == 3 {
		meta, ok = call.Arguments[2].(*ObjectExpression)
		if !ok {
			p.report(name, "resource meta-arguments must be an object")
			return nil
		}
	}
	return &ResourceDeclaration{
		BaseNode:           BaseNode{Span: Span{Start: start, End: call.GetSpan().End}},
		Name:               name.Lexeme,
		ProviderConfigName: provider.Name,
		Kind:               member.Name,
		Label:              label,
		Arguments:          arguments,
		MetaArguments:      meta,
	}
}

func (p *parser) parseDataDeclaration() Declaration {
	start := p.advance().Span.Start
	name := p.expect(TokenIdentifier, "expected data source name")
	p.expect(TokenAssign, "expected '=' after data source name")
	value := p.parseExpression()
	call, ok := value.(*CallExpression)
	if !ok {
		p.report(name, "data source value must be a provider data source call")
		return nil
	}
	member, ok := call.Callee.(*MemberExpression)
	if !ok {
		p.report(name, "data source call must use providerConfig.dataSourceKind(...)")
		return nil
	}
	provider, ok := member.Target.(*IdentifierExpression)
	if !ok {
		p.report(name, "data source call must start with a provider configuration name")
		return nil
	}
	if len(call.Arguments) != 2 {
		p.report(name, "data source call expects a label and arguments")
		return nil
	}
	label, ok := stringLiteral(call.Arguments[0])
	if !ok || label == "" {
		p.report(name, "data source label must be a non-empty literal string")
		return nil
	}
	if !IsTerraformIdentifier(label) {
		p.report(name, fmt.Sprintf("data source label %q is not a valid Terraform identifier", label))
		return nil
	}
	arguments, ok := call.Arguments[1].(*ObjectExpression)
	if !ok {
		p.report(name, "data source arguments must be an object")
		return nil
	}
	return &DataDeclaration{
		BaseNode:           BaseNode{Span: Span{Start: start, End: call.GetSpan().End}},
		Name:               name.Lexeme,
		ProviderConfigName: provider.Name,
		Kind:               member.Name,
		Label:              label,
		Arguments:          arguments,
	}
}

func (p *parser) parseModuleDeclaration() Declaration {
	start := p.advance().Span.Start
	name := p.expect(TokenIdentifier, "expected module name")
	label := p.expect(TokenString, "expected static Terraform module label")
	if !p.expectIdentifier("from") {
		return nil
	}
	source := p.expect(TokenString, "expected module source")
	arguments := p.parseObjectExpression()
	if arguments == nil {
		return nil
	}
	var providers *ObjectExpression
	var meta *ObjectExpression
	end := arguments.GetSpan().End
	for p.checkIdentifier("using") || p.checkIdentifier("with") {
		if p.matchIdentifier("using") {
			providers = p.parseObjectExpression()
			if providers == nil {
				return nil
			}
			end = providers.GetSpan().End
		} else if p.matchIdentifier("with") {
			meta = p.parseObjectExpression()
			if meta == nil {
				return nil
			}
			end = meta.GetSpan().End
		}
	}
	if label.Lexeme == "" {
		p.report(label, "module label must not be empty")
	}
	if !IsTerraformIdentifier(label.Lexeme) {
		p.report(label, fmt.Sprintf("module label %q is not a valid Terraform identifier", label.Lexeme))
		return nil
	}
	return &ModuleDeclaration{
		BaseNode:      BaseNode{Span: Span{Start: start, End: end}},
		Name:          name.Lexeme,
		Label:         label.Lexeme,
		Source:        source.Lexeme,
		Arguments:     arguments,
		Providers:     providers,
		MetaArguments: meta,
	}
}

func (p *parser) parseOutputDeclaration() Declaration {
	start := p.advance().Span.Start
	name := p.expect(TokenIdentifier, "expected output name")
	p.expect(TokenAssign, "expected '=' after output name")
	value := p.parseExpression()
	if value == nil {
		return nil
	}
	end := value.GetSpan().End
	var metadata *ObjectExpression
	if p.matchIdentifier("with") {
		metadata = p.parseObjectExpression()
		if metadata == nil {
			return nil
		}
		end = metadata.GetSpan().End
	}
	return &OutputDeclaration{
		BaseNode: BaseNode{Span: Span{Start: start, End: end}},
		Name:     name.Lexeme,
		Value:    value,
		Metadata: metadata,
	}
}

func (p *parser) parseMovedDeclaration() Declaration {
	start := p.advance().Span.Start
	if !p.expectIdentifier("from") {
		return nil
	}
	from := p.expect(TokenString, "expected literal source address")
	if !p.expectIdentifier("to") {
		return nil
	}
	to := p.expect(TokenString, "expected literal destination address")
	if from.Lexeme == "" || to.Lexeme == "" {
		p.report(from, "moved addresses must not be empty")
	}
	return &MovedDeclaration{BaseNode: BaseNode{Span: Span{Start: start, End: to.Span.End}}, From: from.Lexeme, To: to.Lexeme}
}

func (p *parser) parseExpression() Expression {
	expression := p.parseBinaryExpression(1)
	if expression == nil {
		return nil
	}
	if p.match(TokenQuestion) {
		thenExpression := p.parseExpression()
		p.expect(TokenColon, "expected ':' in conditional expression")
		elseExpression := p.parseExpression()
		if thenExpression == nil || elseExpression == nil {
			return expression
		}
		expression = &ConditionalExpression{
			BaseNode:  BaseNode{Span: Span{Start: expression.GetSpan().Start, End: elseExpression.GetSpan().End}},
			Condition: expression,
			Then:      thenExpression,
			Else:      elseExpression,
		}
	}
	return expression
}

func (p *parser) parseBinaryExpression(minimumPrecedence int) Expression {
	left := p.parseUnaryExpression()
	if left == nil {
		return nil
	}
	for {
		operator := p.peek()
		precedence := binaryPrecedence(operator.Kind)
		if precedence < minimumPrecedence {
			break
		}
		p.advance()
		right := p.parseBinaryExpression(precedence + 1)
		if right == nil {
			return left
		}
		left = &BinaryExpression{
			BaseNode: BaseNode{Span: Span{Start: left.GetSpan().Start, End: right.GetSpan().End}},
			Left:     left,
			Operator: operator.Kind,
			Right:    right,
		}
	}
	return left
}

func (p *parser) parseUnaryExpression() Expression {
	if p.match(TokenBang, TokenMinus) {
		operator := p.previous()
		operand := p.parseUnaryExpression()
		if operand == nil {
			return nil
		}
		return &UnaryExpression{
			BaseNode: BaseNode{Span: Span{Start: operator.Span.Start, End: operand.GetSpan().End}},
			Operator: operator.Kind,
			Operand:  operand,
		}
	}
	return p.parsePostfixExpression()
}

func (p *parser) parsePostfixExpression() Expression {
	expression := p.parsePrimaryExpression()
	if expression == nil {
		return nil
	}
	for {
		switch {
		case p.match(TokenDot):
			name := p.expect(TokenIdentifier, "expected member name after '.'")
			expression = &MemberExpression{
				BaseNode: BaseNode{Span: Span{Start: expression.GetSpan().Start, End: name.Span.End}},
				Target:   expression,
				Name:     name.Lexeme,
			}
		case p.match(TokenLeftBracket):
			index := p.parseExpression()
			end := p.expect(TokenRightBracket, "expected ']' after index")
			expression = &IndexExpression{
				BaseNode: BaseNode{Span: Span{Start: expression.GetSpan().Start, End: end.Span.End}},
				Target:   expression,
				Index:    index,
			}
		case p.match(TokenLeftParen):
			arguments := p.parseCallArguments()
			end := p.expect(TokenRightParen, "expected ')' after arguments")
			expression = &CallExpression{
				BaseNode:  BaseNode{Span: Span{Start: expression.GetSpan().Start, End: end.Span.End}},
				Callee:    expression,
				Arguments: arguments,
			}
		default:
			return expression
		}
	}
}

func (p *parser) parseCallArguments() []Expression {
	var arguments []Expression
	for !p.check(TokenRightParen) && !p.atEnd() {
		argument := p.parseExpression()
		if argument == nil {
			break
		}
		arguments = append(arguments, argument)
		if !p.match(TokenComma) {
			break
		}
	}
	return arguments
}

func (p *parser) parsePrimaryExpression() Expression {
	token := p.peek()
	switch token.Kind {
	case TokenNumber:
		p.advance()
		return &LiteralExpression{BaseNode: BaseNode{Span: token.Span}, Value: json.Number(token.Lexeme)}
	case TokenString:
		p.advance()
		return &LiteralExpression{BaseNode: BaseNode{Span: token.Span}, Value: token.Lexeme}
	case TokenFString:
		p.advance()
		return p.parseTemplateExpression(token)
	case TokenIdentifier:
		p.advance()
		switch token.Lexeme {
		case "true":
			return &LiteralExpression{BaseNode: BaseNode{Span: token.Span}, Value: true}
		case "false":
			return &LiteralExpression{BaseNode: BaseNode{Span: token.Span}, Value: false}
		case "null", "none":
			return &LiteralExpression{BaseNode: BaseNode{Span: token.Span}, Value: nil}
		default:
			return &IdentifierExpression{BaseNode: BaseNode{Span: token.Span}, Name: token.Lexeme}
		}
	case TokenLeftParen:
		start := p.advance().Span.Start
		expression := p.parseExpression()
		end := p.expect(TokenRightParen, "expected ')' after expression")
		if expression != nil {
			switch value := expression.(type) {
			case *IdentifierExpression:
				value.Span = Span{Start: start, End: end.Span.End}
			}
		}
		return expression
	case TokenLeftBracket:
		if p.current+1 < len(p.tokens) && p.tokens[p.current+1].IsIdentifier("for") {
			return p.parseForExpression(false)
		}
		return p.parseArrayExpression()
	case TokenLeftBrace:
		if p.current+1 < len(p.tokens) && p.tokens[p.current+1].IsIdentifier("for") {
			return p.parseForExpression(true)
		}
		return p.parseObjectExpression()
	default:
		p.report(token, fmt.Sprintf("expected expression, found %s", token.Kind))
		if !p.atEnd() {
			p.advance()
		}
		return nil
	}
}

func (p *parser) parseForExpression(object bool) Expression {
	open := TokenLeftBracket
	close := TokenRightBracket
	if object {
		open, close = TokenLeftBrace, TokenRightBrace
	}
	start := p.expect(open, "expected comprehension opening delimiter").Span.Start
	p.expectIdentifier("for")
	first := p.expect(TokenIdentifier, "expected iterator name")
	keyVariable := ""
	valueVariable := first.Lexeme
	if p.match(TokenComma) {
		keyVariable = first.Lexeme
		valueVariable = p.expect(TokenIdentifier, "expected value iterator name").Lexeme
	}
	p.expectIdentifier("in")
	collection := p.parseExpression()
	p.expect(TokenColon, "expected ':' after comprehension collection")
	var key Expression
	value := p.parseExpression()
	if object {
		key = value
		p.expect(TokenArrow, "expected '=>' in object comprehension")
		value = p.parseExpression()
	}
	var condition Expression
	if p.matchIdentifier("if") {
		condition = p.parseExpression()
	}
	end := p.expect(close, "expected comprehension closing delimiter")
	return &ForExpression{
		BaseNode: BaseNode{Span: Span{Start: start, End: end.Span.End}}, KeyVariable: keyVariable,
		ValueVariable: valueVariable, Collection: collection, Key: key, Value: value, Condition: condition, Object: object,
	}
}

func (p *parser) parseArrayExpression() Expression {
	start := p.expect(TokenLeftBracket, "expected '['").Span.Start
	var items []Expression
	for !p.check(TokenRightBracket) && !p.atEnd() {
		item := p.parseExpression()
		if item == nil {
			break
		}
		items = append(items, item)
		if !p.match(TokenComma) {
			break
		}
	}
	end := p.expect(TokenRightBracket, "expected ']' after array")
	return &ArrayExpression{
		BaseNode: BaseNode{Span: Span{Start: start, End: end.Span.End}},
		Items:    items,
	}
}

func (p *parser) parseObjectExpression() *ObjectExpression {
	start := p.expect(TokenLeftBrace, "expected '{'")
	if start.Kind != TokenLeftBrace {
		return nil
	}
	var fields []ObjectField
	for !p.check(TokenRightBrace) && !p.atEnd() {
		name := p.peek()
		if name.Kind != TokenIdentifier && name.Kind != TokenString {
			p.report(name, "expected object field name")
			p.advance()
			continue
		}
		p.advance()
		p.expect(TokenColon, "expected ':' after object field name")
		value := p.parseExpression()
		if value == nil {
			break
		}
		fields = append(fields, ObjectField{
			BaseNode: BaseNode{Span: Span{Start: name.Span.Start, End: value.GetSpan().End}},
			Name:     name.Lexeme,
			Quoted:   name.Kind == TokenString,
			Value:    value,
		})
		p.match(TokenComma, TokenSemicolon)
	}
	end := p.expect(TokenRightBrace, "expected '}' after object")
	return &ObjectExpression{
		BaseNode: BaseNode{Span: Span{Start: start.Span.Start, End: end.Span.End}},
		Fields:   fields,
	}
}

func (p *parser) parseTemplateExpression(token Token) Expression {
	value := token.Lexeme
	var parts []TemplatePart
	var text strings.Builder

	flushText := func() {
		if text.Len() > 0 {
			parts = append(parts, TemplatePart{Text: text.String()})
			text.Reset()
		}
	}

	for index := 0; index < len(value); {
		switch {
		case value[index] == '{' && index+1 < len(value) && value[index+1] == '{':
			text.WriteByte('{')
			index += 2
		case value[index] == '}' && index+1 < len(value) && value[index+1] == '}':
			text.WriteByte('}')
			index += 2
		case value[index] == '{':
			end := interpolationEnd(value, index+1)
			if end < 0 {
				p.report(token, "unterminated interpolation in formatted string")
				text.WriteString(value[index:])
				index = len(value)
				continue
			}
			flushText()
			expressionSource := strings.TrimSpace(value[index+1 : end])
			if expressionSource == "" {
				p.report(token, "empty interpolation in formatted string")
			} else {
				expression, diagnostics := ParseExpression(p.filename, expressionSource)
				if len(diagnostics) > 0 {
					for _, diagnostic := range diagnostics {
						diagnostic.Span = token.Span
						p.diagnostics = append(p.diagnostics, diagnostic)
					}
				} else {
					parts = append(parts, TemplatePart{Expression: expression})
				}
			}
			index = end + 1
		case value[index] == '}':
			p.report(token, "unmatched '}' in formatted string; use '}}' for a literal brace")
			text.WriteByte('}')
			index++
		default:
			text.WriteByte(value[index])
			index++
		}
	}
	flushText()
	return &TemplateExpression{BaseNode: BaseNode{Span: token.Span}, Parts: parts}
}

func interpolationEnd(value string, start int) int {
	depth := 0
	inString := false
	escaped := false
	for index := start; index < len(value); index++ {
		ch := value[index]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			if ch == '}' && depth == 0 {
				return index
			}
			if depth > 0 {
				depth--
			}
		}
	}
	return -1
}

func binaryPrecedence(kind TokenKind) int {
	switch kind {
	case TokenCoalesce:
		return 1
	case TokenOr:
		return 2
	case TokenAnd:
		return 3
	case TokenEqual, TokenNotEqual:
		return 4
	case TokenLess, TokenLessEqual, TokenGreater, TokenGreaterEqual:
		return 5
	case TokenPlus, TokenMinus:
		return 6
	case TokenStar, TokenSlash, TokenPercent:
		return 7
	default:
		return 0
	}
}

func stringLiteral(expression Expression) (string, bool) {
	literal, ok := expression.(*LiteralExpression)
	if !ok {
		return "", false
	}
	value, ok := literal.Value.(string)
	return value, ok
}

func (p *parser) synchronize() {
	for !p.atEnd() {
		if p.previous().Kind == TokenSemicolon {
			return
		}
		if p.peek().Kind == TokenIdentifier {
			switch p.peek().Lexeme {
			case "terraform", "provider", "input", "let", "configure", "resource", "data", "module", "output", "moved":
				return
			}
		}
		p.advance()
	}
}

func (p *parser) report(token Token, message string) {
	p.diagnostics = append(p.diagnostics, Diagnostic{
		Filename: p.filename,
		Span:     token.Span,
		Message:  message,
	})
}

func (p *parser) expect(kind TokenKind, message string) Token {
	if p.check(kind) {
		return p.advance()
	}
	token := p.peek()
	p.report(token, message)
	if !p.atEnd() {
		p.advance()
	}
	return token
}

func (p *parser) expectIdentifier(value string) bool {
	if p.matchIdentifier(value) {
		return true
	}
	p.report(p.peek(), fmt.Sprintf("expected %q", value))
	return false
}

func (p *parser) match(kinds ...TokenKind) bool {
	for _, kind := range kinds {
		if p.check(kind) {
			p.advance()
			return true
		}
	}
	return false
}

func (p *parser) matchIdentifier(value string) bool {
	if p.checkIdentifier(value) {
		p.advance()
		return true
	}
	return false
}

func (p *parser) check(kind TokenKind) bool {
	return p.peek().Kind == kind
}

func (p *parser) checkIdentifier(value string) bool {
	return p.peek().IsIdentifier(value)
}

func (p *parser) advance() Token {
	if !p.atEnd() {
		p.current++
	}
	return p.previous()
}

func (p *parser) atEnd() bool {
	return p.peek().Kind == TokenEOF
}

func (p *parser) peek() Token {
	return p.tokens[p.current]
}

func (p *parser) previous() Token {
	if p.current == 0 {
		return p.tokens[0]
	}
	return p.tokens[p.current-1]
}
