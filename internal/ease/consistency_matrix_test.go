package ease

import (
	"errors"
	"strings"
	"testing"
)

func TestStaticInvariantsAcrossAllContexts(t *testing.T) {
	type ctxCase struct {
		name string
		wrap func(setup, stmt string) string
	}
	contexts := []ctxCase{
		{
			name: "top_level",
			wrap: func(setup, stmt string) string {
				return setup + "\n" + stmt + "\n"
			},
		},
		{
			name: "inside_function",
			wrap: func(setup, stmt string) string {
				return setup + "\nto wrapper\n" + indentBlock(stmt, "  ") + "\nend\n"
			},
		},
		{
			name: "inside_loop",
			wrap: func(setup, stmt string) string {
				return setup + "\nloop range 1 2\n" + indentBlock(stmt, "  ") + "\nend\n"
			},
		},
		{
			name: "inside_swarm_loop",
			wrap: func(setup, stmt string) string {
				return setup + "\nout = swarm loop range 1 2\n" + indentBlock(stmt, "  ") + "\n  -> it\nend\n"
			},
		},
	}

	invariants := []struct {
		name    string
		setup   string
		stmt    string
		wantSub string
	}{
		{
			name:    "standalone_comparison",
			stmt:    `10 > 5`,
			wantSub: "Standalone condition has no effect",
		},
		{
			name:    "standalone_contains",
			stmt:    `contains (list 1, 2) 1`,
			wantSub: "Standalone condition has no effect",
		},
		{
			name:    "standalone_is",
			stmt:    `"a" is "a"`,
			wantSub: "Standalone condition has no effect",
		},
		{
			name:    "store_condition",
			stmt:    `x = 10 > 5`,
			wantSub: "Conditions cannot be stored or printed",
		},
		{
			name:    "print_condition",
			stmt:    `say 10 > 5`,
			wantSub: "Conditions cannot be stored or printed",
		},
		{
			name:    "void_save_as_value",
			stmt:    `x = save "./a.txt" "hi"`,
			wantSub: "does not return a value",
		},
		{
			name:    "void_close_as_value",
			setup:   "ch = inbox as number",
			stmt:    `x = close ch`,
			wantSub: "does not return a value",
		},
		{
			name:    "mixed_plus_coercion",
			stmt:    `x = "41" + 1`,
			wantSub: "Cannot add text and number",
		},
		{
			name:    "invalid_contains_elem_type",
			stmt:    "check contains (list \"a\", \"b\") 32\n  say \"x\"\nend",
			wantSub: "Cannot check 'contains' for number in list of text",
		},
		{
			name:    "call_site_param_mismatch",
			setup:   "to sum_it xs\n  -> sum xs\nend\nps = list \"a\", \"b\"",
			stmt:    `res = sum_it ps`,
			wantSub: "In call to 'sum_it'",
		},
		{
			name:    "record_field_typo",
			setup:   "def Pt\n  x = 0\nend\np = Pt with x = 1",
			stmt:    `say p.y`,
			wantSub: `Pt has no field "y"`,
		},
	}

	for _, inv := range invariants {
		for _, ctx := range contexts {
			// Note: `ch = inbox as number` / `close ch` inside `swarm loop` is tested
			// via top-level `ch` + `close ch` in worker or skipped if swarm blocks outer inbox;
			// `close ch` on top-level `ch` is valid to parse, so `x = close ch` must still fail.
			src := ctx.wrap(inv.setup, inv.stmt)
			t.Run(inv.name+"/"+ctx.name, func(t *testing.T) {
				_, err := ParseProgram(src)
				if err == nil {
					t.Fatalf("expected static TypeError containing %q, got nil for:\n%s", inv.wantSub, src)
				}
				var easeErr *EaseError
				if !errors.As(err, &easeErr) || easeErr.Stage != "TypeError" {
					t.Fatalf("expected EaseError(TypeError), got %v for:\n%s", err, src)
				}
				if !strings.Contains(err.Error(), inv.wantSub) {
					t.Fatalf("expected error containing %q, got:\n%v", inv.wantSub, err)
				}
			})
		}
	}
}

func indentBlock(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
