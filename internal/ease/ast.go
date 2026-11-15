package ease

// Program is the root AST node representing an Ease source file.
type Program struct {
	Imports   []*ImportDecl
	Functions []*FuncDecl
	Defs      []*DefDecl
	Unions    []*UnionDecl
	Checks    []*CheckBlock
	Body      []Stmt
}

type ImportDecl struct {
	posInfo
	Name string
}

// Stmt is implemented by all Ease statement and check-item AST nodes.
type Stmt interface {
	stmtNode()
	Pos() (int, int)
}

// Expr is implemented by all Ease expression AST nodes.
type Expr interface {
	exprNode()
	Pos() (int, int)
}

type posInfo struct {
	Line int
	Col  int
}

func (p posInfo) Pos() (int, int) { return p.Line, p.Col }

// FuncDecl defines a `to <name> <params> ... end` function.
type FuncDecl struct {
	posInfo
	Name   string
	Params []string
	Body   []Stmt
}

// DefField defines a field and its default value (plus optional union allowed values) inside a `def <name> ... end` record.
type DefField struct {
	Line    int
	Col     int
	Name    string
	Default Expr
	Allowed []Expr
}

// DefDecl defines a data record/prototype with `def <name> ... end`.
type DefDecl struct {
	posInfo
	Name     string
	Fields   []DefField
	Imported bool
}

// UnionDecl defines a union type value-space with `def <name> = v1 | v2 | ...`.
type UnionDecl struct {
	posInfo
	Name     string
	Variants []Expr
	Imported bool
}

// CheckBlock defines a top-level Pyret-style `check "label": ... end` block.
type CheckBlock struct {
	posInfo
	Title string
	Body  []Stmt
}

type LetStmt struct {
	posInfo
	Name  string
	Value Expr
}

func (*LetStmt) stmtNode() {}

// AssignStmt represents a direct `name = expr` assignment (defines if new, updates if mutable).
type AssignStmt struct {
	posInfo
	Name  string
	Value Expr
}

func (*AssignStmt) stmtNode() {}

type SayStmt struct {
	posInfo
	Value Expr
}

func (*SayStmt) stmtNode() {}

type IfStmt struct {
	posInfo
	Condition Expr
	ThenBody  []Stmt
	ElseBody  []Stmt
}

func (*IfStmt) stmtNode() {}

type RepeatStmt struct {
	posInfo
	Count Expr
	Body  []Stmt
}

func (*RepeatStmt) stmtNode() {}

type ForStmt struct {
	posInfo
	ItemName string
	Iterable Expr
	Body     []Stmt
}

func (*ForStmt) stmtNode() {}

// CheckRule is a single line inside a `check ... end` guard block.
// If Action != nil (`cond -> action`), Action runs when Cond is true.
// If Action == nil (`cond`), Cond is a precondition that triggers ElseBody when false.
type CheckRule struct {
	Line   int
	Col    int
	Cond   Expr
	Action Stmt
}

// CheckGuardStmt represents `check cond -> action` or `check ... [else ...] end`.
type CheckGuardStmt struct {
	posInfo
	Rules    []CheckRule
	ElseBody []Stmt
}

func (*CheckGuardStmt) stmtNode() {}

type ExprStmt struct {
	posInfo
	Expression Expr
}

func (*ExprStmt) stmtNode() {}

type CheckIsStmt struct {
	posInfo
	Actual   Expr
	Expected Expr
}

func (*CheckIsStmt) stmtNode() {}

type CheckIsNotStmt struct {
	posInfo
	Actual     Expr
	Unexpected Expr
}

func (*CheckIsNotStmt) stmtNode() {}

type CheckRaisesStmt struct {
	posInfo
	Expression Expr
	Expected   Expr
}

func (*CheckRaisesStmt) stmtNode() {}

// Expressions

type NumberExpr struct {
	posInfo
	Value float64
	Raw   string
}

func (*NumberExpr) exprNode() {}

type StringExpr struct {
	posInfo
	Value string
}

func (*StringExpr) exprNode() {}

// InterpPart is either a literal text segment or an interpolated expression inside a string.
type InterpPart struct {
	IsExpr bool
	Text   string
	Expr   Expr
}

// InterpStringExpr represents a string literal containing `{expr}` interpolations.
type InterpStringExpr struct {
	posInfo
	Parts []InterpPart
}

func (*InterpStringExpr) exprNode() {}

type BoolExpr struct {
	posInfo
	Value bool
}

func (*BoolExpr) exprNode() {}

// AskExpr reads a line of user input from stdin, optionally printing a prompt first.
type AskExpr struct {
	posInfo
	Prompt Expr
}

func (*AskExpr) exprNode() {}

type IdentExpr struct {
	posInfo
	Name string
}

func (*IdentExpr) exprNode() {}

type ListExpr struct {
	posInfo
	Elements    []Expr
	Homogeneous bool
}

func (*ListExpr) exprNode() {}

// TypedListExpr represents `list as <type>` or `list as <def> with ... end`.
type TypedListExpr struct {
	posInfo
	ElemType string
	TypeLine int
	TypeCol  int
	Rows     [][]WithFieldUpdate
}

func (*TypedListExpr) exprNode() {}

type UnaryExpr struct {
	posInfo
	Op    string
	Right Expr
}

func (*UnaryExpr) exprNode() {}

// TypeCheckExpr represents `<expr> is [not] <type>` (e.g. `name is not text`).
type TypeCheckExpr struct {
	posInfo
	Target   Expr
	TypeName string
	Negated  bool
}

func (*TypeCheckExpr) exprNode() {}

// CastExpr represents explicit type conversion `<expr> as <type>` (e.g. `ask "Age?" as number`).
type CastExpr struct {
	posInfo
	Target   Expr
	TypeName string
	TypeLine int
	TypeCol  int
}

func (*CastExpr) exprNode() {}

type BinaryExpr struct {
	posInfo
	Left  Expr
	Op    string
	Right Expr
}

func (*BinaryExpr) exprNode() {}

// FieldExpr reads a field from a record (`player.x`).
type FieldExpr struct {
	posInfo
	Target Expr
	Field  string
}

func (*FieldExpr) exprNode() {}

// WithFieldUpdate represents one `field = value` override inside a `with` expression.
type WithFieldUpdate struct {
	Line  int
	Col   int
	Name  string
	Value Expr
}

// WithExpr clones a record/prototype and overrides one or more fields (`player with x = 10`).
type WithExpr struct {
	posInfo
	Base    Expr
	Updates []WithFieldUpdate
}

func (*WithExpr) exprNode() {}

type CallExpr struct {
	posInfo
	Callee string
	Args   []Expr
}

func (*CallExpr) exprNode() {}

type IndexExpr struct {
	posInfo
	Target Expr
	Index  Expr
}

func (*IndexExpr) exprNode() {}
