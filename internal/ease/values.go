package ease

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

type Value = any

// RecordValue represents an Ease `def` prototype or instance created with `with`.
type RecordValue struct {
	TypeName    string
	Fields      map[string]Value
	Allowed     map[string][]Value
	Order       []string
	IsPrototype bool
}

// MapValue represents a structured key-value object produced by `decode_json` or `load ... as csv`.
type MapValue struct {
	Fields  map[string]Value
	Order   []string
	FromCSV bool
}

// UnionValue represents a union value-space defined with `def <name> = v1 | v2 | ...`.
type UnionValue struct {
	TypeName string
	Values   []Value
}

// BaseTypeValue represents a base type member (`text` or `number`) inside a union.
type BaseTypeValue string

// ListValue represents a typed homogeneous list in Ease.
type ListValue struct {
	ElemType string
	Elements []Value
	pushed   bool
}

// FormatValue returns a clean, beginner-friendly display string for an Ease value.
func FormatValue(v Value) string {
	switch val := v.(type) {
	case nil:
		return "none"
	case bool:
		if val {
			return "true"
		}
		return "false"
	case float64:
		if math.IsNaN(val) || math.IsInf(val, 0) {
			return fmt.Sprintf("%v", val)
		}
		if val == 0 {
			return "0"
		}
		if val == math.Trunc(val) {
			return strconv.FormatFloat(val, 'f', 0, 64)
		}
		return strconv.FormatFloat(val, 'f', -1, 64)
	case string:
		return val
	case *ListValue:
		parts := make([]string, len(val.Elements))
		for i, el := range val.Elements {
			if s, ok := el.(string); ok {
				parts[i] = strconv.Quote(s)
			} else {
				parts[i] = FormatValue(el)
			}
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *RecordValue:
		parts := make([]string, 0, len(val.Order))
		for _, k := range val.Order {
			fv := val.Fields[k]
			if s, ok := fv.(string); ok {
				parts = append(parts, fmt.Sprintf("%s: %s", k, strconv.Quote(s)))
			} else {
				parts = append(parts, fmt.Sprintf("%s: %s", k, FormatValue(fv)))
			}
		}
		return fmt.Sprintf("%s(%s)", val.TypeName, strings.Join(parts, ", "))
	case *MapValue:
		parts := make([]string, 0, len(val.Order))
		for _, k := range val.Order {
			fv := val.Fields[k]
			if s, ok := fv.(string); ok {
				parts = append(parts, fmt.Sprintf("%s: %s", k, strconv.Quote(s)))
			} else {
				parts = append(parts, fmt.Sprintf("%s: %s", k, FormatValue(fv)))
			}
		}
		return fmt.Sprintf("map(%s)", strings.Join(parts, ", "))
	case *UnionValue:
		return fmt.Sprintf("%s(%s)", val.TypeName, strings.Join(formatUnionOptions(val.Values), " | "))
	case BaseTypeValue:
		return string(val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func formatUnionOption(opt Value) string {
	switch o := opt.(type) {
	case string:
		return strconv.Quote(o)
	case BaseTypeValue:
		return string(o)
	case *RecordValue:
		if o.IsPrototype {
			return o.TypeName
		}
		return FormatValue(o)
	default:
		return FormatValue(o)
	}
}

func formatUnionOptions(opts []Value) []string {
	out := make([]string, len(opts))
	for i, opt := range opts {
		out[i] = formatUnionOption(opt)
	}
	return out
}

// TypeName returns the human-readable Ease type name of `v`.
func TypeName(v Value) string {
	switch val := v.(type) {
	case nil:
		return "none"
	case bool:
		return "condition"
	case float64:
		return "number"
	case string:
		return "text"
	case *ListValue:
		return "list"
	case *RecordValue:
		return val.TypeName
	case *MapValue:
		return "map"
	case *UnionValue:
		return val.TypeName
	case BaseTypeValue:
		return string(val)
	default:
		return "unknown"
	}
}

func isAtomicEaseValue(v Value) bool {
	switch v.(type) {
	case string, float64:
		return true
	default:
		return false
	}
}

func matchesEaseType(v Value, typeName string) bool {
	switch typeName {
	case "text":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "list":
		_, ok := v.(*ListValue)
		return ok
	default:
		if rec, ok := v.(*RecordValue); ok {
			return rec.TypeName == typeName
		}
		return false
	}
}

// ValuesEqual performs strict structural equality between concrete Ease values.
func ValuesEqual(a, b Value) bool {
	if a == nil && b == nil {
		return true
	}
	switch va := a.(type) {
	case bool:
		vb, ok := b.(bool)
		return ok && va == vb
	case float64:
		vb, ok := b.(float64)
		return ok && va == vb
	case string:
		vb, ok := b.(string)
		return ok && va == vb
	case *ListValue:
		vb, ok := b.(*ListValue)
		if !ok || len(va.Elements) != len(vb.Elements) {
			return false
		}
		for i := range va.Elements {
			if !ValuesEqual(va.Elements[i], vb.Elements[i]) {
				return false
			}
		}
		return true
	case *RecordValue:
		vb, ok := b.(*RecordValue)
		if !ok || va.IsPrototype || vb.IsPrototype || va.TypeName != vb.TypeName {
			return false
		}
		if len(va.Fields) != len(vb.Fields) {
			return false
		}
		for k, v := range va.Fields {
			ov, exists := vb.Fields[k]
			if !exists || !ValuesEqual(v, ov) {
				return false
			}
		}
		return true
	case *MapValue:
		vb, ok := b.(*MapValue)
		if !ok || len(va.Fields) != len(vb.Fields) {
			return false
		}
		for k, v := range va.Fields {
			ov, exists := vb.Fields[k]
			if !exists || !ValuesEqual(v, ov) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
