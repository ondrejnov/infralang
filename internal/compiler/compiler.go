package compiler

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/ondrejnov/infralang/internal/syntax"
)

type symbolKind int

const (
	symbolInput symbolKind = iota
	symbolLocal
	symbolProviderConfig
	symbolResource
	symbolData
	symbolModule
)

type valueKind int

const (
	valueDynamic valueKind = iota
	valueNull
	valueString
	valueNumber
	valueBool
	valueList
	valueMap
)

type valueType struct {
	kind    valueKind
	element *valueType
}

type providerInfo struct {
	name      string
	localName string
	source    string
	version   string
	builtin   bool
}

type providerConfigInfo struct {
	provider *providerInfo
	alias    string
}

type resourceInfo struct {
	terraformType string
	label         string
}

type symbol struct {
	kind           symbolKind
	span           syntax.Span
	valueType      valueType
	providerConfig *providerConfigInfo
	resource       *resourceInfo
	moduleLabel    string
}

type compiler struct {
	file              *syntax.File
	diagnostics       []syntax.Diagnostic
	providers         map[string]*providerInfo
	providerLocals    map[string]*providerInfo
	symbols           map[string]*symbol
	outputs           map[string]syntax.Span
	root              map[string]any
	terraformConfig   map[string]any
	required          map[string]any
	variables         map[string]any
	locals            map[string]any
	providerBlocks    map[string][]any
	providerAliases   map[string]map[string]syntax.Span
	resources         map[string]map[string]any
	dataSources       map[string]map[string]any
	modules           map[string]any
	outputBlocks      map[string]any
	localDeclarations map[string]*syntax.LetDeclaration
	localStates       map[string]int
	inferLocals       bool
	resourceAddresses map[string]syntax.Span
	dataAddresses     map[string]syntax.Span
	moduleAddresses   map[string]syntax.Span
	movedBlocks       []any
	scope             map[string]valueType
}

func Compile(file *syntax.File) ([]byte, []syntax.Diagnostic) {
	c := &compiler{
		file:              file,
		providers:         make(map[string]*providerInfo),
		providerLocals:    make(map[string]*providerInfo),
		symbols:           make(map[string]*symbol),
		outputs:           make(map[string]syntax.Span),
		root:              make(map[string]any),
		terraformConfig:   make(map[string]any),
		required:          make(map[string]any),
		variables:         make(map[string]any),
		locals:            make(map[string]any),
		providerBlocks:    make(map[string][]any),
		providerAliases:   make(map[string]map[string]syntax.Span),
		resources:         make(map[string]map[string]any),
		dataSources:       make(map[string]map[string]any),
		modules:           make(map[string]any),
		outputBlocks:      make(map[string]any),
		localDeclarations: make(map[string]*syntax.LetDeclaration),
		localStates:       make(map[string]int),
		resourceAddresses: make(map[string]syntax.Span),
		dataAddresses:     make(map[string]syntax.Span),
		moduleAddresses:   make(map[string]syntax.Span),
		scope:             make(map[string]valueType),
	}

	c.collectDeclarations()
	if len(c.diagnostics) == 0 {
		c.compileDeclarations()
	}
	if len(c.diagnostics) != 0 {
		return nil, c.diagnostics
	}
	c.assembleRoot()

	result, err := json.MarshalIndent(c.root, "", "  ")
	if err != nil {
		c.addDiagnostic(syntax.Span{}, fmt.Sprintf("failed to encode Terraform JSON: %v", err))
		return nil, c.diagnostics
	}
	return append(result, '\n'), nil
}

func (c *compiler) collectDeclarations() {
	terraformDeclarations := 0
	for _, declaration := range c.file.Declarations {
		switch value := declaration.(type) {
		case *syntax.TerraformDeclaration:
			terraformDeclarations++
			if terraformDeclarations > 1 {
				c.addDiagnostic(value.GetSpan(), "only one terraform declaration is allowed")
			}
		case *syntax.ProviderDeclaration:
			if isReservedName(value.Name) {
				c.addDiagnostic(value.GetSpan(), fmt.Sprintf("provider name %q is reserved", value.Name))
				continue
			}
			if _, exists := c.providers[value.Name]; exists {
				c.addDiagnostic(value.GetSpan(), fmt.Sprintf("provider %q is already declared", value.Name))
				continue
			}
			localName := providerLocalName(value.Source)
			if localName == "" {
				c.addDiagnostic(value.GetSpan(), fmt.Sprintf("cannot derive provider local name from %q", value.Source))
				continue
			}
			if !syntax.IsTerraformIdentifier(localName) {
				c.addDiagnostic(value.GetSpan(), fmt.Sprintf("provider source %q produces invalid Terraform local name %q", value.Source, localName))
				continue
			}
			if previous, exists := c.providerLocals[localName]; exists {
				c.addDiagnostic(value.GetSpan(), fmt.Sprintf("provider %q conflicts with %q on Terraform local name %q", value.Name, previous.name, localName))
				continue
			}
			info := &providerInfo{
				name:      value.Name,
				localName: localName,
				source:    value.Source,
				version:   value.Version,
				builtin:   value.Source == "terraform.io/builtin/terraform",
			}
			c.providers[value.Name] = info
			c.providerLocals[localName] = info
		case *syntax.InputDeclaration:
			c.collectSymbol(value.Name, symbolInput, value.GetSpan())
		case *syntax.LetDeclaration:
			c.collectSymbol(value.Name, symbolLocal, value.GetSpan())
			if _, exists := c.symbols[value.Name]; exists {
				c.localDeclarations[value.Name] = value
			}
		case *syntax.ConfigureDeclaration:
			c.collectSymbol(value.Name, symbolProviderConfig, value.GetSpan())
		case *syntax.ResourceDeclaration:
			c.collectSymbol(value.Name, symbolResource, value.GetSpan())
		case *syntax.DataDeclaration:
			c.collectSymbol(value.Name, symbolData, value.GetSpan())
		case *syntax.ModuleDeclaration:
			c.collectSymbol(value.Name, symbolModule, value.GetSpan())
		case *syntax.OutputDeclaration:
			if isReservedName(value.Name) {
				c.addDiagnostic(value.GetSpan(), fmt.Sprintf("output name %q is reserved", value.Name))
				continue
			}
			if _, exists := c.outputs[value.Name]; exists {
				c.addDiagnostic(value.GetSpan(), fmt.Sprintf("output %q is already declared", value.Name))
			} else {
				c.outputs[value.Name] = value.GetSpan()
			}
		case *syntax.MovedDeclaration:
			// Moved source addresses intentionally refer to declarations that no
			// longer exist, so they are kept as static Terraform addresses.
		}
	}
}

func (c *compiler) collectSymbol(name string, kind symbolKind, span syntax.Span) {
	if isReservedName(name) {
		c.addDiagnostic(span, fmt.Sprintf("name %q is reserved", name))
		return
	}
	if _, exists := c.symbols[name]; exists {
		c.addDiagnostic(span, fmt.Sprintf("name %q is already declared", name))
		return
	}
	c.symbols[name] = &symbol{kind: kind, span: span, valueType: valueType{kind: valueDynamic}}
}

func (c *compiler) compileDeclarations() {
	// Declarations are order-independent. Compile them in dependency phases so
	// resource and module references already have stable Terraform addresses
	// when locals and outputs are lowered.
	for _, declaration := range c.file.Declarations {
		switch value := declaration.(type) {
		case *syntax.TerraformDeclaration:
			c.compileTerraform(value)
		case *syntax.ProviderDeclaration:
			c.compileProvider(value)
		case *syntax.InputDeclaration:
			c.compileInput(value)
		}
	}
	for _, declaration := range c.file.Declarations {
		if value, ok := declaration.(*syntax.ConfigureDeclaration); ok {
			c.registerProviderConfig(value)
		}
	}
	if len(c.diagnostics) != 0 {
		return
	}
	c.registerAddresses()
	if len(c.diagnostics) != 0 {
		return
	}
	c.inferLocals = true
	for _, declaration := range c.file.Declarations {
		if value, ok := declaration.(*syntax.LetDeclaration); ok {
			c.compileLocal(value)
		}
	}
	c.inferLocals = false
	for _, declaration := range c.file.Declarations {
		if value, ok := declaration.(*syntax.ConfigureDeclaration); ok {
			c.compileProviderConfig(value)
		}
	}
	for _, declaration := range c.file.Declarations {
		switch value := declaration.(type) {
		case *syntax.ResourceDeclaration:
			c.compileResource(value)
		case *syntax.DataDeclaration:
			c.compileData(value)
		case *syntax.ModuleDeclaration:
			c.compileModule(value)
		}
	}
	for _, declaration := range c.file.Declarations {
		if value, ok := declaration.(*syntax.OutputDeclaration); ok {
			c.compileOutput(value)
		}
	}
	for _, declaration := range c.file.Declarations {
		if value, ok := declaration.(*syntax.MovedDeclaration); ok {
			c.movedBlocks = append(c.movedBlocks, map[string]any{"from": value.From, "to": value.To})
		}
	}
}

func (c *compiler) compileTerraform(declaration *syntax.TerraformDeclaration) {
	for _, field := range declaration.Config.Fields {
		switch field.Name {
		case "requiredVersion", "required_version":
			value, ok := literalString(field.Value)
			if !ok {
				c.addDiagnostic(field.GetSpan(), "terraform requiredVersion must be a literal string")
				continue
			}
			c.terraformConfig["required_version"] = value
		default:
			c.addDiagnostic(field.GetSpan(), fmt.Sprintf("unsupported terraform setting %q", field.Name))
		}
	}
}

func (c *compiler) compileProvider(declaration *syntax.ProviderDeclaration) {
	info := c.providers[declaration.Name]
	if info == nil || info.builtin {
		return
	}
	config := map[string]any{"source": info.source}
	if info.version != "" {
		config["version"] = info.version
	}
	c.required[info.localName] = config
}

func (c *compiler) compileInput(declaration *syntax.InputDeclaration) {
	typeInfo := c.compileType(declaration.Type)
	c.symbols[declaration.Name].valueType = typeInfo
	block := map[string]any{"type": terraformTypeConstraint(declaration.Type)}
	if declaration.Default != nil {
		actual := c.checkExpression(declaration.Default)
		if !c.inputDefaultAssignable(typeInfo, declaration.Default) || !isAssignable(typeInfo, actual) {
			c.addDiagnostic(declaration.Default.GetSpan(), fmt.Sprintf("default for %q has incompatible type", declaration.Name))
		}
		if !isConstant(declaration.Default) {
			c.addDiagnostic(declaration.Default.GetSpan(), "input default must be a constant expression")
		} else {
			block["default"] = c.encodeExpression(declaration.Default, false)
		}
	} else if declaration.Type != nil && declaration.Type.Name == "optional" {
		block["default"] = nil
	}
	if declaration.Metadata != nil {
		for _, field := range declaration.Metadata.Fields {
			key := toSnakeCase(field.Name)
			switch key {
			case "description", "sensitive", "nullable":
				block[key] = c.encodeExpression(field.Value, false)
				c.checkExpression(field.Value)
			case "validations":
				array, ok := field.Value.(*syntax.ArrayExpression)
				if !ok {
					c.addDiagnostic(field.GetSpan(), "input validations must be an array")
					continue
				}
				validations := make([]any, 0, len(array.Items))
				for _, item := range array.Items {
					object, ok := item.(*syntax.ObjectExpression)
					if !ok {
						c.addDiagnostic(item.GetSpan(), "each input validation must be an object")
						continue
					}
					validations = append(validations, c.encodeObject(object, true))
					c.checkExpression(object)
				}
				block["validation"] = validations
			default:
				c.addDiagnostic(field.GetSpan(), fmt.Sprintf("unsupported input metadata %q", field.Name))
			}
		}
	}
	c.variables[declaration.Name] = block
}

func (c *compiler) compileLocal(declaration *syntax.LetDeclaration) {
	switch c.localStates[declaration.Name] {
	case 1:
		c.addDiagnostic(declaration.GetSpan(), fmt.Sprintf("local %q is part of a dependency cycle", declaration.Name))
		return
	case 2:
		return
	}
	c.localStates[declaration.Name] = 1
	valueType := c.checkExpression(declaration.Value)
	c.symbols[declaration.Name].valueType = valueType
	c.locals[declaration.Name] = c.encodeExpression(declaration.Value, false)
	c.localStates[declaration.Name] = 2
}

func (c *compiler) registerProviderConfig(declaration *syntax.ConfigureDeclaration) {
	provider := c.providers[declaration.ProviderName]
	if provider == nil {
		c.addDiagnostic(declaration.GetSpan(), fmt.Sprintf("unknown provider %q", declaration.ProviderName))
		return
	}
	alias := ""
	if declaration.Alias != nil {
		value, ok := literalString(declaration.Alias)
		if !ok || value == "" {
			c.addDiagnostic(declaration.Alias.GetSpan(), "provider alias must be a non-empty literal string")
			return
		}
		alias = value
		if !syntax.IsTerraformIdentifier(alias) {
			c.addDiagnostic(declaration.Alias.GetSpan(), fmt.Sprintf("provider alias %q is not a valid Terraform identifier", alias))
			return
		}
	}

	if c.providerAliases[provider.localName] == nil {
		c.providerAliases[provider.localName] = make(map[string]syntax.Span)
	}
	if _, exists := c.providerAliases[provider.localName][alias]; exists {
		name := "default"
		if alias != "" {
			name = alias
		}
		c.addDiagnostic(declaration.GetSpan(), fmt.Sprintf("provider %s configuration %q is already declared", provider.localName, name))
		return
	}
	c.providerAliases[provider.localName][alias] = declaration.GetSpan()
	c.symbols[declaration.Name].providerConfig = &providerConfigInfo{provider: provider, alias: alias}
}

func (c *compiler) compileProviderConfig(declaration *syntax.ConfigureDeclaration) {
	providerConfig := c.symbols[declaration.Name].providerConfig
	if providerConfig == nil {
		return
	}
	if declaration.Inherited {
		return
	}
	config := c.encodeObject(declaration.Config, true)
	if providerConfig.alias != "" {
		config["alias"] = providerConfig.alias
	}
	if !providerConfig.provider.builtin {
		localName := providerConfig.provider.localName
		c.providerBlocks[localName] = append(c.providerBlocks[localName], config)
	}
	c.checkExpression(declaration.Config)
}

func (c *compiler) registerAddresses() {
	for _, declaration := range c.file.Declarations {
		switch value := declaration.(type) {
		case *syntax.ResourceDeclaration:
			terraformType, ok := c.managedType(value.ProviderConfigName, value.Kind, value.GetSpan())
			if !ok {
				continue
			}
			address := terraformType + "." + value.Label
			if _, exists := c.resourceAddresses[address]; exists {
				c.addDiagnostic(value.GetSpan(), fmt.Sprintf("Terraform resource %s is already declared", address))
				continue
			}
			c.resourceAddresses[address] = value.GetSpan()
			c.symbols[value.Name].resource = &resourceInfo{terraformType: terraformType, label: value.Label}
		case *syntax.DataDeclaration:
			terraformType, ok := c.managedType(value.ProviderConfigName, value.Kind, value.GetSpan())
			if !ok {
				continue
			}
			address := terraformType + "." + value.Label
			if _, exists := c.dataAddresses[address]; exists {
				c.addDiagnostic(value.GetSpan(), fmt.Sprintf("Terraform data source %s is already declared", address))
				continue
			}
			c.dataAddresses[address] = value.GetSpan()
			c.symbols[value.Name].resource = &resourceInfo{terraformType: terraformType, label: value.Label}
		case *syntax.ModuleDeclaration:
			if _, exists := c.moduleAddresses[value.Label]; exists {
				c.addDiagnostic(value.GetSpan(), fmt.Sprintf("Terraform module %q is already declared", value.Label))
				continue
			}
			c.moduleAddresses[value.Label] = value.GetSpan()
			c.symbols[value.Name].moduleLabel = value.Label
		}
	}
}

func (c *compiler) managedType(providerConfigName, kind string, span syntax.Span) (string, bool) {
	providerSymbol := c.symbols[providerConfigName]
	if providerSymbol == nil || providerSymbol.kind != symbolProviderConfig || providerSymbol.providerConfig == nil {
		c.addDiagnostic(span, fmt.Sprintf("unknown provider configuration %q", providerConfigName))
		return "", false
	}
	localName := providerSymbol.providerConfig.provider.localName
	terraformType := toSnakeCase(kind)
	if !strings.HasPrefix(terraformType, localName+"_") {
		terraformType = localName + "_" + terraformType
	}
	if !syntax.IsTerraformIdentifier(terraformType) {
		c.addDiagnostic(span, fmt.Sprintf("generated Terraform type %q is not a valid identifier", terraformType))
		return "", false
	}
	return terraformType, true
}

func (c *compiler) compileResource(declaration *syntax.ResourceDeclaration) {
	providerSymbol := c.symbols[declaration.ProviderConfigName]
	if providerSymbol == nil || providerSymbol.kind != symbolProviderConfig || providerSymbol.providerConfig == nil {
		c.addDiagnostic(declaration.GetSpan(), fmt.Sprintf("unknown provider configuration %q", declaration.ProviderConfigName))
		return
	}
	providerConfig := providerSymbol.providerConfig
	resource := c.symbols[declaration.Name].resource
	if resource == nil {
		return
	}
	terraformType := resource.terraformType
	if _, exists := c.resources[terraformType]; !exists {
		c.resources[terraformType] = make(map[string]any)
	}

	body := c.encodeObject(declaration.Arguments, true)
	if declaration.MetaArguments != nil {
		for _, field := range declaration.MetaArguments.Fields {
			key := field.Name
			if !field.Quoted {
				key = toSnakeCase(key)
			}
			if _, exists := body[key]; exists {
				c.addDiagnostic(field.GetSpan(), fmt.Sprintf("resource argument %q is defined more than once", key))
				continue
			}
			if key == "depends_on" {
				body[key] = c.encodeStaticTraversals(field.Value)
			} else if key == "lifecycle" {
				body[key] = c.encodeLifecycle(field.Value)
			} else {
				body[key] = c.encodeExpression(field.Value, true)
			}
		}
	}
	if !providerConfig.provider.builtin && providerConfig.alias != "" {
		body["provider"] = providerConfig.provider.localName + "." + providerConfig.alias
	}
	c.checkExpression(declaration.Arguments)
	if declaration.MetaArguments != nil {
		c.checkExpression(declaration.MetaArguments)
	}
	c.resources[terraformType][declaration.Label] = body
}

func (c *compiler) compileData(declaration *syntax.DataDeclaration) {
	providerSymbol := c.symbols[declaration.ProviderConfigName]
	if providerSymbol == nil || providerSymbol.kind != symbolProviderConfig || providerSymbol.providerConfig == nil {
		c.addDiagnostic(declaration.GetSpan(), fmt.Sprintf("unknown provider configuration %q", declaration.ProviderConfigName))
		return
	}
	providerConfig := providerSymbol.providerConfig
	dataSource := c.symbols[declaration.Name].resource
	if dataSource == nil {
		return
	}
	terraformType := dataSource.terraformType
	if _, exists := c.dataSources[terraformType]; !exists {
		c.dataSources[terraformType] = make(map[string]any)
	}

	body := c.encodeObject(declaration.Arguments, true)
	if !providerConfig.provider.builtin && providerConfig.alias != "" {
		body["provider"] = providerConfig.provider.localName + "." + providerConfig.alias
	}
	c.checkExpression(declaration.Arguments)
	c.dataSources[terraformType][declaration.Label] = body
}

func (c *compiler) compileModule(declaration *syntax.ModuleDeclaration) {
	withEach := declaration.MetaArguments != nil && hasObjectField(declaration.MetaArguments, "forEach", "for_each")
	if withEach {
		c.scope["each"] = valueType{kind: valueDynamic}
		defer delete(c.scope, "each")
	}
	body := c.encodeObject(declaration.Arguments, true)
	body["source"] = declaration.Source
	if declaration.Providers != nil {
		providers := make(map[string]any)
		for _, field := range declaration.Providers.Fields {
			identifier, ok := field.Value.(*syntax.IdentifierExpression)
			if !ok {
				c.addDiagnostic(field.GetSpan(), "module provider mapping must reference a provider configuration")
				continue
			}
			providerSymbol := c.symbols[identifier.Name]
			if providerSymbol == nil || providerSymbol.kind != symbolProviderConfig || providerSymbol.providerConfig == nil {
				c.addDiagnostic(field.GetSpan(), fmt.Sprintf("unknown provider configuration %q", identifier.Name))
				continue
			}
			key := field.Name
			if !field.Quoted {
				key = toSnakeCase(key)
			}
			config := providerSymbol.providerConfig
			reference := config.provider.localName
			if config.alias != "" {
				reference += "." + config.alias
			}
			providers[key] = reference
		}
		body["providers"] = providers
	}
	if declaration.MetaArguments != nil {
		for _, field := range declaration.MetaArguments.Fields {
			key := field.Name
			if !field.Quoted {
				key = toSnakeCase(key)
			}
			if _, exists := body[key]; exists {
				c.addDiagnostic(field.GetSpan(), fmt.Sprintf("module argument %q is defined more than once", key))
				continue
			}
			if key == "depends_on" {
				body[key] = c.encodeStaticTraversals(field.Value)
			} else {
				body[key] = c.encodeExpression(field.Value, true)
			}
		}
	}
	c.checkExpression(declaration.Arguments)
	if declaration.MetaArguments != nil {
		c.checkExpression(declaration.MetaArguments)
	}
	c.modules[declaration.Label] = body
}

func (c *compiler) compileOutput(declaration *syntax.OutputDeclaration) {
	c.checkExpression(declaration.Value)
	block := map[string]any{
		"value": c.encodeExpression(declaration.Value, false),
	}
	if declaration.Metadata != nil {
		for _, field := range declaration.Metadata.Fields {
			key := toSnakeCase(field.Name)
			switch key {
			case "description", "sensitive":
				block[key] = c.encodeExpression(field.Value, false)
				c.checkExpression(field.Value)
			default:
				c.addDiagnostic(field.GetSpan(), fmt.Sprintf("unsupported output metadata %q", field.Name))
			}
		}
	}
	c.outputBlocks[declaration.Name] = block
}

func (c *compiler) assembleRoot() {
	if len(c.required) > 0 {
		c.terraformConfig["required_providers"] = c.required
	}
	if len(c.terraformConfig) > 0 {
		c.root["terraform"] = c.terraformConfig
	}
	if len(c.variables) > 0 {
		c.root["variable"] = c.variables
	}
	if len(c.locals) > 0 {
		c.root["locals"] = c.locals
	}
	if len(c.providerBlocks) > 0 {
		providers := make(map[string]any, len(c.providerBlocks))
		for name, blocks := range c.providerBlocks {
			providers[name] = blocks
		}
		c.root["provider"] = providers
	}
	if len(c.resources) > 0 {
		resources := make(map[string]any, len(c.resources))
		for name, blocks := range c.resources {
			resources[name] = blocks
		}
		c.root["resource"] = resources
	}
	if len(c.dataSources) > 0 {
		dataSources := make(map[string]any, len(c.dataSources))
		for name, blocks := range c.dataSources {
			dataSources[name] = blocks
		}
		c.root["data"] = dataSources
	}
	if len(c.modules) > 0 {
		c.root["module"] = c.modules
	}
	if len(c.outputBlocks) > 0 {
		c.root["output"] = c.outputBlocks
	}
	if len(c.movedBlocks) > 0 {
		c.root["moved"] = c.movedBlocks
	}
}

func (c *compiler) compileType(expression *syntax.TypeExpression) valueType {
	if expression == nil {
		return valueType{kind: valueDynamic}
	}
	switch expression.Name {
	case "string":
		c.expectTypeArgumentCount(expression, 0)
		return valueType{kind: valueString}
	case "number":
		c.expectTypeArgumentCount(expression, 0)
		return valueType{kind: valueNumber}
	case "bool":
		c.expectTypeArgumentCount(expression, 0)
		return valueType{kind: valueBool}
	case "dynamic", "any":
		c.expectTypeArgumentCount(expression, 0)
		return valueType{kind: valueDynamic}
	case "object":
		c.expectTypeArgumentCount(expression, 0)
		fields := make(map[string]struct{}, len(expression.Fields))
		for _, field := range expression.Fields {
			if _, exists := fields[field.Name]; exists {
				c.addDiagnostic(field.GetSpan(), fmt.Sprintf("object type field %q is defined more than once", field.Name))
			}
			fields[field.Name] = struct{}{}
			fieldType := c.compileType(field.Type)
			if field.Default != nil {
				if !field.Optional {
					c.addDiagnostic(field.GetSpan(), "only optional object fields may have defaults")
				}
				if !isConstant(field.Default) {
					c.addDiagnostic(field.Default.GetSpan(), "object field default must be constant")
				} else if !c.inputDefaultAssignable(fieldType, field.Default) {
					c.addDiagnostic(field.Default.GetSpan(), fmt.Sprintf("default for object field %q has incompatible type", field.Name))
				}
			}
		}
		return valueType{kind: valueDynamic}
	case "list", "set":
		c.expectTypeArgumentCount(expression, 1)
		element := valueType{kind: valueDynamic}
		if len(expression.Arguments) > 0 {
			element = c.compileType(expression.Arguments[0])
		}
		return valueType{kind: valueList, element: &element}
	case "map":
		c.expectTypeArgumentCount(expression, 1)
		element := valueType{kind: valueDynamic}
		if len(expression.Arguments) > 0 {
			element = c.compileType(expression.Arguments[0])
		}
		return valueType{kind: valueMap, element: &element}
	case "optional":
		c.expectTypeArgumentCount(expression, 1)
		if len(expression.Arguments) > 0 {
			return c.compileType(expression.Arguments[0])
		}
		return valueType{kind: valueDynamic}
	default:
		c.addDiagnostic(expression.GetSpan(), fmt.Sprintf("unknown type %q", expression.Name))
		return valueType{kind: valueDynamic}
	}
}

func (c *compiler) expectTypeArgumentCount(expression *syntax.TypeExpression, expected int) {
	if len(expression.Arguments) != expected {
		c.addDiagnostic(expression.GetSpan(), fmt.Sprintf("type %s expects %d type argument(s)", expression.Name, expected))
	}
}

func terraformTypeConstraint(expression *syntax.TypeExpression) any {
	if expression == nil {
		return "any"
	}
	if expression.Name == "object" && len(expression.Fields) > 0 {
		return terraformTypeConstraintString(expression)
	}
	if len(expression.Arguments) == 0 && len(expression.Fields) == 0 {
		if expression.Name == "dynamic" || expression.Name == "object" {
			return "any"
		}
		return expression.Name
	}
	if expression.Name == "optional" && len(expression.Arguments) == 1 {
		return terraformTypeConstraint(expression.Arguments[0])
	}
	arguments := make([]string, 0, len(expression.Arguments))
	for _, argument := range expression.Arguments {
		arguments = append(arguments, terraformTypeConstraintString(argument))
	}
	return expression.Name + "(" + strings.Join(arguments, ", ") + ")"
}

func terraformTypeConstraintString(expression *syntax.TypeExpression) string {
	if expression.Name == "object" && len(expression.Fields) > 0 {
		fields := make([]string, 0, len(expression.Fields))
		for _, field := range expression.Fields {
			constraint := terraformTypeConstraintString(field.Type)
			if field.Optional {
				if field.Default != nil {
					constraint = "optional(" + constraint + ", " + renderConstant(field.Default) + ")"
				} else {
					constraint = "optional(" + constraint + ")"
				}
			}
			fields = append(fields, field.Name+" = "+constraint)
		}
		return "object({" + strings.Join(fields, ", ") + "})"
	}
	if expression.Name == "dynamic" || expression.Name == "object" {
		return "any"
	}
	if len(expression.Arguments) == 0 {
		return expression.Name
	}
	if expression.Name == "optional" && len(expression.Arguments) == 1 {
		return terraformTypeConstraintString(expression.Arguments[0])
	}
	arguments := make([]string, 0, len(expression.Arguments))
	for _, argument := range expression.Arguments {
		arguments = append(arguments, terraformTypeConstraintString(argument))
	}
	return expression.Name + "(" + strings.Join(arguments, ", ") + ")"
}

func (c *compiler) checkExpression(expression syntax.Expression) valueType {
	if expression == nil {
		return valueType{kind: valueDynamic}
	}
	switch value := expression.(type) {
	case *syntax.LiteralExpression:
		switch value.Value.(type) {
		case nil:
			return valueType{kind: valueNull}
		case string:
			return valueType{kind: valueString}
		case json.Number:
			return valueType{kind: valueNumber}
		case bool:
			return valueType{kind: valueBool}
		default:
			return valueType{kind: valueDynamic}
		}
	case *syntax.IdentifierExpression:
		if scoped, ok := c.scope[value.Name]; ok {
			return scoped
		}
		symbol := c.symbols[value.Name]
		if symbol == nil {
			c.addDiagnostic(value.GetSpan(), fmt.Sprintf("unknown name %q", value.Name))
			return valueType{kind: valueDynamic}
		}
		if symbol.kind == symbolProviderConfig {
			c.addDiagnostic(value.GetSpan(), fmt.Sprintf("provider configuration %q cannot be used as a value", value.Name))
			return valueType{kind: valueDynamic}
		}
		if symbol.kind == symbolLocal && c.inferLocals && c.localStates[value.Name] == 0 {
			if declaration := c.localDeclarations[value.Name]; declaration != nil {
				c.compileLocal(declaration)
			}
		} else if symbol.kind == symbolLocal && c.inferLocals && c.localStates[value.Name] == 1 {
			c.addDiagnostic(value.GetSpan(), fmt.Sprintf("local %q is part of a dependency cycle", value.Name))
		}
		return symbol.valueType
	case *syntax.ArrayExpression:
		element := valueType{kind: valueDynamic}
		for index, item := range value.Items {
			itemType := c.checkExpression(item)
			if index == 0 {
				element = itemType
			} else if !isAssignable(element, itemType) || !isAssignable(itemType, element) {
				element = valueType{kind: valueDynamic}
			}
		}
		return valueType{kind: valueList, element: &element}
	case *syntax.ForExpression:
		c.checkExpression(value.Collection)
		if value.KeyVariable != "" {
			c.scope[value.KeyVariable] = valueType{kind: valueDynamic}
		}
		c.scope[value.ValueVariable] = valueType{kind: valueDynamic}
		if value.Key != nil {
			c.checkExpression(value.Key)
		}
		result := c.checkExpression(value.Value)
		if value.Condition != nil {
			condition := c.checkExpression(value.Condition)
			if !isBoolOrDynamic(condition) {
				c.addDiagnostic(value.Condition.GetSpan(), "comprehension filter expects a bool")
			}
		}
		delete(c.scope, value.ValueVariable)
		if value.KeyVariable != "" {
			delete(c.scope, value.KeyVariable)
		}
		if value.Object {
			return valueType{kind: valueMap, element: &result}
		}
		return valueType{kind: valueList, element: &result}
	case *syntax.ObjectExpression:
		for _, field := range value.Fields {
			c.checkExpression(field.Value)
		}
		return valueType{kind: valueMap, element: &valueType{kind: valueDynamic}}
	case *syntax.TemplateExpression:
		for _, part := range value.Parts {
			if part.Expression != nil {
				c.checkExpression(part.Expression)
			}
		}
		return valueType{kind: valueString}
	case *syntax.UnaryExpression:
		operand := c.checkExpression(value.Operand)
		if value.Operator == syntax.TokenBang {
			if operand.kind != valueBool && operand.kind != valueDynamic {
				c.addDiagnostic(value.GetSpan(), "operator '!' expects a bool")
			}
			return valueType{kind: valueBool}
		}
		if operand.kind != valueNumber && operand.kind != valueDynamic {
			c.addDiagnostic(value.GetSpan(), "numeric unary operator expects a number")
		}
		return valueType{kind: valueNumber}
	case *syntax.BinaryExpression:
		left := c.checkExpression(value.Left)
		right := c.checkExpression(value.Right)
		switch value.Operator {
		case syntax.TokenPlus, syntax.TokenMinus, syntax.TokenStar, syntax.TokenSlash, syntax.TokenPercent:
			if !isNumberOrDynamic(left) || !isNumberOrDynamic(right) {
				c.addDiagnostic(value.GetSpan(), "arithmetic operators expect numbers")
			}
			return valueType{kind: valueNumber}
		case syntax.TokenLess, syntax.TokenLessEqual, syntax.TokenGreater, syntax.TokenGreaterEqual:
			if !isNumberOrDynamic(left) || !isNumberOrDynamic(right) {
				c.addDiagnostic(value.GetSpan(), "ordering operators expect numbers")
			}
			return valueType{kind: valueBool}
		case syntax.TokenEqual, syntax.TokenNotEqual:
			return valueType{kind: valueBool}
		case syntax.TokenAnd, syntax.TokenOr:
			if !isBoolOrDynamic(left) || !isBoolOrDynamic(right) {
				c.addDiagnostic(value.GetSpan(), "boolean operators expect bool operands")
			}
			return valueType{kind: valueBool}
		case syntax.TokenCoalesce:
			if left.kind == valueNull {
				return right
			}
			if left.kind == valueDynamic {
				return right
			}
			return left
		}
	case *syntax.ConditionalExpression:
		condition := c.checkExpression(value.Condition)
		if !isBoolOrDynamic(condition) {
			c.addDiagnostic(value.Condition.GetSpan(), "conditional expression expects a bool condition")
		}
		thenType := c.checkExpression(value.Then)
		elseType := c.checkExpression(value.Else)
		if isAssignable(thenType, elseType) && isAssignable(elseType, thenType) {
			return thenType
		}
		return valueType{kind: valueDynamic}
	case *syntax.MemberExpression:
		c.checkExpression(value.Target)
		return valueType{kind: valueDynamic}
	case *syntax.IndexExpression:
		c.checkExpression(value.Target)
		c.checkExpression(value.Index)
		return valueType{kind: valueDynamic}
	case *syntax.CallExpression:
		if _, ok := value.Callee.(*syntax.IdentifierExpression); !ok {
			c.checkExpression(value.Callee)
		}
		for _, argument := range value.Arguments {
			c.checkExpression(argument)
		}
		return valueType{kind: valueDynamic}
	}
	return valueType{kind: valueDynamic}
}

func (c *compiler) encodeExpression(expression syntax.Expression, transformKeys bool) any {
	if number, ok := constantNumber(expression); ok {
		return number
	}
	switch value := expression.(type) {
	case *syntax.LiteralExpression:
		if text, ok := value.Value.(string); ok {
			return escapeTerraformTemplate(text)
		}
		return value.Value
	case *syntax.ArrayExpression:
		items := make([]any, 0, len(value.Items))
		for _, item := range value.Items {
			items = append(items, c.encodeExpression(item, transformKeys))
		}
		return items
	case *syntax.ObjectExpression:
		return c.encodeObject(value, transformKeys)
	case *syntax.TemplateExpression:
		return c.renderTemplate(value, transformKeys)
	default:
		return "${" + c.renderExpression(expression, transformKeys) + "}"
	}
}

func constantNumber(expression syntax.Expression) (json.Number, bool) {
	switch value := expression.(type) {
	case *syntax.LiteralExpression:
		number, ok := value.Value.(json.Number)
		return number, ok
	case *syntax.UnaryExpression:
		if value.Operator != syntax.TokenMinus {
			return "", false
		}
		number, ok := constantNumber(value.Operand)
		if !ok {
			return "", false
		}
		text := number.String()
		if strings.HasPrefix(text, "-") {
			return json.Number(strings.TrimPrefix(text, "-")), true
		}
		return json.Number("-" + text), true
	default:
		return "", false
	}
}

func (c *compiler) encodeObject(expression *syntax.ObjectExpression, transformKeys bool) map[string]any {
	result := make(map[string]any, len(expression.Fields))
	for _, field := range expression.Fields {
		key := field.Name
		if transformKeys && !field.Quoted {
			key = toSnakeCase(key)
		}
		if _, exists := result[key]; exists {
			c.addDiagnostic(field.GetSpan(), fmt.Sprintf("object field %q is defined more than once", key))
			continue
		}
		result[key] = c.encodeExpression(field.Value, transformKeys)
	}
	return result
}

func (c *compiler) renderTemplate(expression *syntax.TemplateExpression, transformKeys bool) string {
	var result strings.Builder
	for index, part := range expression.Parts {
		if part.Expression != nil {
			result.WriteString("${")
			result.WriteString(c.renderExpression(part.Expression, transformKeys))
			result.WriteByte('}')
		} else {
			text := part.Text
			if index+1 < len(expression.Parts) && expression.Parts[index+1].Expression != nil {
				dollars := len(text) - len(strings.TrimRight(text, "$"))
				text = escapeTerraformTemplate(text[:len(text)-dollars]) + strings.Repeat(`${"$"}`, dollars)
			} else {
				text = escapeTerraformTemplate(text)
			}
			result.WriteString(text)
		}
	}
	return result.String()
}

func (c *compiler) renderExpression(expression syntax.Expression, transformKeys bool) string {
	switch value := expression.(type) {
	case *syntax.IdentifierExpression:
		symbol := c.symbols[value.Name]
		if symbol == nil {
			return value.Name
		}
		switch symbol.kind {
		case symbolInput:
			return "var." + value.Name
		case symbolLocal:
			return "local." + value.Name
		case symbolResource:
			if symbol.resource != nil {
				return symbol.resource.terraformType + "." + symbol.resource.label
			}
		case symbolData:
			if symbol.resource != nil {
				return "data." + symbol.resource.terraformType + "." + symbol.resource.label
			}
		case symbolModule:
			return "module." + symbol.moduleLabel
		}
		return value.Name
	case *syntax.LiteralExpression:
		switch literal := value.Value.(type) {
		case nil:
			return "null"
		case string:
			return quoteHCLString(escapeTerraformTemplate(literal))
		case bool:
			return strconv.FormatBool(literal)
		case json.Number:
			return literal.String()
		}
	case *syntax.ArrayExpression:
		items := make([]string, 0, len(value.Items))
		for _, item := range value.Items {
			items = append(items, c.renderExpression(item, transformKeys))
		}
		return "[" + strings.Join(items, ", ") + "]"
	case *syntax.ForExpression:
		iterator := value.ValueVariable
		if value.KeyVariable != "" {
			iterator = value.KeyVariable + ", " + value.ValueVariable
		}
		body := c.renderExpression(value.Value, transformKeys)
		opening, closing := "[", "]"
		if value.Object {
			opening, closing = "{", "}"
			body = c.renderExpression(value.Key, transformKeys) + " => " + body
		}
		if value.Condition != nil {
			body += " if " + c.renderExpression(value.Condition, transformKeys)
		}
		return opening + "for " + iterator + " in " + c.renderExpression(value.Collection, transformKeys) + " : " + body + closing
	case *syntax.ObjectExpression:
		fields := make([]string, 0, len(value.Fields))
		for _, field := range value.Fields {
			key := field.Name
			if transformKeys && !field.Quoted {
				key = toSnakeCase(key)
			}
			fields = append(fields, quoteHCLString(key)+" = "+c.renderExpression(field.Value, transformKeys))
		}
		return "{" + strings.Join(fields, ", ") + "}"
	case *syntax.TemplateExpression:
		return quoteHCLString(c.renderTemplate(value, transformKeys))
	case *syntax.UnaryExpression:
		return tokenOperator(value.Operator) + c.renderExpression(value.Operand, transformKeys)
	case *syntax.BinaryExpression:
		left := c.renderExpression(value.Left, transformKeys)
		right := c.renderExpression(value.Right, transformKeys)
		if value.Operator == syntax.TokenCoalesce {
			return "(" + left + " != null ? " + left + " : " + right + ")"
		}
		return "(" + left + " " + tokenOperator(value.Operator) + " " + right + ")"
	case *syntax.ConditionalExpression:
		return "(" + c.renderExpression(value.Condition, transformKeys) + " ? " + c.renderExpression(value.Then, transformKeys) + " : " + c.renderExpression(value.Else, transformKeys) + ")"
	case *syntax.MemberExpression:
		return c.renderExpression(value.Target, transformKeys) + "." + value.Name
	case *syntax.IndexExpression:
		return c.renderExpression(value.Target, transformKeys) + "[" + c.renderExpression(value.Index, transformKeys) + "]"
	case *syntax.CallExpression:
		if identifier, ok := value.Callee.(*syntax.IdentifierExpression); ok && identifier.Name == "address" && len(value.Arguments) == 1 {
			if address, ok := literalString(value.Arguments[0]); ok {
				return address
			}
		}
		arguments := make([]string, 0, len(value.Arguments))
		for _, argument := range value.Arguments {
			arguments = append(arguments, c.renderExpression(argument, transformKeys))
		}
		return c.renderExpression(value.Callee, transformKeys) + "(" + strings.Join(arguments, ", ") + ")"
	}
	return "null"
}

func (c *compiler) encodeStaticTraversals(expression syntax.Expression) any {
	array, ok := expression.(*syntax.ArrayExpression)
	if !ok {
		c.addDiagnostic(expression.GetSpan(), "static traversal metadata must be an array")
		return c.encodeExpression(expression, false)
	}
	result := make([]any, 0, len(array.Items))
	for _, item := range array.Items {
		if call, ok := item.(*syntax.CallExpression); ok {
			if identifier, ok := call.Callee.(*syntax.IdentifierExpression); ok && identifier.Name == "address" && len(call.Arguments) == 1 {
				if address, ok := literalString(call.Arguments[0]); ok {
					result = append(result, address)
					continue
				}
			}
		}
		result = append(result, c.renderExpression(item, false))
	}
	return result
}

func (c *compiler) encodeLifecycle(expression syntax.Expression) any {
	object, ok := expression.(*syntax.ObjectExpression)
	if !ok {
		c.addDiagnostic(expression.GetSpan(), "lifecycle metadata must be an object")
		return c.encodeExpression(expression, true)
	}
	result := make(map[string]any, len(object.Fields))
	for _, field := range object.Fields {
		key := field.Name
		if !field.Quoted {
			key = toSnakeCase(key)
		}
		if key == "ignore_changes" || key == "replace_triggered_by" {
			result[key] = c.encodeStaticTraversals(field.Value)
		} else {
			result[key] = c.encodeExpression(field.Value, true)
		}
	}
	return result
}

func isConstant(expression syntax.Expression) bool {
	switch value := expression.(type) {
	case *syntax.LiteralExpression:
		return true
	case *syntax.UnaryExpression:
		return value.Operator == syntax.TokenMinus && isConstant(value.Operand)
	case *syntax.ArrayExpression:
		for _, item := range value.Items {
			if !isConstant(item) {
				return false
			}
		}
		return true
	case *syntax.ObjectExpression:
		for _, field := range value.Fields {
			if !isConstant(field.Value) {
				return false
			}
		}
		return true
	case *syntax.TemplateExpression:
		for _, part := range value.Parts {
			if part.Expression != nil {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func renderConstant(expression syntax.Expression) string {
	if number, ok := constantNumber(expression); ok {
		return number.String()
	}
	switch value := expression.(type) {
	case *syntax.LiteralExpression:
		switch literal := value.Value.(type) {
		case nil:
			return "null"
		case string:
			return quoteHCLString(literal)
		case bool:
			return strconv.FormatBool(literal)
		}
	case *syntax.ArrayExpression:
		items := make([]string, 0, len(value.Items))
		for _, item := range value.Items {
			items = append(items, renderConstant(item))
		}
		return "[" + strings.Join(items, ", ") + "]"
	case *syntax.ObjectExpression:
		fields := make([]string, 0, len(value.Fields))
		for _, field := range value.Fields {
			fields = append(fields, field.Name+" = "+renderConstant(field.Value))
		}
		return "{" + strings.Join(fields, ", ") + "}"
	}
	return "null"
}

func hasObjectField(object *syntax.ObjectExpression, names ...string) bool {
	for _, field := range object.Fields {
		for _, name := range names {
			if field.Name == name {
				return true
			}
		}
	}
	return false
}

func literalString(expression syntax.Expression) (string, bool) {
	literal, ok := expression.(*syntax.LiteralExpression)
	if !ok {
		return "", false
	}
	value, ok := literal.Value.(string)
	return value, ok
}

func providerLocalName(source string) string {
	name := path.Base(strings.TrimSpace(source))
	if name == "." || name == "/" {
		return ""
	}
	return toSnakeCase(name)
}

var nonIdentifier = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

func toSnakeCase(value string) string {
	value = nonIdentifier.ReplaceAllString(value, "_")
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

func tokenOperator(kind syntax.TokenKind) string {
	switch kind {
	case syntax.TokenPlus:
		return "+"
	case syntax.TokenMinus:
		return "-"
	case syntax.TokenStar:
		return "*"
	case syntax.TokenSlash:
		return "/"
	case syntax.TokenPercent:
		return "%"
	case syntax.TokenBang:
		return "!"
	case syntax.TokenEqual:
		return "=="
	case syntax.TokenNotEqual:
		return "!="
	case syntax.TokenLess:
		return "<"
	case syntax.TokenLessEqual:
		return "<="
	case syntax.TokenGreater:
		return ">"
	case syntax.TokenGreaterEqual:
		return ">="
	case syntax.TokenAnd:
		return "&&"
	case syntax.TokenOr:
		return "||"
	default:
		return kind.String()
	}
}

func escapeTerraformTemplate(value string) string {
	value = strings.ReplaceAll(value, "${", "$${")
	return strings.ReplaceAll(value, "%{", "%%{")
}

func quoteHCLString(value string) string {
	var result strings.Builder
	result.WriteByte('"')
	for _, current := range value {
		switch current {
		case '\\':
			result.WriteString(`\\`)
		case '"':
			result.WriteString(`\"`)
		case '\n':
			result.WriteString(`\n`)
		case '\r':
			result.WriteString(`\r`)
		case '\t':
			result.WriteString(`\t`)
		default:
			if current < 0x20 {
				fmt.Fprintf(&result, `\u%04x`, current)
			} else {
				result.WriteRune(current)
			}
		}
	}
	result.WriteByte('"')
	return result.String()
}

func isAssignable(expected, actual valueType) bool {
	if expected.kind == valueDynamic || actual.kind == valueDynamic || actual.kind == valueNull {
		return true
	}
	if expected.kind != actual.kind {
		return false
	}
	if expected.element != nil && actual.element != nil {
		return isAssignable(*expected.element, *actual.element)
	}
	return true
}

func (c *compiler) inputDefaultAssignable(expected valueType, expression syntax.Expression) bool {
	if expected.kind == valueDynamic {
		return true
	}
	switch expected.kind {
	case valueList:
		array, ok := expression.(*syntax.ArrayExpression)
		if !ok {
			return isAssignable(expected, c.checkExpression(expression))
		}
		if expected.element == nil {
			return true
		}
		for _, item := range array.Items {
			if !c.inputDefaultAssignable(*expected.element, item) {
				return false
			}
		}
		return true
	case valueMap:
		object, ok := expression.(*syntax.ObjectExpression)
		if !ok {
			return isAssignable(expected, c.checkExpression(expression))
		}
		if expected.element == nil {
			return true
		}
		for _, field := range object.Fields {
			if !c.inputDefaultAssignable(*expected.element, field.Value) {
				return false
			}
		}
		return true
	default:
		return isAssignable(expected, c.checkExpression(expression))
	}
}

func isReservedName(name string) bool {
	switch name {
	case "true", "false", "null", "none":
		return true
	default:
		return false
	}
}

func isNumberOrDynamic(value valueType) bool {
	return value.kind == valueNumber || value.kind == valueDynamic
}

func isBoolOrDynamic(value valueType) bool {
	return value.kind == valueBool || value.kind == valueDynamic
}

func (c *compiler) addDiagnostic(span syntax.Span, message string) {
	c.diagnostics = append(c.diagnostics, syntax.Diagnostic{
		Filename: c.file.Name,
		Span:     span,
		Message:  message,
	})
}

func SortedDiagnostics(diagnostics []syntax.Diagnostic) []syntax.Diagnostic {
	result := append([]syntax.Diagnostic(nil), diagnostics...)
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Filename != result[j].Filename {
			return result[i].Filename < result[j].Filename
		}
		return result[i].Span.Start.Offset < result[j].Span.Start.Offset
	})
	return result
}
