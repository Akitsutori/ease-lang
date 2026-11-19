package ease

import (
	"fmt"
	"math"
	"strconv"
)

const maxMatchSteps = 100000

// parseStrictEaseNumber parses a canonical Ease numeric string (`[-]?[0-9]+(\.[0-9]+)?`)
// without accepting whitespace, underscores, hex floats, `+`, `NaN`, or `Inf`.
func parseStrictEaseNumber(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	i := 0
	if s[i] == '-' {
		i++
		if i >= len(s) {
			return 0, false
		}
	}
	intDigits := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		intDigits++
		i++
	}
	if intDigits == 0 {
		return 0, false
	}
	if i < len(s) && s[i] == '.' {
		i++
		fracDigits := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			fracDigits++
			i++
		}
		if fracDigits == 0 {
			return 0, false
		}
	}
	if i != len(s) {
		return 0, false
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, false
	}
	if n == 0 {
		n = 0
	}
	return n, true
}

func castMatchCapture(raw, castType string, env *Env) (Value, bool) {
	switch castType {
	case "", "text":
		return raw, true
	case "number":
		n, ok := parseStrictEaseNumber(raw)
		if !ok {
			return nil, false
		}
		return n, true
	default:
		union, ok := lookupUnion(env, castType)
		if !ok || union == nil {
			return nil, false
		}
		for _, opt := range union.Values {
			switch o := opt.(type) {
			case string:
				if raw == o {
					return raw, true
				}
			case float64:
				if n, ok := parseStrictEaseNumber(raw); ok && ValuesEqual(n, o) {
					return n, true
				}
			case BaseTypeValue:
				switch string(o) {
				case "number":
					if n, ok := parseStrictEaseNumber(raw); ok {
						return n, true
					}
				case "text":
					return raw, true
				}
			}
		}
		return nil, false
	}
}

func matchRunesAt(haystack []rune, pos int, needle []rune) bool {
	if pos+len(needle) > len(haystack) {
		return false
	}
	for i, r := range needle {
		if haystack[pos+i] != r {
			return false
		}
	}
	return true
}

func matchPatternRunes(input string, pat MatchPattern, env *Env, source string) (map[string]Value, bool, error) {
	runes := []rune(input)
	segs := pat.Segments
	n := len(segs)

	// Precompute literal rune slices and minimum required trailing runes per segment index.
	litRunes := make([][]rune, n)
	minRem := make([]int, n+1)
	for i := n - 1; i >= 0; i-- {
		minRem[i] = minRem[i+1]
		switch segs[i].Kind {
		case MatchSegLiteral:
			lr := []rune(segs[i].Literal)
			litRunes[i] = lr
			minRem[i] += len(lr)
		case MatchSegCapture:
			if segs[i].FixedWidth > 0 {
				minRem[i] += segs[i].FixedWidth
			}
		}
	}

	if len(runes) < minRem[0] {
		return nil, false, nil
	}

	bindings := make(map[string]Value)
	steps := 0

	var matchFrom func(segIdx, pos int) (bool, bool)
	matchFrom = func(segIdx, pos int) (bool, bool) {
		steps++
		if steps > maxMatchSteps {
			return false, true
		}
		if segIdx == n {
			return pos == len(runes), false
		}
		if pos+minRem[segIdx] > len(runes) {
			return false, false
		}

		seg := segs[segIdx]
		switch seg.Kind {
		case MatchSegLiteral:
			lr := litRunes[segIdx]
			if !matchRunesAt(runes, pos, lr) {
				return false, false
			}
			return matchFrom(segIdx+1, pos+len(lr))

		case MatchSegWildcard:
			maxEnd := len(runes) - minRem[segIdx+1]
			startEnd := pos
			if segIdx == n-1 {
				// Trailing wildcard must reach the end of input.
				startEnd = maxEnd
			}
			var nextLit []rune
			hasNextLit := segIdx+1 < n && segs[segIdx+1].Kind == MatchSegLiteral
			if hasNextLit {
				nextLit = litRunes[segIdx+1]
			}
			for end := startEnd; end <= maxEnd; end++ {
				if hasNextLit && !matchRunesAt(runes, end, nextLit) {
					steps++
					if steps > maxMatchSteps {
						return false, true
					}
					continue
				}
				ok, hitLimit := matchFrom(segIdx+1, end)
				if hitLimit || ok {
					return ok, hitLimit
				}
			}
			return false, false

		case MatchSegCapture:
			if seg.FixedWidth > 0 {
				end := pos + seg.FixedWidth
				if end > len(runes) {
					return false, false
				}
				raw := string(runes[pos:end])
				val, ok := castMatchCapture(raw, seg.CastType, env)
				if !ok {
					return false, false
				}
				prevVal, alreadyBound := bindings[seg.VarName]
				if alreadyBound {
					if !ValuesEqual(prevVal, val) {
						return false, false
					}
				} else {
					bindings[seg.VarName] = val
				}
				matched, hitLimit := matchFrom(segIdx+1, end)
				if hitLimit || matched {
					return matched, hitLimit
				}
				if !alreadyBound {
					delete(bindings, seg.VarName)
				}
				return false, false
			}

			maxEnd := len(runes) - minRem[segIdx+1]
			startEnd := pos
			if segIdx == n-1 {
				// Trailing variable capture must consume the remainder of the string.
				startEnd = maxEnd
			}
			var nextLit []rune
			hasNextLit := segIdx+1 < n && segs[segIdx+1].Kind == MatchSegLiteral
			if hasNextLit {
				nextLit = litRunes[segIdx+1]
			}
			for end := startEnd; end <= maxEnd; end++ {
				if hasNextLit && !matchRunesAt(runes, end, nextLit) {
					steps++
					if steps > maxMatchSteps {
						return false, true
					}
					continue
				}
				raw := string(runes[pos:end])
				val, ok := castMatchCapture(raw, seg.CastType, env)
				if !ok {
					continue
				}
				prevVal, alreadyBound := bindings[seg.VarName]
				if alreadyBound {
					if !ValuesEqual(prevVal, val) {
						continue
					}
				} else {
					bindings[seg.VarName] = val
				}
				matched, hitLimit := matchFrom(segIdx+1, end)
				if hitLimit || matched {
					return matched, hitLimit
				}
				if !alreadyBound {
					delete(bindings, seg.VarName)
				}
			}
			return false, false
		}
		return false, false
	}

	ok, hitLimit := matchFrom(0, 0)
	if hitLimit {
		return nil, false, newError(
			"RuntimeError",
			pat.Line,
			pat.Col,
			source,
			"Pattern match exceeded step limit",
			fmt.Sprintf("Simplify pattern %q by adding literal delimiters or fixed ': N' widths.", pat.Raw),
		)
	}
	if !ok {
		return nil, false, nil
	}
	return bindings, true, nil
}
