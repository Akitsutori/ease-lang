package ease

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
)

const maxSafeEaseInt = 9007199254740992 // 2^53 (exact IEEE-754 integer range)

func parseRandomIntBound(v Value, fnName, label string, line, col int, src string) (int64, error) {
	n, ok := v.(float64)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n {
		return 0, newError(
			"RuntimeError",
			line,
			col,
			src,
			fmt.Sprintf("%s requires integer bounds with low <= high (%s got %s)", fnName, label, TypeName(v)),
			"Pass whole numbers for both low and high bounds, e.g. 'random:roll 1 6'.",
		)
	}
	if n < -maxSafeEaseInt || n > maxSafeEaseInt {
		return 0, newError(
			"RuntimeError",
			line,
			col,
			src,
			fmt.Sprintf("%s bound %s is outside the safe integer range", fnName, label),
			"Keep integer bounds within +/-9007199254740992.",
		)
	}
	return int64(n), nil
}

func (ev *Evaluator) execRandRoll(args []Value, line, col int) (Value, error) {
	low, err := parseRandomIntBound(args[0], "random:roll", "low", line, col, ev.source)
	if err != nil {
		return nil, err
	}
	high, err := parseRandomIntBound(args[1], "random:roll", "high", line, col, ev.source)
	if err != nil {
		return nil, err
	}
	if low > high {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("random:roll requires integer bounds with low <= high (got %d > %d)", low, high),
			"Swap the bounds so low is less than or equal to high.",
		)
	}
	if low == high {
		return float64(low), nil
	}
	span := uint64(high-low) + 1
	offset := rand.Uint64N(span)
	return float64(low + int64(offset)), nil
}

func (ev *Evaluator) execRandPick(args []Value, line, col int) (Value, error) {
	lv, ok := args[0].(*ListValue)
	if !ok {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("random:pick requires a list, got %s", TypeName(args[0])),
			"Pass a non-empty list to 'random:pick items'.",
		)
	}
	if len(lv.Elements) == 0 {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			"random:pick requires a non-empty list",
			"Check 'count items > 0' before calling 'random:pick items'.",
		)
	}
	return lv.Elements[rand.IntN(len(lv.Elements))], nil
}

func (ev *Evaluator) execRandShuffle(args []Value, line, col int) (Value, error) {
	lv, ok := args[0].(*ListValue)
	if !ok {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("random:shuffle requires a list, got %s", TypeName(args[0])),
			"Pass a list to 'random:shuffle items'.",
		)
	}
	copied := slices.Clone(lv.Elements)
	rand.Shuffle(len(copied), func(i, j int) {
		copied[i], copied[j] = copied[j], copied[i]
	})
	return &ListValue{
		ElemType: lv.ElemType,
		Elements: copied,
	}, nil
}
