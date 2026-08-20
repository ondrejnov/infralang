package syntax

type Node interface {
	GetSpan() Span
	GetFile() FileID
	GetExpansion() string
}

type BaseNode struct {
	File      FileID
	Span      Span
	Expansion string
}

func (n BaseNode) GetExpansion() string { return n.Expansion }

func (n BaseNode) GetSpan() Span { return n.Span }
func (n BaseNode) GetFile() FileID {
	if n.File != "" {
		return n.File
	}
	return n.Span.File
}

type File struct {
	Name                    string
	ID                      FileID
	Source                  string
	Declarations            []Declaration
	ComponentArgumentChecks []ComponentArgumentCheck
	ComponentProviderChecks []ComponentProviderCheck
	ComponentExportChecks   []ComponentExportCheck
}

type Declaration interface {
	Node
	declarationNode()
}

type TerraformDeclaration struct {
	BaseNode
	Config *ObjectExpression
}

func (*TerraformDeclaration) declarationNode() {}

type ProviderDeclaration struct {
	BaseNode
	Name    string
	Source  string
	Version string
}

func (*ProviderDeclaration) declarationNode() {}

type TypeExpression struct {
	BaseNode
	Name      string
	Arguments []*TypeExpression
	Fields    []TypeField
}

type TypeAliasDeclaration struct {
	BaseNode
	Name     string
	Type     *TypeExpression
	Exported bool
}

func (*TypeAliasDeclaration) declarationNode() {}

type TypeImportItem struct {
	BaseNode
	ImportedName string
	LocalName    string
}

type TypeImportDeclaration struct {
	BaseNode
	Items []TypeImportItem
	Path  string
}

func (*TypeImportDeclaration) declarationNode() {}

type ConstDeclaration struct {
	BaseNode
	Name  string
	Type  *TypeExpression
	Value Expression
}

func (*ConstDeclaration) declarationNode() {}

type StaticForDeclaration struct {
	BaseNode
	KeyVariable   string
	ValueVariable string
	Collection    Expression
	Declarations  []Declaration
}

func (*StaticForDeclaration) declarationNode() {}

type ComponentParameter struct {
	BaseNode
	Name string
	Type *TypeExpression
}

type ComponentProviderParameter struct {
	BaseNode
	Name         string
	ProviderName string
}

type ComponentDefinition struct {
	BaseNode
	Name         string
	Parameters   []ComponentParameter
	Providers    []ComponentProviderParameter
	Declarations []Declaration
}

func (*ComponentDefinition) declarationNode() {}

type ComponentInstance struct {
	BaseNode
	Name          string
	Index         Expression
	ComponentName string
	Arguments     *ObjectExpression
	Providers     *ObjectExpression
}

func (*ComponentInstance) declarationNode() {}

type ComponentExport struct {
	BaseNode
	Name  string
	Value Expression
}

func (*ComponentExport) declarationNode() {}

type ComponentArgumentCheck struct {
	BaseNode
	ComponentName string
	ParameterName string
	Expected      *TypeExpression
	Actual        Expression
}

type ComponentProviderCheck struct {
	BaseNode
	ComponentName        string
	ParameterName        string
	ExpectedProviderName string
	Actual               Expression
}

type ComponentExportCheck struct {
	BaseNode
	ComponentName string
	ExportName    string
	Value         Expression
}

type TypeField struct {
	BaseNode
	Name         string
	WireName     string
	ExplicitWire bool
	Quoted       bool
	Type         *TypeExpression
	Optional     bool
	Default      Expression
}

type InputDeclaration struct {
	BaseNode
	Name          string
	WireName      string
	ExplicitWire  bool
	Type          *TypeExpression
	Default       Expression
	Metadata      *ObjectExpression
	MetadataItems []InputMetadataItem
}

func (*InputDeclaration) declarationNode() {}

type LetDeclaration struct {
	BaseNode
	Name  string
	Value Expression
}

func (*LetDeclaration) declarationNode() {}

type ConfigureDeclaration struct {
	BaseNode
	Name         string
	Index        Expression
	ProviderName string
	Alias        Expression
	Config       *ObjectExpression
	Inherited    bool
}

func (*ConfigureDeclaration) declarationNode() {}

type ResourceDeclaration struct {
	BaseNode
	Name               string
	ProviderConfigName string
	ProviderConfig     Expression
	Kind               string
	Label              string
	LabelExpression    Expression
	Arguments          *ObjectExpression
	MetaArguments      *ObjectExpression
	Condition          Expression
}

func (*ResourceDeclaration) declarationNode() {}

type DataDeclaration struct {
	BaseNode
	Name               string
	ProviderConfigName string
	ProviderConfig     Expression
	Kind               string
	Label              string
	LabelExpression    Expression
	Arguments          *ObjectExpression
}

func (*DataDeclaration) declarationNode() {}

type ModuleDeclaration struct {
	BaseNode
	Name            string
	Index           Expression
	Label           string
	LabelExpression Expression
	Source          string
	Arguments       *ObjectExpression
	Providers       *ProviderMapping
	MetaArguments   *ObjectExpression
}

func (*ModuleDeclaration) declarationNode() {}

type OutputDeclaration struct {
	BaseNode
	Name     string
	Value    Expression
	Metadata *ObjectExpression
}

func (*OutputDeclaration) declarationNode() {}

type MovedDeclaration struct {
	BaseNode
	From  string
	To    string
	Items []MovedItem
}

func (*MovedDeclaration) declarationNode() {}

type Expression interface {
	Node
	expressionNode()
}

type IdentifierExpression struct {
	BaseNode
	Name string
}

func (*IdentifierExpression) expressionNode() {}

type LiteralExpression struct {
	BaseNode
	Value any
}

func (*LiteralExpression) expressionNode() {}

type ArrayExpression struct {
	BaseNode
	Items []Expression
}

func (*ArrayExpression) expressionNode() {}

type ForExpression struct {
	BaseNode
	KeyVariable   string
	ValueVariable string
	Collection    Expression
	Key           Expression
	Value         Expression
	Condition     Expression
	Object        bool
}

func (*ForExpression) expressionNode() {}

type ObjectField struct {
	BaseNode
	Name      string
	WireName  string
	Quoted    bool
	Value     Expression
	Punned    bool
	Condition Expression
}

type ObjectItem interface {
	Node
	objectItem()
}

func (ObjectField) objectItem() {}

type InputMetadataItem interface {
	Node
	inputMetadataItem()
}

func (ObjectField) inputMetadataItem() {}

type ObjectSpread struct {
	BaseNode
	Value Expression
}

func (ObjectSpread) objectItem()        {}
func (ObjectSpread) inputMetadataItem() {}

type InputsSpread struct {
	BaseNode
	Value Expression
}

func (InputsSpread) objectItem() {}

type ValidationClause struct {
	BaseNode
	Condition Expression
	Message   string
}

func (ValidationClause) inputMetadataItem() {}

type ProviderMapping struct {
	BaseNode
	Explicit *ObjectExpression
	Inferred []Expression
}

type AddressLiteral struct {
	BaseNode
	Raw string
}

type MovedItem struct {
	BaseNode
	From AddressLiteral
	To   AddressLiteral
}

type ObjectExpression struct {
	BaseNode
	Fields []ObjectField
	Items  []ObjectItem
}

func (*ObjectExpression) expressionNode() {}

func (o *ObjectExpression) Field(name string) (ObjectField, bool) {
	for _, field := range o.Fields {
		if field.Name == name {
			return field, true
		}
	}
	return ObjectField{}, false
}

type UnaryExpression struct {
	BaseNode
	Operator TokenKind
	Operand  Expression
}

func (*UnaryExpression) expressionNode() {}

type BinaryExpression struct {
	BaseNode
	Left     Expression
	Operator TokenKind
	Right    Expression
}

func (*BinaryExpression) expressionNode() {}

type ConditionalExpression struct {
	BaseNode
	Condition Expression
	Then      Expression
	Else      Expression
}

func (*ConditionalExpression) expressionNode() {}

type MemberExpression struct {
	BaseNode
	Target Expression
	Name   string
}

func (*MemberExpression) expressionNode() {}

type IndexExpression struct {
	BaseNode
	Target Expression
	Index  Expression
}

func (*IndexExpression) expressionNode() {}

type CallExpression struct {
	BaseNode
	Callee    Expression
	Arguments []Expression
}

func (*CallExpression) expressionNode() {}

type TemplatePart struct {
	Text       string
	Expression Expression
}

type TemplateExpression struct {
	BaseNode
	Parts []TemplatePart
}

func (*TemplateExpression) expressionNode() {}
