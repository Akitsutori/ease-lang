package ease

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"
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

func (s *LSPServer) handleMessage(msg *lspMessage) (exit bool) {
	defer func() {
		if r := recover(); r != nil {
			if msg != nil && len(msg.ID) > 0 && string(msg.ID) != "null" {
				s.sendResponse(msg.ID, nil, &lspRespError{
					Code:    -32603,
					Message: fmt.Sprintf("Internal error: %v", r),
				})
			}
			exit = false
		}
	}()
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
				"version": Version,
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
			symbols := DocumentSymbolsForURI(params.TextDocument.URI, s.getDoc(params.TextDocument.URI))
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
			items := CompletionItemsForURI(params.TextDocument.URI, s.getDoc(params.TextDocument.URI))
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

func fileURIToPath(uri string) string {
	if uri == "" {
		return ""
	}
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	p, err := url.PathUnescape(u.Path)
	if err != nil {
		p = u.Path
	}
	if u.Host != "" {
		p = "//" + u.Host + p
	} else if len(p) >= 3 && p[0] == '/' && ((p[1] >= 'A' && p[1] <= 'Z') || (p[1] >= 'a' && p[1] <= 'z')) && p[2] == ':' {
		// Strip leading slash before Windows drive letters (e.g. "/d:/work" -> "d:/work").
		p = p[1:]
	}
	return filepath.FromSlash(p)
}

func parseProgramForURI(uri, source string) (*Program, error) {
	if filePath := fileURIToPath(uri); filePath != "" {
		return ParseProgramFromFile(source, filePath)
	}
	return ParseProgram(source)
}

func (s *LSPServer) publishDiagnostics(uri, source string) {
	diags := DiagnoseSourceForURI(uri, source)
	s.sendNotification("textDocument/publishDiagnostics", map[string]any{
		"uri":         uri,
		"diagnostics": diags,
	})
}

// DiagnoseSource parses an Ease source string and converts any EaseError into LSP diagnostics.
func DiagnoseSource(source string) (diags []LSPDiagnostic) {
	return DiagnoseSourceForURI("", source)
}

// DiagnoseSourceForURI parses an Ease source string resolved relative to `uri` and converts any EaseError into LSP diagnostics.
func DiagnoseSourceForURI(uri, source string) (diags []LSPDiagnostic) {
	defer func() {
		if r := recover(); r != nil {
			diags = []LSPDiagnostic{
				{
					Range: LSPRange{
						Start: LSPPosition{Line: 0, Character: 0},
						End:   LSPPosition{Line: 0, Character: 1},
					},
					Severity: 1,
					Source:   "ease",
					Message:  fmt.Sprintf("Internal error: %v", r),
				},
			}
		}
	}()
	_, err := parseProgramForURI(uri, source)
	if err == nil {
		return []LSPDiagnostic{}
	}

	var easeErr *EaseError
	if errors.As(err, &easeErr) {
		line := max(0, easeErr.Line-1)
		runeCol := max(0, easeErr.Col-1)
		startChar, endChar := diagnosticUTF16Span(source, line, runeCol)
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
					Start: LSPPosition{Line: line, Character: startChar},
					End:   LSPPosition{Line: line, Character: endChar},
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

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

func runeColToUTF16(lineText string, runeCol int) int {
	if runeCol <= 0 {
		return 0
	}
	n := 0
	idx := 0
	for _, r := range lineText {
		if idx >= runeCol {
			break
		}
		n += utf16.RuneLen(r)
		idx++
	}
	return n
}

func diagnosticUTF16Span(source string, lineIdx, runeCol int) (int, int) {
	lines := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
	if lineIdx < 0 || lineIdx >= len(lines) {
		return runeCol, runeCol + 1
	}
	runes := []rune(lines[lineIdx])
	if runeCol >= len(runes) {
		u := utf16Len(lines[lineIdx])
		return u, u + 1
	}
	startUTF16 := runeColToUTF16(lines[lineIdx], runeCol)
	end := runeCol
	endUTF16 := startUTF16
	for end < len(runes) && runes[end] != ' ' && runes[end] != '\t' {
		endUTF16 += utf16.RuneLen(runes[end])
		end++
	}
	if end == runeCol {
		return startUTF16, startUTF16 + 1
	}
	return startUTF16, endUTF16
}

// DocumentSymbolsForSource extracts outline symbols (defs, unions, functions, checks) from Ease source.
// DocumentSymbolsForSource parses an Ease source string and returns top-level document symbols.
func DocumentSymbolsForSource(source string) (symbols []LSPDocumentSymbol) {
	return DocumentSymbolsForURI("", source)
}

// DocumentSymbolsForURI parses an Ease source string resolved relative to `uri` and returns top-level document symbols.
func DocumentSymbolsForURI(uri, source string) (symbols []LSPDocumentSymbol) {
	defer func() {
		if r := recover(); r != nil {
			symbols = []LSPDocumentSymbol{}
		}
	}()
	prog, err := parseProgramForURI(uri, source)
	if err != nil || prog == nil {
		return []LSPDocumentSymbol{}
	}

	lines := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
	symbols = make([]LSPDocumentSymbol, 0, len(prog.Defs)+len(prog.Unions)+len(prog.Functions)+len(prog.Checks))

	for _, d := range prog.Defs {
		r := symbolRange(lines, d.Line, d.Col, utf16Len(d.Name)+4)
		children := make([]LSPDocumentSymbol, 0, len(d.Fields))
		for _, f := range d.Fields {
			fr := symbolRange(lines, f.Line, f.Col, utf16Len(f.Name))
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
		r := symbolRange(lines, u.Line, u.Col, utf16Len(u.Name)+4)
		symbols = append(symbols, LSPDocumentSymbol{
			Name:           u.Name,
			Detail:         "union",
			Kind:           10, // Enum
			Range:          r,
			SelectionRange: r,
		})
	}

	for _, fn := range prog.Functions {
		r := symbolRange(lines, fn.Line, fn.Col, utf16Len(fn.Name)+3)
		symbols = append(symbols, LSPDocumentSymbol{
			Name:           fn.Name,
			Detail:         strings.Join(fn.Params, " "),
			Kind:           12, // Function
			Range:          r,
			SelectionRange: r,
		})
	}

	for _, chk := range prog.Checks {
		r := symbolRange(lines, chk.Line, chk.Col, utf16Len(chk.Title)+6)
		symbols = append(symbols, LSPDocumentSymbol{
			Name:           "check: " + chk.Title,
			Kind:           3, // Namespace
			Range:          r,
			SelectionRange: r,
		})
	}

	return symbols
}

func symbolRange(lines []string, line, col, utf16Length int) LSPRange {
	l := max(0, line-1)
	runeCol := max(0, col-1)
	startChar := runeCol
	if l >= 0 && l < len(lines) {
		startChar = runeColToUTF16(lines[l], runeCol)
	}
	if utf16Length < 1 {
		utf16Length = 1
	}
	return LSPRange{
		Start: LSPPosition{Line: l, Character: startChar},
		End:   LSPPosition{Line: l, Character: startChar + utf16Length},
	}
}

// CompletionItemsForSource returns Ease keywords plus declared defs/functions in the buffer.
// CompletionItemsForSource returns completion candidates for an Ease source string.
func CompletionItemsForSource(source string) (items []LSPCompletionItem) {
	return CompletionItemsForURI("", source)
}

// CompletionItemsForURI returns completion candidates for an Ease source string resolved relative to `uri`.
func CompletionItemsForURI(uri, source string) (items []LSPCompletionItem) {
	defer func() {
		if r := recover(); r != nil {
			items = []LSPCompletionItem{}
		}
	}()
	items = make([]LSPCompletionItem, 0, len(keywords)+16)
	for kw := range keywords {
		items = append(items, LSPCompletionItem{
			Label:  kw,
			Kind:   14, // Keyword
			Detail: "keyword",
		})
	}

	if prog, err := parseProgramForURI(uri, source); err == nil && prog != nil {
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

const maxLSPContentLength = 16 * 1024 * 1024

func readLSPFrame(r *bufio.Reader) ([]byte, error) {
	var contentLen int64 = -1
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
				n, convErr := strconv.ParseInt(val, 10, 64)
				if convErr != nil || n < 0 {
					return nil, fmt.Errorf("invalid Content-Length header %q", val)
				}
				if n > maxLSPContentLength {
					return nil, fmt.Errorf("Content-Length %d exceeds limit (%d bytes)", n, maxLSPContentLength)
				}
				contentLen = n
			}
		}
	}
	if contentLen < 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}
	buf := make([]byte, int(contentLen))
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
