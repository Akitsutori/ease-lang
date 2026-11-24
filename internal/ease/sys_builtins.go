package ease

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// rollingBuffer is a thread-safe in-memory ring buffer that retains the last
// maxBytes written to it (zero temp-file disk I/O) and heals split leading
// UTF-8 continuation bytes if the front was truncated.
type rollingBuffer struct {
	mu       sync.Mutex
	maxBytes int
	buf      []byte
	pos      int
	wrapped  bool
}

func newRollingBuffer(maxBytes int) *rollingBuffer {
	if maxBytes < 64 {
		maxBytes = 64
	}
	return &rollingBuffer{maxBytes: maxBytes}
}

func (b *rollingBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n == 0 {
		return 0, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if n >= b.maxBytes {
		if len(b.buf)+n > b.maxBytes {
			b.wrapped = true
		}
		if len(b.buf) != b.maxBytes {
			b.buf = make([]byte, b.maxBytes)
		}
		copy(b.buf, p[n-b.maxBytes:])
		b.pos = 0
		return n, nil
	}

	if !b.wrapped {
		if len(b.buf)+n <= b.maxBytes {
			b.buf = append(b.buf, p...)
			return n, nil
		}
		b.wrapped = true
		drop := len(b.buf) + n - b.maxBytes
		ring := make([]byte, b.maxBytes)
		copied := copy(ring, b.buf[drop:])
		copy(ring[copied:], p)
		b.buf = ring
		b.pos = 0
		return n, nil
	}

	first := copy(b.buf[b.pos:], p)
	if first < n {
		copy(b.buf[0:], p[first:])
	}
	b.pos = (b.pos + n) % b.maxBytes
	return n, nil
}

func (b *rollingBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.wrapped {
		out := make([]byte, len(b.buf))
		copy(out, b.buf)
		return out
	}

	out := make([]byte, b.maxBytes)
	n := copy(out, b.buf[b.pos:])
	copy(out[n:], b.buf[:b.pos])

	// If the front was truncated mid-rune, skip up to 3 leading UTF-8
	// continuation bytes (0b10xxxxxx) so valid UTF-8 output stays valid.
	skip := 0
	for skip < 3 && skip < len(out) && (out[skip]&0xC0) == 0x80 {
		skip++
	}
	return out[skip:]
}

func (ev *Evaluator) execSysRun(args []Value, line, col int) (Value, error) {
	rec, ok := args[0].(*RecordValue)
	if !ok || rec.TypeName != "command" {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("system:run expects a 'system.command' record, got %s", TypeName(args[0])),
			"Construct a command with 'system.command with program = \"...\", args = list \"...\"'.",
		)
	}

	program, ok := rec.Fields["program"].(string)
	if !ok || strings.TrimSpace(program) == "" {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			"system:run requires a non-empty command.program",
			"Set 'program = \"...\"' on your system.command record.",
		)
	}

	rawArgs, ok := rec.Fields["args"].(*ListValue)
	if !ok {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			"system:run requires command.args to be a list of text",
			"Set 'args = list \"arg1\", \"arg2\"' (or 'list as text').",
		)
	}
	cmdArgs := make([]string, len(rawArgs.Elements))
	for i, el := range rawArgs.Elements {
		s, ok := el.(string)
		if !ok {
			return nil, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("system:run command.args element %d must be text, got %s", i+1, TypeName(el)),
				"Pass only text items in command.args.",
			)
		}
		cmdArgs[i] = s
	}

	dir, ok := rec.Fields["dir"].(string)
	if !ok {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			"system:run requires command.dir to be text",
			"Set 'dir = \"\"' or a directory path string.",
		)
	}

	timeoutSec, ok := rec.Fields["timeout"].(float64)
	if !ok || math.IsNaN(timeoutSec) || math.IsInf(timeoutSec, 0) || timeoutSec <= 0 {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			"system:run requires command.timeout > 0",
			"Set 'timeout' to a positive number of seconds (default: 30).",
		)
	}

	maxMB, ok := rec.Fields["max_mb"].(float64)
	if !ok || math.IsNaN(maxMB) || math.IsInf(maxMB, 0) || maxMB <= 0 || maxMB > 512 {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			"system:run requires command.max_mb in (0..512]",
			"Set 'max_mb' between 0 (exclusive) and 512 MB (default: 10).",
		)
	}

	maxBytes := int(math.Round(maxMB * 1024 * 1024))
	if maxBytes < 64 {
		maxBytes = 64
	}
	stdoutBuf := newRollingBuffer(maxBytes)
	stderrBuf := newRollingBuffer(maxBytes)

	parentCtx := context.Background()
	if ev.rt != nil && ev.rt.ctx != nil {
		parentCtx = ev.rt.ctx
	}
	timeoutDur := time.Duration(timeoutSec * float64(time.Second))
	if timeoutDur <= 0 {
		timeoutDur = time.Millisecond
	}
	ctx, cancel := context.WithTimeout(parentCtx, timeoutDur)
	defer cancel()

	execCmd := exec.CommandContext(ctx, program, cmdArgs...)
	if dir != "" {
		execCmd.Dir = dir
	}
	execCmd.Stdin = strings.NewReader("")
	execCmd.Stdout = stdoutBuf
	execCmd.Stderr = stderrBuf

	err := execCmd.Run()
	exitCode := 0
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("system:run timed out after %ss running %q", FormatValue(timeoutSec), program),
				"Increase command.timeout or inspect why the subprocess did not finish.",
			)
		}
		if parentCtx.Err() != nil {
			return nil, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				"system:run cancelled",
				"The parent Ease execution context was cancelled.",
			)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("system:run failed to execute %q: %v", program, err),
				"Verify that command.program exists, is executable, and command.dir is valid.",
			)
		}
	}

	outBytes := stdoutBuf.Bytes()
	if !utf8.Valid(outBytes) {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("system:run stdout from %q is not valid UTF-8", program),
			"Ensure the subprocess outputs valid UTF-8 text.",
		)
	}
	errBytes := stderrBuf.Bytes()
	if !utf8.Valid(errBytes) {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("system:run stderr from %q is not valid UTF-8", program),
			"Ensure the subprocess outputs valid UTF-8 text.",
		)
	}

	return &RecordValue{
		TypeName: "process_report",
		Fields: map[string]Value{
			"code":   float64(exitCode),
			"stdout": norm.NFC.String(string(outBytes)),
			"stderr": norm.NFC.String(string(errBytes)),
		},
		Order: []string{"code", "stdout", "stderr"},
	}, nil
}
