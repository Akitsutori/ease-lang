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
	Name         string
	Path         string
	Alias        string
	IsFileImport bool
	ModuleProg   *Program
	ModuleSource string
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

// CheckBlock defines a top-level `test "label" ... end` block.
type CheckBlock struct {
	posInfo
	Title string
	Body  []Stmt
}

// ConstDeclStmt represents an immutable `def name = expr` constant declaration.
type ConstDeclStmt struct {
	posInfo
	Name  string
	Value Expr
}

func (*ConstDeclStmt) stmtNode() {}

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

// LoopStmt represents `loop [<iterable>] ... end` (binding `it.index`, and `it` when an iterable is given).
type LoopStmt struct {
	posInfo
	Iterable Expr
	Body     []Stmt
}

func (*LoopStmt) stmtNode() {}

// LoopEndStmt represents `-> end` inside a `check` or `match` guard to exit the enclosing `loop`/`take`.
type LoopEndStmt struct {
	posInfo
}

func (*LoopEndStmt) stmtNode() {}

// ReturnStmt represents `-> <expr>` inside a `to` function or `swarm loop`.
type ReturnStmt struct {
	posInfo
	Value Expr
}

func (*ReturnStmt) stmtNode() {}

// stmtsHaveReturn reports whether `stmts` contains any `-> <expr>` return statement.
func stmtsHaveReturn(stmts []Stmt) bool {
	for _, s := range stmts {
		switch st := s.(type) {
		case *ReturnStmt:
			return true
		case *LoopStmt:
			if stmtsHaveReturn(st.Body) {
				return true
			}
		case *TakeStmt:
			if stmtsHaveReturn(st.Body) {
				return true
			}
		case *CheckGuardStmt:
			if stmtsHaveReturn(st.Body) || stmtsHaveReturn(st.ElseBody) {
				return true
			}
			for _, r := range st.Rules {
				if r.Action != nil && stmtsHaveReturn([]Stmt{r.Action}) {
					return true
				}
			}
		case *MatchStmt:
			if stmtsHaveReturn(st.ElseBody) {
				return true
			}
			for _, arm := range st.Arms {
				if (arm.Action != nil && stmtsHaveReturn([]Stmt{arm.Action})) || stmtsHaveReturn(arm.Body) {
					return true
				}
			}
		}
	}
	return false
}

// CheckRule is a single `cond -> action` line inside a single-line `check` or `check ... end` verdict block.
type CheckRule struct {
	Line   int
	Col    int
	Cond   Expr
	Action Stmt
}

// CheckGuardStmt represents:
//   - single-line: `check cond -> action`
//   - verdict block: `check\n  cond -> action\n  [else ...]\nend` (`IsBlock == true && !IsEffectBlock`)
//   - effect block: `check cond\n  stmts...\n  [else\n  stmts...]\nend` (`IsBlock == true && IsEffectBlock`)
type CheckGuardStmt struct {
	posInfo
	IsBlock       bool
	IsEffectBlock bool
	Cond          Expr
	Body          []Stmt
	Rules         []CheckRule
	ElseBody      []Stmt
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

// CheckCondStmt represents a direct boolean condition assertion inside a `test` block.
type CheckCondStmt struct {
	posInfo
	Condition Expr
}

func (*CheckCondStmt) stmtNode() {}

// CheckIsErrorStmt represents `expr is error` or `expr is not error` inside a `test` block.
type CheckIsErrorStmt struct {
	posInfo
	Expression Expr
	Negated    bool
}

func (*CheckIsErrorStmt) stmtNode() {}

// CheckErrorBlockStmt represents `expr -> ... end` for inspecting `error.type` and `error.message`.
type CheckErrorBlockStmt struct {
	posInfo
	Expression Expr
	Body       []Stmt
}

func (*CheckErrorBlockStmt) stmtNode() {}

// MatchSegKind identifies the kind of segment in a parsed `match` pattern.
type MatchSegKind int

const (
	MatchSegLiteral MatchSegKind = iota
	MatchSegWildcard
	MatchSegCapture
)

// MatchSegment represents one parsed unit inside a `match` string pattern.
type MatchSegment struct {
	Kind         MatchSegKind
	Literal      string // for MatchSegLiteral (already NFC-normalized by lexer)
	VarName      string // for MatchSegCapture
	FixedWidth   int    // >0 for `{name: N}` rune count, 0 for variable-width `{name}`
	CastType     string // empty or explicit/inherited type target
	ExplicitCast bool   // true if `as <type>` was written in this segment
	TypeLine     int    // 1-based line of type target token (when ExplicitCast is true)
	TypeCol      int    // 1-based column of type target token (when ExplicitCast is true)
	Line         int    // 1-based placeholder line
	Col          int    // 1-based placeholder column
}

// MatchPattern is a compiled pattern template on a `match` arm.
type MatchPattern struct {
	posInfo
	Raw      string
	Segments []MatchSegment
}

// MatchArmStmt represents one `<pattern> -> <action>` branch in a `match` statement.
type MatchArmStmt struct {
	Line    int
	Col     int
	Pattern MatchPattern
	Action  Stmt
	Body    []Stmt
}

// MatchStmt represents `match <target> ... end` in statement position.
type MatchStmt struct {
	posInfo
	Target   Expr
	Arms     []MatchArmStmt
	ElseBody []Stmt
}

func (*MatchStmt) stmtNode() {}

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

// CastExpr represents explicit type conversion `<expr> as <type> [or <fallback>]` (e.g. `ask "Age?" as number or 0`).
type CastExpr struct {
	posInfo
	Target   Expr
	TypeName string
	TypeLine int
	TypeCol  int
	Fallback Expr
	OrLine   int
	OrCol    int
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

// WithIndexUpdate represents one `.<index> = value` element override inside a list `with` expression.
type WithIndexUpdate struct {
	Line  int
	Col   int
	Index Expr
	Value Expr
}

// WithExpr clones a record/prototype or list and overrides fields or indexed elements.
type WithExpr struct {
	posInfo
	Base         Expr
	Updates      []WithFieldUpdate
	IndexUpdates []WithIndexUpdate
}

func (*WithExpr) exprNode() {}

// WhereExpr filters a list using `it` and `it.index` against one or more conditions.
type WhereExpr struct {
	posInfo
	Target     Expr
	Conditions []Expr
	IsBlock    bool
}

func (*WhereExpr) exprNode() {}

type CallExpr struct {
	posInfo
	Module string
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

// MatchArmExpr represents one `<pattern> -> <expr>` branch in a `match` expression.
type MatchArmExpr struct {
	Line    int
	Col     int
	Pattern MatchPattern
	Value   Expr
}

// MatchExpr represents `match <target> ... end` in expression position.
type MatchExpr struct {
	posInfo
	Target    Expr
	Arms      []MatchArmExpr
	HasElse   bool
	ElseBody  []Stmt
	ElseValue Expr
}

func (*MatchExpr) exprNode() {}

// SwarmLoopExpr represents parallel ordered iteration `swarm loop <iterable> ... end`.
type SwarmLoopExpr struct {
	posInfo
	Iterable Expr
	Body     []Stmt
}

func (*SwarmLoopExpr) exprNode() {}

// InboxExpr represents `inbox as <type>` creating a typed concurrent FIFO inbox.
type InboxExpr struct {
	posInfo
	ElemType string
	TypeLine int
	TypeCol  int
}

func (*InboxExpr) exprNode() {}

// SpawnStmt represents `spawn <call>` launching a background worker goroutine.
type SpawnStmt struct {
	posInfo
	Call *CallExpr
}

func (*SpawnStmt) stmtNode() {}

// TakeStmt represents `take <inbox> ... end` consuming messages until closed and drained.
type TakeStmt struct {
	posInfo
	Inbox Expr
	Body  []Stmt
}

func (*TakeStmt) stmtNode() {}
