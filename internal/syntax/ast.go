package syntax

type Node interface {
	GetSpan() Span
}

type BaseNode struct {
	Span Span
}

func (n BaseNode) GetSpan() Span { return n.Span }

type File struct {
	Name         string
	Source       string
	Declarations []Declaration
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
}

type InputDeclaration struct {
	BaseNode
	Name    string
	Type    *TypeExpression
	Default Expression
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
	ProviderName string
	Alias        Expression
	Config       *ObjectExpression
}

func (*ConfigureDeclaration) declarationNode() {}

type ResourceDeclaration struct {
	BaseNode
	Name               string
	ProviderConfigName string
	Kind               string
	Label              string
	Arguments          *ObjectExpression
	MetaArguments      *ObjectExpression
}

func (*ResourceDeclaration) declarationNode() {}

type DataDeclaration struct {
	BaseNode
	Name               string
	ProviderConfigName string
	Kind               string
	Label              string
	Arguments          *ObjectExpression
}

func (*DataDeclaration) declarationNode() {}

type ModuleDeclaration struct {
	BaseNode
	Name      string
	Label     string
	Source    string
	Arguments *ObjectExpression
	Providers *ObjectExpression
}

func (*ModuleDeclaration) declarationNode() {}

type OutputDeclaration struct {
	BaseNode
	Name  string
	Value Expression
}

func (*OutputDeclaration) declarationNode() {}

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

type ObjectField struct {
	BaseNode
	Name   string
	Quoted bool
	Value  Expression
}

type ObjectExpression struct {
	BaseNode
	Fields []ObjectField
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
