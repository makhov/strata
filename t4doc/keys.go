package t4doc

import (
	"encoding/base64"
	"fmt"
	"strings"
)

const rootPrefix = "\x00t4/doc/v1/"

func docPrefix(collection string) string {
	return rootPrefix + "d/" + collection + "/"
}

func docKey(collection, id string) string {
	return docPrefix(collection) + encodeID(id)
}

func catalogKey(collection string) string {
	return rootPrefix + "m/" + collection + "/catalog"
}

func indexPrefix(collection, indexName, encodedValue string) string {
	return rootPrefix + "x/" + collection + "/" + indexName + "/" + encodedValue + "/"
}

func indexKey(collection, indexName, encodedValue, id string) string {
	return indexPrefix(collection, indexName, encodedValue) + encodeID(id)
}

func encodeID(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id))
}

func decodeID(encoded string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func idFromIndexKey(key string) (string, error) {
	i := strings.LastIndexByte(key, '/')
	if i < 0 || i == len(key)-1 {
		return "", fmt.Errorf("t4doc: malformed index key")
	}
	return decodeID(key[i+1:])
}

func idFromDocKey(collection, key string) (string, error) {
	prefix := docPrefix(collection)
	if !strings.HasPrefix(key, prefix) {
		return "", fmt.Errorf("t4doc: malformed document key")
	}
	return decodeID(key[len(prefix):])
}

func encodeIndexValue(spec IndexSpec, value any) (string, bool) {
	switch spec.Type {
	case String:
		s, ok := value.(string)
		if !ok {
			return "", false
		}
		return "s:" + base64.RawURLEncoding.EncodeToString([]byte(s)), true
	case Int64:
		i, ok := toInt64(value)
		if !ok {
			return "", false
		}
		u := uint64(i) ^ (uint64(1) << 63)
		return "i:" + fmt.Sprintf("%020d", u), true
	case Bool:
		b, ok := value.(bool)
		if !ok {
			return "", false
		}
		if b {
			return "b:1", true
		}
		return "b:0", true
	default:
		return "", false
	}
}

func filterValueForIndex(spec IndexSpec, f Filter) (string, bool) {
	if f.op != filterEq || f.field != spec.Field {
		return "", false
	}
	switch spec.Type {
	case String:
		s, ok := f.value.(string)
		if !ok {
			return "", false
		}
		return encodeIndexValue(spec, s)
	case Int64:
		switch v := f.value.(type) {
		case int:
			return encodeIndexValue(spec, int64(v))
		case int64:
			return encodeIndexValue(spec, v)
		default:
			return "", false
		}
	case Bool:
		b, ok := f.value.(bool)
		if !ok {
			return "", false
		}
		return encodeIndexValue(spec, b)
	default:
		return "", false
	}
}
