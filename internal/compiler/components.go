package compiler

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/ondrejnov/infralang/internal/syntax"
)

type componentExpander struct {
	preparer       *preparer
	definitions    map[string]*syntax.ComponentDefinition
	providers      map[string]*syntax.ProviderDeclaration
	invalid        map[string]bool
	cycleState     map[string]uint8
	cycleStack     []string
	cycleReported  map[string]bool
	ordinary       map[string]*componentInstanceResult
	indexed        map[string]map[string]*componentInstanceResult
	runtimeNames   map[string]syntax.Node
	argumentChecks []syntax.ComponentArgumentCheck
	providerChecks []syntax.ComponentProviderCheck
	exportChecks   []syntax.ComponentExportCheck
	resolving      map[string]bool
	instances      []*componentInstanceResult
}

type componentInstanceResult struct {
	identity    string
	expansion   string
	exports     map[string]syntax.Expression
	exportNodes map[string]*syntax.ComponentExport
	resolved    map[string]syntax.Expression
	used        map[string]bool
	node        syntax.Node
}

func (p *preparer) expandComponents(declarations []syntax.Declaration) ([]syntax.Declaration, []syntax.ComponentArgumentCheck, []syntax.ComponentProviderCheck, []syntax.ComponentExportCheck) {
	expander := &componentExpander{
		preparer: p, definitions: make(map[string]*syntax.ComponentDefinition), providers: make(map[string]*syntax.ProviderDeclaration),
		invalid: make(map[string]bool), cycleState: make(map[string]uint8), cycleReported: make(map[string]bool),
		ordinary: make(map[string]*componentInstanceResult), indexed: make(map[string]map[string]*componentInstanceResult),
		runtimeNames: make(map[string]syntax.Node), resolving: make(map[string]bool),
	}
	expander.collect(declarations)
	expander.checkDependencyCycles()

	var result []syntax.Declaration
	for _, declaration := range declarations {
		switch value := declaration.(type) {
		case *syntax.ComponentDefinition:
			continue
		case *syntax.ComponentInstance:
			result = append(result, expander.expandInstance(value, "")...)
		case *syntax.ComponentExport:
			p.addDiagnostic(value, "component exports are valid only inside a component body")
		default:
			result = append(result, declaration)
		}
	}
	for _, declaration := range result {
		expander.rewriteDeclaration(declaration)
	}
	for index := range expander.argumentChecks {
		expander.argumentChecks[index].Actual = expander.rewriteExpression(expander.argumentChecks[index].Actual)
	}
	for index := range expander.providerChecks {
		expander.providerChecks[index].Actual = expander.rewriteExpression(expander.providerChecks[index].Actual)
	}
	expander.resolveAllExports()
	return result, expander.argumentChecks, expander.providerChecks, expander.exportChecks
}

func (expander *componentExpander) collect(declarations []syntax.Declaration) {
	for _, declaration := range declarations {
		switch value := declaration.(type) {
		case *syntax.ProviderDeclaration:
			expander.providers[value.Name] = value
		case *syntax.ComponentDefinition:
			if previous := expander.definitions[value.Name]; previous != nil {
				message := fmt.Sprintf("component %q conflicts with another definition", value.Name)
				expander.preparer.addDiagnostic(previous, message)
				expander.preparer.addDiagnostic(value, message)
				expander.invalid[value.Name] = true
				continue
			}
			expander.definitions[value.Name] = value
		default:
			if name := runtimeSourceName(declaration); name != "" {
				expander.runtimeNames[name] = declaration
			}
		}
	}
	for _, definition := range expander.definitions {
		expander.validateDefinition(definition)
	}
}

func (expander *componentExpander) validateDefinition(definition *syntax.ComponentDefinition) {
	names := make(map[string]syntax.Node)
	for index := range definition.Parameters {
		parameter := &definition.Parameters[index]
		if previous := names[parameter.Name]; previous != nil {
			expander.reportDefinitionConflict(definition, previous, parameter, parameter.Name)
		} else {
			names[parameter.Name] = parameter
		}
		expander.preparer.constType(parameter.Type, make(map[string]bool))
	}
	for index := range definition.Providers {
		provider := &definition.Providers[index]
		if previous := names[provider.Name]; previous != nil {
			expander.reportDefinitionConflict(definition, previous, provider, provider.Name)
		} else {
			names[provider.Name] = provider
		}
		if expander.providers[provider.ProviderName] == nil {
			expander.preparer.addDiagnostic(provider, fmt.Sprintf("component provider parameter %q references unknown provider %q", provider.Name, provider.ProviderName))
			expander.invalid[definition.Name] = true
		}
	}
	exports := make(map[string]*syntax.ComponentExport)
	internalNames := make(map[string]syntax.Node)
	for _, declaration := range definition.Declarations {
		switch value := declaration.(type) {
		case *syntax.ComponentExport:
			if previous := exports[value.Name]; previous != nil {
				message := fmt.Sprintf("component export %q conflicts with another export", value.Name)
				expander.preparer.addDiagnostic(previous, message)
				expander.preparer.addDiagnostic(value, message)
				expander.invalid[definition.Name] = true
			} else {
				exports[value.Name] = value
			}
		case *syntax.LetDeclaration, *syntax.ConfigureDeclaration, *syntax.ResourceDeclaration, *syntax.DataDeclaration, *syntax.ModuleDeclaration, *syntax.ComponentInstance:
			name := componentBodySourceName(declaration)
			if previous := internalNames[name]; previous != nil {
				expander.reportDefinitionConflict(definition, previous, declaration, name)
			} else if previous := names[name]; previous != nil {
				expander.reportDefinitionConflict(definition, previous, declaration, name)
			} else {
				internalNames[name] = declaration
			}
		default:
			expander.preparer.addDiagnostic(declaration, fmt.Sprintf("declaration %T is not supported in component %q", declaration, definition.Name))
			expander.invalid[definition.Name] = true
		}
	}
}

func (expander *componentExpander) reportDefinitionConflict(definition *syntax.ComponentDefinition, previous, current syntax.Node, name string) {
	message := fmt.Sprintf("name %q conflicts within component %q", name, definition.Name)
	expander.preparer.addDiagnostic(previous, message)
	expander.preparer.addDiagnostic(current, message)
	expander.invalid[definition.Name] = true
}

func (expander *componentExpander) checkDependencyCycles() {
	names := make([]string, 0, len(expander.definitions))
	for name := range expander.definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if expander.cycleState[name] == 0 {
			expander.visitComponent(name)
		}
	}
}

func (expander *componentExpander) visitComponent(name string) {
	definition := expander.definitions[name]
	if definition == nil {
		return
	}
	expander.cycleState[name] = 1
	expander.cycleStack = append(expander.cycleStack, name)
	for _, declaration := range definition.Declarations {
		instance, ok := declaration.(*syntax.ComponentInstance)
		if !ok {
			continue
		}
		if expander.definitions[instance.ComponentName] == nil {
			expander.preparer.addDiagnostic(instance, fmt.Sprintf("unknown component %q in component %q", instance.ComponentName, name))
			expander.invalid[name] = true
			continue
		}
		switch expander.cycleState[instance.ComponentName] {
		case 0:
			expander.visitComponent(instance.ComponentName)
		case 1:
			start := 0
			for index, candidate := range expander.cycleStack {
				if candidate == instance.ComponentName {
					start = index
					break
				}
			}
			cycle := append(append([]string(nil), expander.cycleStack[start:]...), instance.ComponentName)
			cycleText := strings.Join(cycle, " -> ")
			if !expander.cycleReported[cycleText] {
				expander.cycleReported[cycleText] = true
				for _, componentName := range cycle[:len(cycle)-1] {
					expander.invalid[componentName] = true
					expander.preparer.addDiagnostic(expander.definitions[componentName], "component dependency cycle: "+cycleText)
				}
			}
		}
	}
	expander.cycleStack = expander.cycleStack[:len(expander.cycleStack)-1]
	expander.cycleState[name] = 2
}

func (expander *componentExpander) expandInstance(instance *syntax.ComponentInstance, parentIdentity string) []syntax.Declaration {
	definition := expander.definitions[instance.ComponentName]
	if definition == nil {
		expander.preparer.addDiagnostic(instance, fmt.Sprintf("unknown component %q", instance.ComponentName))
		return nil
	}
	if expander.invalid[definition.Name] {
		return nil
	}
	key := ""
	indexed := instance.Index != nil
	if indexed {
		value, ok := expander.preparer.eval(instance.Index, nil)
		var valid bool
		key, valid = canonicalIndex(value, true)
		if !ok || !valid {
			expander.preparer.addDiagnosticAt(instance.Index, "component instance index must be a nonempty compile-time string or exact number")
			return nil
		}
	}
	identity := instance.Name
	if indexed {
		identity += "[" + key + "]"
	}
	if parentIdentity != "" {
		identity = parentIdentity + "/" + identity
	}
	expansion := "component " + identity
	if inherited := instance.GetExpansion(); inherited != "" {
		expansion = inherited + "/" + expansion
	}

	arguments, argumentValid := expander.bindArguments(definition, instance, expansion)
	providers, providerValid := expander.bindProviders(definition, instance, expansion)
	if !argumentValid || !providerValid {
		return nil
	}
	result := &componentInstanceResult{
		identity: identity, expansion: expansion, exports: make(map[string]syntax.Expression),
		exportNodes: make(map[string]*syntax.ComponentExport), resolved: make(map[string]syntax.Expression),
		used: make(map[string]bool), node: instance,
	}
	if !expander.registerInstance(instance, key, indexed, result) {
		return nil
	}

	renames := make(map[string]string)
	for _, declaration := range definition.Declarations {
		if name := componentBodySourceName(declaration); name != "" {
			renames[name] = hygienicComponentName(identity, name)
		}
	}
	var declarations []syntax.Declaration
	for _, declaration := range definition.Declarations {
		substituted := substituteComponentDeclaration(declaration, arguments, providers, renames, expansion)
		switch value := substituted.(type) {
		case *syntax.ComponentExport:
			result.exports[value.Name] = value.Value
			result.exportNodes[value.Name] = value
		case *syntax.ComponentInstance:
			declarations = append(declarations, expander.expandInstance(value, identity)...)
		default:
			if substituted != nil {
				declarations = append(declarations, substituted)
			}
		}
	}
	return declarations
}

func (expander *componentExpander) bindArguments(definition *syntax.ComponentDefinition, instance *syntax.ComponentInstance, expansion string) (map[string]syntax.Expression, bool) {
	parameters := make(map[string]syntax.ComponentParameter, len(definition.Parameters))
	for _, parameter := range definition.Parameters {
		parameters[parameter.Name] = parameter
	}
	result := make(map[string]syntax.Expression, len(parameters))
	valid := true
	for _, item := range objectItems(instance.Arguments) {
		field, ok := item.(syntax.ObjectField)
		if !ok {
			expander.preparer.addDiagnostic(item, "component arguments must be explicit named fields")
			valid = false
			continue
		}
		name := field.Name
		parameter, known := parameters[name]
		if !known {
			expander.preparer.addDiagnostic(field, fmt.Sprintf("unknown argument %q for component %q", name, definition.Name))
			valid = false
			continue
		}
		if _, duplicate := result[name]; duplicate {
			expander.preparer.addDiagnostic(field, fmt.Sprintf("component argument %q is supplied more than once", name))
			valid = false
			continue
		}
		result[name] = field.Value
		expander.argumentChecks = append(expander.argumentChecks, syntax.ComponentArgumentCheck{
			BaseNode: componentCheckBase(field.BaseNode, expansion), ComponentName: definition.Name,
			ParameterName: name, Expected: cloneType(parameter.Type, nil, ""), Actual: field.Value,
		})
	}
	for _, parameter := range definition.Parameters {
		if result[parameter.Name] == nil {
			expander.preparer.addDiagnostic(instance, fmt.Sprintf("missing argument %q for component %q", parameter.Name, definition.Name))
			valid = false
		}
	}
	return result, valid
}

func (expander *componentExpander) bindProviders(definition *syntax.ComponentDefinition, instance *syntax.ComponentInstance, expansion string) (map[string]syntax.Expression, bool) {
	parameters := make(map[string]syntax.ComponentProviderParameter, len(definition.Providers))
	for _, parameter := range definition.Providers {
		parameters[parameter.Name] = parameter
	}
	result := make(map[string]syntax.Expression, len(parameters))
	valid := true
	for _, item := range objectItems(instance.Providers) {
		field, ok := item.(syntax.ObjectField)
		if !ok {
			expander.preparer.addDiagnostic(item, "component provider mappings must be explicit named fields")
			valid = false
			continue
		}
		name := field.Name
		parameter, known := parameters[name]
		if !known {
			expander.preparer.addDiagnostic(field, fmt.Sprintf("unknown provider argument %q for component %q", name, definition.Name))
			valid = false
			continue
		}
		if _, duplicate := result[name]; duplicate {
			expander.preparer.addDiagnostic(field, fmt.Sprintf("component provider argument %q is supplied more than once", name))
			valid = false
			continue
		}
		result[name] = field.Value
		expander.providerChecks = append(expander.providerChecks, syntax.ComponentProviderCheck{
			BaseNode: componentCheckBase(field.BaseNode, expansion), ComponentName: definition.Name, ParameterName: name,
			ExpectedProviderName: parameter.ProviderName, Actual: field.Value,
		})
	}
	for _, parameter := range definition.Providers {
		if result[parameter.Name] == nil {
			expander.preparer.addDiagnostic(instance, fmt.Sprintf("missing provider argument %q for component %q", parameter.Name, definition.Name))
			valid = false
		}
	}
	return result, valid
}

func (expander *componentExpander) registerInstance(instance *syntax.ComponentInstance, key string, indexed bool, result *componentInstanceResult) bool {
	if indexed {
		if constant := expander.preparer.constants[instance.Name]; constant != nil {
			message := fmt.Sprintf("indexed component namespace %q conflicts with constant %q", instance.Name, instance.Name)
			expander.preparer.addDiagnostic(constant.declaration, message)
			expander.preparer.addDiagnostic(instance, message)
			return false
		}
		if handles := expander.preparer.indexed[instance.Name]; len(handles) != 0 {
			message := fmt.Sprintf("indexed component namespace %q conflicts with an indexed provider or module namespace", instance.Name)
			for _, previous := range handles {
				expander.preparer.addIdentityDiagnostic(previous, message)
				break
			}
			expander.preparer.addDiagnostic(instance, message)
			return false
		}
		if expander.indexed[instance.Name] == nil {
			expander.indexed[instance.Name] = make(map[string]*componentInstanceResult)
		}
		if previous := expander.indexed[instance.Name][key]; previous != nil {
			message := fmt.Sprintf("component instance %s[%q] is already declared", instance.Name, key)
			expander.preparer.addDiagnostic(previous.node, message)
			expander.preparer.addDiagnostic(instance, message)
			return false
		}
		expander.indexed[instance.Name][key] = result
		expander.instances = append(expander.instances, result)
		return true
	}
	if previous := expander.ordinary[instance.Name]; previous != nil {
		message := fmt.Sprintf("component instance %q is already declared", instance.Name)
		expander.preparer.addDiagnostic(previous.node, message)
		expander.preparer.addDiagnostic(instance, message)
		return false
	}
	if constant := expander.preparer.constants[instance.Name]; constant != nil {
		message := fmt.Sprintf("component instance %q conflicts with constant %q", instance.Name, instance.Name)
		expander.preparer.addDiagnostic(constant.declaration, message)
		expander.preparer.addDiagnostic(instance, message)
		return false
	}
	if runtime := expander.runtimeNames[instance.Name]; runtime != nil {
		message := fmt.Sprintf("component instance %q conflicts with a runtime declaration", instance.Name)
		expander.preparer.addDiagnostic(runtime, message)
		expander.preparer.addDiagnostic(instance, message)
		return false
	}
	expander.ordinary[instance.Name] = result
	expander.instances = append(expander.instances, result)
	return true
}

func (expander *componentExpander) resolveAllExports() {
	for _, instance := range expander.instances {
		names := make([]string, 0, len(instance.exports))
		for name := range instance.exports {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if instance.used[name] {
				continue
			}
			node := instance.exportNodes[name]
			value := expander.resolveExport(instance, instance.identity, name, node)
			if value == nil {
				continue
			}
			expander.exportChecks = append(expander.exportChecks, syntax.ComponentExportCheck{
				BaseNode: node.BaseNode, ComponentName: instance.identity, ExportName: name, Value: value,
			})
		}
	}
}

func (expander *componentExpander) resolveExport(instance *componentInstanceResult, identity, name string, context syntax.Node) syntax.Expression {
	if resolved := instance.resolved[name]; resolved != nil {
		return resolved
	}
	exported := instance.exports[name]
	if exported == nil {
		expander.preparer.addDiagnostic(context, fmt.Sprintf("component instance %s has no export %q", identity, name))
		return nil
	}
	resolution := identity + "." + name
	if expander.resolving[resolution] {
		expander.preparer.addDiagnostic(context, "component export dependency cycle: "+resolution)
		return nil
	}
	expander.resolving[resolution] = true
	resolved := expander.rewriteExpressionScoped(cloneExpression(exported, nil, ""), nil)
	delete(expander.resolving, resolution)
	instance.resolved[name] = resolved
	return resolved
}

func componentCheckBase(base syntax.BaseNode, expansion string) syntax.BaseNode {
	base.Expansion = expansion
	return base
}

func hygienicComponentName(identity, name string) string {
	return "component__" + hex.EncodeToString([]byte(identity)) + "__" + hex.EncodeToString([]byte(name))
}

func componentBodySourceName(declaration syntax.Declaration) string {
	switch value := declaration.(type) {
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
	case *syntax.ComponentInstance:
		return value.Name
	default:
		return ""
	}
}

func substituteComponentDeclaration(declaration syntax.Declaration, arguments, providers map[string]syntax.Expression, renames map[string]string, expansion string) syntax.Declaration {
	substitute := func(expression syntax.Expression) syntax.Expression {
		return substituteComponentExpression(expression, arguments, providers, renames, nil, expansion)
	}
	switch value := declaration.(type) {
	case *syntax.LetDeclaration:
		return &syntax.LetDeclaration{BaseNode: componentBase(value.BaseNode, expansion), Name: renames[value.Name], Value: substitute(value.Value)}
	case *syntax.ConfigureDeclaration:
		result := *value
		result.BaseNode = componentBase(value.BaseNode, expansion)
		result.Name = renames[value.Name]
		result.Index = substitute(value.Index)
		result.Alias = substitute(value.Alias)
		result.Config = substituteComponentObject(value.Config, arguments, providers, renames, nil, expansion)
		return &result
	case *syntax.ResourceDeclaration:
		result := *value
		result.BaseNode = componentBase(value.BaseNode, expansion)
		result.Name = renames[value.Name]
		result.ProviderConfig = substitute(value.ProviderConfig)
		if identifier, ok := result.ProviderConfig.(*syntax.IdentifierExpression); ok {
			result.ProviderConfigName = identifier.Name
		} else {
			result.ProviderConfigName = ""
		}
		result.LabelExpression = substitute(value.LabelExpression)
		result.Arguments = substituteComponentObject(value.Arguments, arguments, providers, renames, nil, expansion)
		result.With = substituteComponentObject(value.With, arguments, providers, renames, nil, expansion)
		result.Condition = substitute(value.Condition)
		return &result
	case *syntax.DataDeclaration:
		result := *value
		result.BaseNode = componentBase(value.BaseNode, expansion)
		result.Name = renames[value.Name]
		result.ProviderConfig = substitute(value.ProviderConfig)
		if identifier, ok := result.ProviderConfig.(*syntax.IdentifierExpression); ok {
			result.ProviderConfigName = identifier.Name
		} else {
			result.ProviderConfigName = ""
		}
		result.LabelExpression = substitute(value.LabelExpression)
		result.Arguments = substituteComponentObject(value.Arguments, arguments, providers, renames, nil, expansion)
		return &result
	case *syntax.ModuleDeclaration:
		result := *value
		result.BaseNode = componentBase(value.BaseNode, expansion)
		result.Name = renames[value.Name]
		result.Index = substitute(value.Index)
		result.LabelExpression = substitute(value.LabelExpression)
		result.Arguments = substituteComponentObject(value.Arguments, arguments, providers, renames, nil, expansion)
		result.MetaArguments = substituteComponentObject(value.MetaArguments, arguments, providers, renames, nil, expansion)
		if value.Providers != nil {
			result.Providers = &syntax.ProviderMapping{BaseNode: componentBase(value.Providers.BaseNode, expansion)}
			result.Providers.Explicit = substituteComponentObject(value.Providers.Explicit, arguments, providers, renames, nil, expansion)
			for _, expression := range value.Providers.Inferred {
				result.Providers.Inferred = append(result.Providers.Inferred, substitute(expression))
			}
		}
		return &result
	case *syntax.ComponentInstance:
		result := *value
		result.BaseNode = componentBase(value.BaseNode, expansion)
		result.Name = renames[value.Name]
		result.Index = substitute(value.Index)
		result.Arguments = substituteComponentObject(value.Arguments, arguments, providers, renames, nil, expansion)
		result.Providers = substituteComponentObject(value.Providers, arguments, providers, renames, nil, expansion)
		return &result
	case *syntax.ComponentExport:
		return &syntax.ComponentExport{BaseNode: componentBase(value.BaseNode, expansion), Name: value.Name, Value: substitute(value.Value)}
	default:
		return nil
	}
}

func substituteComponentExpression(expression syntax.Expression, arguments, providers map[string]syntax.Expression, renames map[string]string, blocked map[string]bool, expansion string) syntax.Expression {
	if expression == nil {
		return nil
	}
	if identifier, ok := expression.(*syntax.IdentifierExpression); ok {
		if !blocked[identifier.Name] {
			if replacement := arguments[identifier.Name]; replacement != nil {
				return cloneExpression(replacement, nil, "")
			}
			if replacement := providers[identifier.Name]; replacement != nil {
				return cloneExpression(replacement, nil, "")
			}
			if renamed := renames[identifier.Name]; renamed != "" {
				return &syntax.IdentifierExpression{BaseNode: componentBase(identifier.BaseNode, expansion), Name: renamed}
			}
		}
		return &syntax.IdentifierExpression{BaseNode: componentBase(identifier.BaseNode, expansion), Name: identifier.Name}
	}
	substitute := func(value syntax.Expression, scope map[string]bool) syntax.Expression {
		return substituteComponentExpression(value, arguments, providers, renames, scope, expansion)
	}
	switch value := expression.(type) {
	case *syntax.LiteralExpression:
		return &syntax.LiteralExpression{BaseNode: componentBase(value.BaseNode, expansion), Value: value.Value}
	case *syntax.ArrayExpression:
		result := &syntax.ArrayExpression{BaseNode: componentBase(value.BaseNode, expansion)}
		for _, item := range value.Items {
			result.Items = append(result.Items, substitute(item, blocked))
		}
		return result
	case *syntax.ObjectExpression:
		return substituteComponentObject(value, arguments, providers, renames, blocked, expansion)
	case *syntax.ForExpression:
		inner := copyBlockedNames(blocked)
		inner[value.ValueVariable] = true
		if value.KeyVariable != "" {
			inner[value.KeyVariable] = true
		}
		return &syntax.ForExpression{
			BaseNode: componentBase(value.BaseNode, expansion), KeyVariable: value.KeyVariable, ValueVariable: value.ValueVariable,
			Collection: substitute(value.Collection, blocked), Key: substitute(value.Key, inner), Value: substitute(value.Value, inner),
			Condition: substitute(value.Condition, inner), Object: value.Object,
		}
	case *syntax.TemplateExpression:
		result := &syntax.TemplateExpression{BaseNode: componentBase(value.BaseNode, expansion)}
		for _, part := range value.Parts {
			result.Parts = append(result.Parts, syntax.TemplatePart{Text: part.Text, Expression: substitute(part.Expression, blocked)})
		}
		return result
	case *syntax.UnaryExpression:
		return &syntax.UnaryExpression{BaseNode: componentBase(value.BaseNode, expansion), Operator: value.Operator, Operand: substitute(value.Operand, blocked)}
	case *syntax.BinaryExpression:
		return &syntax.BinaryExpression{BaseNode: componentBase(value.BaseNode, expansion), Left: substitute(value.Left, blocked), Operator: value.Operator, Right: substitute(value.Right, blocked)}
	case *syntax.ConditionalExpression:
		return &syntax.ConditionalExpression{BaseNode: componentBase(value.BaseNode, expansion), Condition: substitute(value.Condition, blocked), Then: substitute(value.Then, blocked), Else: substitute(value.Else, blocked)}
	case *syntax.MemberExpression:
		return &syntax.MemberExpression{BaseNode: componentBase(value.BaseNode, expansion), Target: substitute(value.Target, blocked), Name: value.Name}
	case *syntax.IndexExpression:
		return &syntax.IndexExpression{BaseNode: componentBase(value.BaseNode, expansion), Target: substitute(value.Target, blocked), Index: substitute(value.Index, blocked)}
	case *syntax.CallExpression:
		result := &syntax.CallExpression{BaseNode: componentBase(value.BaseNode, expansion), Callee: substitute(value.Callee, blocked)}
		for _, argument := range value.Arguments {
			result.Arguments = append(result.Arguments, substitute(argument, blocked))
		}
		return result
	default:
		return cloneExpression(expression, nil, expansion)
	}
}

func substituteComponentObject(object *syntax.ObjectExpression, arguments, providers map[string]syntax.Expression, renames map[string]string, blocked map[string]bool, expansion string) *syntax.ObjectExpression {
	if object == nil {
		return nil
	}
	result := &syntax.ObjectExpression{BaseNode: componentBase(object.BaseNode, expansion)}
	for _, item := range objectItems(object) {
		switch item := item.(type) {
		case syntax.ObjectField:
			item.BaseNode = componentBase(item.BaseNode, expansion)
			item.Value = substituteComponentExpression(item.Value, arguments, providers, renames, blocked, expansion)
			item.Condition = substituteComponentExpression(item.Condition, arguments, providers, renames, blocked, expansion)
			result.Items = append(result.Items, item)
			result.Fields = append(result.Fields, item)
		case syntax.ObjectSpread:
			item.BaseNode = componentBase(item.BaseNode, expansion)
			item.Value = substituteComponentExpression(item.Value, arguments, providers, renames, blocked, expansion)
			result.Items = append(result.Items, item)
		case syntax.InputsSpread:
			item.BaseNode = componentBase(item.BaseNode, expansion)
			item.Value = substituteComponentExpression(item.Value, arguments, providers, renames, blocked, expansion)
			result.Items = append(result.Items, item)
		}
	}
	return result
}

func componentBase(base syntax.BaseNode, expansion string) syntax.BaseNode {
	base.Expansion = expansion
	return base
}

func (expander *componentExpander) rewriteDeclaration(declaration syntax.Declaration) {
	switch value := declaration.(type) {
	case *syntax.TerraformDeclaration:
		value.Config = expander.rewriteObject(value.Config)
	case *syntax.InputDeclaration:
		value.Default = expander.rewriteExpression(value.Default)
		value.Metadata = expander.rewriteObject(value.Metadata)
	case *syntax.LetDeclaration:
		value.Value = expander.rewriteExpression(value.Value)
	case *syntax.ConfigureDeclaration:
		value.Index = expander.rewriteExpression(value.Index)
		value.Alias = expander.rewriteExpression(value.Alias)
		value.Config = expander.rewriteObject(value.Config)
	case *syntax.ResourceDeclaration:
		value.ProviderConfig = expander.rewriteExpression(value.ProviderConfig)
		value.LabelExpression = expander.rewriteExpression(value.LabelExpression)
		value.Arguments = expander.rewriteObject(value.Arguments)
		value.With = expander.rewriteObject(value.With)
		value.Condition = expander.rewriteExpression(value.Condition)
	case *syntax.DataDeclaration:
		value.ProviderConfig = expander.rewriteExpression(value.ProviderConfig)
		value.LabelExpression = expander.rewriteExpression(value.LabelExpression)
		value.Arguments = expander.rewriteObject(value.Arguments)
	case *syntax.ModuleDeclaration:
		value.Index = expander.rewriteExpression(value.Index)
		value.LabelExpression = expander.rewriteExpression(value.LabelExpression)
		value.Arguments = expander.rewriteObject(value.Arguments)
		value.MetaArguments = expander.rewriteObject(value.MetaArguments)
		if value.Providers != nil {
			value.Providers.Explicit = expander.rewriteObject(value.Providers.Explicit)
			for index, expression := range value.Providers.Inferred {
				value.Providers.Inferred[index] = expander.rewriteExpression(expression)
			}
		}
	case *syntax.OutputDeclaration:
		value.Value = expander.rewriteExpression(value.Value)
		value.Metadata = expander.rewriteObject(value.Metadata)
	}
}

func (expander *componentExpander) rewriteObject(object *syntax.ObjectExpression) *syntax.ObjectExpression {
	return expander.rewriteObjectScoped(object, nil)
}

func (expander *componentExpander) rewriteObjectScoped(object *syntax.ObjectExpression, blocked map[string]bool) *syntax.ObjectExpression {
	if object == nil {
		return nil
	}
	for index, item := range object.Items {
		switch item := item.(type) {
		case syntax.ObjectField:
			item.Value = expander.rewriteExpressionScoped(item.Value, blocked)
			item.Condition = expander.rewriteExpressionScoped(item.Condition, blocked)
			object.Items[index] = item
		case syntax.ObjectSpread:
			item.Value = expander.rewriteExpressionScoped(item.Value, blocked)
			object.Items[index] = item
		case syntax.InputsSpread:
			item.Value = expander.rewriteExpressionScoped(item.Value, blocked)
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

func (expander *componentExpander) rewriteExpression(expression syntax.Expression) syntax.Expression {
	return expander.rewriteExpressionScoped(expression, nil)
}

func (expander *componentExpander) rewriteExpressionScoped(expression syntax.Expression, blocked map[string]bool) syntax.Expression {
	if expression == nil {
		return nil
	}
	if member, ok := expression.(*syntax.MemberExpression); ok {
		if result, identity, known := expander.componentTarget(member.Target, blocked); known {
			if result == nil {
				return expression
			}
			result.used[member.Name] = true
			if rewritten := expander.resolveExport(result, identity, member.Name, member); rewritten != nil {
				return rewritten
			}
			return expression
		}
	}
	switch value := expression.(type) {
	case *syntax.IdentifierExpression:
		if !blocked[value.Name] && expander.ordinary[value.Name] != nil {
			expander.preparer.addDiagnostic(value, fmt.Sprintf("component instance %q can be used only through a named export", value.Name))
		}
	case *syntax.ArrayExpression:
		for index, item := range value.Items {
			value.Items[index] = expander.rewriteExpressionScoped(item, blocked)
		}
	case *syntax.ObjectExpression:
		return expander.rewriteObjectScoped(value, blocked)
	case *syntax.ForExpression:
		value.Collection = expander.rewriteExpressionScoped(value.Collection, blocked)
		inner := copyBlockedNames(blocked)
		inner[value.ValueVariable] = true
		if value.KeyVariable != "" {
			inner[value.KeyVariable] = true
		}
		value.Key = expander.rewriteExpressionScoped(value.Key, inner)
		value.Value = expander.rewriteExpressionScoped(value.Value, inner)
		value.Condition = expander.rewriteExpressionScoped(value.Condition, inner)
	case *syntax.TemplateExpression:
		for index, part := range value.Parts {
			value.Parts[index].Expression = expander.rewriteExpressionScoped(part.Expression, blocked)
		}
	case *syntax.UnaryExpression:
		value.Operand = expander.rewriteExpressionScoped(value.Operand, blocked)
	case *syntax.BinaryExpression:
		value.Left = expander.rewriteExpressionScoped(value.Left, blocked)
		value.Right = expander.rewriteExpressionScoped(value.Right, blocked)
	case *syntax.ConditionalExpression:
		value.Condition = expander.rewriteExpressionScoped(value.Condition, blocked)
		value.Then = expander.rewriteExpressionScoped(value.Then, blocked)
		value.Else = expander.rewriteExpressionScoped(value.Else, blocked)
	case *syntax.MemberExpression:
		value.Target = expander.rewriteExpressionScoped(value.Target, blocked)
	case *syntax.IndexExpression:
		if result, identity, known := expander.componentTarget(value, blocked); known {
			if result != nil {
				expander.preparer.addDiagnostic(value, fmt.Sprintf("component instance %s can be used only through a named export", identity))
			}
			return expression
		}
		value.Target = expander.rewriteExpressionScoped(value.Target, blocked)
		value.Index = expander.rewriteExpressionScoped(value.Index, blocked)
	case *syntax.CallExpression:
		value.Callee = expander.rewriteExpressionScoped(value.Callee, blocked)
		for index, argument := range value.Arguments {
			value.Arguments[index] = expander.rewriteExpressionScoped(argument, blocked)
		}
	}
	return expression
}

func (expander *componentExpander) componentTarget(expression syntax.Expression, blocked map[string]bool) (*componentInstanceResult, string, bool) {
	switch value := expression.(type) {
	case *syntax.IdentifierExpression:
		if blocked[value.Name] {
			return nil, "", false
		}
		result := expander.ordinary[value.Name]
		return result, value.Name, result != nil
	case *syntax.IndexExpression:
		identifier, ok := value.Target.(*syntax.IdentifierExpression)
		if !ok || blocked[identifier.Name] || expander.indexed[identifier.Name] == nil {
			return nil, "", false
		}
		index, evaluated := expander.preparer.eval(value.Index, nil)
		key, valid := canonicalIndex(index, true)
		if !evaluated || !valid {
			expander.preparer.addDiagnosticAt(value.Index, "component lookup index must be a nonempty compile-time string or exact number")
			return nil, identifier.Name, true
		}
		result := expander.indexed[identifier.Name][key]
		identity := identifier.Name + "[" + key + "]"
		if result == nil {
			expander.preparer.addDiagnostic(value, fmt.Sprintf("unknown indexed component instance %s", identity))
			return nil, identity, true
		}
		return result, identity, true
	default:
		return nil, "", false
	}
}

func (c *compiler) checkComponentChecks() {
	for _, check := range c.file.ComponentArgumentChecks {
		expected := c.compileType(check.Expected)
		actual := c.checkExpression(check.Actual)
		context := fmt.Sprintf("component %q argument %q%s", check.ComponentName, check.ParameterName, componentDiagnosticSuffix(check.GetExpansion()))
		c.checkAssignment(expected, actual, check.GetSpan(), context)
	}
	for _, check := range c.file.ComponentProviderChecks {
		suffix := componentDiagnosticSuffix(check.GetExpansion())
		expected := c.providers[check.ExpectedProviderName]
		if expected == nil {
			c.addDiagnostic(check.GetSpan(), fmt.Sprintf("component %q provider parameter %q references unknown provider %q%s", check.ComponentName, check.ParameterName, check.ExpectedProviderName, suffix))
			continue
		}
		identifier, ok := check.Actual.(*syntax.IdentifierExpression)
		if !ok {
			c.checkExpression(check.Actual)
			c.addDiagnostic(check.GetSpan(), fmt.Sprintf("component %q provider argument %q must be a static provider configuration handle%s", check.ComponentName, check.ParameterName, suffix))
			continue
		}
		symbol := c.symbols[identifier.Name]
		if symbol == nil || symbol.kind != symbolProviderConfig || symbol.providerConfig == nil {
			c.addDiagnostic(check.GetSpan(), fmt.Sprintf("component %q provider argument %q references unknown provider configuration %q%s", check.ComponentName, check.ParameterName, identifier.Name, suffix))
			continue
		}
		actual := symbol.providerConfig.provider
		if actual.source != expected.source {
			c.addDiagnostic(check.GetSpan(), fmt.Sprintf("component %q provider argument %q requires source %q, got %q%s", check.ComponentName, check.ParameterName, expected.source, actual.source, suffix))
		}
	}
	for _, check := range c.file.ComponentExportChecks {
		c.checkExpression(check.Value)
	}
}

func componentDiagnosticSuffix(expansion string) string {
	if expansion == "" {
		return ""
	}
	return " [" + expansion + "]"
}
