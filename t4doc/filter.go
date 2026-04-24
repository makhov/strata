package t4doc

import (
	"encoding/json"
	"fmt"

	"github.com/t4db/t4/t4doc/t4docpb"
)

// Filter matches documents during Find and Watch.
type Filter struct {
	op       filterOp
	field    string
	value    any
	children []Filter
}

type filterOp uint8

const (
	filterAll filterOp = iota
	filterEq
	filterAnd
)

// All matches every document.
func All() Filter { return Filter{op: filterAll} }

// Eq matches documents whose field equals value.
func Eq(field string, value any) Filter {
	return Filter{op: filterEq, field: field, value: value}
}

// And matches documents that satisfy every child filter.
func And(filters ...Filter) Filter {
	return Filter{op: filterAnd, children: filters}
}

func (f Filter) match(raw json.RawMessage) bool {
	switch f.op {
	case filterAll:
		return true
	case filterEq:
		got, ok := extractJSONField(raw, f.field)
		if !ok {
			return false
		}
		return valuesEqual(got, f.value)
	case filterAnd:
		for _, child := range f.children {
			if !child.match(raw) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (f Filter) eqFilters(out []Filter) []Filter {
	switch f.op {
	case filterEq:
		return append(out, f)
	case filterAnd:
		for _, child := range f.children {
			out = child.eqFilters(out)
		}
	}
	return out
}

func filterToProto(f Filter) (*t4docpb.Filter, error) {
	switch f.op {
	case filterAll:
		return nil, nil
	case filterEq:
		v, err := valueToProto(f.value)
		if err != nil {
			return nil, err
		}
		return &t4docpb.Filter{
			Kind: &t4docpb.Filter_Eq{Eq: &t4docpb.EqFilter{Field: f.field, Value: v}},
		}, nil
	case filterAnd:
		children := make([]*t4docpb.Filter, 0, len(f.children))
		for _, child := range f.children {
			pb, err := filterToProto(child)
			if err != nil {
				return nil, err
			}
			if pb != nil {
				children = append(children, pb)
			}
		}
		return &t4docpb.Filter{
			Kind: &t4docpb.Filter_And{And: &t4docpb.AndFilter{Filters: children}},
		}, nil
	default:
		return nil, fmt.Errorf("t4doc: unknown filter op %d", f.op)
	}
}

func filterFromProto(pb *t4docpb.Filter) (Filter, error) {
	if pb == nil {
		return All(), nil
	}
	switch kind := pb.Kind.(type) {
	case *t4docpb.Filter_Eq:
		if kind.Eq == nil || kind.Eq.Value == nil {
			return Filter{}, fmt.Errorf("t4doc: malformed eq filter")
		}
		return Eq(kind.Eq.Field, valueFromProto(kind.Eq.Value)), nil
	case *t4docpb.Filter_And:
		if kind.And == nil {
			return Filter{}, fmt.Errorf("t4doc: malformed and filter")
		}
		children := make([]Filter, len(kind.And.Filters))
		for i, child := range kind.And.Filters {
			f, err := filterFromProto(child)
			if err != nil {
				return Filter{}, err
			}
			children[i] = f
		}
		return And(children...), nil
	default:
		return Filter{}, fmt.Errorf("t4doc: unsupported filter")
	}
}

func valueToProto(v any) (*t4docpb.Value, error) {
	switch x := v.(type) {
	case string:
		return &t4docpb.Value{Kind: &t4docpb.Value_StringValue{StringValue: x}}, nil
	case int:
		return &t4docpb.Value{Kind: &t4docpb.Value_Int64Value{Int64Value: int64(x)}}, nil
	case int64:
		return &t4docpb.Value{Kind: &t4docpb.Value_Int64Value{Int64Value: x}}, nil
	case bool:
		return &t4docpb.Value{Kind: &t4docpb.Value_BoolValue{BoolValue: x}}, nil
	default:
		return nil, fmt.Errorf("t4doc: unsupported filter value type %T", v)
	}
}

func valueFromProto(v *t4docpb.Value) any {
	switch kind := v.Kind.(type) {
	case *t4docpb.Value_StringValue:
		return kind.StringValue
	case *t4docpb.Value_Int64Value:
		return kind.Int64Value
	case *t4docpb.Value_BoolValue:
		return kind.BoolValue
	default:
		return nil
	}
}
