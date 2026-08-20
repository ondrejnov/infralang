package compiler

import (
	"fmt"

	"github.com/ondrejnov/infralang/internal/syntax"
)

type CompileOptions struct {
	ProjectRoot     string
	ModuleID        string
	LocalModules    map[string]ModuleInterface
	ProviderSchemas ProviderSchemas
}

type ProviderSchemas map[string]ProviderSchema

type ProviderSchema struct {
	BlockTypes map[string]ProviderBlockSchema
}

type ProviderBlockSchema struct {
	NestingMode string
}

type ModuleInterface struct {
	ModuleID  string
	Inputs    []InputInterface
	Outputs   []OutputInterface
	Providers []ProviderInterface
}

type InputInterface struct {
	Name        BindingName
	Type        valueType
	Default     syntax.Expression
	Required    bool
	Sensitive   bool
	Declaration *syntax.InputDeclaration
}

type OutputInterface struct {
	Name        BindingName
	Type        valueType
	Sensitive   bool
	Declaration *syntax.OutputDeclaration
}

type ProviderInterface struct {
	Name        BindingName
	Source      string
	Declaration *syntax.ConfigureDeclaration
}

func CollectInterface(file *syntax.File, options CompileOptions) (ModuleInterface, []syntax.Diagnostic) {
	prepared, diagnostics := Prepare(file)
	if len(diagnostics) != 0 {
		return ModuleInterface{ModuleID: options.ModuleID}, diagnostics
	}
	file = prepared
	c := newCompiler(file, options)
	c.collectDeclarations()
	c.compileDeclarations()

	result := ModuleInterface{ModuleID: options.ModuleID}
	for _, declaration := range file.Declarations {
		switch value := declaration.(type) {
		case *syntax.InputDeclaration:
			symbol := c.symbols[value.Name]
			if symbol == nil || symbol.kind != symbolInput {
				continue
			}
			result.Inputs = append(result.Inputs, InputInterface{
				Name: symbol.name, Type: symbol.valueType, Default: value.Default,
				Required:  value.Default == nil && (value.Type == nil || value.Type.Name != "optional"),
				Sensitive: symbol.valueType.sensitive, Declaration: value,
			})
		case *syntax.OutputDeclaration:
			outputType := c.outputTypes[value.Name]
			result.Outputs = append(result.Outputs, OutputInterface{
				Name: BindingName{Source: value.Name, Wire: value.Name}, Type: outputType,
				Sensitive: outputType.sensitive || outputMetadataSensitive(value), Declaration: value,
			})
		case *syntax.ConfigureDeclaration:
			if !value.Inherited {
				continue
			}
			provider := c.providers[value.ProviderName]
			if provider == nil || provider.builtin {
				continue
			}
			result.Providers = append(result.Providers, ProviderInterface{
				Name: BindingName{Source: value.Name, Wire: syntax.SourceNameToWire(value.Name)}, Source: provider.source, Declaration: value,
			})
		}
	}
	return result, syntax.SortDiagnostics(c.diagnostics)
}

func outputMetadataSensitive(declaration *syntax.OutputDeclaration) bool {
	if declaration.Metadata == nil {
		return false
	}
	for _, field := range declaration.Metadata.Fields {
		if objectFieldName(field).Wire != "sensitive" {
			continue
		}
		literal, ok := field.Value.(*syntax.LiteralExpression)
		if !ok {
			return false
		}
		value, _ := literal.Value.(bool)
		return value
	}
	return false
}

func (contract ModuleInterface) inputByWire(name string) (InputInterface, bool) {
	for _, input := range contract.Inputs {
		if input.Name.Wire == name {
			return input, true
		}
	}
	return InputInterface{}, false
}

func (contract ModuleInterface) providerByWire(name string) (ProviderInterface, bool) {
	for _, provider := range contract.Providers {
		if provider.Name.Wire == name {
			return provider, true
		}
	}
	return ProviderInterface{}, false
}

func (contract ModuleInterface) outputObjectType() valueType {
	result := valueType{kind: valueObject}
	for _, output := range contract.Outputs {
		result.fields = append(result.fields, valueField{
			name: output.Name, typeInfo: output.Type, sensitive: output.Sensitive,
			span: output.Declaration.GetSpan(),
		})
		result.sensitive = result.sensitive || output.Sensitive
	}
	return result
}

func (c *compiler) compileModuleArguments(declaration *syntax.ModuleDeclaration) map[string]any {
	filtered := &syntax.ObjectExpression{BaseNode: declaration.Arguments.BaseNode}
	for _, item := range objectItems(declaration.Arguments) {
		if _, forwarding := item.(syntax.InputsSpread); forwarding {
			continue
		}
		filtered.Items = append(filtered.Items, item)
		if field, ok := item.(syntax.ObjectField); ok {
			filtered.Fields = append(filtered.Fields, field)
		}
	}

	body := c.encodeBlockBody(filtered)
	actualType := c.checkExpression(filtered)
	symbol := c.symbols[declaration.Name]
	var contract *ModuleInterface
	if symbol != nil {
		contract = symbol.moduleContract
	}

	provided := make(map[string]valueField)
	for _, field := range actualType.fields {
		provided[field.name.Wire] = field
	}

	forwardedAt := make(map[string]syntax.Span)
	forwardConflictReported := make(map[string]bool)
	for _, item := range objectItems(declaration.Arguments) {
		forwarding, ok := item.(syntax.InputsSpread)
		if !ok {
			continue
		}
		if forwarding.Value == nil {
			continue
		}
		if contract == nil {
			c.checkExpression(forwarding.Value)
			c.addDiagnostic(forwarding.GetSpan(), "inputs forwarding requires a local InfraLang child interface")
			continue
		}
		forwardType := c.checkExpression(forwarding.Value)
		if forwardType.kind != valueObject || forwardType.open {
			c.addDiagnostic(forwarding.Value.GetSpan(), fmt.Sprintf("inputs forwarding expects a statically structural object, got %s", diagnosticTypeDescription(forwardType)))
			continue
		}
		var encoded map[string]any
		if object, ok := forwarding.Value.(*syntax.ObjectExpression); ok {
			encoded, _ = c.encodeStaticObject(object)
		}
		for _, field := range forwardType.fields {
			input, known := contract.inputByWire(field.name.Wire)
			if !known {
				c.addDiagnostic(field.span, fmt.Sprintf("forwarded field %q is not an input of local module %q", field.name.Wire, contract.ModuleID))
				continue
			}
			if previous, exists := forwardedAt[field.name.Wire]; exists {
				if !forwardConflictReported[field.name.Wire] {
					c.addDiagnostic(previous, fmt.Sprintf("input %q is contributed by multiple forwarding items", field.name.Wire))
					forwardConflictReported[field.name.Wire] = true
				}
				c.addDiagnostic(forwarding.GetSpan(), fmt.Sprintf("input %q is contributed by multiple forwarding items", field.name.Wire))
				continue
			}
			forwardedAt[field.name.Wire] = forwarding.GetSpan()
			if _, explicit := body[field.name.Wire]; explicit {
				continue
			}
			if encoded != nil {
				body[field.name.Wire] = encoded[field.name.Wire]
			} else {
				body[field.name.Wire] = "${" + c.renderFieldAccess(forwarding.Value, field.name) + "}"
			}
			provided[field.name.Wire] = valueField{
				name: input.Name, typeInfo: field.typeInfo, optional: field.optional,
				sensitive: field.sensitive, span: forwarding.GetSpan(),
			}
		}
	}

	if contract == nil {
		return body
	}
	if actualType.open {
		c.addDiagnostic(declaration.Arguments.GetSpan(), "local module argument keys must be statically known")
	}
	for wire, field := range provided {
		input, known := contract.inputByWire(wire)
		if !known {
			c.addDiagnostic(field.span, fmt.Sprintf("unknown input %q for local module %q", wire, contract.ModuleID))
			continue
		}
		c.checkAssignment(input.Type, field.typeInfo, field.span, "module input "+fmt.Sprintf("%q", input.Name.Source))
	}
	for _, input := range contract.Inputs {
		if !input.Required {
			continue
		}
		if _, exists := provided[input.Name.Wire]; !exists {
			c.addDiagnostic(declaration.Arguments.GetSpan(), fmt.Sprintf("missing required input %q for local module %q", input.Name.Source, contract.ModuleID))
		}
	}
	return body
}

func (c *compiler) checkModuleProviders(declaration *syntax.ModuleDeclaration, supplied map[string]*providerInfo) {
	symbol := c.symbols[declaration.Name]
	if symbol == nil || symbol.moduleContract == nil {
		return
	}
	contract := symbol.moduleContract
	for key, provider := range supplied {
		slot, known := contract.providerByWire(key)
		if !known {
			c.addDiagnostic(declaration.GetSpan(), fmt.Sprintf("unknown provider slot %q for local module %q", key, contract.ModuleID))
			continue
		}
		if provider.source != slot.Source {
			c.addDiagnostic(declaration.GetSpan(), fmt.Sprintf("provider slot %q requires source %q, got %q", slot.Name.Source, slot.Source, provider.source))
		}
	}
	for _, slot := range contract.Providers {
		if _, exists := supplied[slot.Name.Wire]; !exists {
			c.addDiagnostic(declaration.GetSpan(), fmt.Sprintf("missing provider slot %q for local module %q", slot.Name.Source, contract.ModuleID))
		}
	}
}
