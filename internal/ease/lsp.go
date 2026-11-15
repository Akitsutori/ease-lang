package ease

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// LSPPosition represents a zero-based line and character offset in a text document.
type LSPPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// LSPRange represents a start and end position in a text document.
type LSPRange struct {
	Start LSPPosition `json:"start"`
	End   LSPPosition `json:"end"`
}

// LSPDiagnostic represents an Ease syntax or semantic error reported to an LSP client.
type LSPDiagnostic struct {
	Range    LSPRange `json:"range"`
	Severity int      `json:"severity"` // 1 = Error, 2 = Warning, 3 = Info, 4 = Hint
	Source   string   `json:"source"`
	Message  string   `json:"message"`
}

// LSPDocumentSymbol represents an outline symbol (function, record, union, check).
type LSPDocumentSymbol struct {
	Name           string              `json:"name"`
	Detail         string              `json:"detail,omitempty"`
	Kind           int                 `json:"kind"` // 5=Class, 12=Function, 13=Variable, 14=Constant, 22=Struct
	Range          LSPRange            `json:"range"`
	SelectionRange LSPRange            `json:"selectionRange"`
	Children       []LSPDocumentSymbol `json:"children,omitempty"`
}

// LSPCompletionItem represents one autocomplete suggestion.
type LSPCompletionItem struct {
	Label  string `json:"label"`
	Kind   int    `json:"kind"` // 3=Function, 14=Keyword, 22=Struct
	Detail string `json:"detail,omitempty"`
}

type lspMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type lspResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *lspRespError   `json:"error,omitempty"`
}

type lspRespError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type lspNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// LSPServer implements a stdio JSON-RPC 2.0 Language Server for Ease.
type LSPServer struct {
	in       *bufio.Reader
	out      io.Writer
	outMu    sync.Mutex
	docsMu   sync.RWMutex
	docs     map[string]string
	shutdown bool
}

// NewLSPServer creates a new Ease Language Server reading from r and writing to w.
func NewLSPServer(r io.Reader, w io.Writer) *LSPServer {
	return &LSPServer{
		in:   bufio.NewReader(r),
		out:  w,
		docs: make(map[string]string),
	}
}

// Run processes incoming LSP messages until EOF or an exit notification.
func (s *LSPServer) Run() error {
	for {
		payload, err := readLSPFrame(s.in)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		var msg lspMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			continue
		}

		if exit := s.handleMessage(&msg); exit {
			return nil
		}
	}
}

func (s *LSPServer) handleMessage(msg *lspMessage) bool {
	switch msg.Method {
	case "initialize":
		result := map[string]any{
			"capabilities": map[string]any{
				"textDocumentSync":       1, // Full document sync
				"documentSymbolProvider": true,
				"completionProvider": map[string]any{
					"triggerCharacters": []string{".", " "},
				},
			},
			"serverInfo": map[string]any{
				"name":    "ease-lsp",
				"version": "0.1.0",
			},
		}
		s.sendResponse(msg.ID, result, nil)

	case "initialized":
		// Nothing required.

	case "shutdown":
		s.shutdown = true
		s.sendResponse(msg.ID, nil, nil)

	case "exit":
		return true

	case "textDocument/didOpen":
		var params struct {
			TextDocument struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"textDocument"`
		}
		if err := json.Unmarshal(msg.Params, &params); err == nil {
			s.setDoc(params.TextDocument.URI, params.TextDocument.Text)
			s.publishDiagnostics(params.TextDocument.URI, params.TextDocument.Text)
		}

	case "textDocument/didChange":
		var params struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			ContentChanges []struct {
				Text string `json:"text"`
			} `json:"contentChanges"`
		}
		if err := json.Unmarshal(msg.Params, &params); err == nil && len(params.ContentChanges) > 0 {
			text := params.ContentChanges[len(params.ContentChanges)-1].Text
			s.setDoc(params.TextDocument.URI, text)
			s.publishDiagnostics(params.TextDocument.URI, text)
		}

	case "textDocument/didSave":
		var params struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			Text *string `json:"text,omitempty"`
		}
		if err := json.Unmarshal(msg.Params, &params); err == nil {
			text := s.getDoc(params.TextDocument.URI)
			if params.Text != nil {
				text = *params.Text
				s.setDoc(params.TextDocument.URI, text)
			}
			s.publishDiagnostics(params.TextDocument.URI, text)
		}

	case "textDocument/didClose":
		var params struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		if err := json.Unmarshal(msg.Params, &params); err == nil {
			s.deleteDoc(params.TextDocument.URI)
			s.sendNotification("textDocument/publishDiagnostics", map[string]any{
				"uri":         params.TextDocument.URI,
				"diagnostics": []LSPDiagnostic{},
			})
		}

	case "textDocument/documentSymbol":
		var params struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		if err := json.Unmarshal(msg.Params, &params); err == nil {
			symbols := DocumentSymbolsForSource(s.getDoc(params.TextDocument.URI))
			s.sendResponse(msg.ID, symbols, nil)
		} else {
			s.sendResponse(msg.ID, []LSPDocumentSymbol{}, nil)
		}

	case "textDocument/completion":
		var params struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		if err := json.Unmarshal(msg.Params, &params); err == nil {
			items := CompletionItemsForSource(s.getDoc(params.TextDocument.URI))
			s.sendResponse(msg.ID, items, nil)
		} else {
			s.sendResponse(msg.ID, []LSPCompletionItem{}, nil)
		}

	default:
		if len(msg.ID) > 0 && string(msg.ID) != "null" {
			s.sendResponse(msg.ID, nil, &lspRespError{
				Code:    -32601,
				Message: fmt.Sprintf("Method not found: %s", msg.Method),
			})
		}
	}

	return false
}

func (s *LSPServer) setDoc(uri, text string) {
	s.docsMu.Lock()
	defer s.docsMu.Unlock()
	s.docs[uri] = text
}

func (s *LSPServer) getDoc(uri string) string {
	s.docsMu.RLock()
	defer s.docsMu.RUnlock()
	return s.docs[uri]
}

func (s *LSPServer) deleteDoc(uri string) {
	s.docsMu.Lock()
	defer s.docsMu.Unlock()
	delete(s.docs, uri)
}

func (s *LSPServer) publishDiagnostics(uri, source string) {
	diags := DiagnoseSource(source)
	s.sendNotification("textDocument/publishDiagnostics", map[string]any{
		"uri":         uri,
		"diagnostics": diags,
	})
}

// DiagnoseSource parses an Ease source string and converts any EaseError into LSP diagnostics.
func DiagnoseSource(source string) []LSPDiagnostic {
	_, err := ParseProgram(source)
	if err == nil {
		return []LSPDiagnostic{}
	}

	var easeErr *EaseError
	if errors.As(err, &easeErr) {
		line := max(0, easeErr.Line-1)
		col := max(0, easeErr.Col-1)
		endCol := diagnosticEndCol(source, line, col)
		msg := easeErr.Message
		if easeErr.Hint != "" {
			msg += "\nHint: " + easeErr.Hint
		}
		src := easeErr.Stage
		if src == "" {
			src = "ease"
		}
		return []LSPDiagnostic{
			{
				Range: LSPRange{
					Start: LSPPosition{Line: line, Character: col},
					End:   LSPPosition{Line: line, Character: endCol},
				},
				Severity: 1,
				Source:   src,
				Message:  msg,
			},
		}
	}

	return []LSPDiagnostic{
		{
			Range: LSPRange{
				Start: LSPPosition{Line: 0, Character: 0},
				End:   LSPPosition{Line: 0, Character: 1},
			},
			Severity: 1,
			Source:   "ease",
			Message:  err.Error(),
		},
	}
}

func diagnosticEndCol(source string, lineIdx, colIdx int) int {
	lines := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
	if lineIdx < 0 || lineIdx >= len(lines) {
		return colIdx + 1
	}
	lineText := lines[lineIdx]
	if colIdx >= len(lineText) {
		return colIdx + 1
	}
	end := colIdx
	for end < len(lineText) && lineText[end] != ' ' && lineText[end] != '\t' {
		end++
	}
	if end == colIdx {
		return colIdx + 1
	}
	return end
}

// DocumentSymbolsForSource extracts outline symbols (defs, unions, functions, checks) from Ease source.
func DocumentSymbolsForSource(source string) []LSPDocumentSymbol {
	prog, err := ParseProgram(source)
	if err != nil || prog == nil {
		return []LSPDocumentSymbol{}
	}

	symbols := make([]LSPDocumentSymbol, 0, len(prog.Defs)+len(prog.Unions)+len(prog.Functions)+len(prog.Checks))

	for _, d := range prog.Defs {
		r := symbolRange(d.Line, d.Col, len(d.Name)+4)
		children := make([]LSPDocumentSymbol, 0, len(d.Fields))
		for _, f := range d.Fields {
			fr := symbolRange(f.Line, f.Col, len(f.Name))
			children = append(children, LSPDocumentSymbol{
				Name:           f.Name,
				Kind:           8, // Field
				Range:          fr,
				SelectionRange: fr,
			})
		}
		symbols = append(symbols, LSPDocumentSymbol{
			Name:           d.Name,
			Detail:         "def",
			Kind:           23, // Struct
			Range:          r,
			SelectionRange: r,
			Children:       children,
		})
	}

	for _, u := range prog.Unions {
		r := symbolRange(u.Line, u.Col, len(u.Name)+4)
		symbols = append(symbols, LSPDocumentSymbol{
			Name:           u.Name,
			Detail:         "union",
			Kind:           10, // Enum
			Range:          r,
			SelectionRange: r,
		})
	}

	for _, fn := range prog.Functions {
		r := symbolRange(fn.Line, fn.Col, len(fn.Name)+3)
		symbols = append(symbols, LSPDocumentSymbol{
			Name:           fn.Name,
			Detail:         strings.Join(fn.Params, " "),
			Kind:           12, // Function
			Range:          r,
			SelectionRange: r,
		})
	}

	for _, chk := range prog.Checks {
		r := symbolRange(chk.Line, chk.Col, len(chk.Title)+6)
		symbols = append(symbols, LSPDocumentSymbol{
			Name:           "check: " + chk.Title,
			Kind:           3, // Namespace
			Range:          r,
			SelectionRange: r,
		})
	}

	return symbols
}

func symbolRange(line, col, length int) LSPRange {
	l := max(0, line-1)
	c := max(0, col-1)
	if length < 1 {
		length = 1
	}
	return LSPRange{
		Start: LSPPosition{Line: l, Character: c},
		End:   LSPPosition{Line: l, Character: c + length},
	}
}

// CompletionItemsForSource returns Ease keywords plus declared defs/functions in the buffer.
func CompletionItemsForSource(source string) []LSPCompletionItem {
	items := make([]LSPCompletionItem, 0, len(keywords)+16)
	for kw := range keywords {
		items = append(items, LSPCompletionItem{
			Label:  kw,
			Kind:   14, // Keyword
			Detail: "keyword",
		})
	}

	if prog, err := ParseProgram(source); err == nil && prog != nil {
		for _, d := range prog.Defs {
			items = append(items, LSPCompletionItem{
				Label:  d.Name,
				Kind:   22, // Struct
				Detail: "def " + d.Name,
			})
		}
		for _, u := range prog.Unions {
			items = append(items, LSPCompletionItem{
				Label:  u.Name,
				Kind:   13, // Enum
				Detail: "union " + u.Name,
			})
		}
		for _, fn := range prog.Functions {
			items = append(items, LSPCompletionItem{
				Label:  fn.Name,
				Kind:   3, // Function
				Detail: "to " + fn.Name + " " + strings.Join(fn.Params, " "),
			})
		}
	}

	return items
}

func (s *LSPServer) sendResponse(id json.RawMessage, result any, respErr *lspRespError) {
	if len(id) == 0 {
		return
	}
	resp := lspResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
		Error:   respErr,
	}
	s.writePayload(resp)
}

func (s *LSPServer) sendNotification(method string, params any) {
	s.writePayload(lspNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
}

func (s *LSPServer) writePayload(v any) {
	body, err := json.Marshal(v)
	if err != nil {
		return
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_, _ = io.WriteString(s.out, header)
	_, _ = s.out.Write(body)
}

func readLSPFrame(r *bufio.Reader) ([]byte, error) {
	contentLen := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			break
		}
		if colon := strings.IndexByte(trimmed, ':'); colon > 0 {
			name := strings.ToLower(strings.TrimSpace(trimmed[:colon]))
			val := strings.TrimSpace(trimmed[colon+1:])
			if name == "content-length" {
				n, convErr := strconv.Atoi(val)
				if convErr == nil && n >= 0 {
					contentLen = n
				}
			}
		}
	}
	if contentLen < 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}
	buf := make([]byte, contentLen)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
