package ease

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSpawnAndInboxProducerConsumer(t *testing.T) {
	t.Run("producer-consumer with record inbox, take it/it.index, and close", func(t *testing.T) {
		src := `
def ClientEvent
  kind = "join" | "msg"
  user = ""
  body = ""
end

to produce chat
  put chat (ClientEvent with kind = "join", user = "Ada", body = "hi")
  put chat (ClientEvent with kind = "msg", user = "Ada", body = "hello")
  close chat
end

chat = inbox as ClientEvent
spawn produce chat

seen = 0
take chat
  say "{it.index}:{it.kind}:{it.user}:{it.body}"
  seen = seen + 1
end
say "done:{seen}"
`
		var out bytes.Buffer
		if _, err := EvalProgram(src, &out); err != nil {
			t.Fatalf("EvalProgram failed: %v", err)
		}
		want := "1:join:Ada:hi\n2:msg:Ada:hello\ndone:2\n"
		if out.String() != want {
			t.Fatalf("unexpected output:\ngot:\n%q\nwant:\n%q", out.String(), want)
		}
	})

	t.Run("multiple spawn producers and early exit via check -> end in take", func(t *testing.T) {
		src := `
to worker ch start count_n
  loop range start (start + count_n - 1)
    put ch it
  end
end

ch = inbox as number
w1 = spawn worker ch 1 5
# Wait: spawn is a statement, launch two producers and close after waiting via a coordinator or second inbox
`
		_ = src
		src2 := `
to worker ch done start count_n
  loop range start (start + count_n - 1)
    put ch it
  end
  put done 1
end

to coordinator ch done
  finished = 0
  take done
    finished = finished + it
    check finished >= 2
      close ch
    end
  end
end

ch = inbox as number
done = inbox as number
spawn worker ch done 1 5
spawn worker ch done 10 5
spawn coordinator ch done

total = 0
count_seen = 0
take ch
  total = total + it
  count_seen = count_seen + 1
  check count_seen >= 10 -> stop
end
close done
say "{count_seen}|{total}"
`
		var out bytes.Buffer
		if _, err := EvalProgram(src2, &out); err != nil {
			t.Fatalf("EvalProgram failed: %v", err)
		}
		if out.String() != "10|75\n" {
			t.Fatalf("unexpected output: got %q, want %q", out.String(), "10|75\n")
		}
	})

	t.Run("nested worker pipeline (jobs + results), idempotent close, and value freeze on put", func(t *testing.T) {
		src := `
def Job
  id   = 0
  nums = list as number
end

def Result
  id  = 0
  sum = 0
end

to worker_loop jobs results done
  take jobs
    s = sum it.nums
    put results (Result with id = it.id, sum = s)
  end
  put done 1
end

to close_when_done done results n
  cnt = 0
  take done
    cnt = cnt + it
    check cnt >= n -> stop
  end
  close results
end

jobs = inbox as Job
results = inbox as Result
done = inbox as number

spawn worker_loop jobs results done
spawn worker_loop jobs results done
spawn close_when_done done results 2

base = list 1, 2
put jobs (Job with id = 1, nums = append base 3)
put jobs (Job with id = 2, nums = append base 10)
# Mutating/appending base after put must not affect sent jobs
base2 = append base 999
close jobs
close jobs

total_sum = 0
take results
  total_sum = total_sum + it.sum
end
close done
say "{total_sum}|{count base2}"
`
		var out bytes.Buffer
		if _, err := EvalProgram(src, &out); err != nil {
			t.Fatalf("EvalProgram failed: %v", err)
		}
		if out.String() != "19|3\n" {
			t.Fatalf("unexpected output: got %q, want %q", out.String(), "19|3\n")
		}
	})

	t.Run("spawn module function, worker error aborts blocked take, and stress test without goroutine leaks", func(t *testing.T) {
		dir := t.TempDir()
		modPath := filepath.Join(dir, "worker_mod.ease")
		mainPath := filepath.Join(dir, "main.ease")
		if err := os.WriteFile(modPath, []byte(`
to emit ch count_n
  loop range 1 count_n
    put ch it
  end
  close ch
end
`), 0o644); err != nil {
			t.Fatal(err)
		}
		mainSrc := `
import "./worker_mod.ease" as wm

ch = inbox as number
spawn wm:emit ch 100
total = 0
take ch
  total = total + it
end
say total
`
		if err := os.WriteFile(mainPath, []byte(mainSrc), 0o644); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if _, err := EvalProgramFromFile(mainSrc, mainPath, &out); err != nil {
			t.Fatalf("EvalProgramFromFile failed: %v", err)
		}
		if out.String() != "5050\n" {
			t.Fatalf("unexpected output: %q", out.String())
		}

		// Worker error unblocks main goroutine waiting in `take`
		errSrc := `
to failing_worker ch
  put ch 1
  fail "worker-boom"
end

ch = inbox as number
spawn failing_worker ch
take ch
  say it
end
`
		_, err := EvalProgram(errSrc, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "worker-boom") {
			t.Fatalf("expected worker-boom error, got: %v", err)
		}

		// Stress test & goroutine leak check on ev.Close()
		beforeG := runtime.NumGoroutine()
		stressSrc := `
to sender ch done n
  loop range 1 n
    put ch it
  end
  put done 1
end

to close_after done ch total_workers
  cnt = 0
  take done
    cnt = cnt + it
    check cnt >= total_workers -> stop
  end
  close ch
end

ch = inbox as number
done = inbox as number
loop range 1 16
  spawn sender ch done 50
end
spawn close_after done ch 16

sum_all = 0
take ch
  sum_all = sum_all + it
end
close done
say sum_all
`
		var stressOut bytes.Buffer
		if _, err := EvalProgram(stressSrc, &stressOut); err != nil {
			t.Fatalf("stress EvalProgram failed: %v", err)
		}
		if stressOut.String() != "20400\n" {
			t.Fatalf("unexpected stress output: got %q, want %q", stressOut.String(), "20400\n")
		}

		// Blocked take + ev.Close() cleans up goroutines cleanly
		blockedSrc := `
to blocked_take ch
  take ch
    say it
  end
end
ch = inbox as number
spawn blocked_take ch
`
		prog, err := ParseProgram(blockedSrc)
		if err != nil {
			t.Fatal(err)
		}
		ev := NewEvaluator(blockedSrc, prog, &bytes.Buffer{})
		if _, err := ev.ExecTopLevel(prog.Body); err != nil {
			ev.Close()
			t.Fatal(err)
		}
		ev.Close()

		time.Sleep(20 * time.Millisecond)
		afterG := runtime.NumGoroutine()
		if afterG > beforeG+4 {
			t.Fatalf("possible goroutine leak: before=%d after=%d", beforeG, afterG)
		}
	})
}

func TestSpawnAndInboxStaticAndRuntimeErrors(t *testing.T) {
	errorCases := []struct {
		name    string
		src     string
		wantSub string
	}{
		{
			name:    "spawn non-call expression rejected",
			src:     "spawn 1 + 2\n",
			wantSub: "'spawn' requires a function call",
		},
		{
			name:    "spawn say rejected",
			src:     "spawn say \"hi\"\n",
			wantSub: "'spawn' requires a function call",
		},
		{
			name:    "inbox missing as rejected",
			src:     "ch = inbox number\n",
			wantSub: "Expected 'as <type>' after 'inbox'",
		},
		{
			name:    "inbox bool rejected",
			src:     "ch = inbox as bool\n",
			wantSub: `Boolean ("bool") is not a built-in type`,
		},
		{
			name:    "inbox csv rejected",
			src:     "ch = inbox as csv\n",
			wantSub: `Type "csv" cannot be used with 'inbox as'`,
		},
		{
			name:    "put wrong type rejected statically",
			src:     "ch = inbox as number\nput ch \"oops\"\n",
			wantSub: "Cannot 'put' text into inbox of number",
		},
		{
			name:    "put wrong union literal rejected statically",
			src:     "def Mode = \"fast\" | \"safe\"\nch = inbox as Mode\nput ch \"turbo\"\n",
			wantSub: `Value "turbo" is not valid for inbox union "Mode"`,
		},
		{
			name:    "put into non-inbox rejected statically",
			src:     "xs = list 1, 2\nput xs 3\n",
			wantSub: "'put' requires an inbox as first argument",
		},
		{
			name:    "close on non-inbox rejected statically",
			src:     "x = 1\nclose x\n",
			wantSub: "'close' requires an inbox",
		},
		{
			name:    "take on non-inbox rejected statically",
			src:     "xs = list 1, 2\ntake xs\n  say it\nend\n",
			wantSub: "'take' requires an inbox",
		},
		{
			name: "take infers record field types statically",
			src: `
def Msg
  body = ""
end
ch = inbox as Msg
take ch
  say it.missing
end
`,
			wantSub: `Msg has no field "missing"`,
		},
	}

	for _, tc := range errorCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProgram(tc.src)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error containing %q, got:\n%v", tc.wantSub, err)
			}
		})
	}

	// Put into closed inbox returns RuntimeError (never panics)
	closedPutSrc := `
ch = inbox as number
close ch
put ch 1
`
	if _, err := EvalProgram(closedPutSrc, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Cannot 'put' into a closed inbox") {
		t.Fatalf("expected RuntimeError on put into closed inbox, got: %v", err)
	}

	// Dynamic put type mismatch
	dynPutMismatch := `
to send_any ch v
  put ch v
end
ch = inbox as number
send_any ch "bad"
`
	if _, err := EvalProgram(dynPutMismatch, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Cannot 'put' text into inbox of number") {
		t.Fatalf("expected TypeError on dynamic put mismatch, got: %v", err)
	}
}
