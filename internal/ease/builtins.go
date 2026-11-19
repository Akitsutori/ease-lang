package ease

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const maxFileSize = 50 * 1024 * 1024

// builtinArities defines the fixed parameter count for every built-in function.
var builtinArities = map[string]int{
	"ask":            1,
	"count":          1,
	"type":           1,
	"abs":            1,
	"floor":          1,
	"round":          1,
	"upper":          1,
	"lower":          1,
	"fail":           1,
	"fields":         1,
	"decode_json":    1,
	"encode_json":    1,
	"http_call":      1,
	"sum":            1,
	"avg":            1,
	"sort":           1,
	"trim":           1,
	"load":           1,
	"range":          2,
	"append":         2,
	"split":          2,
	"join":           2,
	"contains":       2,
	"save":           2,
	"put":            2,
	"close":          1,
	"min":            2,
	"max":            2,
	"screen_present": 2,
	"replace":        3,
	"slice":          3,
}

func (ev *Evaluator) callBuiltin(name string, args []Value, line, col int) (Value, bool, error) {
	switch name {
	case "ask":
		if ev.inSwarmWorker {
			return nil, true, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				"'ask' cannot be used inside 'swarm loop'",
				"Read interactive input before 'swarm loop', not inside parallel workers.",
			)
		}
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "ask requires 1 argument (prompt)", "Example: ask \"Name?\" or ask \"\"")
		}
		promptText := FormatValue(args[0])
		if promptText != "" {
			if !strings.HasSuffix(promptText, " ") {
				promptText += " "
			}
			fmt.Fprint(ev.out, promptText)
		}
		ev.logTrace("line %d: ask (waiting for stdin...)", line)
		inputLine, err := ev.inReader.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, true, newError("RuntimeError", line, col, ev.source, "Failed to read input: "+err.Error(), "")
		}
		if !utf8.ValidString(inputLine) {
			return nil, true, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				"Input from stdin contains invalid UTF-8 bytes",
				"Ensure standard input provides valid UTF-8 text.",
			)
		}
		res := norm.NFC.String(strings.TrimRight(inputLine, "\r\n"))
		ev.logTrace("line %d: ask received %q", line, res)
		return res, true, nil

	case "count":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "count takes 1 argument", "")
		}
		switch v := args[0].(type) {
		case *ListValue:
			return float64(len(v.Elements)), true, nil
		case string:
			return float64(len([]rune(v))), true, nil
		default:
			return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("count requires a list or text, got %s", TypeName(args[0])), "")
		}

	case "type":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "type() takes 1 argument", "")
		}
		return TypeName(args[0]), true, nil

	case "range":
		if len(args) != 2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "range(start, end) takes 2 arguments", "")
		}
		s, ok1 := args[0].(float64)
		e, ok2 := args[1].(float64)
		if !ok1 || !ok2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "range() requires two numbers", "")
		}
		startF, endF := math.Trunc(s), math.Trunc(e)
		if math.Abs(startF) > float64(math.MaxInt64/2) || math.Abs(endF) > float64(math.MaxInt64/2) {
			return nil, true, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("range too large (limit %d elements)", maxListSize),
				fmt.Sprintf("Keep 'range start end' within %d elements.", maxListSize),
			)
		}
		if endF >= startF && (endF-startF+1) > float64(maxListSize) {
			return nil, true, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("range too large (limit %d elements)", maxListSize),
				fmt.Sprintf("Keep 'range start end' within %d elements.", maxListSize),
			)
		}
		start, end := int(startF), int(endF)
		out := make([]Value, 0, max(0, end-start+1))
		for i := start; i <= end; i++ {
			out = append(out, float64(i))
		}
		return &ListValue{ElemType: "number", Elements: out}, true, nil

	case "append":
		if len(args) != 2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "append takes 2 arguments", "")
		}
		listVal, ok := args[0].(*ListValue)
		if !ok {
			return nil, true, newError("TypeError", line, col, ev.source, fmt.Sprintf("append expects a list as first argument, got %s", TypeName(args[0])), "")
		}
		if otherList, isList := args[1].(*ListValue); isList && listVal.ElemType != "list" {
			if listVal.ElemType != "" && otherList.ElemType != "" && listVal.ElemType != otherList.ElemType {
				return nil, true, newError(
					"TypeError",
					line,
					col,
					ev.source,
					fmt.Sprintf("Cannot append list of %s into list of %s", otherList.ElemType, listVal.ElemType),
					"Both lists in 'append' must have compatible element types.",
				)
			}
			elemType := listVal.ElemType
			if elemType == "" {
				elemType = otherList.ElemType
			}
			if elemType != "" && otherList.ElemType == "" {
				for _, el := range otherList.Elements {
					if !matchesListElemType(el, elemType, ev.global) {
						return nil, true, newError(
							"TypeError",
							line,
							col,
							ev.source,
							fmt.Sprintf("Cannot append %s into list of %s", structuredTypeName(el), elemType),
							fmt.Sprintf("This list only accepts elements of type %q.", elemType),
						)
					}
				}
			}
			if len(listVal.Elements)+len(otherList.Elements) > maxListSize {
				return nil, true, newError(
					"RuntimeError",
					line,
					col,
					ev.source,
					fmt.Sprintf("list size exceeds limit (%d elements)", maxListSize),
					fmt.Sprintf("Keep lists within %d elements.", maxListSize),
				)
			}
			var next []Value
			if !listVal.pushed {
				listVal.pushed = true
				next = append(listVal.Elements, otherList.Elements...)
			} else {
				newLen := len(listVal.Elements) + len(otherList.Elements)
				newCap := max(4, newLen)
				next = make([]Value, newLen, newCap)
				copy(next, listVal.Elements)
				copy(next[len(listVal.Elements):], otherList.Elements)
			}
			return &ListValue{ElemType: elemType, Elements: next}, true, nil
		}
		if len(listVal.Elements) >= maxListSize {
			return nil, true, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("list size exceeds limit (%d elements)", maxListSize),
				fmt.Sprintf("Keep lists within %d elements.", maxListSize),
			)
		}
		gotType := structuredTypeName(args[1])
		elemType := listVal.ElemType
		if elemType != "" {
			if !matchesListElemType(args[1], elemType, ev.global) {
				return nil, true, newError(
					"TypeError",
					line,
					col,
					ev.source,
					fmt.Sprintf("Cannot append %s into list of %s", gotType, elemType),
					fmt.Sprintf("This list only accepts elements of type %q.", elemType),
				)
			}
		} else {
			elemType = gotType
		}
		var next []Value
		if !listVal.pushed {
			listVal.pushed = true
			next = append(listVal.Elements, args[1])
		} else {
			newLen := len(listVal.Elements) + 1
			newCap := max(4, newLen*2)
			next = make([]Value, newLen, newCap)
			copy(next, listVal.Elements)
			next[len(listVal.Elements)] = args[1]
		}
		return &ListValue{ElemType: elemType, Elements: next}, true, nil

	case "put":
		if len(args) != 2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "put takes 2 arguments", "Write: put <inbox> <value>")
		}
		ib, ok := args[0].(*InboxValue)
		if !ok {
			return nil, true, newError("TypeError", line, col, ev.source, fmt.Sprintf("'put' requires an inbox as first argument, got %s", TypeName(args[0])), "Create an inbox with 'ch = inbox as <type>' first.")
		}
		if !matchesListElemType(args[1], ib.ElemType, ev.global) {
			return nil, true, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Cannot 'put' %s into inbox of %s", structuredTypeName(args[1]), ib.ElemType),
				fmt.Sprintf("This inbox only accepts values of type %q.", ib.ElemType),
			)
		}
		freezeValueForParallel(args[1], make(map[any]bool))
		if err := ib.Put(ev.ctx(), args[1], line, col, ev.source); err != nil {
			return nil, true, err
		}
		return nil, true, nil

	case "close":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "close takes 1 argument", "Write: close <inbox>")
		}
		ib, ok := args[0].(*InboxValue)
		if !ok {
			return nil, true, newError("TypeError", line, col, ev.source, fmt.Sprintf("'close' requires an inbox, got %s", TypeName(args[0])), "Pass an inbox created with 'inbox as <type>'.")
		}
		ib.Close()
		return nil, true, nil

	case "sum":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "sum takes 1 argument", "")
		}
		lv, ok := args[0].(*ListValue)
		if !ok || !isNumericList(lv) {
			return nil, true, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("sum requires a list of numbers, got %s", describeListOrType(args[0])),
				"Pass a numeric list, e.g. sum (list 1, 2, 3).",
			)
		}
		total := 0.0
		for _, el := range lv.Elements {
			total += el.(float64)
		}
		return total, true, nil

	case "avg":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "avg takes 1 argument", "")
		}
		lv, ok := args[0].(*ListValue)
		if !ok || !isNumericList(lv) {
			return nil, true, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("avg requires a list of numbers, got %s", describeListOrType(args[0])),
				"Pass a non-empty numeric list, e.g. avg (list 10, 20).",
			)
		}
		if len(lv.Elements) == 0 {
			return nil, true, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				"Cannot compute average of an empty list",
				"Check 'count xs > 0' before calling 'avg'.",
			)
		}
		total := 0.0
		for _, el := range lv.Elements {
			total += el.(float64)
		}
		return total / float64(len(lv.Elements)), true, nil

	case "sort":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "sort takes 1 argument", "")
		}
		lv, ok := args[0].(*ListValue)
		if !ok {
			return nil, true, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("sort requires a list of numbers or text, got %s", TypeName(args[0])),
				"Pass a list of numbers or text, e.g. sort (list 3, 1, 2).",
			)
		}
		if len(lv.Elements) == 0 {
			return &ListValue{ElemType: lv.ElemType, Elements: []Value{}}, true, nil
		}
		if isNumericList(lv) {
			nums := make([]float64, len(lv.Elements))
			for i, el := range lv.Elements {
				nums[i] = el.(float64)
			}
			slices.Sort(nums)
			out := make([]Value, len(nums))
			for i, n := range nums {
				out[i] = n
			}
			return &ListValue{ElemType: "number", Elements: out}, true, nil
		}
		if isTextList(lv) {
			strs := make([]string, len(lv.Elements))
			for i, el := range lv.Elements {
				strs[i] = el.(string)
			}
			slices.Sort(strs)
			out := make([]Value, len(strs))
			for i, s := range strs {
				out[i] = s
			}
			return &ListValue{ElemType: "text", Elements: out}, true, nil
		}
		return nil, true, newError(
			"TypeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("sort requires a homogeneous list of numbers or text, got %s", describeListOrType(args[0])),
			"Only number and text lists can be sorted directly.",
		)

	case "abs":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "abs() takes 1 argument", "")
		}
		n, ok := args[0].(float64)
		if !ok {
			return nil, true, newError("RuntimeError", line, col, ev.source, "abs() requires a number", "")
		}
		return math.Abs(n), true, nil

	case "floor":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "floor takes 1 argument", "")
		}
		n, ok := args[0].(float64)
		if !ok {
			return nil, true, newError("TypeError", line, col, ev.source, fmt.Sprintf("floor requires a number, got %s", TypeName(args[0])), "Example: floor 3.7")
		}
		return math.Floor(n), true, nil

	case "round":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "round takes 1 argument", "")
		}
		n, ok := args[0].(float64)
		if !ok {
			return nil, true, newError("TypeError", line, col, ev.source, fmt.Sprintf("round requires a number, got %s", TypeName(args[0])), "Example: round 3.5")
		}
		return math.Round(n), true, nil

	case "min", "max":
		if len(args) != 2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("%s() takes 2 arguments", name), "")
		}
		a, ok1 := args[0].(float64)
		b, ok2 := args[1].(float64)
		if !ok1 || !ok2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("%s() requires numbers", name), "")
		}
		if name == "min" {
			return math.Min(a, b), true, nil
		}
		return math.Max(a, b), true, nil

	case "fail":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "fail requires 1 argument (message)", "Example: fail \"Invalid input\"")
		}
		return nil, true, newError("RuntimeError", line, col, ev.source, FormatValue(args[0]), "")

	case "fields":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "fields takes 1 argument", "Example: fields player")
		}
		if mv, ok := args[0].(*MapValue); ok {
			out := make([]Value, len(mv.Order))
			for i, k := range mv.Order {
				out[i] = k
			}
			return &ListValue{ElemType: "text", Elements: out}, true, nil
		}
		rec, ok := args[0].(*RecordValue)
		if !ok {
			return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("fields() requires a 'def' record or map, got %s", TypeName(args[0])), "Define a record with 'def ... end'.")
		}
		out := make([]Value, len(rec.Order))
		for i, k := range rec.Order {
			out[i] = k
		}
		return &ListValue{ElemType: "text", Elements: out}, true, nil

	case "decode_json":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "decode_json takes 1 argument (text)", "Example: decode_json raw_text")
		}
		rawStr, ok := args[0].(string)
		if !ok {
			return nil, true, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("decode_json requires text, got %s", TypeName(args[0])),
				"Pass a JSON string to decode, e.g. decode_json '{\"id\": 1}'.",
			)
		}
		if strings.TrimSpace(rawStr) == "" {
			return nil, true, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				"Cannot decode empty text as JSON",
				"Check for empty responses before calling 'decode_json'.",
			)
		}
		var parsed any
		if err := json.Unmarshal([]byte(rawStr), &parsed); err != nil {
			return nil, true, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("Invalid JSON in decode_json: %v", err),
				"Provide valid JSON text.",
			)
		}
		return jsonAnyToEase(parsed), true, nil

	case "encode_json":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "encode_json takes 1 argument", "Example: encode_json user")
		}
		if err := validateJSONEncodable(args[0]); err != nil {
			return nil, true, newError("TypeError", line, col, ev.source, err.Error(), "Conditions and union definitions cannot be encoded as JSON.")
		}
		b, err := json.Marshal(easeValueToJSON(args[0]))
		if err != nil {
			return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Cannot encode %s to JSON: %v", TypeName(args[0]), err), "")
		}
		return string(b), true, nil

	case "split":
		if len(args) != 2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "split takes 2 arguments", "")
		}
		s, ok1 := args[0].(string)
		sep, ok2 := args[1].(string)
		if !ok1 || !ok2 {
			return nil, true, newError("TypeError", line, col, ev.source, fmt.Sprintf("split requires two text arguments, got %s and %s", TypeName(args[0]), TypeName(args[1])), "Example: split \"a,b,c\" \",\"")
		}
		parts := strings.Split(s, sep)
		if len(parts) > maxListSize {
			return nil, true, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("split result exceeds list limit (%d elements)", maxListSize),
				fmt.Sprintf("Keep split results within %d elements.", maxListSize),
			)
		}
		out := make([]Value, len(parts))
		for i, p := range parts {
			out[i] = p
		}
		return &ListValue{ElemType: "text", Elements: out}, true, nil

	case "join":
		if len(args) != 2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "join takes 2 arguments", "")
		}
		lv, ok1 := args[0].(*ListValue)
		sep, ok2 := args[1].(string)
		if !ok1 || !isTextList(lv) || !ok2 {
			return nil, true, newError("TypeError", line, col, ev.source, fmt.Sprintf("join requires a list of text and a text separator, got %s and %s", describeListOrType(args[0]), TypeName(args[1])), "Example: join (list \"a\", \"b\") \"-\"")
		}
		strs := make([]string, len(lv.Elements))
		for i, el := range lv.Elements {
			strs[i] = el.(string)
		}
		return strings.Join(strs, sep), true, nil

	case "trim":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "trim takes 1 argument", "")
		}
		s, ok := args[0].(string)
		if !ok {
			return nil, true, newError("TypeError", line, col, ev.source, fmt.Sprintf("trim requires text, got %s", TypeName(args[0])), "Example: trim \"  hello  \"")
		}
		return strings.TrimSpace(s), true, nil

	case "upper":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "upper takes 1 argument", "")
		}
		s, ok := args[0].(string)
		if !ok {
			return nil, true, newError("TypeError", line, col, ev.source, fmt.Sprintf("upper requires text, got %s", TypeName(args[0])), "Example: upper \"hello\"")
		}
		return strings.ToUpper(s), true, nil

	case "lower":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "lower takes 1 argument", "")
		}
		s, ok := args[0].(string)
		if !ok {
			return nil, true, newError("TypeError", line, col, ev.source, fmt.Sprintf("lower requires text, got %s", TypeName(args[0])), "Example: lower \"HELLO\"")
		}
		return strings.ToLower(s), true, nil

	case "slice":
		if len(args) != 3 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "slice takes 3 arguments", "Example: slice items 1 3")
		}
		startNum, ok1 := args[1].(float64)
		endNum, ok2 := args[2].(float64)
		if !ok1 || !ok2 {
			return nil, true, newError("TypeError", line, col, ev.source, "slice start and end must be numbers", "Example: slice items 0 2")
		}
		if math.IsNaN(startNum) || math.IsInf(startNum, 0) || math.Trunc(startNum) != startNum ||
			math.IsNaN(endNum) || math.IsInf(endNum, 0) || math.Trunc(endNum) != endNum {
			return nil, true, newError("RuntimeError", line, col, ev.source, "slice start and end must be whole numbers", "Use integer indices such as 0, 1, 2.")
		}
		clampBounds := func(length int) (int, int) {
			s := int(startNum)
			e := int(endNum)
			if s < 0 {
				s = 0
			} else if s > length {
				s = length
			}
			if e < s {
				e = s
			} else if e > length {
				e = length
			}
			return s, e
		}
		switch target := args[0].(type) {
		case *ListValue:
			s, e := clampBounds(len(target.Elements))
			out := make([]Value, e-s)
			copy(out, target.Elements[s:e])
			return &ListValue{ElemType: target.ElemType, Elements: out}, true, nil
		case string:
			runes := []rune(target)
			s, e := clampBounds(len(runes))
			return string(runes[s:e]), true, nil
		default:
			return nil, true, newError("TypeError", line, col, ev.source, fmt.Sprintf("slice requires a list or text, got %s", TypeName(args[0])), "Example: slice \"hello\" 1 4")
		}

	case "replace":
		if len(args) != 3 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "replace takes 3 arguments", "")
		}
		s, ok1 := args[0].(string)
		oldStr, ok2 := args[1].(string)
		newStr, ok3 := args[2].(string)
		if !ok1 || !ok2 || !ok3 {
			return nil, true, newError("TypeError", line, col, ev.source, fmt.Sprintf("replace requires three text arguments, got %s, %s, and %s", TypeName(args[0]), TypeName(args[1]), TypeName(args[2])), "Example: replace \"foo bar\" \"foo\" \"baz\"")
		}
		if oldStr == "" {
			return nil, true, newError("RuntimeError", line, col, ev.source, "replace requires a non-empty search text", "Pass a non-empty substring as the second argument to 'replace'.")
		}
		return strings.ReplaceAll(s, oldStr, newStr), true, nil

	case "contains":
		if len(args) != 2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "contains takes 2 arguments", "")
		}
		s, ok1 := args[0].(string)
		sub, ok2 := args[1].(string)
		if !ok1 || !ok2 {
			return nil, true, newError("TypeError", line, col, ev.source, fmt.Sprintf("contains requires two text arguments, got %s and %s", TypeName(args[0]), TypeName(args[1])), "Example: check contains email \"@example.com\" -> ...")
		}
		return strings.Contains(s, sub), true, nil

	case "load":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "load takes 1 argument (file path)", "")
		}
		pathStr, ok := args[0].(string)
		if !ok {
			return nil, true, newError("TypeError", line, col, ev.source, fmt.Sprintf("load requires a text file path, got %s", TypeName(args[0])), "Example: load \"./notes.txt\"")
		}
		val, err := ev.evalLoad(pathStr, "text", line, col)
		if err != nil {
			return nil, true, err
		}
		return val, true, nil

	case "save":
		if len(args) != 2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "save takes 2 arguments (path, data)", "")
		}
		pathStr, ok := args[0].(string)
		if !ok || strings.TrimSpace(pathStr) == "" {
			return nil, true, newError("TypeError", line, col, ev.source, "save requires a non-empty text file path as first argument", "Example: save \"./out.txt\" content")
		}
		switch data := args[1].(type) {
		case string:
			if !utf8.ValidString(data) {
				return nil, true, newError("RuntimeError", line, col, ev.source, "Text passed to save contains invalid UTF-8 bytes", "")
			}
			raw := []byte(data)
			if err := os.WriteFile(pathStr, raw, 0o644); err != nil {
				return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Failed to save file %q: %v", pathStr, err), "Check that the target directory exists and is writable.")
			}
			return float64(len(raw)), true, nil
		case *ListValue:
			buf := make([]byte, len(data.Elements))
			for i, el := range data.Elements {
				n, isNum := el.(float64)
				if !isNum || n != math.Trunc(n) || n < 0 || n > 255 {
					return nil, true, newError(
						"RuntimeError",
						line,
						col,
						ev.source,
						fmt.Sprintf("Byte value at index %d must be an integer in 0..255, got %s", i, FormatValue(el)),
						"Pass a list of integer byte values (0..255) to save binary files.",
					)
				}
				buf[i] = byte(int(n))
			}
			if err := os.WriteFile(pathStr, buf, 0o644); err != nil {
				return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Failed to save file %q: %v", pathStr, err), "Check that the target directory exists and is writable.")
			}
			return float64(len(buf)), true, nil
		default:
			return nil, true, newError("TypeError", line, col, ev.source, fmt.Sprintf("save expects text or a list of bytes (0..255), got %s", TypeName(args[1])), "Pass text or a list of numbers (0..255) to 'save'.")
		}

	case "http_call":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "http_call takes 1 argument (http_request)", "")
		}
		reqRec, ok := args[0].(*RecordValue)
		if !ok {
			return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("http_call requires an http_request record, got %s", TypeName(args[0])), "")
		}
		resRec, err := ev.execHTTPCall(reqRec, line, col)
		if err != nil {
			return nil, true, err
		}
		return resRec, true, nil

	case "screen_present":
		if len(args) != 2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "screen_present takes 2 arguments (screen, scene)", "")
		}
		winRec, ok1 := args[0].(*RecordValue)
		sceneRec, ok2 := args[1].(*RecordValue)
		if !ok1 || !ok2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "screen_present requires (screen, scene) records", "")
		}
		if ev.headlessScreen {
			ev.screenFrameCount++
			st := screenFrameState{Status: "open"}
			if ev.screenFrameCount == 1 {
				st.Key = "right"
			} else {
				st.Status = "closed"
			}
			b, _ := json.Marshal(st)
			return string(b), true, nil
		}
		if ev.activeScreen == nil {
			w := 800
			h := 600
			title := "Ease Window"
			if v, ok := winRec.Fields["width"].(float64); ok && v > 0 {
				w = int(v)
			}
			if v, ok := winRec.Fields["height"].(float64); ok && v > 0 {
				h = int(v)
			}
			if v, ok := winRec.Fields["title"].(string); ok && v != "" {
				title = v
			}
			ev.activeScreen = startScreenSession(w, h, title)
		}
		return ev.activeScreen.present(recordToScreenScene(sceneRec)), true, nil
	}

	return nil, false, nil
}

func (ev *Evaluator) newHTTPResponseRecord(status float64, body, errMsg string) *RecordValue {
	return &RecordValue{
		TypeName: "http_response",
		Fields: map[string]Value{
			"status": status,
			"body":   body,
			"error":  errMsg,
		},
		Order: []string{"status", "body", "error"},
	}
}

func (ev *Evaluator) execHTTPCall(reqRec *RecordValue, line, col int) (*RecordValue, error) {
	method := "GET"
	if v, ok := reqRec.Fields["method"].(string); ok && v != "" {
		method = strings.ToUpper(v)
	}
	targetURL, _ := reqRec.Fields["url"].(string)
	bodyText, _ := reqRec.Fields["body"].(string)
	token, _ := reqRec.Fields["token"].(string)
	timeoutSec := 5.0
	if t, ok := reqRec.Fields["timeout"].(float64); ok && t > 0 {
		timeoutSec = t
	}

	var bodyReader io.Reader
	if bodyText != "" {
		bodyReader = strings.NewReader(bodyText)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec*float64(time.Second)))
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, method, targetURL, bodyReader)
	if err != nil {
		return ev.newHTTPResponseRecord(0, "", err.Error()), nil
	}
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	if bodyText != "" && (method == "POST" || method == "PUT") {
		trimmed := strings.TrimSpace(bodyText)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			httpReq.Header.Set("Content-Type", "application/json")
		} else {
			httpReq.Header.Set("Content-Type", "text/plain; charset=utf-8")
		}
	}

	client := ev.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return ev.newHTTPResponseRecord(0, "", err.Error()), nil
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBodyBytes+1))
	if err != nil {
		return ev.newHTTPResponseRecord(float64(resp.StatusCode), "", err.Error()), nil
	}
	if len(rawBody) > maxHTTPBodyBytes {
		return ev.newHTTPResponseRecord(
			float64(resp.StatusCode),
			"",
			fmt.Sprintf("HTTP response body exceeds %d MB limit", maxHTTPBodyBytes/(1024*1024)),
		), nil
	}
	if !utf8.Valid(rawBody) {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			"HTTP response body contains invalid UTF-8 bytes",
			"The remote endpoint returned non-UTF-8 binary or corrupted data.",
		)
	}
	return ev.newHTTPResponseRecord(float64(resp.StatusCode), norm.NFC.String(string(rawBody)), ""), nil
}

func jsonAnyToEase(v any) Value {
	switch x := v.(type) {
	case map[string]any:
		fields := make(map[string]Value, len(x))
		order := make([]string, 0, len(x))
		for k := range x {
			order = append(order, k)
		}
		slices.Sort(order)
		for _, k := range order {
			fields[k] = jsonAnyToEase(x[k])
		}
		return &MapValue{Fields: fields, Order: order}
	case []any:
		out := make([]Value, len(x))
		elemType := ""
		for i, el := range x {
			conv := jsonAnyToEase(el)
			out[i] = conv
			et := structuredTypeName(conv)
			if i == 0 {
				elemType = et
			} else if elemType != "" && et != elemType {
				elemType = ""
			}
		}
		return &ListValue{ElemType: elemType, Elements: out}
	case bool:
		if x {
			return "true"
		}
		return "false"
	case string:
		return norm.NFC.String(x)
	default:
		return x
	}
}

func isTrueFalseUnion(opts []Value) bool {
	if len(opts) != 2 {
		return false
	}
	s0, ok0 := opts[0].(string)
	s1, ok1 := opts[1].(string)
	if !ok0 || !ok1 {
		return false
	}
	return (s0 == "true" && s1 == "false") || (s0 == "false" && s1 == "true")
}

// CheckNotConditionValue rejects raw boolean condition values where a data value is expected.
func CheckNotConditionValue(v Value, line, col int, source string) error {
	if _, isCond := v.(bool); isCond {
		return newError(
			"TypeError",
			line,
			col,
			source,
			"Conditions cannot be stored or printed. Use a union type instead, e.g.: def result = \"yes\" | \"no\"",
			"Evaluate conditions directly inside 'check' or 'test', and return/store explicit union variants instead.",
		)
	}
	return nil
}

func (ev *Evaluator) checkNotConditionValue(v Value, line, col int) error {
	return CheckNotConditionValue(v, line, col, ev.source)
}

func (ev *Evaluator) checkRuntimeUnionLiteral(lit string, opts []Value, label string, line, col int) error {
	var allowedDisplay []string
	for _, opt := range opts {
		if bt, ok := opt.(BaseTypeValue); ok && string(bt) == "text" {
			return nil
		}
		if s, ok := opt.(string); ok && s == lit {
			return nil
		}
		allowedDisplay = append(allowedDisplay, formatUnionOption(opt))
	}
	return newError(
		"TypeError",
		line,
		col,
		ev.source,
		fmt.Sprintf("%q is not a value of %s (%s)", lit, label, strings.Join(allowedDisplay, " | ")),
		fmt.Sprintf("Valid values for %s: %s.", label, strings.Join(allowedDisplay, " | ")),
	)
}

func validateJSONEncodable(v Value) error {
	switch x := v.(type) {
	case bool:
		return fmt.Errorf("Cannot encode condition to JSON")
	case BaseTypeValue:
		return fmt.Errorf("Cannot encode base type %q to JSON", string(x))
	case *UnionValue:
		return fmt.Errorf("Cannot encode union definition %q to JSON", x.TypeName)
	case *RecordValue:
		for _, k := range x.Order {
			if err := validateJSONEncodable(x.Fields[k]); err != nil {
				return err
			}
		}
	case *MapValue:
		for _, k := range x.Order {
			if err := validateJSONEncodable(x.Fields[k]); err != nil {
				return err
			}
		}
	case *ListValue:
		for _, el := range x.Elements {
			if err := validateJSONEncodable(el); err != nil {
				return err
			}
		}
	}
	return nil
}

func easeValueToJSON(v Value) any {
	switch x := v.(type) {
	case *RecordValue:
		out := make(map[string]any, len(x.Fields))
		for k, val := range x.Fields {
			if isTrueFalseUnion(x.Allowed[k]) {
				if s, ok := val.(string); ok {
					if s == "true" {
						out[k] = true
						continue
					}
					if s == "false" {
						out[k] = false
						continue
					}
				}
			}
			out[k] = easeValueToJSON(val)
		}
		return out
	case *MapValue:
		out := make(map[string]any, len(x.Fields))
		for k, val := range x.Fields {
			out[k] = easeValueToJSON(val)
		}
		return out
	case *ListValue:
		out := make([]any, len(x.Elements))
		for i, el := range x.Elements {
			out[i] = easeValueToJSON(el)
		}
		return out
	default:
		return x
	}
}

func isNumericList(lv *ListValue) bool {
	if lv.ElemType != "" {
		return lv.ElemType == "number"
	}
	for _, el := range lv.Elements {
		if _, ok := el.(float64); !ok {
			return false
		}
	}
	return true
}

func isTextList(lv *ListValue) bool {
	if lv.ElemType != "" {
		return lv.ElemType == "text"
	}
	for _, el := range lv.Elements {
		if _, ok := el.(string); !ok {
			return false
		}
	}
	return true
}

func describeListOrType(v Value) string {
	if lv, ok := v.(*ListValue); ok {
		if lv.ElemType != "" {
			return "list of " + lv.ElemType
		}
		return "list"
	}
	return TypeName(v)
}

func (ev *Evaluator) evalLoad(pathStr, mode string, line, col int) (Value, error) {
	if strings.TrimSpace(pathStr) == "" {
		return nil, newError("RuntimeError", line, col, ev.source, "load requires a non-empty file path", "Example: load \"./notes.txt\"")
	}
	f, err := os.Open(pathStr)
	if err != nil {
		return nil, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Failed to read file %q: %v", pathStr, err), "Check that the file path exists and is readable.")
	}
	defer f.Close()

	if info, err := f.Stat(); err == nil && info.Size() > maxFileSize {
		return nil, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("File %q exceeds %d MB limit", pathStr, maxFileSize/(1024*1024)), "Keep loaded files within the 50 MB size limit.")
	}

	raw, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Failed to read file %q: %v", pathStr, err), "")
	}
	if len(raw) > maxFileSize {
		return nil, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("File %q exceeds %d MB limit", pathStr, maxFileSize/(1024*1024)), "Keep loaded files within the 50 MB size limit.")
	}

	if mode == "bytes" {
		out := make([]Value, len(raw))
		for i, b := range raw {
			out[i] = float64(b)
		}
		return &ListValue{ElemType: "number", Elements: out}, nil
	}

	if !utf8.Valid(raw) {
		return nil, newError(
			"RuntimeError",
			line,
			col,
			ev.source,
			fmt.Sprintf("File %q contains invalid UTF-8 bytes", pathStr),
			"Use 'load <path> as bytes' for binary files.",
		)
	}
	text := norm.NFC.String(strings.TrimPrefix(string(raw), "\uFEFF"))
	if mode == "text" {
		return text, nil
	}

	// mode == "csv"
	reader := csv.NewReader(strings.NewReader(text))
	records, err := reader.ReadAll()
	if err != nil {
		return nil, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Invalid CSV in %q: %v", pathStr, err), "Check CSV quoting and ensure every row has the same number of columns.")
	}
	if len(records) == 0 || len(records[0]) == 0 {
		return nil, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("CSV file %q is empty", pathStr), "Provide at least a header row in the CSV file.")
	}

	headers := make([]string, len(records[0]))
	seenHeaders := make(map[string]bool, len(records[0]))
	for i, h := range records[0] {
		nh := norm.NFC.String(h)
		if seenHeaders[nh] {
			return nil, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Duplicate CSV header %q in %q", nh, pathStr), "Ensure every CSV column header has a unique name.")
		}
		seenHeaders[nh] = true
		headers[i] = nh
	}

	rows := make([]Value, 0, len(records)-1)
	for _, row := range records[1:] {
		fields := make(map[string]Value, len(headers))
		order := make([]string, len(headers))
		copy(order, headers)
		for i, h := range headers {
			fields[h] = norm.NFC.String(row[i])
		}
		rows = append(rows, &MapValue{Fields: fields, Order: order, FromCSV: true})
	}
	return &ListValue{Elements: rows}, nil
}
