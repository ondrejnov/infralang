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

	file := &File{Name: filename, ID: FileID(filename), Source: source}
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
	return file, SortDiagnostics(p.diagnostics)
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
	return expression, SortDiagnostics(p.diagnostics)
}

func (p *parser) parseDeclaration() Declaration {
	switch {
	case p.checkIdentifier("terraform"):
		return p.parseTerraformDeclaration()
	case p.checkIdentifier("provider"):
		return p.parseProviderDeclaration()
	case p.checkIdentifier("input"):
		return p.parseInputDeclaration()
	case p.checkIdentifier("type"):
		return p.parseTypeAliasDeclaration(false, p.peek().Span.Start)
	case p.checkIdentifier("export"):
		return p.parseExportTypeAliasDeclaration()
	case p.checkIdentifier("import"):
		return p.parseImportDeclaration()
	case p.checkIdentifier("const"):
		return p.parseConstDeclaration()
	case p.checkIdentifier("static"):
		return p.parseStaticForDeclaration()
	case p.checkIdentifier("component"):
		return p.parseComponentDefinition()
	case p.checkIdentifier("instantiate"):
		return p.parseComponentInstance()
	case p.checkIdentifier("let"):
		return p.parseLetDeclaration()
	case p.checkIdentifier("if"):
		return p.parseIfDeclaration()
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

func (p *parser) parseConstDeclaration() Declaration {
	start := p.advance().Span.Start
	name := p.expect(TokenIdentifier, "expected constant name")
	var typeExpression *TypeExpression
	if p.match(TokenColon) {
		typeExpression = p.parseTypeExpression()
		if typeExpression == nil {
			return nil
		}
	}
	p.expect(TokenAssign, "expected '=' after constant name")
	value := p.parseExpression()
	if value == nil {
		return nil
	}
	return &ConstDeclaration{BaseNode: p.base(start, value.GetSpan().End), Name: name.Lexeme, Type: typeExpression, Value: value}
}

func (p *parser) parseStaticForDeclaration() Declaration {
	start := p.advance().Span.Start
	if !p.expectIdentifier("for") {
		return nil
	}
	first := p.expect(TokenIdentifier, "expected static iterator name")
	keyVariable := ""
	valueVariable := first.Lexeme
	if p.match(TokenComma) {
		keyVariable = first.Lexeme
		valueVariable = p.expect(TokenIdentifier, "expected static value iterator name").Lexeme
	}
	if !p.expectIdentifier("in") {
		return nil
	}
	collection := p.parseExpression()
	if collection == nil {
		return nil
	}
	p.expect(TokenLeftBrace, "expected '{' before static loop declarations")
	var declarations []Declaration
	for !p.check(TokenRightBrace) && !p.atEnd() {
		if p.match(TokenSemicolon) {
			continue
		}
		declaration := p.parseDeclaration()
		if declaration != nil {
			declarations = append(declarations, declaration)
		} else {
			p.synchronize()
		}
		p.match(TokenSemicolon)
	}
	end := p.expect(TokenRightBrace, "expected '}' after static loop declarations")
	return &StaticForDeclaration{
		BaseNode: p.base(start, end.Span.End), KeyVariable: keyVariable,
		ValueVariable: valueVariable, Collection: collection, Declarations: declarations,
	}
}

func (p *parser) parseComponentDefinition() Declaration {
	start := p.advance().Span.Start
	name := p.expect(TokenIdentifier, "expected component name")
	p.expect(TokenLeftParen, "expected '(' after component name")
	var parameters []ComponentParameter
	for !p.check(TokenRightParen) && !p.atEnd() {
		parameterName := p.expect(TokenIdentifier, "expected component parameter name")
		p.expect(TokenColon, "expected ':' after component parameter name")
		parameterType := p.parseTypeExpression()
		if parameterType == nil {
			return nil
		}
		parameters = append(parameters, ComponentParameter{
			BaseNode: p.base(parameterName.Span.Start, parameterType.GetSpan().End), Name: parameterName.Lexeme, Type: parameterType,
		})
		if !p.match(TokenComma) && !p.check(TokenRightParen) {
			p.report(p.peek(), "expected ',' between component parameters")
			break
		}
	}
	p.expect(TokenRightParen, "expected ')' after component parameters")
	var providers []ComponentProviderParameter
	if p.matchIdentifier("using") {
		p.expect(TokenLeftBrace, "expected '{' after component using")
		for !p.check(TokenRightBrace) && !p.atEnd() {
			parameterName := p.expect(TokenIdentifier, "expected component provider parameter name")
			p.expect(TokenColon, "expected ':' after component provider parameter name")
			providerName := p.expect(TokenIdentifier, "expected provider declaration name")
			providers = append(providers, ComponentProviderParameter{
				BaseNode: p.base(parameterName.Span.Start, providerName.Span.End), Name: parameterName.Lexeme, ProviderName: providerName.Lexeme,
			})
			if !p.match(TokenComma) && !p.check(TokenRightBrace) {
				p.report(p.peek(), "expected ',' between component provider parameters")
				break
			}
		}
		p.expect(TokenRightBrace, "expected '}' after component provider parameters")
	}
	p.expect(TokenLeftBrace, "expected '{' before component body")
	var declarations []Declaration
	for !p.check(TokenRightBrace) && !p.atEnd() {
		if p.match(TokenSemicolon) {
			continue
		}
		var declaration Declaration
		if p.checkIdentifier("export") {
			declaration = p.parseComponentExport()
		} else {
			declaration = p.parseDeclaration()
		}
		if declaration != nil {
			declarations = append(declarations, declaration)
		} else {
			p.synchronize()
		}
		p.match(TokenSemicolon)
	}
	end := p.expect(TokenRightBrace, "expected '}' after component body")
	return &ComponentDefinition{
		BaseNode: p.base(start, end.Span.End), Name: name.Lexeme,
		Parameters: parameters, Providers: providers, Declarations: declarations,
	}
}

func (p *parser) parseComponentExport() Declaration {
	start := p.advance().Span.Start
	name := p.expect(TokenIdentifier, "expected component export name")
	p.expect(TokenAssign, "expected '=' after component export name")
	value := p.parseExpression()
	if value == nil {
		return nil
	}
	return &ComponentExport{BaseNode: p.base(start, value.GetSpan().End), Name: name.Lexeme, Value: value}
}

func (p *parser) parseComponentInstance() Declaration {
	start := p.advance().Span.Start
	name := p.expect(TokenIdentifier, "expected component instance name")
	var index Expression
	if p.match(TokenLeftBracket) {
		index = p.parseExpression()
		p.expect(TokenRightBracket, "expected ']' after component instance index")
	}
	p.expect(TokenAssign, "expected '=' after component instance name")
	componentName := p.expect(TokenIdentifier, "expected component name")
	arguments := p.parseComponentArguments()
	if arguments == nil {
		return nil
	}
	var providers *ObjectExpression
	end := arguments.GetSpan().End
	if p.matchIdentifier("using") {
		providers = p.parseObjectExpression()
		if providers == nil {
			return nil
		}
		end = providers.GetSpan().End
	}
	return &ComponentInstance{
		BaseNode: p.base(start, end), Name: name.Lexeme, Index: index,
		ComponentName: componentName.Lexeme, Arguments: arguments, Providers: providers,
	}
}

func (p *parser) parseComponentArguments() *ObjectExpression {
	start := p.expect(TokenLeftParen, "expected '(' after component name")
	if start.Kind != TokenLeftParen {
		return nil
	}
	result := &ObjectExpression{BaseNode: p.baseSpan(start.Span)}
	for !p.check(TokenRightParen) && !p.atEnd() {
		name := p.expect(TokenIdentifier, "expected component argument name")
		p.expect(TokenColon, "expected ':' after component argument name")
		value := p.parseExpression()
		if value == nil {
			return nil
		}
		field := ObjectField{
			BaseNode: p.base(name.Span.Start, value.GetSpan().End), Name: name.Lexeme,
			WireName: SourceNameToWire(name.Lexeme), Value: value,
		}
		result.Fields = append(result.Fields, field)
		result.Items = append(result.Items, field)
		if !p.match(TokenComma) && !p.check(TokenRightParen) {
			p.report(p.peek(), "expected ',' between component arguments")
			break
		}
	}
	end := p.expect(TokenRightParen, "expected ')' after component arguments")
	result.Span = p.span(start.Span.Start, end.Span.End)
	return result
}

func (p *parser) parseExportTypeAliasDeclaration() Declaration {
	start := p.advance().Span.Start
	if !p.checkIdentifier("type") {
		p.report(p.peek(), "export accepts type aliases only")
		return nil
	}
	return p.parseTypeAliasDeclaration(true, start)
}

func (p *parser) parseTypeAliasDeclaration(exported bool, start Position) Declaration {
	p.advance()
	name := p.expect(TokenIdentifier, "expected type alias name")
	p.expect(TokenAssign, "expected '=' after type alias name")
	typeExpression := p.parseTypeExpression()
	if typeExpression == nil {
		return nil
	}
	return &TypeAliasDeclaration{
		BaseNode: p.base(start, typeExpression.GetSpan().End),
		Name:     name.Lexeme,
		Type:     typeExpression,
		Exported: exported,
	}
}

func (p *parser) parseImportDeclaration() Declaration {
	start := p.advance().Span.Start
	switch {
	case p.checkIdentifier("type"):
		return p.parseTypeImportDeclaration(start)
	case p.checkIdentifier("module"):
		return p.parseModuleImportDeclaration(start)
	default:
		p.report(p.peek(), "expected 'type' or 'module' after 'import'")
		return nil
	}
}

func (p *parser) parseTypeImportDeclaration(start Position) Declaration {
	p.advance()
	p.expect(TokenLeftBrace, "expected '{' after 'import type'")
	var items []TypeImportItem
	localNames := make(map[string]Token)
	for !p.check(TokenRightBrace) && !p.atEnd() {
		imported := p.expect(TokenIdentifier, "expected imported type name")
		local := imported
		end := imported.Span.End
		if p.matchIdentifier("as") {
			local = p.expect(TokenIdentifier, "expected local type name after 'as'")
			end = local.Span.End
		}
		if previous, duplicate := localNames[local.Lexeme]; duplicate {
			p.report(previous, fmt.Sprintf("imported local type name %q is duplicated", local.Lexeme))
			p.report(local, fmt.Sprintf("imported local type name %q is duplicated", local.Lexeme))
		} else {
			localNames[local.Lexeme] = local
		}
		items = append(items, TypeImportItem{
			BaseNode: p.base(imported.Span.Start, end), ImportedName: imported.Lexeme, LocalName: local.Lexeme,
		})
		if !p.match(TokenComma) && !p.check(TokenRightBrace) {
			p.report(p.peek(), "expected ',' between imported type names")
			break
		}
	}
	close := p.expect(TokenRightBrace, "expected '}' after imported type names")
	if len(items) == 0 {
		p.report(close, "import type requires at least one imported name")
	}
	if !p.expectIdentifier("from") {
		return nil
	}
	path := p.expect(TokenString, "expected relative .infra import path")
	return &TypeImportDeclaration{BaseNode: p.base(start, path.Span.End), Items: items, Path: path.Lexeme}
}

func (p *parser) parseModuleImportDeclaration(start Position) Declaration {
	p.advance()
	name := p.expect(TokenIdentifier, "expected imported module name")
	if !p.expectIdentifier("from") {
		return nil
	}
	source := p.expect(TokenString, "expected module source")
	declaration := &ModuleImportDeclaration{BaseNode: p.base(start, source.Span.End), Name: name.Lexeme, Source: source.Lexeme}
	if p.checkIdentifier("version") && p.lookaheadKind(1) == TokenString {
		p.advance()
		version := p.expect(TokenString, "expected version constraint string after 'version'")
		if version.Lexeme == "" {
			p.report(version, "module import version constraint must not be empty")
		}
		declaration.Version = version.Lexeme
		declaration.BaseNode = p.base(start, version.Span.End)
	}
	return declaration
}

func (p *parser) parseTerraformDeclaration() Declaration {
	start := p.advance().Span.Start
	open := p.expect(TokenLeftBrace, "expected '{' after terraform")
	if open.Kind != TokenLeftBrace {
		return nil
	}
	config := &ObjectExpression{BaseNode: BaseNode{File: FileID(p.filename), Span: open.Span}}
	var blocks []*TerraformBlockClause
	backendCount := 0
	seenCloud := false
	for !p.check(TokenRightBrace) && !p.atEnd() {
		if clause, ok := p.parseTerraformBlockClause(backendCount, seenCloud); ok {
			if clause != nil {
				blocks = append(blocks, clause)
				if clause.Kind == "backend" {
					backendCount++
				} else {
					seenCloud = true
				}
			}
			if !p.match(TokenComma, TokenSemicolon) && !p.check(TokenRightBrace) {
				p.report(p.peek(), "expected ',' or ';' between terraform settings")
			}
			continue
		}
		item, ok := p.parseObjectItem()
		if !ok {
			break
		}
		config.Items = append(config.Items, item)
		if field, ok := item.(ObjectField); ok {
			config.Fields = append(config.Fields, field)
		}
		if !p.match(TokenComma, TokenSemicolon) && !p.check(TokenRightBrace) {
			p.report(p.peek(), "expected ',' or ';' between terraform settings")
		}
	}
	end := p.expect(TokenRightBrace, "expected '}' after terraform block")
	config.Span = Span{File: FileID(p.filename), Start: open.Span.Start, End: end.Span.End}
	return &TerraformDeclaration{
		BaseNode: p.base(start, end.Span.End),
		Config:   config,
		Blocks:   blocks,
	}
}

func (p *parser) parseTerraformBlockClause(backendCount int, seenCloud bool) (*TerraformBlockClause, bool) {
	token := p.peek()
	var kind string
	switch {
	case token.IsIdentifier("backend") && p.lookaheadKind(1) == TokenIdentifier:
		kind = "backend"
	case token.IsIdentifier("cloud") && p.lookaheadKind(1) == TokenAssign:
		kind = "cloud"
	default:
		return nil, false
	}
	start := p.advance().Span.Start
	name := ""
	if kind == "backend" {
		nameToken := p.advance()
		name = nameToken.Lexeme
		if !IsTerraformIdentifier(name) {
			p.report(nameToken, fmt.Sprintf("backend type %q is not a valid Terraform identifier", name))
		}
		if backendCount >= 1 {
			p.report(nameToken, "only one terraform backend is allowed")
		}
	} else if seenCloud {
		p.report(token, "cloud configuration is already declared")
	}
	if !p.match(TokenAssign) {
		p.report(p.peek(), fmt.Sprintf("expected '=' after terraform %s", kind))
	}
	config := p.parseObjectExpression()
	if config == nil {
		return nil, true
	}
	return &TerraformBlockClause{
		BaseNode: p.base(start, config.GetSpan().End),
		Kind:     kind,
		Name:     name,
		Config:   config,
	}, true
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
		BaseNode: p.base(start, end),
		Name:     name.Lexeme,
		Source:   source.Lexeme,
		Version:  version,
	}
}

func (p *parser) parseInputDeclaration() Declaration {
	start := p.advance().Span.Start
	name := p.expect(TokenIdentifier, "expected input name")
	wireName := SourceNameToWire(name.Lexeme)
	explicitWire := false
	if p.check(TokenString) {
		wire := p.advance()
		wireName = wire.Lexeme
		explicitWire = true
		if wireName == "" || !IsTerraformIdentifier(wireName) {
			p.report(wire, fmt.Sprintf("input wire name %q is not a valid Terraform identifier", wireName))
		}
	}
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
	var metadataItems []InputMetadataItem
	if p.matchIdentifier("with") {
		metadata, metadataItems = p.parseInputMetadata()
		if metadata == nil {
			return nil
		}
		end = metadata.GetSpan().End
	}
	return &InputDeclaration{
		BaseNode:      p.base(start, end),
		Name:          name.Lexeme,
		WireName:      wireName,
		ExplicitWire:  explicitWire,
		Type:          typeExpression,
		Default:       defaultValue,
		Metadata:      metadata,
		MetadataItems: metadataItems,
	}
}

func (p *parser) parseTypeExpression() *TypeExpression {
	first := p.parsePrimaryTypeExpression()
	if first == nil || !p.match(TokenAmpersand) {
		return first
	}
	operands := []*TypeExpression{first}
	for {
		operand := p.parsePrimaryTypeExpression()
		if operand == nil {
			return first
		}
		operands = append(operands, operand)
		if !p.match(TokenAmpersand) {
			return &TypeExpression{
				BaseNode: p.base(first.GetSpan().Start, operand.GetSpan().End),
				Operands: operands,
			}
		}
	}
}

func (p *parser) parsePrimaryTypeExpression() *TypeExpression {
	name := p.expect(TokenIdentifier, "expected type name")
	if name.Kind != TokenIdentifier {
		return nil
	}
	typeExpression := &TypeExpression{
		BaseNode: p.baseSpan(name.Span),
		Name:     name.Lexeme,
	}
	if name.Lexeme == "object" && p.check(TokenLeftBrace) {
		start := p.advance()
		for !p.check(TokenRightBrace) && !p.atEnd() {
			fieldName := p.peek()
			if fieldName.Kind != TokenIdentifier && fieldName.Kind != TokenString {
				p.report(fieldName, "expected object type field name")
				break
			}
			p.advance()
			wireName := fieldName.Lexeme
			explicitWire := false
			if fieldName.Kind == TokenIdentifier && p.check(TokenString) {
				wire := p.advance()
				wireName = wire.Lexeme
				explicitWire = true
			}
			if fieldName.Kind == TokenIdentifier && !explicitWire {
				wireName = SourceNameToWire(fieldName.Lexeme)
			}
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
				BaseNode: p.base(fieldName.Span.Start, fieldEnd),
				Name:     fieldName.Lexeme, WireName: wireName, ExplicitWire: explicitWire,
				Quoted: fieldName.Kind == TokenString, Type: fieldType, Optional: optional, Default: defaultValue,
			})
			if !p.match(TokenComma, TokenSemicolon) && !p.check(TokenRightBrace) {
				p.report(p.peek(), "expected ',' or ';' between object type fields")
			}
		}
		end := p.expect(TokenRightBrace, "expected '}' after object type")
		typeExpression.Span = p.span(name.Span.Start, end.Span.End)
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
		BaseNode: p.base(start, value.GetSpan().End),
		Name:     name.Lexeme,
		Value:    value,
	}
}

func (p *parser) parseIfDeclaration() Declaration {
	start := p.advance()
	p.expect(TokenLeftParen, "expected '(' after 'if'")
	condition := p.parseExpression()
	if condition == nil {
		return nil
	}
	p.expect(TokenRightParen, "expected ')' after if condition")
	p.expect(TokenLeftBrace, "expected '{' before if body")
	var assignments []LetAssignment
	diagnosticsBeforeBody := len(p.diagnostics)
	for !p.check(TokenRightBrace) && !p.atEnd() {
		if p.match(TokenSemicolon) {
			continue
		}
		name := p.peek()
		if name.Kind == TokenIdentifier && p.current+1 < len(p.tokens) && p.tokens[p.current+1].Kind == TokenAssign {
			p.advance()
			p.advance()
			value := p.parseExpression()
			if value == nil {
				break
			}
			assignments = append(assignments, LetAssignment{
				BaseNode: p.base(name.Span.Start, value.GetSpan().End),
				Name:     name.Lexeme,
				Value:    value,
			})
			p.match(TokenSemicolon)
			continue
		}

		p.report(name, "if bodies accept only assignments to previously declared lets")
		if p.isDeclarationStart() {
			if p.parseDeclaration() == nil {
				p.synchronize()
			}
			p.match(TokenSemicolon)
			continue
		}
		if !p.atEnd() {
			p.advance()
		}
	}
	end := p.expect(TokenRightBrace, "expected '}' after if body")
	if len(assignments) == 0 && len(p.diagnostics) == diagnosticsBeforeBody {
		p.report(start, "if body must contain at least one assignment")
	}
	return &IfDeclaration{
		BaseNode:  p.base(start.Span.Start, end.Span.End),
		Condition: condition, Assignments: assignments,
	}
}

func (p *parser) isDeclarationStart() bool {
	if !p.check(TokenIdentifier) {
		return false
	}
	switch p.peek().Lexeme {
	case "terraform", "provider", "input", "type", "export", "import", "const", "static", "component", "instantiate", "let", "if", "configure", "resource", "data", "module", "output", "moved":
		return true
	default:
		return false
	}
}

func (p *parser) parseConfigureDeclaration() Declaration {
	start := p.advance().Span.Start
	name := p.expect(TokenIdentifier, "expected provider configuration name")
	var index Expression
	if p.match(TokenLeftBracket) {
		index = p.parseExpression()
		p.expect(TokenRightBracket, "expected ']' after provider configuration index")
	}
	p.expect(TokenAssign, "expected '=' after provider configuration name")
	providerName := p.expect(TokenIdentifier, "expected provider name")
	if !p.check(TokenLeftParen) {
		return &ConfigureDeclaration{
			BaseNode: p.base(start, providerName.Span.End),
			Name:     name.Lexeme, Index: index, ProviderName: providerName.Lexeme, Inherited: true,
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
		BaseNode:     p.base(start, end.Span.End),
		Name:         name.Lexeme,
		Index:        index,
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
	provider := member.Target
	providerName := ""
	if identifier, ok := provider.(*IdentifierExpression); ok {
		providerName = identifier.Name
	} else if _, ok := provider.(*IndexExpression); !ok {
		p.report(name, "resource call must start with a provider configuration handle")
		return nil
	}
	if len(call.Arguments) != 2 {
		p.report(name, "resource call expects a label and arguments")
		return nil
	}
	labelExpression := call.Arguments[0]
	label, _ := stringLiteral(labelExpression)
	arguments, ok := call.Arguments[1].(*ObjectExpression)
	if !ok {
		p.report(name, "resource arguments must be an object")
		return nil
	}
	var with *ObjectExpression
	var condition Expression
	end := call.GetSpan().End
	if p.matchIdentifier("with") {
		with = p.parseObjectExpression()
		if with == nil {
			return nil
		}
		end = with.GetSpan().End
	}
	if p.matchIdentifier("when") {
		condition = p.parseExpression()
		if condition == nil {
			return nil
		}
		end = condition.GetSpan().End
	}
	return &ResourceDeclaration{
		BaseNode:           p.base(start, end),
		Name:               name.Lexeme,
		ProviderConfigName: providerName,
		ProviderConfig:     provider,
		Kind:               member.Name,
		Label:              label,
		LabelExpression:    labelExpression,
		Arguments:          arguments,
		With:               with,
		Condition:          condition,
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
	provider := member.Target
	providerName := ""
	if identifier, ok := provider.(*IdentifierExpression); ok {
		providerName = identifier.Name
	} else if _, ok := provider.(*IndexExpression); !ok {
		p.report(name, "data source call must start with a provider configuration handle")
		return nil
	}
	if len(call.Arguments) != 2 {
		p.report(name, "data source call expects a label and arguments")
		return nil
	}
	labelExpression := call.Arguments[0]
	label, _ := stringLiteral(labelExpression)
	arguments, ok := call.Arguments[1].(*ObjectExpression)
	if !ok {
		p.report(name, "data source arguments must be an object")
		return nil
	}
	return &DataDeclaration{
		BaseNode:           p.base(start, call.GetSpan().End),
		Name:               name.Lexeme,
		ProviderConfigName: providerName,
		ProviderConfig:     provider,
		Kind:               member.Name,
		Label:              label,
		LabelExpression:    labelExpression,
		Arguments:          arguments,
	}
}

func (p *parser) parseModuleDeclaration() Declaration {
	start := p.advance().Span.Start
	name := p.expect(TokenIdentifier, "expected module name")
	var index Expression
	if p.match(TokenLeftBracket) {
		index = p.parseExpression()
		p.expect(TokenRightBracket, "expected ']' after module index")
	}
	p.expect(TokenAssign, "expected '=' after module name")
	moduleName := p.expect(TokenIdentifier, "expected imported module name")
	p.expect(TokenLeftParen, "expected '(' after imported module name")
	labelExpression := p.parseExpression()
	if labelExpression == nil {
		return nil
	}
	if !p.match(TokenComma) {
		p.report(p.peek(), "expected ',' after module label")
		return nil
	}
	arguments := p.parseObjectExpression()
	if arguments == nil {
		return nil
	}
	p.match(TokenComma)
	close := p.expect(TokenRightParen, "expected ')' after module arguments")
	var providers *ProviderMapping
	var meta *ObjectExpression
	seenUsing := false
	seenWith := false
	end := close.Span.End
	for p.checkIdentifier("using") || p.checkIdentifier("with") {
		if p.matchIdentifier("using") {
			if seenUsing {
				p.report(p.previous(), "module may contain only one using clause")
			}
			seenUsing = true
			providers = p.parseProviderMapping()
			if providers == nil {
				return nil
			}
			end = providers.GetSpan().End
		} else if p.matchIdentifier("with") {
			if seenWith {
				p.report(p.previous(), "module may contain only one with clause")
			}
			seenWith = true
			meta = p.parseObjectExpression()
			if meta == nil {
				return nil
			}
			end = meta.GetSpan().End
		}
	}
	label, _ := stringLiteral(labelExpression)
	return &ModuleDeclaration{
		BaseNode:        p.base(start, end),
		Name:            name.Lexeme,
		Index:           index,
		ModuleName:      moduleName.Lexeme,
		Label:           label,
		LabelExpression: labelExpression,
		Arguments:       arguments,
		Providers:       providers,
		MetaArguments:   meta,
	}
}

func (p *parser) parseProviderMapping() *ProviderMapping {
	if p.check(TokenLeftBrace) {
		explicit := p.parseObjectExpression()
		if explicit == nil {
			return nil
		}
		return &ProviderMapping{
			BaseNode: BaseNode{File: FileID(p.filename), Span: explicit.GetSpan()},
			Explicit: explicit,
		}
	}
	if !p.check(TokenLeftBracket) {
		p.report(p.peek(), "expected provider mapping object or shorthand list")
		return nil
	}
	expression := p.parseArrayExpression()
	array, ok := expression.(*ArrayExpression)
	if !ok {
		return nil
	}
	return &ProviderMapping{
		BaseNode: BaseNode{File: FileID(p.filename), Span: array.GetSpan()},
		Inferred: array.Items,
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
		BaseNode: p.base(start, end),
		Name:     name.Lexeme,
		Value:    value,
		Metadata: metadata,
	}
}

func (p *parser) parseMovedDeclaration() Declaration {
	start := p.advance().Span.Start
	if p.match(TokenLeftBrace) {
		var items []MovedItem
		for !p.check(TokenRightBrace) && !p.atEnd() {
			from := p.expect(TokenRawAddress, "expected raw source address")
			p.expect(TokenArrow, "expected '->' between moved addresses")
			to := p.expect(TokenRawAddress, "expected raw destination address")
			if from.Lexeme == "" {
				p.report(from, "moved source address must not be empty")
			}
			if to.Lexeme == "" {
				p.report(to, "moved destination address must not be empty")
			}
			itemSpan := Span{File: FileID(p.filename), Start: from.Span.Start, End: to.Span.End}
			items = append(items, MovedItem{
				BaseNode: BaseNode{File: FileID(p.filename), Span: itemSpan},
				From:     AddressLiteral{BaseNode: BaseNode{File: FileID(p.filename), Span: from.Span}, Raw: from.Lexeme},
				To:       AddressLiteral{BaseNode: BaseNode{File: FileID(p.filename), Span: to.Span}, Raw: to.Lexeme},
			})
			if !p.match(TokenComma) && !p.check(TokenRightBrace) {
				p.report(p.peek(), "expected ',' between moved address pairs")
				break
			}
		}
		end := p.expect(TokenRightBrace, "expected '}' after moved addresses")
		return &MovedDeclaration{BaseNode: BaseNode{File: FileID(p.filename), Span: Span{File: FileID(p.filename), Start: start, End: end.Span.End}}, Items: items}
	}
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
	return &MovedDeclaration{BaseNode: BaseNode{File: FileID(p.filename), Span: Span{File: FileID(p.filename), Start: start, End: to.Span.End}}, From: from.Lexeme, To: to.Lexeme}
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
			BaseNode:  p.base(expression.GetSpan().Start, elseExpression.GetSpan().End),
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
			BaseNode: p.base(left.GetSpan().Start, right.GetSpan().End),
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
			BaseNode: p.base(operator.Span.Start, operand.GetSpan().End),
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
				BaseNode: p.base(expression.GetSpan().Start, name.Span.End),
				Target:   expression,
				Name:     name.Lexeme,
			}
		case p.match(TokenLeftBracket):
			index := p.parseExpression()
			end := p.expect(TokenRightBracket, "expected ']' after index")
			expression = &IndexExpression{
				BaseNode: p.base(expression.GetSpan().Start, end.Span.End),
				Target:   expression,
				Index:    index,
			}
		case p.match(TokenLeftParen):
			arguments := p.parseCallArguments()
			end := p.expect(TokenRightParen, "expected ')' after arguments")
			expression = &CallExpression{
				BaseNode:  p.base(expression.GetSpan().Start, end.Span.End),
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
		return &LiteralExpression{BaseNode: p.baseSpan(token.Span), Value: json.Number(token.Lexeme)}
	case TokenString:
		p.advance()
		return &LiteralExpression{BaseNode: p.baseSpan(token.Span), Value: token.Lexeme}
	case TokenFString:
		p.advance()
		return p.parseTemplateExpression(token)
	case TokenIdentifier:
		p.advance()
		switch token.Lexeme {
		case "true":
			return &LiteralExpression{BaseNode: p.baseSpan(token.Span), Value: true}
		case "false":
			return &LiteralExpression{BaseNode: p.baseSpan(token.Span), Value: false}
		case "null", "none":
			return &LiteralExpression{BaseNode: p.baseSpan(token.Span), Value: nil}
		default:
			return &IdentifierExpression{BaseNode: p.baseSpan(token.Span), Name: token.Lexeme}
		}
	case TokenLeftParen:
		start := p.advance().Span.Start
		expression := p.parseExpression()
		end := p.expect(TokenRightParen, "expected ')' after expression")
		if expression != nil {
			switch value := expression.(type) {
			case *IdentifierExpression:
				value.Span = p.span(start, end.Span.End)
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
		p.expect(TokenFatArrow, "expected '=>' in object comprehension")
		value = p.parseExpression()
	}
	var condition Expression
	if p.matchIdentifier("if") {
		condition = p.parseExpression()
	}
	end := p.expect(close, "expected comprehension closing delimiter")
	return &ForExpression{
		BaseNode: p.base(start, end.Span.End), KeyVariable: keyVariable,
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
		BaseNode: p.base(start, end.Span.End),
		Items:    items,
	}
}

func (p *parser) parseInputMetadata() (*ObjectExpression, []InputMetadataItem) {
	start := p.expect(TokenLeftBrace, "expected '{'")
	if start.Kind != TokenLeftBrace {
		return nil, nil
	}
	var fields []ObjectField
	var objectItems []ObjectItem
	var metadataItems []InputMetadataItem
	for !p.check(TokenRightBrace) && !p.atEnd() {
		if p.matchIdentifier("validate") {
			clauseStart := p.previous()
			condition := p.parseExpression()
			if condition == nil {
				break
			}
			if !p.expectIdentifier("else") {
				break
			}
			message := p.expect(TokenString, "validation message must be a literal string")
			if message.Kind != TokenString {
				break
			}
			if message.Lexeme == "" {
				p.report(message, "validation message must not be empty")
			}
			clause := ValidationClause{
				BaseNode:  BaseNode{File: FileID(p.filename), Span: Span{File: FileID(p.filename), Start: clauseStart.Span.Start, End: message.Span.End}},
				Condition: condition,
				Message:   message.Lexeme,
			}
			metadataItems = append(metadataItems, clause)
			if !p.match(TokenComma) {
				p.report(p.peek(), "expected ',' after validation clause")
			}
			continue
		}

		item, ok := p.parseObjectItem()
		if !ok {
			break
		}
		objectItems = append(objectItems, item)
		switch value := item.(type) {
		case ObjectField:
			fields = append(fields, value)
			metadataItems = append(metadataItems, value)
		case ObjectSpread:
			metadataItems = append(metadataItems, value)
		}
		if !p.match(TokenComma, TokenSemicolon) && !p.check(TokenRightBrace) {
			p.report(p.peek(), "expected ',' or ';' between metadata items")
		}
	}
	end := p.expect(TokenRightBrace, "expected '}' after input metadata")
	object := &ObjectExpression{
		BaseNode: BaseNode{File: FileID(p.filename), Span: Span{File: FileID(p.filename), Start: start.Span.Start, End: end.Span.End}},
		Fields:   fields,
		Items:    objectItems,
	}
	return object, metadataItems
}

func (p *parser) parseObjectExpression() *ObjectExpression {
	start := p.expect(TokenLeftBrace, "expected '{'")
	if start.Kind != TokenLeftBrace {
		return nil
	}
	var fields []ObjectField
	var items []ObjectItem
	for !p.check(TokenRightBrace) && !p.atEnd() {
		item, ok := p.parseObjectItem()
		if !ok {
			break
		}
		items = append(items, item)
		if field, ok := item.(ObjectField); ok {
			fields = append(fields, field)
		}
		if !p.match(TokenComma, TokenSemicolon) && !p.check(TokenRightBrace) {
			p.report(p.peek(), "expected ',' or ';' between object fields")
		}
	}
	end := p.expect(TokenRightBrace, "expected '}' after object")
	return &ObjectExpression{
		BaseNode: p.base(start.Span.Start, end.Span.End),
		Fields:   fields,
		Items:    items,
	}
}

func (p *parser) parseObjectItem() (ObjectItem, bool) {
	if p.match(TokenEllipsis) {
		start := p.previous()
		value := p.parseExpression()
		if value == nil {
			return nil, false
		}
		if call, ok := value.(*CallExpression); ok {
			if callee, ok := call.Callee.(*IdentifierExpression); ok && callee.Name == "inputs" {
				if len(call.Arguments) != 1 {
					p.report(Token{Span: call.GetSpan()}, "inputs forwarding expects exactly one argument")
					return InputsSpread{BaseNode: p.base(start.Span.Start, value.GetSpan().End)}, true
				}
				return InputsSpread{BaseNode: p.base(start.Span.Start, value.GetSpan().End), Value: call.Arguments[0]}, true
			}
		}
		return ObjectSpread{BaseNode: p.base(start.Span.Start, value.GetSpan().End), Value: value}, true
	}
	return p.parseObjectField()
}

func (p *parser) parseObjectField() (ObjectField, bool) {
	name := p.peek()
	if name.Kind != TokenIdentifier && name.Kind != TokenString {
		p.report(name, "expected object field name")
		if !p.atEnd() {
			p.advance()
		}
		return ObjectField{}, false
	}
	p.advance()
	quoted := name.Kind == TokenString
	punned := false
	var value Expression
	if !quoted && !p.check(TokenColon) {
		punned = true
		value = &IdentifierExpression{BaseNode: BaseNode{File: FileID(p.filename), Span: name.Span}, Name: name.Lexeme}
	} else {
		p.expect(TokenColon, "expected ':' after object field name")
		value = p.parseExpression()
		if value == nil {
			return ObjectField{}, false
		}
	}
	wireName := name.Lexeme
	if !quoted {
		wireName = SourceNameToWire(name.Lexeme)
	}
	var condition Expression
	if p.matchIdentifier("when") {
		if punned {
			p.report(p.previous(), "punned object fields cannot have conditions")
		}
		condition = p.parseExpression()
		if condition == nil {
			return ObjectField{}, false
		}
	}
	end := value.GetSpan().End
	if condition != nil {
		end = condition.GetSpan().End
	}
	return ObjectField{
		BaseNode:  BaseNode{File: FileID(p.filename), Span: Span{File: FileID(p.filename), Start: name.Span.Start, End: end}},
		Name:      name.Lexeme,
		WireName:  wireName,
		Quoted:    quoted,
		Value:     value,
		Punned:    punned,
		Condition: condition,
	}, true
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
	return &TemplateExpression{BaseNode: p.baseSpan(token.Span), Parts: parts}
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
			case "terraform", "provider", "input", "type", "export", "import", "const", "static", "component", "instantiate", "let", "if", "configure", "resource", "data", "module", "output", "moved":
				return
			}
		}
		p.advance()
	}
}

func (p *parser) report(token Token, message string) {
	p.diagnostics = append(p.diagnostics, NewDiagnostic(FileID(p.filename), token.Span, "PARSE_ERROR", message))
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

func (p *parser) lookaheadKind(offset int) TokenKind {
	index := p.current + offset
	if index >= len(p.tokens) {
		return TokenEOF
	}
	return p.tokens[index].Kind
}

func (p *parser) previous() Token {
	if p.current == 0 {
		return p.tokens[0]
	}
	return p.tokens[p.current-1]
}

func (p *parser) span(start, end Position) Span {
	return Span{File: FileID(p.filename), Start: start, End: end}
}

func (p *parser) base(start, end Position) BaseNode {
	return BaseNode{File: FileID(p.filename), Span: p.span(start, end)}
}

func (p *parser) baseSpan(span Span) BaseNode {
	span.File = FileID(p.filename)
	return BaseNode{File: FileID(p.filename), Span: span}
}
