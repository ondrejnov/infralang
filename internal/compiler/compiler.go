package compiler

import (
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"

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
	conditional   bool
}

type symbol struct {
	kind           symbolKind
	span           syntax.Span
	valueType      valueType
	providerConfig *providerConfigInfo
	resource       *resourceInfo
	moduleLabel    string
	name           BindingName
	moduleContract *ModuleInterface
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
	outputTypes       map[string]valueType
	localDeclarations map[string]*syntax.LetDeclaration
	localStates       map[string]int
	inferLocals       bool
	resourceAddresses map[string]syntax.Span
	dataAddresses     map[string]syntax.Span
	moduleAddresses   map[string]syntax.Span
	movedBlocks       []any
	scope             *scopeFrame
	typeCache         map[syntax.Expression]valueType
	inputWires        map[string]*syntax.InputDeclaration
	inputSources      map[string]*syntax.InputDeclaration
	typeAliases       map[string]*typeAliasInfo
	aliasStack        []string
	aliasCycles       map[string]bool
	options           CompileOptions
}

func Compile(file *syntax.File) ([]byte, []syntax.Diagnostic) {
	return CompileWithOptions(file, CompileOptions{})
}

func CompileWithOptions(file *syntax.File, options CompileOptions) ([]byte, []syntax.Diagnostic) {
	prepared, diagnostics := Prepare(file)
	if len(diagnostics) != 0 {
		return nil, diagnostics
	}
	c := newCompiler(prepared, options)
	return c.run()
}

func newCompiler(file *syntax.File, options CompileOptions) *compiler {
	return &compiler{
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
		outputTypes:       make(map[string]valueType),
		localDeclarations: make(map[string]*syntax.LetDeclaration),
		localStates:       make(map[string]int),
		resourceAddresses: make(map[string]syntax.Span),
		dataAddresses:     make(map[string]syntax.Span),
		moduleAddresses:   make(map[string]syntax.Span),
		scope:             newScope(nil),
		typeCache:         make(map[syntax.Expression]valueType),
		inputWires:        make(map[string]*syntax.InputDeclaration),
		inputSources:      make(map[string]*syntax.InputDeclaration),
		typeAliases:       make(map[string]*typeAliasInfo),
		aliasCycles:       make(map[string]bool),
		options:           options,
	}
}

func (c *compiler) run() ([]byte, []syntax.Diagnostic) {
	c.collectDeclarations()
	c.compileDeclarations()
	if len(c.diagnostics) != 0 {
		return nil, syntax.SortDiagnostics(c.diagnostics)
	}
	c.assembleRoot()

	result, err := json.MarshalIndent(c.root, "", "  ")
	if err != nil {
		c.addDiagnostic(syntax.Span{}, fmt.Sprintf("failed to encode Terraform JSON: %v", err))
		return nil, syntax.SortDiagnostics(c.diagnostics)
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
			if previous, exists := c.inputSources[value.Name]; exists {
				c.addDiagnostic(previous.GetSpan(), fmt.Sprintf("input source name %q conflicts with another input", value.Name))
				c.addDiagnostic(value.GetSpan(), fmt.Sprintf("input source name %q conflicts with another input", value.Name))
				continue
			}
			c.inputSources[value.Name] = value
			wire := value.WireName
			if wire == "" || !value.ExplicitWire {
				wire = syntax.SourceNameToWire(value.Name)
			}
			if previous, exists := c.inputWires[wire]; exists {
				c.addDiagnostic(previous.GetSpan(), fmt.Sprintf("input wire name %q conflicts with another input", wire))
				c.addDiagnostic(value.GetSpan(), fmt.Sprintf("input wire name %q conflicts with another input", wire))
				continue
			} else {
				c.inputWires[wire] = value
			}
			c.collectSymbol(value.Name, symbolInput, value.GetSpan())
			if input := c.symbols[value.Name]; input != nil && input.kind == symbolInput {
				if value.ExplicitWire {
					input.name = aliasedInputName(value.Name, wire)
				} else {
					input.name = unaliasedInputName(value.Name)
				}
			}
		case *syntax.TypeAliasDeclaration:
			if isBuiltinType(value.Name) {
				c.addDiagnostic(value.GetSpan(), fmt.Sprintf("type alias %q cannot shadow a builtin type", value.Name))
				continue
			}
			if previous, exists := c.typeAliases[value.Name]; exists {
				c.addDiagnostic(previous.declaration.GetSpan(), fmt.Sprintf("type alias %q conflicts with another alias", value.Name))
				c.addDiagnostic(value.GetSpan(), fmt.Sprintf("type alias %q conflicts with another alias", value.Name))
				continue
			}
			c.typeAliases[value.Name] = &typeAliasInfo{declaration: value}
		case *syntax.TypeImportDeclaration:
			c.addDiagnostic(value.GetSpan(), "type imports require project compilation")
		case *syntax.ModuleImportDeclaration:
			// Module imports are bound and erased during preparation.
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
	c.symbols[name] = &symbol{kind: kind, span: span, valueType: valueType{kind: valueDynamic}, name: BindingName{Source: name, Wire: name}}
}

func (c *compiler) compileDeclarations() {
	// Declarations are order-independent. Compile them in dependency phases so
	// resource and module references already have stable Terraform addresses
	// when locals and outputs are lowered.
	for _, declaration := range c.file.Declarations {
		if alias, ok := declaration.(*syntax.TypeAliasDeclaration); ok {
			if info := c.typeAliases[alias.Name]; info != nil && info.declaration == alias {
				c.compileAlias(alias.Name, alias.Type.GetSpan())
			}
		}
	}
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
	c.registerAddresses()
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
	c.checkComponentChecks()
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
			if len(value.Items) == 0 {
				c.movedBlocks = append(c.movedBlocks, map[string]any{"from": value.From, "to": value.To})
				continue
			}
			for _, item := range value.Items {
				c.movedBlocks = append(c.movedBlocks, map[string]any{"from": item.From.Raw, "to": item.To.Raw})
			}
		}
	}
}

func (c *compiler) compileTerraform(declaration *syntax.TerraformDeclaration) {
	config := c.encodeBlockBody(declaration.Config)
	for key, encoded := range config {
		switch key {
		case "requiredVersion", "required_version":
			value, ok := encoded.(string)
			if !ok || strings.HasPrefix(value, "${") {
				c.addDiagnostic(declaration.Config.GetSpan(), "terraform requiredVersion must be a literal string")
				continue
			}
			c.terraformConfig["required_version"] = value
		default:
			c.addDiagnostic(declaration.Config.GetSpan(), fmt.Sprintf("unsupported terraform setting %q", key))
		}
	}
	c.checkExpression(declaration.Config)
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
	symbol := c.symbols[declaration.Name]
	if symbol == nil || symbol.kind != symbolInput {
		return
	}
	symbol.valueType = typeInfo
	block := map[string]any{"type": c.terraformTypeConstraint(declaration.Type)}
	if declaration.Default != nil {
		actual := c.checkExpression(declaration.Default)
		c.checkAssignment(typeInfo, actual, declaration.Default.GetSpan(), "default for "+strconv.Quote(declaration.Name))
		if !isConstant(declaration.Default) {
			c.addDiagnostic(declaration.Default.GetSpan(), "input default must be a constant expression")
		} else {
			block["default"] = c.encodeValueWithDefaults(declaration.Default, typeInfo)
		}
	} else if declaration.Type != nil && declaration.Type.Name == "optional" {
		block["default"] = nil
	}
	if declaration.Metadata != nil {
		items := declaration.MetadataItems
		if len(items) == 0 {
			items = make([]syntax.InputMetadataItem, 0, len(declaration.Metadata.Fields))
			for _, field := range declaration.Metadata.Fields {
				items = append(items, field)
			}
		}
		items = c.expandInputMetadataItems(items)
		var validations []any
		for _, item := range items {
			switch value := item.(type) {
			case syntax.ValidationClause:
				conditionType := c.checkExpression(value.Condition)
				if !isBoolOrDynamic(conditionType) {
					c.addDiagnostic(value.Condition.GetSpan(), "input validation condition expects a bool")
				}
				validations = append(validations, map[string]any{
					"condition":     c.encodeValue(value.Condition),
					"error_message": value.Message,
				})
			case syntax.ObjectField:
				field := value
				if field.Condition != nil {
					conditionType := c.checkExpression(field.Condition)
					if !isBoolOrDynamic(conditionType) {
						c.addDiagnostic(field.Condition.GetSpan(), "conditional object field expects a bool condition")
						continue
					}
					condition, constant := literalBool(field.Condition)
					if !constant {
						c.addDiagnostic(field.Condition.GetSpan(), "Terraform metadata fields must be statically known")
						continue
					}
					if !condition {
						continue
					}
				}
				key := field.WireName
				if key == "" {
					key = syntax.SourceNameToWire(field.Name)
				}
				switch key {
				case "description", "sensitive", "nullable":
					block[key] = c.encodeValue(field.Value)
					fieldType := c.checkExpression(field.Value)
					if key == "sensitive" {
						if literal, ok := field.Value.(*syntax.LiteralExpression); ok {
							if sensitive, ok := literal.Value.(bool); ok && sensitive {
								typeInfo.sensitive = true
								symbol.valueType.sensitive = true
							}
						}
						if !isBoolOrDynamic(fieldType) {
							c.addDiagnostic(field.Value.GetSpan(), "input sensitive metadata expects a bool")
						}
					}
				case "validations":
					array, ok := field.Value.(*syntax.ArrayExpression)
					if !ok {
						c.addDiagnostic(field.GetSpan(), "input validations must be an array")
						continue
					}
					for _, item := range array.Items {
						object, ok := item.(*syntax.ObjectExpression)
						if !ok {
							c.addDiagnostic(item.GetSpan(), "each input validation must be an object")
							continue
						}
						validations = append(validations, c.encodeBlockBody(object))
						c.checkExpression(object)
					}
				default:
					c.addDiagnostic(field.GetSpan(), fmt.Sprintf("unsupported input metadata %q", field.Name))
				}
			}
		}
		if len(validations) > 0 {
			block["validation"] = validations
		}
	}
	c.variables[symbol.name.Wire] = block
}

func (c *compiler) expandInputMetadataItems(items []syntax.InputMetadataItem) []syntax.InputMetadataItem {
	var result []syntax.InputMetadataItem
	for _, item := range items {
		spread, ok := item.(syntax.ObjectSpread)
		if !ok {
			result = append(result, item)
			continue
		}
		object, ok := spread.Value.(*syntax.ObjectExpression)
		if !ok {
			c.checkExpression(spread.Value)
			c.addDiagnostic(spread.GetSpan(), "Terraform metadata spread must have a statically known object shape")
			continue
		}
		var expanded []syntax.InputMetadataItem
		for _, objectItem := range objectItems(object) {
			switch value := objectItem.(type) {
			case syntax.ObjectField:
				expanded = append(expanded, value)
			case syntax.ObjectSpread:
				expanded = append(expanded, value)
			}
		}
		result = append(result, c.expandInputMetadataItems(expanded)...)
	}
	return result
}

func (c *compiler) compileLocal(declaration *syntax.LetDeclaration) {
	localSymbol := c.symbols[declaration.Name]
	if localSymbol == nil || localSymbol.kind != symbolLocal {
		return
	}
	switch c.localStates[declaration.Name] {
	case 1:
		c.addDiagnostic(declaration.GetSpan(), fmt.Sprintf("local %q is part of a dependency cycle", declaration.Name))
		return
	case 2:
		return
	}
	c.localStates[declaration.Name] = 1
	valueType := c.checkExpression(declaration.Value)
	localSymbol.valueType = valueType
	c.locals[declaration.Name] = c.encodeValue(declaration.Value)
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
	providerSymbol := c.symbols[declaration.Name]
	if providerSymbol == nil || providerSymbol.kind != symbolProviderConfig {
		return
	}
	providerSymbol.providerConfig = &providerConfigInfo{provider: provider, alias: alias}
}

func (c *compiler) compileProviderConfig(declaration *syntax.ConfigureDeclaration) {
	providerSymbol := c.symbols[declaration.Name]
	if providerSymbol == nil || providerSymbol.kind != symbolProviderConfig {
		return
	}
	providerConfig := providerSymbol.providerConfig
	if providerConfig == nil {
		return
	}
	if declaration.Inherited {
		return
	}
	config := c.encodeProviderConfigBody(declaration.Config, providerConfig.provider)
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
			if symbol := c.symbols[value.Name]; symbol != nil && symbol.kind == symbolResource {
				symbol.resource = &resourceInfo{terraformType: terraformType, label: value.Label, conditional: value.Condition != nil}
			}
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
			if symbol := c.symbols[value.Name]; symbol != nil && symbol.kind == symbolData {
				symbol.resource = &resourceInfo{terraformType: terraformType, label: value.Label}
			}
		case *syntax.ModuleDeclaration:
			if _, exists := c.moduleAddresses[value.Label]; exists {
				c.addDiagnostic(value.GetSpan(), fmt.Sprintf("Terraform module %q is already declared", value.Label))
				continue
			}
			c.moduleAddresses[value.Label] = value.GetSpan()
			if symbol := c.symbols[value.Name]; symbol != nil && symbol.kind == symbolModule {
				symbol.moduleLabel = value.Label
				if contract, exists := c.options.LocalModules[value.Source]; exists {
					contract := contract
					symbol.moduleContract = &contract
					outputType := contract.outputObjectType()
					if _, iterated := objectField(value.MetaArguments, "forEach", "for_each"); iterated {
						symbol.valueType = valueType{kind: valueMap, element: &outputType, sensitive: outputType.sensitive}
					} else {
						symbol.valueType = outputType
					}
				}
			}
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
	if terraformType != localName && !strings.HasPrefix(terraformType, localName+"_") {
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

	previousScope := c.scope
	if field, ok := objectField(declaration.With, "forEach", "for_each"); ok {
		collectionType := c.checkExpression(field.Value)
		keyType, itemType, valid := eachTypes(collectionType)
		if !valid && collectionType.kind != valueDynamic {
			c.addDiagnostic(field.Value.GetSpan(), fmt.Sprintf("invalid forEach type %s; expected map<T>, object, or set<string>", valueTypeDescription(collectionType)))
		}
		c.scope = newScope(previousScope)
		c.scope.bindings["each"] = eachObjectType(keyType, itemType)
		defer func() { c.scope = previousScope }()
	}
	body := c.encodeResourceBody(declaration.Arguments)
	if declaration.Condition != nil {
		conditionType := c.checkExpression(declaration.Condition)
		if !isBoolOrDynamic(conditionType) {
			c.addDiagnostic(declaration.Condition.GetSpan(), "resource when condition expects a bool")
		}
		body["count"] = "${" + c.renderExpression(declaration.Condition) + " ? 1 : 0}"
	}
	if declaration.With != nil {
		metadata := c.encodeBlockBody(declaration.With)
		for _, field := range declaration.With.Fields {
			key := objectFieldName(field).Wire
			if !includedObjectField(field) {
				continue
			}
			if key == "depends_on" {
				metadata[key] = c.encodeStaticTraversals(field.Value)
			} else if key == "lifecycle" {
				metadata[key] = c.encodeLifecycle(field.Value)
			}
		}
		for key, value := range metadata {
			if declaration.Condition != nil && (key == "count" || key == "for_each") {
				c.addDiagnostic(declaration.With.GetSpan(), "resource when conflicts with explicit cardinality metadata")
				continue
			}
			if _, exists := body[key]; exists {
				c.addDiagnostic(declaration.With.GetSpan(), fmt.Sprintf("resource argument %q is defined more than once", key))
				continue
			}
			body[key] = value
		}
	}
	if !providerConfig.provider.builtin && providerConfig.alias != "" {
		body["provider"] = providerConfig.provider.localName + "." + providerConfig.alias
	}
	c.checkExpression(declaration.Arguments)
	if declaration.With != nil {
		c.checkExpression(declaration.With)
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

	body := c.encodeBlockBody(declaration.Arguments)
	if !providerConfig.provider.builtin && providerConfig.alias != "" {
		body["provider"] = providerConfig.provider.localName + "." + providerConfig.alias
	}
	c.checkExpression(declaration.Arguments)
	c.dataSources[terraformType][declaration.Label] = body
}

func (c *compiler) compileModule(declaration *syntax.ModuleDeclaration) {
	previousScope := c.scope
	if field, ok := objectField(declaration.MetaArguments, "forEach", "for_each"); ok {
		collectionType := c.checkExpression(field.Value)
		keyType, itemType, valid := eachTypes(collectionType)
		if !valid && collectionType.kind != valueDynamic {
			c.addDiagnostic(field.Value.GetSpan(), fmt.Sprintf("invalid forEach type %s; expected map<T>, object, or set<string>", valueTypeDescription(collectionType)))
		}
		c.scope = newScope(previousScope)
		c.scope.bindings["each"] = eachObjectType(keyType, itemType)
		defer func() { c.scope = previousScope }()
	}
	body := c.compileModuleArguments(declaration)
	body["source"] = declaration.Source
	suppliedProviders := make(map[string]*providerInfo)
	if declaration.Providers != nil {
		providers := make(map[string]any)
		if declaration.Providers.Explicit != nil {
			for _, field := range declaration.Providers.Explicit.Fields {
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
				key := objectFieldName(field).Wire
				config := providerSymbol.providerConfig
				reference := config.provider.localName
				if config.alias != "" {
					reference += "." + config.alias
				}
				providers[key] = reference
				suppliedProviders[key] = config.provider
			}
		} else {
			seenHandles := make(map[string]struct{})
			seenProviders := make(map[*providerInfo]struct{})
			for _, expression := range declaration.Providers.Inferred {
				identifier, ok := expression.(*syntax.IdentifierExpression)
				if !ok {
					c.addDiagnostic(expression.GetSpan(), "module provider shorthand must reference a provider configuration")
					continue
				}
				providerSymbol := c.symbols[identifier.Name]
				if providerSymbol == nil || providerSymbol.kind != symbolProviderConfig || providerSymbol.providerConfig == nil {
					c.addDiagnostic(expression.GetSpan(), fmt.Sprintf("unknown provider configuration %q", identifier.Name))
					continue
				}
				if _, exists := seenHandles[identifier.Name]; exists {
					c.addDiagnostic(expression.GetSpan(), fmt.Sprintf("provider configuration %q is repeated in shorthand", identifier.Name))
					continue
				}
				config := providerSymbol.providerConfig
				if _, exists := seenProviders[config.provider]; exists {
					c.addDiagnostic(expression.GetSpan(), fmt.Sprintf("provider %q is repeated through multiple configurations", config.provider.localName))
					continue
				}
				key := config.provider.localName
				if _, exists := providers[key]; exists {
					c.addDiagnostic(expression.GetSpan(), fmt.Sprintf("module provider key %q is inferred more than once", key))
					continue
				}
				reference := config.provider.localName
				if config.alias != "" {
					reference += "." + config.alias
				}
				providers[key] = reference
				suppliedProviders[key] = config.provider
				seenHandles[identifier.Name] = struct{}{}
				seenProviders[config.provider] = struct{}{}
			}
		}
		body["providers"] = providers
	}
	c.checkModuleProviders(declaration, suppliedProviders)
	if declaration.MetaArguments != nil {
		metadata := c.encodeBlockBody(declaration.MetaArguments)
		for _, field := range declaration.MetaArguments.Fields {
			key := objectFieldName(field).Wire
			if !includedObjectField(field) {
				continue
			}
			if key == "depends_on" {
				metadata[key] = c.encodeStaticTraversals(field.Value)
			}
		}
		for key, value := range metadata {
			if _, exists := body[key]; exists {
				c.addDiagnostic(declaration.MetaArguments.GetSpan(), fmt.Sprintf("module argument %q is defined more than once", key))
				continue
			}
			body[key] = value
		}
	}
	if declaration.MetaArguments != nil {
		c.checkExpression(declaration.MetaArguments)
	}
	c.modules[declaration.Label] = body
}

func (c *compiler) compileOutput(declaration *syntax.OutputDeclaration) {
	outputType := c.checkExpression(declaration.Value)
	c.outputTypes[declaration.Name] = outputType
	block := map[string]any{
		"value": c.encodeValue(declaration.Value),
	}
	if declaration.Metadata != nil {
		for key, value := range c.encodeBlockBody(declaration.Metadata) {
			switch key {
			case "description", "sensitive":
				block[key] = value
			default:
				c.addDiagnostic(declaration.Metadata.GetSpan(), fmt.Sprintf("unsupported output metadata %q", key))
			}
		}
		c.checkExpression(declaration.Metadata)
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
	if len(expression.Operands) > 0 {
		return c.compileTypeComposition(expression)
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
		fields := make(map[string]syntax.TypeField, len(expression.Fields))
		wireFields := make(map[string]syntax.TypeField, len(expression.Fields))
		result := valueType{kind: valueObject}
		for _, field := range expression.Fields {
			sourceConflict := false
			if !field.Quoted {
				if previous, exists := fields[field.Name]; exists {
					c.addDiagnostic(previous.GetSpan(), fmt.Sprintf("object type source field %q conflicts with another field", field.Name))
					c.addDiagnostic(field.GetSpan(), fmt.Sprintf("object type source field %q conflicts with another field", field.Name))
					sourceConflict = true
				}
				fields[field.Name] = field
			}
			wire := field.WireName
			if wire == "" {
				if field.Quoted {
					wire = field.Name
				} else {
					wire = syntax.SourceNameToWire(field.Name)
				}
			}
			if previous, exists := wireFields[wire]; exists && !sourceConflict {
				c.addDiagnostic(previous.GetSpan(), fmt.Sprintf("object type wire field %q conflicts with another field", wire))
				c.addDiagnostic(field.GetSpan(), fmt.Sprintf("object type wire field %q conflicts with another field", wire))
			}
			wireFields[wire] = field
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
			name := BindingName{Source: field.Name, Wire: wire, ExplicitWire: field.ExplicitWire, Quoted: field.Quoted}
			if field.Quoted {
				name.Source = ""
			}
			result.fields = append(result.fields, valueField{
				name: name, typeInfo: fieldType, optional: field.Optional,
				defaulted: field.Default != nil, defaultValue: field.Default, span: field.GetSpan(),
			})
		}
		return result
	case "list", "set":
		c.expectTypeArgumentCount(expression, 1)
		element := valueType{kind: valueDynamic}
		if len(expression.Arguments) > 0 {
			element = c.compileType(expression.Arguments[0])
		}
		kind := valueList
		if expression.Name == "set" {
			kind = valueSet
		}
		return valueType{kind: kind, element: &element}
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
		return c.compileAlias(expression.Name, expression.GetSpan())
	}
}

func (c *compiler) compileTypeComposition(expression *syntax.TypeExpression) valueType {
	result := valueType{kind: valueObject}
	type fieldOwner struct {
		field   valueField
		operand int
	}
	sourceFields := make(map[string]fieldOwner)
	wireFields := make(map[string]fieldOwner)
	for operandIndex, operand := range expression.Operands {
		operandType := c.compileType(operand)
		objectOperand, resolvedName := c.isObjectCompositionOperand(operand, make(map[string]bool))
		if !objectOperand {
			c.addDiagnostic(operand.GetSpan(), fmt.Sprintf("type composition operand must resolve directly to an object type, got %s", resolvedName))
			continue
		}
		result.sensitive = result.sensitive || operandType.sensitive
		result.open = result.open || operandType.open
		for _, field := range operandType.fields {
			sourceConflict := false
			if field.name.Source != "" {
				if previous, exists := sourceFields[field.name.Source]; exists && previous.operand != operandIndex {
					c.addDiagnostic(previous.field.span, fmt.Sprintf("composed object type source field %q conflicts with another field", field.name.Source))
					c.addDiagnostic(field.span, fmt.Sprintf("composed object type source field %q conflicts with another field", field.name.Source))
					sourceConflict = true
				}
				sourceFields[field.name.Source] = fieldOwner{field: field, operand: operandIndex}
			}
			if previous, exists := wireFields[field.name.Wire]; exists && previous.operand != operandIndex && !sourceConflict {
				c.addDiagnostic(previous.field.span, fmt.Sprintf("composed object type wire field %q conflicts with another field", field.name.Wire))
				c.addDiagnostic(field.span, fmt.Sprintf("composed object type wire field %q conflicts with another field", field.name.Wire))
			}
			wireFields[field.name.Wire] = fieldOwner{field: field, operand: operandIndex}
			result.fields = append(result.fields, field)
		}
	}
	return result
}

func (c *compiler) isObjectCompositionOperand(expression *syntax.TypeExpression, visiting map[string]bool) (bool, string) {
	if len(expression.Operands) > 0 || expression.Name == "object" {
		return true, "object"
	}
	if alias := c.typeAliases[expression.Name]; alias != nil && !visiting[expression.Name] {
		visiting[expression.Name] = true
		valid, name := c.isObjectCompositionOperand(alias.declaration.Type, visiting)
		delete(visiting, expression.Name)
		return valid, name
	}
	return false, expression.Name
}

func (c *compiler) compileAlias(name string, reference syntax.Span) valueType {
	alias := c.typeAliases[name]
	if alias == nil {
		c.addDiagnostic(reference, fmt.Sprintf("unknown type %q", name))
		return valueType{kind: valueDynamic}
	}
	switch alias.state {
	case 1:
		start := 0
		for index, item := range c.aliasStack {
			if item == name {
				start = index
				break
			}
		}
		for _, item := range c.aliasStack[start:] {
			if c.aliasCycles[item] {
				continue
			}
			c.aliasCycles[item] = true
			declaration := c.typeAliases[item].declaration
			c.addDiagnostic(declaration.GetSpan(), fmt.Sprintf("type alias %q is part of a dependency cycle", item))
		}
		return valueType{kind: valueDynamic}
	case 2:
		return alias.typeInfo
	}
	alias.state = 1
	c.aliasStack = append(c.aliasStack, name)
	alias.typeInfo = c.compileType(alias.declaration.Type)
	c.aliasStack = c.aliasStack[:len(c.aliasStack)-1]
	alias.state = 2
	return alias.typeInfo
}

func isBuiltinType(name string) bool {
	switch name {
	case "string", "number", "bool", "dynamic", "any", "object", "list", "set", "map", "optional":
		return true
	default:
		return false
	}
}

func (c *compiler) expectTypeArgumentCount(expression *syntax.TypeExpression, expected int) {
	if len(expression.Arguments) != expected {
		c.addDiagnostic(expression.GetSpan(), fmt.Sprintf("type %s expects %d type argument(s)", expression.Name, expected))
	}
}

func (c *compiler) terraformTypeConstraint(expression *syntax.TypeExpression) any {
	if expression == nil {
		return "any"
	}
	if len(expression.Operands) > 0 {
		return c.terraformTypeConstraintString(expression, make(map[string]bool))
	}
	if alias := c.typeAliases[expression.Name]; alias != nil {
		return c.terraformTypeConstraintString(alias.declaration.Type, map[string]bool{expression.Name: true})
	}
	if expression.Name == "object" && len(expression.Fields) > 0 {
		return c.terraformTypeConstraintString(expression, make(map[string]bool))
	}
	if len(expression.Arguments) == 0 && len(expression.Fields) == 0 {
		if expression.Name == "dynamic" || expression.Name == "object" {
			return "any"
		}
		return expression.Name
	}
	if expression.Name == "optional" && len(expression.Arguments) == 1 {
		return c.terraformTypeConstraint(expression.Arguments[0])
	}
	arguments := make([]string, 0, len(expression.Arguments))
	for _, argument := range expression.Arguments {
		arguments = append(arguments, c.terraformTypeConstraintString(argument, make(map[string]bool)))
	}
	return expression.Name + "(" + strings.Join(arguments, ", ") + ")"
}

func (c *compiler) terraformTypeConstraintString(expression *syntax.TypeExpression, visiting map[string]bool) string {
	if len(expression.Operands) > 0 {
		fields, ok := c.terraformComposedObjectFields(expression, visiting)
		if !ok {
			return "any"
		}
		return "object({" + strings.Join(fields, ", ") + "})"
	}
	if alias := c.typeAliases[expression.Name]; alias != nil {
		if visiting[expression.Name] {
			return "any"
		}
		next := make(map[string]bool, len(visiting)+1)
		for name, value := range visiting {
			next[name] = value
		}
		next[expression.Name] = true
		return c.terraformTypeConstraintString(alias.declaration.Type, next)
	}
	if expression.Name == "object" && len(expression.Fields) > 0 {
		fields := make([]string, 0, len(expression.Fields))
		for _, field := range expression.Fields {
			constraint := c.terraformTypeConstraintString(field.Type, visiting)
			if field.Optional {
				if field.Default != nil {
					constraint = "optional(" + constraint + ", " + renderConstant(field.Default) + ")"
				} else {
					constraint = "optional(" + constraint + ")"
				}
			}
			wire := field.WireName
			if wire == "" {
				if field.Quoted {
					wire = field.Name
				} else {
					wire = syntax.SourceNameToWire(field.Name)
				}
			}
			if !syntax.IsTerraformIdentifier(wire) {
				wire = quoteHCLString(wire)
			}
			fields = append(fields, wire+" = "+constraint)
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
		return c.terraformTypeConstraintString(expression.Arguments[0], visiting)
	}
	arguments := make([]string, 0, len(expression.Arguments))
	for _, argument := range expression.Arguments {
		arguments = append(arguments, c.terraformTypeConstraintString(argument, visiting))
	}
	return expression.Name + "(" + strings.Join(arguments, ", ") + ")"
}

func (c *compiler) terraformComposedObjectFields(expression *syntax.TypeExpression, visiting map[string]bool) ([]string, bool) {
	if len(expression.Operands) > 0 {
		var result []string
		for _, operand := range expression.Operands {
			fields, ok := c.terraformComposedObjectFields(operand, visiting)
			if !ok {
				return nil, false
			}
			result = append(result, fields...)
		}
		return result, true
	}
	if alias := c.typeAliases[expression.Name]; alias != nil {
		if visiting[expression.Name] {
			return nil, false
		}
		next := make(map[string]bool, len(visiting)+1)
		for name, value := range visiting {
			next[name] = value
		}
		next[expression.Name] = true
		return c.terraformComposedObjectFields(alias.declaration.Type, next)
	}
	if expression.Name != "object" {
		return nil, false
	}
	fields := make([]string, 0, len(expression.Fields))
	for _, field := range expression.Fields {
		constraint := c.terraformTypeConstraintString(field.Type, visiting)
		if field.Optional {
			if field.Default != nil {
				constraint = "optional(" + constraint + ", " + renderConstant(field.Default) + ")"
			} else {
				constraint = "optional(" + constraint + ")"
			}
		}
		wire := field.WireName
		if wire == "" {
			if field.Quoted {
				wire = field.Name
			} else {
				wire = syntax.SourceNameToWire(field.Name)
			}
		}
		if !syntax.IsTerraformIdentifier(wire) {
			wire = quoteHCLString(wire)
		}
		fields = append(fields, wire+" = "+constraint)
	}
	return fields, true
}

func (c *compiler) checkExpression(expression syntax.Expression) valueType {
	if expression == nil {
		return valueType{kind: valueDynamic}
	}
	if cached, ok := c.typeCache[expression]; ok {
		return cached
	}
	result := c.checkExpressionUncached(expression)
	c.typeCache[expression] = result
	return result
}

func (c *compiler) checkExpressionUncached(expression syntax.Expression) valueType {
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
		if scoped, ok := c.scope.lookup(value.Name); ok {
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
		tuple := make([]valueType, 0, len(value.Items))
		sensitive := false
		for _, item := range value.Items {
			itemType := c.checkExpression(item)
			tuple = append(tuple, itemType)
			sensitive = sensitive || itemType.sensitive
		}
		return valueType{kind: valueTuple, tuple: tuple, sensitive: sensitive}
	case *syntax.ForExpression:
		collectionType := c.checkExpression(value.Collection)
		keyType, itemType := iteratorTypes(collectionType)
		previousScope := c.scope
		c.scope = newScope(previousScope)
		defer func() { c.scope = previousScope }()
		if value.KeyVariable != "" {
			c.scope.bindings[value.KeyVariable] = keyType
		}
		c.scope.bindings[value.ValueVariable] = itemType
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
		if value.Object {
			return valueType{kind: valueMap, element: &result, sensitive: result.sensitive || collectionType.sensitive}
		}
		return valueType{kind: valueList, element: &result, sensitive: result.sensitive || collectionType.sensitive}
	case *syntax.ObjectExpression:
		result := valueType{kind: valueObject}
		sourceNames := make(map[string]syntax.ObjectField)
		wireNames := make(map[string]syntax.ObjectField)
		for _, item := range objectItems(value) {
			switch item := item.(type) {
			case syntax.ObjectField:
				name := objectFieldName(item)
				sourceConflict := false
				if name.Source != "" {
					if previous, exists := sourceNames[name.Source]; exists {
						c.addDiagnostic(previous.GetSpan(), fmt.Sprintf("object source field %q conflicts with another field", name.Source))
						c.addDiagnostic(item.GetSpan(), fmt.Sprintf("object source field %q conflicts with another field", name.Source))
						sourceConflict = true
					}
					sourceNames[name.Source] = item
				}
				if previous, exists := wireNames[name.Wire]; exists && !sourceConflict {
					c.addDiagnostic(previous.GetSpan(), fmt.Sprintf("object field %q is defined more than once", name.Wire))
					c.addDiagnostic(item.GetSpan(), fmt.Sprintf("object field %q is defined more than once", name.Wire))
				}
				wireNames[name.Wire] = item
				optional := false
				if item.Condition != nil {
					conditionType := c.checkExpression(item.Condition)
					if !isBoolOrDynamic(conditionType) {
						c.addDiagnostic(item.Condition.GetSpan(), "conditional object field expects a bool condition")
					}
					optional = true
				}
				field := valueField{
					name: name, typeInfo: c.checkExpression(item.Value), optional: optional,
					conditional: item.Condition != nil, span: item.GetSpan(),
				}
				result.sensitive = result.sensitive || field.typeInfo.sensitive
				result.fields = mergeValueField(result.fields, field)
			case syntax.ObjectSpread:
				spreadType := c.checkExpression(item.Value)
				switch spreadType.kind {
				case valueObject:
					for _, field := range spreadType.fields {
						result.fields = mergeValueField(result.fields, field)
					}
					result.open = result.open || spreadType.open
					result.sensitive = result.sensitive || spreadType.sensitive
				case valueMap, valueDynamic:
					result.open = true
					result.sensitive = result.sensitive || spreadType.sensitive
				default:
					c.addDiagnostic(item.Value.GetSpan(), fmt.Sprintf("object spread expects object, map, or dynamic; got %s", valueTypeName(spreadType)))
				}
			case syntax.InputsSpread:
				if item.Value != nil {
					c.checkExpression(item.Value)
				}
				c.addDiagnostic(item.GetSpan(), "inputs forwarding is only valid in a module argument body")
			}
		}
		return result
	case *syntax.TemplateExpression:
		sensitive := false
		for _, part := range value.Parts {
			if part.Expression != nil {
				sensitive = sensitive || c.checkExpression(part.Expression).sensitive
			}
		}
		return valueType{kind: valueString, sensitive: sensitive}
	case *syntax.UnaryExpression:
		operand := c.checkExpression(value.Operand)
		if value.Operator == syntax.TokenBang {
			if operand.kind != valueBool && operand.kind != valueDynamic {
				c.addDiagnostic(value.GetSpan(), "operator '!' expects a bool")
			}
			return valueType{kind: valueBool, sensitive: operand.sensitive}
		}
		if operand.kind != valueNumber && operand.kind != valueDynamic {
			c.addDiagnostic(value.GetSpan(), "numeric unary operator expects a number")
		}
		return valueType{kind: valueNumber, sensitive: operand.sensitive}
	case *syntax.BinaryExpression:
		left := c.checkExpression(value.Left)
		right := c.checkExpression(value.Right)
		switch value.Operator {
		case syntax.TokenPlus, syntax.TokenMinus, syntax.TokenStar, syntax.TokenSlash, syntax.TokenPercent:
			if !isNumberOrDynamic(left) || !isNumberOrDynamic(right) {
				c.addDiagnostic(value.GetSpan(), "arithmetic operators expect numbers")
			}
			return valueType{kind: valueNumber, sensitive: left.sensitive || right.sensitive}
		case syntax.TokenLess, syntax.TokenLessEqual, syntax.TokenGreater, syntax.TokenGreaterEqual:
			if !isNumberOrDynamic(left) || !isNumberOrDynamic(right) {
				c.addDiagnostic(value.GetSpan(), "ordering operators expect numbers")
			}
			return valueType{kind: valueBool, sensitive: left.sensitive || right.sensitive}
		case syntax.TokenEqual, syntax.TokenNotEqual:
			return valueType{kind: valueBool, sensitive: left.sensitive || right.sensitive}
		case syntax.TokenAnd, syntax.TokenOr:
			if !isBoolOrDynamic(left) || !isBoolOrDynamic(right) {
				c.addDiagnostic(value.GetSpan(), "boolean operators expect bool operands")
			}
			return valueType{kind: valueBool, sensitive: left.sensitive || right.sensitive}
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
		if common, ok := commonType(thenType, elseType); ok {
			common.sensitive = common.sensitive || condition.sensitive
			return common
		}
		c.addDiagnostic(value.GetSpan(), "conditional expression branches have incompatible types")
		return valueType{kind: valueDynamic}
	case *syntax.MemberExpression:
		targetType := c.checkExpression(value.Target)
		if identifier, ok := value.Target.(*syntax.IdentifierExpression); ok {
			if target := c.symbols[identifier.Name]; target != nil && target.kind == symbolResource && target.resource != nil && target.resource.conditional {
				c.addDiagnostic(value.GetSpan(), fmt.Sprintf("conditional resource %q is a collection; index it before attribute access", identifier.Name))
				return valueType{kind: valueDynamic}
			}
			if target := c.symbols[identifier.Name]; target != nil && target.kind == symbolModule && target.moduleContract != nil && target.valueType.kind == valueMap {
				c.addDiagnostic(value.GetSpan(), fmt.Sprintf("iterated module %q is a collection; index it before output access", identifier.Name))
				return valueType{kind: valueDynamic}
			}
		}
		if targetType.kind == valueObject {
			for _, field := range targetType.fields {
				if field.name.Source == value.Name && field.name.Source != "" {
					result := field.typeInfo
					result.sensitive = result.sensitive || field.sensitive || targetType.sensitive
					return result
				}
			}
			if targetType.open {
				return valueType{kind: valueDynamic, sensitive: targetType.sensitive}
			}
			c.addDiagnostic(value.GetSpan(), fmt.Sprintf("object has no field %q", value.Name))
		}
		return valueType{kind: valueDynamic, sensitive: targetType.sensitive}
	case *syntax.IndexExpression:
		targetType := c.checkExpression(value.Target)
		c.checkExpression(value.Index)
		if targetType.kind == valueObject {
			if key, ok := literalString(value.Index); ok {
				for _, field := range targetType.fields {
					if field.name.Wire == key {
						result := field.typeInfo
						result.sensitive = result.sensitive || field.sensitive || targetType.sensitive
						return result
					}
				}
			}
		}
		if targetType.element != nil {
			result := *targetType.element
			result.sensitive = result.sensitive || targetType.sensitive
			return result
		}
		return valueType{kind: valueDynamic, sensitive: targetType.sensitive}
	case *syntax.CallExpression:
		if _, ok := value.Callee.(*syntax.IdentifierExpression); !ok {
			c.checkExpression(value.Callee)
		}
		sensitive := false
		for _, argument := range value.Arguments {
			sensitive = sensitive || c.checkExpression(argument).sensitive
		}
		return valueType{kind: valueDynamic, sensitive: sensitive}
	}
	return valueType{kind: valueDynamic}
}

func iteratorTypes(collection valueType) (valueType, valueType) {
	dynamic := valueType{kind: valueDynamic, sensitive: collection.sensitive}
	withSensitivity := func(value valueType) valueType {
		value.sensitive = value.sensitive || collection.sensitive
		return value
	}
	switch collection.kind {
	case valueMap:
		if collection.element != nil {
			return withSensitivity(valueType{kind: valueString}), withSensitivity(*collection.element)
		}
	case valueObject:
		common := dynamic
		for index, field := range collection.fields {
			if index == 0 {
				common = field.typeInfo
				continue
			}
			if next, ok := commonType(common, field.typeInfo); ok {
				common = next
			} else {
				common = dynamic
			}
		}
		return withSensitivity(valueType{kind: valueString}), withSensitivity(common)
	case valueSet:
		if collection.element != nil && collection.element.kind == valueString {
			return withSensitivity(*collection.element), withSensitivity(*collection.element)
		}
	case valueList:
		if collection.element != nil {
			return withSensitivity(valueType{kind: valueNumber}), withSensitivity(*collection.element)
		}
	case valueTuple:
		common := dynamic
		for index, item := range collection.tuple {
			if index == 0 {
				common = item
				continue
			}
			if next, ok := commonType(common, item); ok {
				common = next
			} else {
				common = dynamic
			}
		}
		return withSensitivity(valueType{kind: valueNumber}), withSensitivity(common)
	}
	return dynamic, dynamic
}

func eachTypes(collection valueType) (valueType, valueType, bool) {
	key, value := iteratorTypes(collection)
	switch collection.kind {
	case valueMap, valueObject:
		return key, value, true
	case valueSet:
		if collection.element != nil && collection.element.kind == valueString {
			return key, value, true
		}
	case valueDynamic:
		return key, value, true
	}
	return key, value, false
}

func eachObjectType(key, value valueType) valueType {
	return valueType{kind: valueObject, fields: []valueField{
		{name: BindingName{Source: "key", Wire: "key"}, typeInfo: key},
		{name: BindingName{Source: "value", Wire: "value"}, typeInfo: value},
	}, sensitive: key.sensitive || value.sensitive}
}

func objectItems(expression *syntax.ObjectExpression) []syntax.ObjectItem {
	if expression == nil {
		return nil
	}
	if len(expression.Items) > 0 {
		return expression.Items
	}
	items := make([]syntax.ObjectItem, 0, len(expression.Fields))
	for _, field := range expression.Fields {
		items = append(items, field)
	}
	return items
}

func mergeValueField(fields []valueField, next valueField) []valueField {
	for index, field := range fields {
		if field.name.Wire == next.name.Wire {
			fields[index] = next
			return fields
		}
	}
	return append(fields, next)
}

func (c *compiler) encodeValue(expression syntax.Expression) any {
	return c.encodeExpression(expression)
}

func (c *compiler) encodeBlockValue(expression syntax.Expression) any {
	return c.encodeExpression(expression)
}

func (c *compiler) encodeExpression(expression syntax.Expression) any {
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
			items = append(items, c.encodeExpression(item))
		}
		return items
	case *syntax.ObjectExpression:
		if object, ok := c.encodeStaticObject(value); ok {
			return object
		}
		return "${" + c.renderObjectExpression(value) + "}"
	case *syntax.TemplateExpression:
		return c.renderTemplate(value)
	default:
		return "${" + c.renderExpression(expression) + "}"
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

func (c *compiler) encodeBlockBody(expression *syntax.ObjectExpression) map[string]any {
	result := make(map[string]any)
	for _, item := range objectItems(expression) {
		switch item := item.(type) {
		case syntax.ObjectField:
			if item.Condition != nil {
				conditionType := c.checkExpression(item.Condition)
				if !isBoolOrDynamic(conditionType) {
					continue
				}
				condition, constant := literalBool(item.Condition)
				if !constant {
					c.addDiagnostic(item.Condition.GetSpan(), "Terraform block fields must be statically known")
					continue
				}
				if !condition {
					continue
				}
			}
			result[objectFieldName(item).Wire] = c.encodeExpression(item.Value)
		case syntax.ObjectSpread:
			if object, ok := item.Value.(*syntax.ObjectExpression); ok {
				for key, value := range c.encodeBlockBody(object) {
					result[key] = value
				}
				continue
			}
			spreadType := c.checkExpression(item.Value)
			fixed := spreadType.kind == valueObject && !spreadType.open
			if fixed {
				for _, field := range spreadType.fields {
					if field.conditional {
						fixed = false
						break
					}
				}
			}
			if !fixed {
				if spreadType.kind == valueMap || spreadType.kind == valueDynamic || spreadType.open {
					c.addDiagnostic(item.GetSpan(), "Terraform block spread must have a statically known object shape")
				} else if spreadType.kind == valueObject {
					c.addDiagnostic(item.GetSpan(), "Terraform block spread cannot contain runtime conditional fields")
				}
				continue
			}
			for _, field := range spreadType.fields {
				result[field.name.Wire] = "${" + c.renderFieldAccess(item.Value, field.name) + "}"
			}
		}
	}
	return result
}

func (c *compiler) encodeResourceBody(expression *syntax.ObjectExpression) map[string]any {
	result := c.encodeBlockBody(expression)
	for _, item := range objectItems(expression) {
		field, ok := item.(syntax.ObjectField)
		if !ok || !includedObjectField(field) || objectFieldName(field).Wire != "connection" {
			continue
		}
		valueType := c.checkExpression(field.Value)
		if valueType.kind == valueObject && !valueType.open {
			result["connection"] = c.encodeObjectAsBlock(field.Value, valueType)
		}
	}
	return result
}

func (c *compiler) encodeProviderConfigBody(expression *syntax.ObjectExpression, provider *providerInfo) map[string]any {
	result := make(map[string]any)
	for _, item := range objectItems(expression) {
		switch item := item.(type) {
		case syntax.ObjectField:
			if item.Condition != nil {
				conditionType := c.checkExpression(item.Condition)
				if !isBoolOrDynamic(conditionType) {
					continue
				}
				condition, constant := literalBool(item.Condition)
				if !constant {
					c.addDiagnostic(item.Condition.GetSpan(), "Terraform block fields must be statically known")
					continue
				}
				if !condition {
					continue
				}
			}
			key := objectFieldName(item).Wire
			valueType := c.checkExpression(item.Value)
			if schema, ok := c.providerSchema(provider); ok {
				block, isBlock := schema.BlockTypes[key]
				if isBlock && block.NestingMode != "" && valueType.kind == valueObject {
					result[key] = []any{c.encodeObjectAsBlock(item.Value, valueType)}
					continue
				}
			}
			result[key] = c.encodeExpression(item.Value)
		case syntax.ObjectSpread:
			if object, ok := item.Value.(*syntax.ObjectExpression); ok {
				for key, value := range c.encodeProviderConfigBody(object, provider) {
					result[key] = value
				}
				continue
			}
			spreadType := c.checkExpression(item.Value)
			fixed := spreadType.kind == valueObject && !spreadType.open
			if fixed {
				for _, field := range spreadType.fields {
					if field.conditional {
						fixed = false
						break
					}
				}
			}
			if !fixed {
				if spreadType.kind == valueMap || spreadType.kind == valueDynamic || spreadType.open {
					c.addDiagnostic(item.GetSpan(), "Terraform block spread must have a statically known object shape")
				} else if spreadType.kind == valueObject {
					c.addDiagnostic(item.GetSpan(), "Terraform block spread cannot contain runtime conditional fields")
				}
				continue
			}
			for _, field := range spreadType.fields {
				result[field.name.Wire] = "${" + c.renderFieldAccess(item.Value, field.name) + "}"
			}
		}
	}
	return result
}

func (c *compiler) providerSchema(provider *providerInfo) (ProviderSchema, bool) {
	if schema, ok := c.options.ProviderSchemas[provider.source]; ok {
		return schema, true
	}
	for source, schema := range c.options.ProviderSchemas {
		normalized := strings.TrimPrefix(source, "registry.terraform.io/")
		if normalized == provider.source || strings.HasSuffix(normalized, "/"+provider.source) {
			return schema, true
		}
	}
	return ProviderSchema{}, false
}

func (c *compiler) encodeObjectAsBlock(expression syntax.Expression, typeInfo valueType) map[string]any {
	if object, ok := expression.(*syntax.ObjectExpression); ok {
		return c.encodeBlockBody(object)
	}
	result := make(map[string]any, len(typeInfo.fields))
	for _, field := range typeInfo.fields {
		result[field.name.Wire] = "${" + c.renderFieldAccess(expression, field.name) + "}"
	}
	return result
}

func (c *compiler) encodeStaticObject(expression *syntax.ObjectExpression) (map[string]any, bool) {
	result := make(map[string]any)
	for _, item := range objectItems(expression) {
		switch item := item.(type) {
		case syntax.ObjectField:
			if item.Condition != nil {
				condition, ok := literalBool(item.Condition)
				if !ok {
					return nil, false
				}
				if !condition {
					continue
				}
			}
			result[objectFieldName(item).Wire] = c.encodeExpression(item.Value)
		case syntax.ObjectSpread:
			object, ok := item.Value.(*syntax.ObjectExpression)
			if !ok {
				return nil, false
			}
			spread, ok := c.encodeStaticObject(object)
			if !ok {
				return nil, false
			}
			for key, value := range spread {
				result[key] = value
			}
		}
	}
	return result, true
}

func (c *compiler) renderTemplate(expression *syntax.TemplateExpression) string {
	var result strings.Builder
	for index, part := range expression.Parts {
		if part.Expression != nil {
			result.WriteString("${")
			result.WriteString(c.renderExpression(part.Expression))
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

func (c *compiler) renderExpression(expression syntax.Expression) string {
	switch value := expression.(type) {
	case *syntax.IdentifierExpression:
		symbol := c.symbols[value.Name]
		if symbol == nil {
			return value.Name
		}
		switch symbol.kind {
		case symbolInput:
			return "var." + symbol.name.Wire
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
			items = append(items, c.renderExpression(item))
		}
		return "[" + strings.Join(items, ", ") + "]"
	case *syntax.ForExpression:
		iterator := value.ValueVariable
		if value.KeyVariable != "" {
			iterator = value.KeyVariable + ", " + value.ValueVariable
		}
		collectionType := c.checkExpression(value.Collection)
		keyType, itemType := iteratorTypes(collectionType)
		previousScope := c.scope
		c.scope = newScope(previousScope)
		if value.KeyVariable != "" {
			c.scope.bindings[value.KeyVariable] = keyType
		}
		c.scope.bindings[value.ValueVariable] = itemType
		body := c.renderExpression(value.Value)
		opening, closing := "[", "]"
		if value.Object {
			opening, closing = "{", "}"
			body = c.renderExpression(value.Key) + " => " + body
		}
		if value.Condition != nil {
			body += " if " + c.renderExpression(value.Condition)
		}
		c.scope = previousScope
		return opening + "for " + iterator + " in " + c.renderExpression(value.Collection) + " : " + body + closing
	case *syntax.ObjectExpression:
		return c.renderObjectExpression(value)
	case *syntax.TemplateExpression:
		return quoteHCLString(c.renderTemplate(value))
	case *syntax.UnaryExpression:
		return tokenOperator(value.Operator) + c.renderExpression(value.Operand)
	case *syntax.BinaryExpression:
		left := c.renderExpression(value.Left)
		right := c.renderExpression(value.Right)
		if value.Operator == syntax.TokenCoalesce {
			return "(" + left + " != null ? " + left + " : " + right + ")"
		}
		return "(" + left + " " + tokenOperator(value.Operator) + " " + right + ")"
	case *syntax.ConditionalExpression:
		return "(" + c.renderExpression(value.Condition) + " ? " + c.renderExpression(value.Then) + " : " + c.renderExpression(value.Else) + ")"
	case *syntax.MemberExpression:
		target := c.renderExpression(value.Target)
		targetType := c.checkExpression(value.Target)
		if targetType.kind == valueObject {
			for _, field := range targetType.fields {
				if field.name.Source == value.Name && field.name.Source != "" {
					if syntax.IsTerraformIdentifier(field.name.Wire) {
						return target + "." + field.name.Wire
					}
					return target + "[" + quoteHCLString(field.name.Wire) + "]"
				}
			}
		}
		return target + "." + value.Name
	case *syntax.IndexExpression:
		return c.renderExpression(value.Target) + "[" + c.renderExpression(value.Index) + "]"
	case *syntax.CallExpression:
		if identifier, ok := value.Callee.(*syntax.IdentifierExpression); ok && identifier.Name == "address" && len(value.Arguments) == 1 {
			if address, ok := literalString(value.Arguments[0]); ok {
				return address
			}
		}
		arguments := make([]string, 0, len(value.Arguments))
		for _, argument := range value.Arguments {
			arguments = append(arguments, c.renderExpression(argument))
		}
		return c.renderExpression(value.Callee) + "(" + strings.Join(arguments, ", ") + ")"
	}
	return "null"
}

func (c *compiler) renderObjectExpression(expression *syntax.ObjectExpression) string {
	var fragments []string
	var fields []string
	flushFields := func() {
		if len(fields) == 0 {
			return
		}
		fragments = append(fragments, "{"+strings.Join(fields, ", ")+"}")
		fields = nil
	}
	for _, item := range objectItems(expression) {
		switch item := item.(type) {
		case syntax.ObjectField:
			field := quoteHCLString(objectFieldName(item).Wire) + " = " + c.renderExpression(item.Value)
			if item.Condition == nil {
				fields = append(fields, field)
				continue
			}
			flushFields()
			fragments = append(fragments, "("+c.renderExpression(item.Condition)+" ? {"+field+"} : {})")
		case syntax.ObjectSpread:
			flushFields()
			fragments = append(fragments, c.renderExpression(item.Value))
		}
	}
	flushFields()
	if len(fragments) == 0 {
		return "{}"
	}
	if len(fragments) == 1 {
		return fragments[0]
	}
	return "merge(" + strings.Join(fragments, ", ") + ")"
}

func (c *compiler) renderFieldAccess(target syntax.Expression, name BindingName) string {
	rendered := c.renderExpression(target)
	if name.Source != "" && syntax.IsTerraformIdentifier(name.Wire) {
		return rendered + "." + name.Wire
	}
	return rendered + "[" + quoteHCLString(name.Wire) + "]"
}

func literalBool(expression syntax.Expression) (bool, bool) {
	literal, ok := expression.(*syntax.LiteralExpression)
	if !ok {
		return false, false
	}
	value, ok := literal.Value.(bool)
	return value, ok
}

func includedObjectField(field syntax.ObjectField) bool {
	if field.Condition == nil {
		return true
	}
	condition, constant := literalBool(field.Condition)
	return constant && condition
}

func (c *compiler) encodeStaticTraversals(expression syntax.Expression) any {
	array, ok := expression.(*syntax.ArrayExpression)
	if !ok {
		c.addDiagnostic(expression.GetSpan(), "static traversal metadata must be an array")
		return c.encodeValue(expression)
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
		result = append(result, c.renderExpression(item))
	}
	return result
}

func (c *compiler) encodeLifecycle(expression syntax.Expression) any {
	object, ok := expression.(*syntax.ObjectExpression)
	if !ok {
		c.addDiagnostic(expression.GetSpan(), "lifecycle metadata must be an object")
		return c.encodeBlockValue(expression)
	}
	result := c.encodeBlockBody(object)
	for _, field := range object.Fields {
		if !includedObjectField(field) {
			continue
		}
		key := objectFieldName(field).Wire
		if key == "ignore_changes" || key == "replace_triggered_by" {
			result[key] = c.encodeStaticTraversals(field.Value)
		} else {
			result[key] = c.encodeBlockValue(field.Value)
		}
	}
	for _, key := range []string{"ignore_changes", "replace_triggered_by"} {
		if field, found := objectField(object, key); found {
			result[key] = c.encodeStaticTraversals(field.Value)
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
		for _, item := range objectItems(value) {
			switch item := item.(type) {
			case syntax.ObjectField:
				if !isConstant(item.Value) || item.Condition != nil && !isConstant(item.Condition) {
					return false
				}
			case syntax.ObjectSpread:
				if !isConstant(item.Value) {
					return false
				}
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
			fields = append(fields, objectFieldName(field).Wire+" = "+renderConstant(field.Value))
		}
		return "{" + strings.Join(fields, ", ") + "}"
	}
	return "null"
}

func hasObjectField(object *syntax.ObjectExpression, names ...string) bool {
	_, ok := objectField(object, names...)
	return ok
}

func objectField(object *syntax.ObjectExpression, names ...string) (syntax.ObjectField, bool) {
	if object == nil {
		return syntax.ObjectField{}, false
	}
	items := objectItems(object)
	for index := len(items) - 1; index >= 0; index-- {
		switch item := items[index].(type) {
		case syntax.ObjectField:
			if !includedObjectField(item) {
				continue
			}
			for _, name := range names {
				if item.Name == name || objectFieldName(item).Wire == name {
					return item, true
				}
			}
		case syntax.ObjectSpread:
			if spread, ok := item.Value.(*syntax.ObjectExpression); ok {
				if field, found := objectField(spread, names...); found {
					return field, true
				}
			}
		}
	}
	return syntax.ObjectField{}, false
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

func toSnakeCase(value string) string {
	return syntax.SourceNameToWire(value)
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

func (c *compiler) checkAssignment(expected, actual valueType, span syntax.Span, context string) bool {
	if expected.kind == valueDynamic || actual.kind == valueDynamic || actual.kind == valueNull {
		return true
	}
	if expected.kind == valueObject {
		if actual.kind != valueObject {
			c.addDiagnostic(span, fmt.Sprintf("%s has incompatible type: expected object, got %s", context, diagnosticTypeDescription(actual)))
			return false
		}
		actualFields := make(map[string]valueField, len(actual.fields))
		actualSources := make(map[string]valueField, len(actual.fields))
		for _, field := range actual.fields {
			actualFields[field.name.Wire] = field
			if field.name.Source != "" {
				actualSources[field.name.Source] = field
			}
		}
		valid := true
		for _, field := range expected.fields {
			actualField, exists := actualFields[field.name.Wire]
			if !exists && field.name.Source != "" {
				actualField, exists = actualSources[field.name.Source]
			}
			fieldContext := context + "." + field.name.Wire
			if !exists {
				if field.optional || field.defaulted || actual.open {
					continue
				}
				c.addDiagnostic(span, fmt.Sprintf("%s is required", fieldContext))
				valid = false
				continue
			}
			if !c.checkAssignment(field.typeInfo, actualField.typeInfo, actualField.span, fieldContext) {
				valid = false
			}
		}
		return valid
	}
	if expected.kind == valueList || expected.kind == valueSet {
		if actual.kind == valueTuple && expected.element != nil {
			valid := true
			for index, item := range actual.tuple {
				if !c.checkAssignment(*expected.element, item, span, fmt.Sprintf("%s[%d]", context, index)) {
					valid = false
				}
			}
			return valid
		}
	}
	if expected.kind == valueMap && actual.kind == valueObject && expected.element != nil {
		valid := true
		for _, field := range actual.fields {
			if !c.checkAssignment(*expected.element, field.typeInfo, field.span, context+"["+strconv.Quote(field.name.Wire)+"]") {
				valid = false
			}
		}
		return valid
	}
	if !isAssignable(expected, actual) {
		c.addDiagnostic(span, fmt.Sprintf("%s has incompatible type: expected %s, got %s", context, diagnosticTypeDescription(expected), diagnosticTypeDescription(actual)))
		return false
	}
	return true
}

func (c *compiler) encodeValueWithDefaults(expression syntax.Expression, expected valueType) any {
	return c.completeDefaults(c.encodeValue(expression), expected)
}

func (c *compiler) completeDefaults(encoded any, expected valueType) any {
	switch expected.kind {
	case valueObject:
		object, ok := encoded.(map[string]any)
		if !ok {
			return encoded
		}
		for _, field := range expected.fields {
			value, exists := object[field.name.Wire]
			if !exists && field.name.Source != "" {
				sourceWire := syntax.SourceNameToWire(field.name.Source)
				if sourceValue, sourceExists := object[sourceWire]; sourceExists {
					value, exists = sourceValue, true
					delete(object, sourceWire)
					object[field.name.Wire] = sourceValue
				}
			}
			if !exists {
				if field.defaultValue != nil {
					object[field.name.Wire] = c.completeDefaults(c.encodeValue(field.defaultValue), field.typeInfo)
				}
				continue
			}
			object[field.name.Wire] = c.completeDefaults(value, field.typeInfo)
		}
	case valueList, valueSet:
		items, ok := encoded.([]any)
		if !ok || expected.element == nil {
			return encoded
		}
		for index, item := range items {
			items[index] = c.completeDefaults(item, *expected.element)
		}
	}
	return encoded
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
	file := span.File
	if file == "" {
		file = c.file.ID
	}
	if file == "" {
		file = syntax.FileID(c.file.Name)
	}
	c.diagnostics = append(c.diagnostics, syntax.NewDiagnostic(file, span, "COMPILER_ERROR", message))
}

func SortedDiagnostics(diagnostics []syntax.Diagnostic) []syntax.Diagnostic {
	return syntax.SortDiagnostics(diagnostics)
}
