package ease

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSwarmLoopParallelOrderingGuardsAndIO(t *testing.T) {
	t.Run("parallel execution with inverse latencies preserves 0..N-1 order and say output", func(t *testing.T) {
		var inflight atomic.Int64
		var maxInflight atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cur := inflight.Add(1)
			for {
				prev := maxInflight.Load()
				if cur <= prev || maxInflight.CompareAndSwap(prev, cur) {
					break
				}
			}
			defer inflight.Add(-1)

			id := strings.TrimPrefix(r.URL.Path, "/item/")
			switch id {
			case "0":
				time.Sleep(45 * time.Millisecond)
			case "1":
				time.Sleep(25 * time.Millisecond)
			case "2":
				time.Sleep(10 * time.Millisecond)
			default:
				time.Sleep(2 * time.Millisecond)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id": %s, "val": %d}`, id, len(id)*10)
		}))
		defer srv.Close()

		src := fmt.Sprintf(`
import network

def ItemResp
  id  = 0
  val = 0
end

client = network.network with url = %q, timeout = 5

to fetch_item idx
  raw = network:fetch client "/item/{idx}"
  (decode_json raw) as ItemResp
end

items = swarm loop range 0 3
  say "say:{it}"
  fetch_item it
end

say "count={count items}"
say "ids={items.0.id},{items.1.id},{items.2.id},{items.3.id}"
`, srv.URL)

		var out bytes.Buffer
		if _, err := EvalProgram(src, &out); err != nil {
			t.Fatalf("EvalProgram failed: %v", err)
		}
		if maxInflight.Load() < 2 {
			t.Fatalf("expected concurrent HTTP requests in swarm loop, got maxInflight=%d", maxInflight.Load())
		}
		want := "say:0\nsay:1\nsay:2\nsay:3\ncount=4\nids=0,1,2,3\n"
		if out.String() != want {
			t.Fatalf("unexpected ordered output:\ngot:\n%q\nwant:\n%q", out.String(), want)
		}
	})

	t.Run("check guards, implicit return in to, nested swarm, inner sequential loop, and empty/single fast paths", func(t *testing.T) {
		src := `
def Order
  id     = 0
  amount = 0
end

to calc_shipping orders
  swarm loop orders
    check it.amount >= 50 -> 0
    check
      it.amount <= 0 -> fail "invalid amount"
    end
    5.90
  end
end

to nested_sums n
  swarm loop range 1 n
    row = swarm loop range 1 3
      it * 10
    end
    sum row + it
  end
end

to inner_seq_loop items
  swarm loop items
    acc = 0
    loop range 1 it
      check it > 3 -> end
      acc = acc + it
    end
    acc
  end
end

test "guards, nesting, and fast paths"
  orders = list as Order with
    id = 1, amount = 20
    id = 2, amount = 75
    id = 3, amount = 50
  end
  costs = calc_shipping orders
  costs is list 5.9, 0, 0

  # Parenthesized expression + postfix where
  big = (swarm loop range 1 5
    it * it
  end) where it > 10
  big is list 16, 25

  # Empty and single-element fast paths
  empty_res = swarm loop (list as number)
    it + 1
  end
  count empty_res is 0

  one_res = swarm loop (list 7)
    check it is 7 -> 70
    0
  end
  one_res is list 70

  nested_sums 2 is list 61, 62
  inner_seq_loop (list 2, 5) is list 3, 6
end
`
		report, err := RunChecks(src)
		if err != nil {
			t.Fatalf("RunChecks failed: %v", err)
		}
		if report.Failed != 0 || report.Passed != 6 {
			t.Fatalf("unexpected check report: %+v", report)
		}
	})

	t.Run("deterministic error reports smallest failing it.index", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := strings.TrimPrefix(r.URL.Path, "/")
			if id == "2" {
				time.Sleep(35 * time.Millisecond)
			}
			fmt.Fprint(w, id)
		}))
		defer srv.Close()

		src := fmt.Sprintf(`
import network
client = network.network with url = %q, timeout = 5
res = swarm loop range 0 5
  txt = network:fetch client "/{it}"
  check txt is "4" -> fail "err-at-4"
  check txt is "2" -> fail "err-at-2"
  txt
end
`, srv.URL)
		_, err := EvalProgram(src, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "err-at-2") {
			t.Fatalf("expected deterministic error from smallest index (err-at-2), got: %v", err)
		}
	})

	t.Run("race detector stress test with append, frozen outer list, records, with, json, and functions", func(t *testing.T) {
		src := `
def Point
  x = 0
  label = ""
end

shared = list 10, 20

to transform n base_list
  xs = append base_list n
  p = Point with x = sum xs, label = "n={n}"
  raw = encode_json p
  (decode_json raw) as Point
end

out = swarm loop range 1 200
  local_xs = append shared it
  check it % 2 is 0 -> transform it local_xs
  Point with x = it, label = "odd"
end

say "{count out}|{out.0.x}|{out.1.label}|{count shared}"
`
		var out bytes.Buffer
		if _, err := EvalProgram(src, &out); err != nil {
			t.Fatalf("stress EvalProgram failed: %v", err)
		}
		if out.String() != "200|1|n=2|2\n" {
			t.Fatalf("unexpected stress output: %q", out.String())
		}
	})
}

func TestSwarmLoopStaticAndRuntimeErrors(t *testing.T) {
	errorCases := []struct {
		name    string
		src     string
		wantSub string
	}{
		{
			name:    "unbounded swarm loop rejected",
			src:     "xs = swarm loop\n  1\nend\n",
			wantSub: "'swarm loop' requires a list or 'range start end'",
		},
		{
			name:    "bare swarm loop as function arg rejected",
			src:     "total = sum swarm loop range 1 5\n  it\nend\n",
			wantSub: "Wrap 'swarm loop ... end' in parentheses",
		},
		{
			name:    "loop exit -> end rejected inside swarm loop",
			src:     "xs = swarm loop range 1 5\n  check it > 2 -> end\n  it\nend\n",
			wantSub: "'-> end' cannot be used inside 'swarm loop'",
		},
		{
			name:    "mutating outer variable inside swarm loop rejected statically",
			src:     "total = 0\nxs = swarm loop range 1 5\n  total = total + it\n  total\nend\n",
			wantSub: `Cannot modify outer variable "total" inside 'swarm loop'`,
		},
		{
			name:    "ask inside swarm loop rejected statically",
			src:     "xs = swarm loop range 1 3\n  ask \"name?\"\nend\n",
			wantSub: "'ask' cannot be used inside 'swarm loop'",
		},
		{
			name:    "mixed static return types rejected",
			src:     "xs = swarm loop range 1 3\n  check it is 1 -> \"one\"\n  it\nend\n",
			wantSub: "Mixed result types in 'swarm loop': expected text, got number",
		},
		{
			name:    "condition returned in swarm loop rejected",
			src:     "xs = swarm loop range 1 3\n  it > 1\nend\n",
			wantSub: "Conditions cannot be stored or printed",
		},
		{
			name: "downstream inferred record field check",
			src: `
def Item
  code = 0
end
res = swarm loop range 1 3
  Item with code = it
end
bad = res where it.missing is 1
`,
			wantSub: `Item has no field "missing"`,
		},
		{
			name: "downstream inferred text list rejected by sum",
			src: `
res = swarm loop range 1 3
  "{it}"
end
s = sum res
`,
			wantSub: "sum requires a list of numbers",
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

	// Dynamic mixed return types via untyped helper
	dynMixed := `
to id x
  x
end
res = swarm loop range 1 2
  check it is 1 -> id 10
  id "two"
end
`
	if _, err := EvalProgram(dynMixed, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "Mixed result types in 'swarm loop'") {
		t.Fatalf("expected runtime mixed result type error, got: %v", err)
	}
}
