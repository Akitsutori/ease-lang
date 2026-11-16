package ease

// TokenType identifies the lexical kind of a token in Ease source code.
type TokenType string

const (
	TOK_EOF     TokenType = "EOF"
	TOK_NEWLINE TokenType = "NEWLINE"

	TOK_IDENT         TokenType = "IDENT"
	TOK_NUMBER        TokenType = "NUMBER"
	TOK_STRING        TokenType = "STRING"
	TOK_INTERP_STRING TokenType = "INTERP_STRING"

	// Keywords
	TOK_TO     TokenType = "to"
	TOK_IMPORT TokenType = "import"
	TOK_DEF    TokenType = "def"
	TOK_WITH   TokenType = "with"
	TOK_END    TokenType = "end"
	TOK_TEST   TokenType = "test"
	TOK_CHECK  TokenType = "check"
	TOK_IS     TokenType = "is"
	TOK_RAISES TokenType = "raises"
	TOK_DO     TokenType = "do"
	TOK_SAY    TokenType = "say"
	TOK_ASK    TokenType = "ask"
	TOK_AS     TokenType = "as"
	TOK_ELSE   TokenType = "else"
	TOK_REPEAT TokenType = "repeat"
	TOK_FOR    TokenType = "for"
	TOK_IN     TokenType = "in"
	TOK_AND    TokenType = "and"
	TOK_OR     TokenType = "or"
	TOK_NOT    TokenType = "not"

	TOK_TEXT      TokenType = "text"
	TOK_NUMBER_KW TokenType = "number"
	TOK_LIST_KW   TokenType = "list"

	// Punctuation & Operators
	TOK_ARROW  TokenType = "->"
	TOK_LPAREN TokenType = "("
	TOK_RPAREN TokenType = ")"
	TOK_COMMA  TokenType = ","
	TOK_DOT      TokenType = "."
	TOK_PIPE     TokenType = "|"
	TOK_ASSIGN   TokenType = "="

	TOK_LT  TokenType = "<"
	TOK_LTE TokenType = "<="
	TOK_GT  TokenType = ">"
	TOK_GTE TokenType = ">="

	TOK_PLUS  TokenType = "+"
	TOK_MINUS TokenType = "-"
	TOK_STAR  TokenType = "*"
	TOK_SLASH TokenType = "/"
	TOK_MOD   TokenType = "%"
)

var keywords = map[string]TokenType{
	"to":     TOK_TO,
	"import": TOK_IMPORT,
	"def":    TOK_DEF,
	"with":   TOK_WITH,
	"end":    TOK_END,
	"test":   TOK_TEST,
	"check":  TOK_CHECK,
	"is":     TOK_IS,
	"raises": TOK_RAISES,
	"do":     TOK_DO,
	"say":    TOK_SAY,
	"ask":    TOK_ASK,
	"as":     TOK_AS,
	"else":   TOK_ELSE,
	"repeat": TOK_REPEAT,
	"for":    TOK_FOR,
	"in":     TOK_IN,
	"and":    TOK_AND,
	"or":     TOK_OR,
	"not":    TOK_NOT,
	"text":   TOK_TEXT,
	"number": TOK_NUMBER_KW,
	"list":   TOK_LIST_KW,
}

// StringSegment represents either literal text or an embedded `{expr}` slice inside an interpolated string.
type StringSegment struct {
	IsExpr bool
	Text   string
	Line   int
	Col    int
}

// Token represents one lexical unit with 1-based source coordinates.
type Token struct {
	Type     TokenType
	Lexeme   string
	Line     int
	Col      int
	Segments []StringSegment
}
