package compiler

import (
	"fmt"
	"strings"

	"github.com/ondrejnov/infralang/internal/syntax"
)

func (p *preparer) expandDeclarations(declarations []syntax.Declaration, environment *staticEnv, expansion string) []syntax.Declaration {
	var result []syntax.Declaration
	for _, declaration := range declarations {
		switch value := declaration.(type) {
		case *syntax.ConstDeclaration:
			if environment == nil {
				continue
			}
			constant, ok := p.eval(value.Value, environment)
			if !ok {
				continue
			}
			if value.Type != nil {
				expected := p.constType(value.Type, make(map[string]bool))
				if !isAssignable(expected, constValueType(constant)) {
					p.addDiagnosticAt(value.Value, fmt.Sprintf("constant %q has incompatible annotation", value.Name))
					continue
				}
			}
			if _, duplicate := environment.values[value.Name]; duplicate {
				p.addDiagnostic(value, fmt.Sprintf("constant %q is already declared in this static scope", value.Name))
				continue
			}
			environment.values[value.Name] = constant
		case *syntax.StaticForDeclaration:
			collection, ok := p.eval(value.Collection, environment)
			if !ok {
				continue
			}
			keys, values, iterable := constIterationItems(collection)
			if !iterable {
				p.addDiagnosticAt(value.Collection, "static for expects a compile-time list or object")
				continue
			}
			for index := range values {
				frame := &staticEnv{parent: environment, values: map[string]constValue{value.ValueVariable: values[index]}}
				if value.KeyVariable != "" {
					frame.values[value.KeyVariable] = keys[index]
				}
				identity, _ := constValueString(keys[index])
				iteration := value.ValueVariable + "=" + identity
				if expansion != "" {
					iteration = expansion + "/" + iteration
				}
				result = append(result, p.expandDeclarations(value.Declarations, frame, iteration)...)
			}
		default:
			result = append(result, cloneDeclaration(declaration, environment, expansion))
		}
	}
	return result
}

func (p *preparer) resolveIdentities(declarations []syntax.Declaration) {
	for _, declaration := range declarations {
		switch value := declaration.(type) {
		case *syntax.ConfigureDeclaration:
			if value.Index != nil {
				index, ok := p.eval(value.Index, nil)
				key, valid := canonicalIndex(index, false)
				if !ok || !valid || !syntax.IsTerraformIdentifier(key) {
					p.addDiagnosticAt(value.Index, "indexed provider key must be a nonempty valid Terraform identifier string")
					continue
				}
				if value.Inherited {
					p.addDiagnostic(value, "indexed provider configurations cannot be inherited handles")
					continue
				}
				generated := syntheticHandleName(value.Name, key)
				if !p.registerIndexed(value.Name, key, indexedHandle{kind: indexedProvider, name: generated, span: value.GetSpan(), expansion: value.GetExpansion()}, value) {
					continue
				}
				value.Name = generated
				value.Alias = constValueExpression(constValue{kind: constString, text: key}, value.BaseNode)
				value.Index = nil
			} else if value.Alias != nil {
				alias, ok := p.requireStaticLabel(value.Alias, "provider alias")
				if ok {
					value.Alias = constValueExpression(constValue{kind: constString, text: alias}, value.BaseNode)
				}
			}
		case *syntax.ResourceDeclaration:
			if value.LabelExpression != nil {
				label, ok := p.requireStaticLabel(value.LabelExpression, "resource label")
				if ok {
					value.Label = label
				}
			} else if !validResolvedLabel(value.Label) {
				p.addDiagnostic(value, "resource label must be a nonempty valid Terraform identifier")
			}
		case *syntax.DataDeclaration:
			if value.LabelExpression != nil {
				label, ok := p.requireStaticLabel(value.LabelExpression, "data source label")
				if ok {
					value.Label = label
				}
			} else if !validResolvedLabel(value.Label) {
				p.addDiagnostic(value, "data source label must be a nonempty valid Terraform identifier")
			}
		case *syntax.ModuleDeclaration:
			if value.LabelExpression != nil {
				label, ok := p.requireStaticLabel(value.LabelExpression, "module label")
				if ok {
					value.Label = label
				}
			} else if !validResolvedLabel(value.Label) {
				p.addDiagnostic(value, "module label must be a nonempty valid Terraform identifier")
			}
			if value.Index != nil {
				index, ok := p.eval(value.Index, nil)
				key, valid := canonicalIndex(index, true)
				if !ok || !valid {
					p.addDiagnosticAt(value.Index, "indexed module key must be a nonempty compile-time string or exact number")
					continue
				}
				generated := syntheticHandleName(value.Name, key)
				if !p.registerIndexed(value.Name, key, indexedHandle{kind: indexedModule, name: generated, span: value.GetSpan(), expansion: value.GetExpansion()}, value) {
					continue
				}
				value.Name = generated
				value.Index = nil
			}
		}
	}
}

func validResolvedLabel(label string) bool {
	return label != "" && syntax.IsTerraformIdentifier(label)
}

func (p *preparer) requireStaticLabel(expression syntax.Expression, position string) (string, bool) {
	value, ok := p.eval(expression, nil)
	if !ok {
		return "", false
	}
	if value.kind != constString || value.text == "" {
		p.addDiagnosticAt(expression, position+" must be a nonempty compile-time string")
		return "", false
	}
	if !syntax.IsTerraformIdentifier(value.text) {
		p.addDiagnosticAt(expression, fmt.Sprintf("%s %q is not a valid Terraform identifier", position, value.text))
		return "", false
	}
	return value.text, true
}

func (p *preparer) registerIndexed(namespace, key string, handle indexedHandle, node syntax.Node) bool {
	if p.indexed[namespace] == nil {
		p.indexed[namespace] = make(map[string]indexedHandle)
	}
	for _, previous := range p.indexed[namespace] {
		if previous.kind != handle.kind {
			message := fmt.Sprintf("indexed namespace %q cannot contain both provider and module handles", namespace)
			p.addIdentityDiagnostic(previous, message)
			p.addDiagnostic(node, message)
			return false
		}
		break
	}
	if previous, exists := p.indexed[namespace][key]; exists {
		message := fmt.Sprintf("indexed handle %s[%q] is already declared", namespace, key)
		p.addIdentityDiagnostic(previous, message)
		p.addDiagnostic(node, message)
		return false
	}
	p.indexed[namespace][key] = handle
	return true
}

type finalIdentity struct {
	node syntax.Node
}

func (p *preparer) checkFinalIdentities(declarations []syntax.Declaration) {
	providers := make(map[string]string)
	configurations := make(map[string]string)
	constantConflicts := make(map[string]bool)
	for _, declaration := range declarations {
		name := runtimeSourceName(declaration)
		constant := p.constants[name]
		if name == "" || constant == nil || constantConflicts[name] {
			continue
		}
		constantConflicts[name] = true
		message := fmt.Sprintf("constant %q conflicts with a runtime declaration", name)
		p.addDiagnostic(constant.declaration, message)
		p.addDiagnostic(declaration, message)
	}
	for _, declaration := range declarations {
		if provider, ok := declaration.(*syntax.ProviderDeclaration); ok {
			providers[provider.Name] = providerLocalName(provider.Source)
		}
	}
	for _, declaration := range declarations {
		if configuration, ok := declaration.(*syntax.ConfigureDeclaration); ok {
			configurations[configuration.Name] = providers[configuration.ProviderName]
		}
	}

	symbols := make(map[string]finalIdentity)
	outputs := make(map[string]finalIdentity)
	providerAliases := make(map[string]finalIdentity)
	resourceAddresses := make(map[string]finalIdentity)
	dataAddresses := make(map[string]finalIdentity)
	moduleAddresses := make(map[string]finalIdentity)
	for _, declaration := range declarations {
		switch value := declaration.(type) {
		case *syntax.InputDeclaration:
			p.recordGeneratedIdentity(symbols, value.Name, value, fmt.Sprintf("name %q is already declared", value.Name))
		case *syntax.LetDeclaration:
			p.recordGeneratedIdentity(symbols, value.Name, value, fmt.Sprintf("name %q is already declared", value.Name))
		case *syntax.ConfigureDeclaration:
			p.recordGeneratedIdentity(symbols, value.Name, value, fmt.Sprintf("name %q is already declared", value.Name))
			alias := ""
			if value.Alias != nil {
				alias, _ = literalString(value.Alias)
			}
			if provider := configurations[value.Name]; provider != "" {
				identity := provider + "." + alias
				p.recordGeneratedIdentity(providerAliases, identity, value, fmt.Sprintf("provider configuration alias %q is already declared", alias))
			}
		case *syntax.ResourceDeclaration:
			p.recordGeneratedIdentity(symbols, value.Name, value, fmt.Sprintf("name %q is already declared", value.Name))
			provider := configurations[value.ProviderConfigName]
			terraformType := toSnakeCase(value.Kind)
			if provider != "" && !strings.HasPrefix(terraformType, provider+"_") {
				terraformType = provider + "_" + terraformType
			}
			if provider != "" && value.Label != "" {
				address := terraformType + "." + value.Label
				p.recordGeneratedIdentity(resourceAddresses, address, value, fmt.Sprintf("Terraform resource %s is already declared", address))
			}
		case *syntax.DataDeclaration:
			p.recordGeneratedIdentity(symbols, value.Name, value, fmt.Sprintf("name %q is already declared", value.Name))
			provider := configurations[value.ProviderConfigName]
			terraformType := toSnakeCase(value.Kind)
			if provider != "" && !strings.HasPrefix(terraformType, provider+"_") {
				terraformType = provider + "_" + terraformType
			}
			if provider != "" && value.Label != "" {
				address := terraformType + "." + value.Label
				p.recordGeneratedIdentity(dataAddresses, address, value, fmt.Sprintf("Terraform data source %s is already declared", address))
			}
		case *syntax.ModuleDeclaration:
			p.recordGeneratedIdentity(symbols, value.Name, value, fmt.Sprintf("name %q is already declared", value.Name))
			if value.Label != "" {
				p.recordGeneratedIdentity(moduleAddresses, value.Label, value, fmt.Sprintf("Terraform module %q is already declared", value.Label))
			}
		case *syntax.OutputDeclaration:
			p.recordGeneratedIdentity(outputs, value.Name, value, fmt.Sprintf("output %q is already declared", value.Name))
		}
	}
}

func runtimeSourceName(declaration syntax.Declaration) string {
	switch value := declaration.(type) {
	case *syntax.InputDeclaration:
		return value.Name
	case *syntax.LetDeclaration:
		return value.Name
	case *syntax.ConfigureDeclaration:
		return value.Name
	case *syntax.ResourceDeclaration:
		return value.Name
	case *syntax.DataDeclaration:
		return value.Name
	case *syntax.ModuleDeclaration:
		return value.Name
	default:
		return ""
	}
}

func (p *preparer) recordGeneratedIdentity(identities map[string]finalIdentity, key string, node syntax.Node, message string) {
	previous, exists := identities[key]
	if !exists {
		identities[key] = finalIdentity{node: node}
		return
	}
	if previous.node.GetExpansion() == "" && node.GetExpansion() == "" {
		return
	}
	p.addDiagnostic(previous.node, message)
	p.addDiagnostic(node, message)
}

func (p *preparer) addIdentityDiagnostic(identity indexedHandle, message string) {
	if identity.expansion != "" {
		message += " [" + identity.expansion + "]"
	}
	file := identity.span.File
	if file == "" {
		file = p.file.ID
	}
	p.diagnostics = append(p.diagnostics, syntax.NewDiagnostic(file, identity.span, "COMPILE_TIME_ERROR", message))
}

func (p *preparer) rewriteDeclaration(declaration syntax.Declaration) {
	switch value := declaration.(type) {
	case *syntax.TerraformDeclaration:
		value.Config = p.rewriteObject(value.Config)
	case *syntax.TypeAliasDeclaration:
		value.Type = p.rewriteType(value.Type)
	case *syntax.ComponentDefinition:
		for index := range value.Parameters {
			value.Parameters[index].Type = p.rewriteType(value.Parameters[index].Type)
		}
	case *syntax.ComponentInstance:
		value.Index = p.rewriteExpression(value.Index)
		value.Arguments = p.rewriteObject(value.Arguments)
		value.Providers = p.rewriteObject(value.Providers)
	case *syntax.ComponentExport:
		value.Value = p.rewriteExpression(value.Value)
	case *syntax.InputDeclaration:
		value.Type = p.rewriteType(value.Type)
		value.Default = p.rewriteExpression(value.Default)
		value.Metadata = p.rewriteObject(value.Metadata)
		value.MetadataItems = rewriteMetadataItems(value.MetadataItems, p)
	case *syntax.LetDeclaration:
		value.Value = p.rewriteExpression(value.Value)
	case *syntax.ConfigureDeclaration:
		value.Alias = p.rewriteExpression(value.Alias)
		value.Config = p.rewriteObject(value.Config)
	case *syntax.ResourceDeclaration:
		value.ProviderConfig = p.rewriteExpression(value.ProviderConfig)
		if identifier, ok := value.ProviderConfig.(*syntax.IdentifierExpression); ok {
			value.ProviderConfigName = identifier.Name
		}
		value.LabelExpression = p.rewriteExpression(value.LabelExpression)
		value.Arguments = p.rewriteObject(value.Arguments)
		value.MetaArguments = p.rewriteObject(value.MetaArguments)
		value.Condition = p.rewriteExpression(value.Condition)
	case *syntax.DataDeclaration:
		value.ProviderConfig = p.rewriteExpression(value.ProviderConfig)
		if identifier, ok := value.ProviderConfig.(*syntax.IdentifierExpression); ok {
			value.ProviderConfigName = identifier.Name
		}
		value.LabelExpression = p.rewriteExpression(value.LabelExpression)
		value.Arguments = p.rewriteObject(value.Arguments)
	case *syntax.ModuleDeclaration:
		value.LabelExpression = p.rewriteExpression(value.LabelExpression)
		value.Arguments = p.rewriteObject(value.Arguments)
		value.MetaArguments = p.rewriteObject(value.MetaArguments)
		if value.Providers != nil {
			value.Providers.Explicit = p.rewriteObject(value.Providers.Explicit)
			for index, expression := range value.Providers.Inferred {
				value.Providers.Inferred[index] = p.rewriteExpression(expression)
			}
		}
	case *syntax.OutputDeclaration:
		value.Value = p.rewriteExpression(value.Value)
		value.Metadata = p.rewriteObject(value.Metadata)
	}
}

func (p *preparer) rewriteExpression(expression syntax.Expression) syntax.Expression {
	return p.rewriteExpressionScoped(expression, nil)
}

func (p *preparer) rewriteExpressionScoped(expression syntax.Expression, blocked map[string]bool) syntax.Expression {
	if expression == nil {
		return nil
	}
	if identifier, ok := expression.(*syntax.IdentifierExpression); ok {
		if !blocked[identifier.Name] {
			if constant, exists := p.evalConstant(identifier.Name); exists {
				return constValueExpression(constant, identifier.BaseNode)
			}
		}
		return expression
	}
	if index, ok := expression.(*syntax.IndexExpression); ok {
		if target, ok := index.Target.(*syntax.IdentifierExpression); ok {
			if handles := p.indexed[target.Name]; !blocked[target.Name] && handles != nil {
				value, evaluated := p.eval(index.Index, nil)
				if !evaluated {
					return expression
				}
				var key string
				if value.kind == constString {
					key = value.text
				} else if value.kind == constNumber {
					key, _ = value.number.canonical()
				}
				handle, exists := handles[key]
				if !exists {
					p.addDiagnosticAt(index, fmt.Sprintf("unknown indexed handle %s[%q]", target.Name, key))
					return expression
				}
				return &syntax.IdentifierExpression{BaseNode: index.BaseNode, Name: handle.name}
			}
		}
		index.Target = p.rewriteExpressionScoped(index.Target, blocked)
		index.Index = p.rewriteExpressionScoped(index.Index, blocked)
		return index
	}
	switch value := expression.(type) {
	case *syntax.ArrayExpression:
		for index, item := range value.Items {
			value.Items[index] = p.rewriteExpressionScoped(item, blocked)
		}
	case *syntax.ObjectExpression:
		return p.rewriteObjectScoped(value, blocked)
	case *syntax.ForExpression:
		value.Collection = p.rewriteExpressionScoped(value.Collection, blocked)
		inner := copyBlockedNames(blocked)
		inner[value.ValueVariable] = true
		if value.KeyVariable != "" {
			inner[value.KeyVariable] = true
		}
		value.Key = p.rewriteExpressionScoped(value.Key, inner)
		value.Value = p.rewriteExpressionScoped(value.Value, inner)
		value.Condition = p.rewriteExpressionScoped(value.Condition, inner)
	case *syntax.TemplateExpression:
		for index, part := range value.Parts {
			value.Parts[index].Expression = p.rewriteExpressionScoped(part.Expression, blocked)
		}
	case *syntax.UnaryExpression:
		value.Operand = p.rewriteExpressionScoped(value.Operand, blocked)
	case *syntax.BinaryExpression:
		value.Left = p.rewriteExpressionScoped(value.Left, blocked)
		value.Right = p.rewriteExpressionScoped(value.Right, blocked)
	case *syntax.ConditionalExpression:
		value.Condition = p.rewriteExpressionScoped(value.Condition, blocked)
		value.Then = p.rewriteExpressionScoped(value.Then, blocked)
		value.Else = p.rewriteExpressionScoped(value.Else, blocked)
	case *syntax.MemberExpression:
		value.Target = p.rewriteExpressionScoped(value.Target, blocked)
	case *syntax.CallExpression:
		value.Callee = p.rewriteExpressionScoped(value.Callee, blocked)
		for index, argument := range value.Arguments {
			value.Arguments[index] = p.rewriteExpressionScoped(argument, blocked)
		}
	}
	return expression
}

func (p *preparer) rewriteObject(object *syntax.ObjectExpression) *syntax.ObjectExpression {
	return p.rewriteObjectScoped(object, nil)
}

func (p *preparer) rewriteObjectScoped(object *syntax.ObjectExpression, blocked map[string]bool) *syntax.ObjectExpression {
	if object == nil {
		return nil
	}
	for index, item := range object.Items {
		switch item := item.(type) {
		case syntax.ObjectField:
			item.Value = p.rewriteExpressionScoped(item.Value, blocked)
			item.Condition = p.rewriteExpressionScoped(item.Condition, blocked)
			object.Items[index] = item
		case syntax.ObjectSpread:
			item.Value = p.rewriteExpressionScoped(item.Value, blocked)
			object.Items[index] = item
		case syntax.InputsSpread:
			item.Value = p.rewriteExpressionScoped(item.Value, blocked)
			object.Items[index] = item
		}
	}
	object.Fields = nil
	for _, item := range object.Items {
		if field, ok := item.(syntax.ObjectField); ok {
			object.Fields = append(object.Fields, field)
		}
	}
	return object
}

func copyBlockedNames(blocked map[string]bool) map[string]bool {
	result := make(map[string]bool, len(blocked)+2)
	for name := range blocked {
		result[name] = true
	}
	return result
}

func (p *preparer) rewriteType(expression *syntax.TypeExpression) *syntax.TypeExpression {
	if expression == nil {
		return nil
	}
	for index, argument := range expression.Arguments {
		expression.Arguments[index] = p.rewriteType(argument)
	}
	for index, field := range expression.Fields {
		field.Type = p.rewriteType(field.Type)
		field.Default = p.rewriteExpression(field.Default)
		expression.Fields[index] = field
	}
	return expression
}

func rewriteMetadataItems(items []syntax.InputMetadataItem, p *preparer) []syntax.InputMetadataItem {
	result := make([]syntax.InputMetadataItem, 0, len(items))
	for _, item := range items {
		switch value := item.(type) {
		case syntax.ObjectField:
			value.Value = p.rewriteExpression(value.Value)
			value.Condition = p.rewriteExpression(value.Condition)
			result = append(result, value)
		case syntax.ObjectSpread:
			value.Value = p.rewriteExpression(value.Value)
			result = append(result, value)
		case syntax.ValidationClause:
			value.Condition = p.rewriteExpression(value.Condition)
			result = append(result, value)
		}
	}
	return result
}

func cloneDeclaration(declaration syntax.Declaration, environment *staticEnv, expansion string) syntax.Declaration {
	switch value := declaration.(type) {
	case *syntax.TerraformDeclaration:
		return &syntax.TerraformDeclaration{BaseNode: cloneBase(value.BaseNode, expansion), Config: cloneObject(value.Config, environment, expansion)}
	case *syntax.ProviderDeclaration:
		result := *value
		result.BaseNode = cloneBase(value.BaseNode, expansion)
		return &result
	case *syntax.TypeAliasDeclaration:
		return &syntax.TypeAliasDeclaration{BaseNode: cloneBase(value.BaseNode, expansion), Name: value.Name, Type: cloneType(value.Type, environment, expansion), Exported: value.Exported}
	case *syntax.TypeImportDeclaration:
		result := *value
		result.BaseNode = cloneBase(value.BaseNode, expansion)
		result.Items = append([]syntax.TypeImportItem(nil), value.Items...)
		for index := range result.Items {
			result.Items[index].BaseNode = cloneBase(result.Items[index].BaseNode, expansion)
		}
		return &result
	case *syntax.ComponentDefinition:
		result := *value
		result.BaseNode = cloneBase(value.BaseNode, expansion)
		result.Parameters = append([]syntax.ComponentParameter(nil), value.Parameters...)
		for index := range result.Parameters {
			result.Parameters[index].BaseNode = cloneBase(result.Parameters[index].BaseNode, expansion)
			result.Parameters[index].Type = cloneType(result.Parameters[index].Type, environment, expansion)
		}
		result.Providers = append([]syntax.ComponentProviderParameter(nil), value.Providers...)
		for index := range result.Providers {
			result.Providers[index].BaseNode = cloneBase(result.Providers[index].BaseNode, expansion)
		}
		result.Declarations = nil
		for _, bodyDeclaration := range value.Declarations {
			result.Declarations = append(result.Declarations, cloneDeclaration(bodyDeclaration, environment, expansion))
		}
		return &result
	case *syntax.ComponentInstance:
		result := *value
		result.BaseNode = cloneBase(value.BaseNode, expansion)
		result.Index = cloneExpression(value.Index, environment, expansion)
		result.Arguments = cloneObject(value.Arguments, environment, expansion)
		result.Providers = cloneObject(value.Providers, environment, expansion)
		return &result
	case *syntax.ComponentExport:
		return &syntax.ComponentExport{BaseNode: cloneBase(value.BaseNode, expansion), Name: value.Name, Value: cloneExpression(value.Value, environment, expansion)}
	case *syntax.InputDeclaration:
		result := *value
		result.BaseNode = cloneBase(value.BaseNode, expansion)
		result.Type = cloneType(value.Type, environment, expansion)
		result.Default = cloneExpression(value.Default, environment, expansion)
		result.Metadata = cloneObject(value.Metadata, environment, expansion)
		result.MetadataItems = cloneMetadataItems(value.MetadataItems, environment, expansion)
		return &result
	case *syntax.LetDeclaration:
		return &syntax.LetDeclaration{BaseNode: cloneBase(value.BaseNode, expansion), Name: value.Name, Value: cloneExpression(value.Value, environment, expansion)}
	case *syntax.ConfigureDeclaration:
		result := *value
		result.BaseNode = cloneBase(value.BaseNode, expansion)
		result.Index = cloneExpression(value.Index, environment, expansion)
		result.Alias = cloneExpression(value.Alias, environment, expansion)
		result.Config = cloneObject(value.Config, environment, expansion)
		return &result
	case *syntax.ResourceDeclaration:
		result := *value
		result.BaseNode = cloneBase(value.BaseNode, expansion)
		result.ProviderConfig = cloneExpression(value.ProviderConfig, environment, expansion)
		result.LabelExpression = cloneExpression(value.LabelExpression, environment, expansion)
		result.Arguments = cloneObject(value.Arguments, environment, expansion)
		result.MetaArguments = cloneObject(value.MetaArguments, environment, expansion)
		result.Condition = cloneExpression(value.Condition, environment, expansion)
		return &result
	case *syntax.DataDeclaration:
		result := *value
		result.BaseNode = cloneBase(value.BaseNode, expansion)
		result.ProviderConfig = cloneExpression(value.ProviderConfig, environment, expansion)
		result.LabelExpression = cloneExpression(value.LabelExpression, environment, expansion)
		result.Arguments = cloneObject(value.Arguments, environment, expansion)
		return &result
	case *syntax.ModuleDeclaration:
		result := *value
		result.BaseNode = cloneBase(value.BaseNode, expansion)
		result.Index = cloneExpression(value.Index, environment, expansion)
		result.LabelExpression = cloneExpression(value.LabelExpression, environment, expansion)
		result.Arguments = cloneObject(value.Arguments, environment, expansion)
		result.MetaArguments = cloneObject(value.MetaArguments, environment, expansion)
		result.Providers = cloneProviderMapping(value.Providers, environment, expansion)
		return &result
	case *syntax.OutputDeclaration:
		result := *value
		result.BaseNode = cloneBase(value.BaseNode, expansion)
		result.Value = cloneExpression(value.Value, environment, expansion)
		result.Metadata = cloneObject(value.Metadata, environment, expansion)
		return &result
	case *syntax.MovedDeclaration:
		result := *value
		result.BaseNode = cloneBase(value.BaseNode, expansion)
		result.Items = append([]syntax.MovedItem(nil), value.Items...)
		return &result
	default:
		return declaration
	}
}

func cloneBase(base syntax.BaseNode, expansion string) syntax.BaseNode {
	result := base
	if expansion != "" {
		result.Expansion = expansion
	}
	return result
}

func cloneExpression(expression syntax.Expression, environment *staticEnv, expansion string) syntax.Expression {
	if expression == nil {
		return nil
	}
	if identifier, ok := expression.(*syntax.IdentifierExpression); ok {
		if environment != nil {
			if value, exists := environment.lookup(identifier.Name); exists {
				return constValueExpression(value, cloneBase(identifier.BaseNode, expansion))
			}
		}
		return &syntax.IdentifierExpression{BaseNode: cloneBase(identifier.BaseNode, expansion), Name: identifier.Name}
	}
	switch value := expression.(type) {
	case *syntax.LiteralExpression:
		return &syntax.LiteralExpression{BaseNode: cloneBase(value.BaseNode, expansion), Value: value.Value}
	case *syntax.ArrayExpression:
		result := &syntax.ArrayExpression{BaseNode: cloneBase(value.BaseNode, expansion)}
		for _, item := range value.Items {
			result.Items = append(result.Items, cloneExpression(item, environment, expansion))
		}
		return result
	case *syntax.ObjectExpression:
		return cloneObject(value, environment, expansion)
	case *syntax.ForExpression:
		inner := &staticEnv{parent: environment, blocked: map[string]bool{value.ValueVariable: true}}
		if value.KeyVariable != "" {
			inner.blocked[value.KeyVariable] = true
		}
		return &syntax.ForExpression{
			BaseNode: cloneBase(value.BaseNode, expansion), KeyVariable: value.KeyVariable, ValueVariable: value.ValueVariable,
			Collection: cloneExpression(value.Collection, environment, expansion), Key: cloneExpression(value.Key, inner, expansion),
			Value: cloneExpression(value.Value, inner, expansion), Condition: cloneExpression(value.Condition, inner, expansion), Object: value.Object,
		}
	case *syntax.TemplateExpression:
		result := &syntax.TemplateExpression{BaseNode: cloneBase(value.BaseNode, expansion)}
		for _, part := range value.Parts {
			result.Parts = append(result.Parts, syntax.TemplatePart{Text: part.Text, Expression: cloneExpression(part.Expression, environment, expansion)})
		}
		return result
	case *syntax.UnaryExpression:
		return &syntax.UnaryExpression{BaseNode: cloneBase(value.BaseNode, expansion), Operator: value.Operator, Operand: cloneExpression(value.Operand, environment, expansion)}
	case *syntax.BinaryExpression:
		return &syntax.BinaryExpression{BaseNode: cloneBase(value.BaseNode, expansion), Left: cloneExpression(value.Left, environment, expansion), Operator: value.Operator, Right: cloneExpression(value.Right, environment, expansion)}
	case *syntax.ConditionalExpression:
		return &syntax.ConditionalExpression{BaseNode: cloneBase(value.BaseNode, expansion), Condition: cloneExpression(value.Condition, environment, expansion), Then: cloneExpression(value.Then, environment, expansion), Else: cloneExpression(value.Else, environment, expansion)}
	case *syntax.MemberExpression:
		return &syntax.MemberExpression{BaseNode: cloneBase(value.BaseNode, expansion), Target: cloneExpression(value.Target, environment, expansion), Name: value.Name}
	case *syntax.IndexExpression:
		return &syntax.IndexExpression{BaseNode: cloneBase(value.BaseNode, expansion), Target: cloneExpression(value.Target, environment, expansion), Index: cloneExpression(value.Index, environment, expansion)}
	case *syntax.CallExpression:
		result := &syntax.CallExpression{BaseNode: cloneBase(value.BaseNode, expansion), Callee: cloneExpression(value.Callee, environment, expansion)}
		for _, argument := range value.Arguments {
			result.Arguments = append(result.Arguments, cloneExpression(argument, environment, expansion))
		}
		return result
	default:
		return expression
	}
}

func cloneObject(object *syntax.ObjectExpression, environment *staticEnv, expansion string) *syntax.ObjectExpression {
	if object == nil {
		return nil
	}
	result := &syntax.ObjectExpression{BaseNode: cloneBase(object.BaseNode, expansion)}
	for _, item := range objectItems(object) {
		switch item := item.(type) {
		case syntax.ObjectField:
			item.BaseNode = cloneBase(item.BaseNode, expansion)
			item.Value = cloneExpression(item.Value, environment, expansion)
			item.Condition = cloneExpression(item.Condition, environment, expansion)
			result.Items = append(result.Items, item)
			result.Fields = append(result.Fields, item)
		case syntax.ObjectSpread:
			item.BaseNode = cloneBase(item.BaseNode, expansion)
			item.Value = cloneExpression(item.Value, environment, expansion)
			result.Items = append(result.Items, item)
		case syntax.InputsSpread:
			item.BaseNode = cloneBase(item.BaseNode, expansion)
			item.Value = cloneExpression(item.Value, environment, expansion)
			result.Items = append(result.Items, item)
		}
	}
	return result
}

func cloneType(expression *syntax.TypeExpression, environment *staticEnv, expansion string) *syntax.TypeExpression {
	if expression == nil {
		return nil
	}
	result := &syntax.TypeExpression{BaseNode: cloneBase(expression.BaseNode, expansion), Name: expression.Name}
	for _, argument := range expression.Arguments {
		result.Arguments = append(result.Arguments, cloneType(argument, environment, expansion))
	}
	for _, field := range expression.Fields {
		field.BaseNode = cloneBase(field.BaseNode, expansion)
		field.Type = cloneType(field.Type, environment, expansion)
		field.Default = cloneExpression(field.Default, environment, expansion)
		result.Fields = append(result.Fields, field)
	}
	return result
}

func cloneMetadataItems(items []syntax.InputMetadataItem, environment *staticEnv, expansion string) []syntax.InputMetadataItem {
	var result []syntax.InputMetadataItem
	for _, item := range items {
		switch value := item.(type) {
		case syntax.ObjectField:
			value.BaseNode = cloneBase(value.BaseNode, expansion)
			value.Value = cloneExpression(value.Value, environment, expansion)
			value.Condition = cloneExpression(value.Condition, environment, expansion)
			result = append(result, value)
		case syntax.ObjectSpread:
			value.BaseNode = cloneBase(value.BaseNode, expansion)
			value.Value = cloneExpression(value.Value, environment, expansion)
			result = append(result, value)
		case syntax.ValidationClause:
			value.BaseNode = cloneBase(value.BaseNode, expansion)
			value.Condition = cloneExpression(value.Condition, environment, expansion)
			result = append(result, value)
		}
	}
	return result
}

func cloneProviderMapping(mapping *syntax.ProviderMapping, environment *staticEnv, expansion string) *syntax.ProviderMapping {
	if mapping == nil {
		return nil
	}
	result := &syntax.ProviderMapping{BaseNode: cloneBase(mapping.BaseNode, expansion), Explicit: cloneObject(mapping.Explicit, environment, expansion)}
	for _, expression := range mapping.Inferred {
		result.Inferred = append(result.Inferred, cloneExpression(expression, environment, expansion))
	}
	return result
}
