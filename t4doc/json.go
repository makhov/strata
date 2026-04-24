package t4doc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
)

type envelope struct {
	FormatVersion int             `json:"format_version"`
	Document      json.RawMessage `json:"document"`
}

func marshalEnvelope(v any) ([]byte, json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	env, err := json.Marshal(envelope{FormatVersion: 1, Document: raw})
	if err != nil {
		return nil, nil, err
	}
	return env, raw, nil
}

func envelopeFromRaw(raw json.RawMessage) ([]byte, error) {
	return json.Marshal(envelope{FormatVersion: 1, Document: raw})
}

func decodeEnvelope(data []byte) (json.RawMessage, error) {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if env.FormatVersion != 1 {
		return nil, fmt.Errorf("t4doc: unsupported envelope version %d", env.FormatVersion)
	}
	if len(env.Document) == 0 {
		return nil, fmt.Errorf("t4doc: empty document")
	}
	return env.Document, nil
}

func decodeDocument[T any](id string, revision, createRevision int64, data []byte) (*Document[T], json.RawMessage, error) {
	raw, err := decodeEnvelope(data)
	if err != nil {
		return nil, nil, err
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, nil, err
	}
	return &Document[T]{
		ID:             id,
		Value:          value,
		Revision:       revision,
		CreateRevision: createRevision,
	}, raw, nil
}

func extractJSONField(raw json.RawMessage, path string) (any, bool) {
	if path == "" {
		return nil, false
	}
	var root any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return nil, false
	}
	cur := root
	start := 0
	for i := 0; i <= len(path); i++ {
		if i != len(path) && path[i] != '.' {
			continue
		}
		part := path[start:i]
		if part == "" {
			return nil, false
		}
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[part]
		if !ok || cur == nil {
			return nil, false
		}
		start = i + 1
	}
	return cur, true
}

func valuesEqual(left any, right any) bool {
	switch r := right.(type) {
	case string:
		l, ok := left.(string)
		return ok && l == r
	case bool:
		l, ok := left.(bool)
		return ok && l == r
	case int:
		l, ok := toInt64(left)
		return ok && l == int64(r)
	case int64:
		l, ok := toInt64(left)
		return ok && l == r
	default:
		return false
	}
}

func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case json.Number:
		i, err := x.Int64()
		return i, err == nil
	case float64:
		if math.Trunc(x) != x {
			return 0, false
		}
		return int64(x), true
	default:
		return 0, false
	}
}

func applyMergePatch(target json.RawMessage, patch MergePatch) (json.RawMessage, error) {
	var patchValue any
	dec := json.NewDecoder(bytes.NewReader(patch))
	dec.UseNumber()
	if err := dec.Decode(&patchValue); err != nil {
		return nil, err
	}

	var targetValue any
	dec = json.NewDecoder(bytes.NewReader(target))
	dec.UseNumber()
	if err := dec.Decode(&targetValue); err != nil {
		return nil, err
	}

	merged := mergePatchValue(targetValue, patchValue)
	return json.Marshal(merged)
}

func mergePatchValue(target any, patch any) any {
	patchObj, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	targetObj, _ := target.(map[string]any)
	if targetObj == nil {
		targetObj = make(map[string]any)
	}
	for k, v := range patchObj {
		if v == nil {
			delete(targetObj, k)
			continue
		}
		targetObj[k] = mergePatchValue(targetObj[k], v)
	}
	return targetObj
}
