package ease

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type dapMessage struct {
	Seq       int             `json:"seq"`
	Type      string          `json:"type"`
	Command   string          `json:"command,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type dapResponse struct {
	Seq        int    `json:"seq"`
	Type       string `json:"type"`
	RequestSeq int    `json:"request_seq"`
	Success    bool   `json:"success"`
	Command    string `json:"command"`
	Message    string `json:"message,omitempty"`
	Body       any    `json:"body,omitempty"`
}

type dapEvent struct {
	Seq   int    `json:"seq"`
	Type  string `json:"type"`
	Event string `json:"event"`
	Body  any    `json:"body,omitempty"`
}

type dapOutputWriter struct {
	server *DAPServer
	cat    string
}

func (w *dapOutputWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.server.sendEvent("output", map[string]any{
		"category": w.cat,
		"output":   string(p),
	})
	return len(p), nil
}

// DAPServer implements the Debug Adapter Protocol over stdio for Ease programs.
type DAPServer struct {
	in          *bufio.Reader
	out         io.Writer
	outMu       sync.Mutex
	seq         int
	ctrl        *DebugController
	programPath string
	programArgs []string
	stopOnEntry bool
	launched    bool
	doneCh      chan struct{}
}

// NewDAPServer creates a DAPServer reading frames from r and writing frames to w.
func NewDAPServer(r io.Reader, w io.Writer) *DAPServer {
	s := &DAPServer{
		in:     bufio.NewReader(r),
		out:    w,
		doneCh: make(chan struct{}),
	}
	return s
}

// Run processes incoming DAP requests until EOF or disconnect/terminate.
func (s *DAPServer) Run() error {
	for {
		payload, err := readLSPFrame(s.in)
		if err != nil {
			if errors.Is(err, io.EOF) {
				if s.ctrl != nil {
					s.ctrl.Abort()
				}
				return nil
			}
			return err
		}
		var msg dapMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			continue
		}
		if exit := s.handleMessage(&msg); exit {
			return nil
		}
	}
}

func (s *DAPServer) handleMessage(msg *dapMessage) (exit bool) {
	switch msg.Command {
	case "initialize":
		s.sendResponse(msg.Seq, msg.Command, true, "", map[string]any{
			"supportsConfigurationDoneRequest": true,
			"supportsTerminateRequest":         true,
			"supportsEvaluateForHovers":        true,
			"supportsRestartRequest":           false,
		})

	case "launch":
		var args struct {
			Program     string   `json:"program"`
			Args        []string `json:"args"`
			StopOnEntry bool     `json:"stopOnEntry"`
		}
		if err := json.Unmarshal(msg.Arguments, &args); err != nil || strings.TrimSpace(args.Program) == "" {
			s.sendResponse(msg.Seq, msg.Command, false, "Missing 'program' path in launch configuration", nil)
			return false
		}
		s.programPath = filepath.Clean(args.Program)
		s.programArgs = args.Args
		s.stopOnEntry = args.StopOnEntry
		s.ctrl = NewDebugController(args.StopOnEntry, func(reason string, threadID int) {
			s.sendEvent("stopped", map[string]any{
				"reason":            reason,
				"threadId":          threadID,
				"allThreadsStopped": true,
			})
		})
		s.sendResponse(msg.Seq, msg.Command, true, "", map[string]any{})
		s.sendEvent("initialized", map[string]any{})

	case "setBreakpoints":
		var args struct {
			Source struct {
				Path string `json:"path"`
			} `json:"source"`
			Breakpoints []struct {
				Line int `json:"line"`
			} `json:"breakpoints"`
		}
		if err := json.Unmarshal(msg.Arguments, &args); err != nil {
			s.sendResponse(msg.Seq, msg.Command, false, err.Error(), nil)
			return false
		}
		lines := make([]int, 0, len(args.Breakpoints))
		for _, bp := range args.Breakpoints {
			lines = append(lines, bp.Line)
		}
		var verified []int
		if s.ctrl != nil {
			verified = s.ctrl.SetBreakpoints(args.Source.Path, lines)
		}
		bps := make([]map[string]any, 0, len(verified))
		for _, l := range verified {
			bps = append(bps, map[string]any{
				"verified": true,
				"line":     l,
			})
		}
		s.sendResponse(msg.Seq, msg.Command, true, "", map[string]any{"breakpoints": bps})

	case "configurationDone":
		s.sendResponse(msg.Seq, msg.Command, true, "", map[string]any{})
		if !s.launched {
			s.launched = true
			go s.runProgram()
		}

	case "threads":
		s.sendResponse(msg.Seq, msg.Command, true, "", map[string]any{
			"threads": []map[string]any{
				{"id": 1, "name": "main"},
			},
		})

	case "stackTrace":
		frames := []map[string]any{}
		if s.ctrl != nil {
			for _, f := range s.ctrl.StackTrace() {
				srcName := filepath.Base(f.Source)
				if f.Source == "" {
					srcName = "<main>"
				}
				item := map[string]any{
					"id":     f.ID,
					"name":   f.Name,
					"line":   f.Line,
					"column": f.Col,
				}
				if f.Source != "" {
					item["source"] = map[string]any{
						"name": srcName,
						"path": f.Source,
					}
				}
				frames = append(frames, item)
			}
		}
		s.sendResponse(msg.Seq, msg.Command, true, "", map[string]any{
			"stackFrames": frames,
			"totalFrames": len(frames),
		})

	case "scopes":
		var args struct {
			FrameID int `json:"frameId"`
		}
		_ = json.Unmarshal(msg.Arguments, &args)
		scopes := []map[string]any{}
		if s.ctrl != nil {
			for _, sc := range s.ctrl.Scopes(args.FrameID) {
				scopes = append(scopes, map[string]any{
					"name":               sc.Name,
					"variablesReference": sc.VariablesReference,
					"expensive":          sc.Expensive,
				})
			}
		}
		s.sendResponse(msg.Seq, msg.Command, true, "", map[string]any{"scopes": scopes})

	case "variables":
		var args struct {
			VariablesReference int `json:"variablesReference"`
		}
		_ = json.Unmarshal(msg.Arguments, &args)
		vars := []map[string]any{}
		if s.ctrl != nil {
			for _, v := range s.ctrl.Variables(args.VariablesReference) {
				vars = append(vars, map[string]any{
					"name":               v.Name,
					"value":              v.Value,
					"type":               v.Type,
					"variablesReference": v.VariablesReference,
				})
			}
		}
		s.sendResponse(msg.Seq, msg.Command, true, "", map[string]any{"variables": vars})

	case "continue":
		s.sendResponse(msg.Seq, msg.Command, true, "", map[string]any{"allThreadsContinued": true})
		if s.ctrl != nil {
			s.ctrl.Continue()
		}

	case "next":
		s.sendResponse(msg.Seq, msg.Command, true, "", map[string]any{})
		if s.ctrl != nil {
			s.ctrl.StepOver()
		}

	case "stepIn":
		s.sendResponse(msg.Seq, msg.Command, true, "", map[string]any{})
		if s.ctrl != nil {
			s.ctrl.StepInto()
		}

	case "stepOut":
		s.sendResponse(msg.Seq, msg.Command, true, "", map[string]any{})
		if s.ctrl != nil {
			s.ctrl.StepOut()
		}

	case "pause":
		s.sendResponse(msg.Seq, msg.Command, true, "", map[string]any{})
		if s.ctrl != nil {
			s.ctrl.Pause()
		}

	case "evaluate":
		var args struct {
			Expression string `json:"expression"`
			FrameID    int    `json:"frameId"`
		}
		if err := json.Unmarshal(msg.Arguments, &args); err != nil {
			s.sendResponse(msg.Seq, msg.Command, false, err.Error(), nil)
			return false
		}
		if s.ctrl == nil {
			s.sendResponse(msg.Seq, msg.Command, false, "Debugger not active", nil)
			return false
		}
		dv, err := s.ctrl.EvaluateInFrame(args.FrameID, args.Expression)
		if err != nil {
			s.sendResponse(msg.Seq, msg.Command, false, err.Error(), nil)
			return false
		}
		s.sendResponse(msg.Seq, msg.Command, true, "", map[string]any{
			"result":             dv.Value,
			"type":               dv.Type,
			"variablesReference": dv.VariablesReference,
		})

	case "disconnect", "terminate":
		if s.ctrl != nil {
			s.ctrl.Abort()
		}
		s.sendResponse(msg.Seq, msg.Command, true, "", map[string]any{})
		return true

	default:
		s.sendResponse(msg.Seq, msg.Command, true, "", map[string]any{})
	}
	return false
}

func (s *DAPServer) runProgram() {
	defer close(s.doneCh)
	exitCode := 0
	raw, err := os.ReadFile(s.programPath)
	if err != nil {
		s.sendEvent("output", map[string]any{
			"category": "stderr",
			"output":   fmt.Sprintf("Cannot read %q: %v\n", s.programPath, err),
		})
		exitCode = 1
	} else {
		_, execErr := EvalProgramWithOptions(string(raw), s.programPath, EvalOptions{
			In:    strings.NewReader(""),
			Out:   &dapOutputWriter{server: s, cat: "stdout"},
			Args:  s.programArgs,
			Debug: s.ctrl,
		})
		if execErr != nil && !errors.Is(execErr, errDebugAborted) {
			var exitErr *ExitError
			if errors.As(execErr, &exitErr) {
				exitCode = exitErr.Code
				if exitErr.Message != "" {
					s.sendEvent("output", map[string]any{
						"category": "stderr",
						"output":   exitErr.Message + "\n",
					})
				}
			} else {
				exitCode = 1
				s.sendEvent("output", map[string]any{
					"category": "stderr",
					"output":   execErr.Error() + "\n",
				})
			}
		}
	}
	s.sendEvent("exited", map[string]any{"exitCode": exitCode})
	s.sendEvent("terminated", map[string]any{})
}

func (s *DAPServer) sendResponse(reqSeq int, command string, success bool, message string, body any) {
	s.outMu.Lock()
	s.seq++
	seq := s.seq
	s.outMu.Unlock()
	resp := dapResponse{
		Seq:        seq,
		Type:       "response",
		RequestSeq: reqSeq,
		Success:    success,
		Command:    command,
		Message:    message,
		Body:       body,
	}
	s.writeDAP(resp)
}

func (s *DAPServer) sendEvent(event string, body any) {
	s.outMu.Lock()
	s.seq++
	seq := s.seq
	s.outMu.Unlock()
	ev := dapEvent{
		Seq:   seq,
		Type:  "event",
		Event: event,
		Body:  body,
	}
	s.writeDAP(ev)
}

func (s *DAPServer) writeDAP(v any) {
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
