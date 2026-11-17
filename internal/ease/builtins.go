package ease

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

func (ev *Evaluator) callBuiltin(name string, args []Value, line, col int) (Value, bool, error) {
	switch name {
	case "len":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "len() takes 1 argument", "")
		}
		switch v := args[0].(type) {
		case *ListValue:
			return float64(len(v.Elements)), true, nil
		case string:
			return float64(len([]rune(v))), true, nil
		default:
			return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("len() requires a list or string, got %s", TypeName(args[0])), "")
		}

	case "str":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "str() takes 1 argument", "")
		}
		return FormatValue(args[0]), true, nil

	case "num":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "num() takes 1 argument", "")
		}
		switch v := args[0].(type) {
		case float64:
			return v, true, nil
		case string:
			n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Cannot convert %q to number", v), "")
			}
			return n, true, nil
		default:
			return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Cannot convert %s to number", TypeName(args[0])), "")
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
				fmt.Sprintf("Keep 'do range start end' within %d elements.", maxListSize),
			)
		}
		if endF >= startF && (endF-startF+1) > float64(maxListSize) {
			return nil, true, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("range too large (limit %d elements)", maxListSize),
				fmt.Sprintf("Keep 'do range start end' within %d elements.", maxListSize),
			)
		}
		start, end := int(startF), int(endF)
		out := make([]Value, 0, max(0, end-start+1))
		for i := start; i <= end; i++ {
			out = append(out, float64(i))
		}
		return &ListValue{ElemType: "number", Elements: out}, true, nil

	case "push":
		if len(args) != 2 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "push(list, item) takes 2 arguments", "")
		}
		listVal, ok := args[0].(*ListValue)
		if !ok {
			return nil, true, newError("TypeError", line, col, ev.source, "push() expects a list as first argument", "")
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
					fmt.Sprintf("Cannot push %s into list of %s", gotType, elemType),
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

	case "abs":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "abs() takes 1 argument", "")
		}
		n, ok := args[0].(float64)
		if !ok {
			return nil, true, newError("RuntimeError", line, col, ev.source, "abs() requires a number", "")
		}
		return math.Abs(n), true, nil

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
		msg := "Explicit failure"
		if len(args) > 0 {
			msg = FormatValue(args[0])
		}
		return nil, true, newError("RuntimeError", line, col, ev.source, msg, "")

	case "fields":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "fields takes 1 argument", "Example: do fields player")
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
			return nil, true, newError("RuntimeError", line, col, ev.source, "decode_json takes 1 argument (text)", "Example: do decode_json raw_text")
		}
		rawStr, ok := args[0].(string)
		if !ok {
			return nil, true, newError(
				"TypeError",
				line,
				col,
				ev.source,
				fmt.Sprintf("decode_json requires text, got %s", TypeName(args[0])),
				"Pass a JSON string to decode, e.g. do decode_json '{\"id\": 1}'.",
			)
		}
		if strings.TrimSpace(rawStr) == "" {
			return nil, true, newError(
				"RuntimeError",
				line,
				col,
				ev.source,
				"Cannot decode empty text as JSON",
				"Check for empty responses before calling 'do decode_json'.",
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
			return nil, true, newError("RuntimeError", line, col, ev.source, "encode_json takes 1 argument", "Example: do encode_json user")
		}
		if err := validateJSONEncodable(args[0]); err != nil {
			return nil, true, newError("TypeError", line, col, ev.source, err.Error(), "Conditions and union definitions cannot be encoded as JSON.")
		}
		b, err := json.Marshal(easeValueToJSON(args[0]))
		if err != nil {
			return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("Cannot encode %s to JSON: %v", TypeName(args[0]), err), "")
		}
		return string(b), true, nil

	case "http_call":
		if len(args) != 1 {
			return nil, true, newError("RuntimeError", line, col, ev.source, "http_call takes 1 argument (http_request)", "")
		}
		reqRec, ok := args[0].(*RecordValue)
		if !ok {
			return nil, true, newError("RuntimeError", line, col, ev.source, fmt.Sprintf("http_call requires an http_request record, got %s", TypeName(args[0])), "")
		}
		return ev.execHTTPCall(reqRec), true, nil

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

func (ev *Evaluator) execHTTPCall(reqRec *RecordValue) *RecordValue {
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
		return ev.newHTTPResponseRecord(0, "", err.Error())
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
		return ev.newHTTPResponseRecord(0, "", err.Error())
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBodyBytes+1))
	if err != nil {
		return ev.newHTTPResponseRecord(float64(resp.StatusCode), "", err.Error())
	}
	if len(rawBody) > maxHTTPBodyBytes {
		return ev.newHTTPResponseRecord(
			float64(resp.StatusCode),
			"",
			fmt.Sprintf("HTTP response body exceeds %d MB limit", maxHTTPBodyBytes/(1024*1024)),
		)
	}
	return ev.newHTTPResponseRecord(float64(resp.StatusCode), string(rawBody), "")
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

func (ev *Evaluator) checkNotConditionValue(v Value, line, col int) error {
	if _, isCond := v.(bool); isCond {
		return newError(
			"TypeError",
			line,
			col,
			ev.source,
			"Conditions cannot be stored or printed. Use a union type instead, e.g.: def result = \"yes\" | \"no\"",
			"Evaluate conditions directly inside 'check' or 'test', and return/store explicit union variants instead.",
		)
	}
	return nil
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

