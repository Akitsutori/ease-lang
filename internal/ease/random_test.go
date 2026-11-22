package ease

import (
	"bytes"
	"strings"
	"testing"
)

func TestRandomRollBoundsAndErrors(t *testing.T) {
	src := `import random

test "random:roll covers inclusive integer bounds including negatives and equality"
    random:roll 5 5 is 5
    random:roll -3 -3 is -3

    loop range 1 50
        v = random:roll 1 6
        v >= 1
        v <= 6
        (floor v) is v
    end

    loop range 1 30
        n = random:roll -5 2
        n >= -5
        n <= 2
        (floor n) is n
    end

    (random:roll 10 3) is error
    (random:roll 1.5 6) is error
    (random:roll 1 4.2) is error
    (random:roll "a" 5) is error
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed == 0 {
		t.Fatalf("unexpected report: passed=%d failed=%d", report.Passed, report.Failed)
	}

	var out bytes.Buffer
	if _, err := EvalProgram("import random\nsay random:roll 10 2\n", &out); err == nil || !strings.Contains(err.Error(), "low <= high") {
		t.Fatalf("expected low <= high RuntimeError, got %v", err)
	}
	if _, err := EvalProgram("import random\nsay random:roll 1.5 5\n", &out); err == nil || !strings.Contains(err.Error(), "integer bounds") {
		t.Fatalf("expected integer bounds RuntimeError, got %v", err)
	}
}

func TestRandomPickAndShuffleImmutabilityAndErrors(t *testing.T) {
	src := `import random

test "random:pick and immutable random:shuffle"
    items = list "alpha", "beta", "gamma", "delta"
    picked = random:pick items
    picked is text

    original_snapshot = encode_json items
    shuffled = random:shuffle items

    encode_json items is original_snapshot
    count shuffled is 4
    encode_json (sort shuffled) is encode_json (sort items)

    empty = list as number
    count (random:shuffle empty) is 0
    (random:pick empty) is error
    (random:pick 42) is error
    (random:shuffle "abc") is error
end
`
	report, err := RunChecks(src)
	if err != nil {
		t.Fatalf("RunChecks failed: %v", err)
	}
	if report.Failed != 0 || report.Passed == 0 {
		t.Fatalf("unexpected report: passed=%d failed=%d", report.Passed, report.Failed)
	}
}

func TestRandomStaticTypeInferenceAndAlias(t *testing.T) {
	goodSrc := `import random as r

def card
    suit  = "hearts" | "spades"
    value = 0
end

deck = list as card with
    suit = "hearts", value = 10
    suit = "spades", value = 11
end

chosen = r:pick deck
say chosen.suit

mixed = r:shuffle deck
say mixed.1.value

nums = r:shuffle (list 1, 2, 3)
rolled = r:roll 1 10
say "{sum nums}|{rolled + chosen.value}"
`
	var out bytes.Buffer
	if _, err := EvalProgram(goodSrc, &out); err != nil {
		t.Fatalf("expected valid aliased random usage to pass, got: %v", err)
	}

	errCases := []struct {
		name    string
		src     string
		wantSub string
	}{
		{
			name: "pick infers record type and catches missing field statically",
			src: `import random
def card
    value = 0
end
deck = list (card with value = 1)
c = random:pick deck
say c.missing
`,
			wantSub: `card has no field "missing"`,
		},
		{
			name: "shuffle preserves list element record type and catches missing field",
			src: `import random as r
def card
    value = 0
end
deck = list (card with value = 1)
mixed = r:shuffle deck
say mixed.1.missing
`,
			wantSub: `card has no field "missing"`,
		},
		{
			name: "shuffle preserves non-numeric element type for sum check",
			src: `import random
def card
    value = 0
end
deck = list (card with value = 1)
mixed = random:shuffle deck
say sum mixed
`,
			wantSub: "sum requires a list of numbers",
		},
	}

	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProgram(tc.src)
			if err == nil {
				t.Fatalf("expected static TypeError containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantSub, err)
			}
		})
	}
}
