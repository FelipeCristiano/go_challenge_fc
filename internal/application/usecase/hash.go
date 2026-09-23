package usecase

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// CanonicalPayloadHash calcula o hash SHA-256 canônico a partir dos campos de negócio.
// Exclui a chave de idempotência e metadados de transporte. As chaves do mapa são
// ordenadas lexicograficamente garantindo determinismo idêntico para HTTP e SQS.
func CanonicalPayloadHash(fields map[string]any) (string, error) {
	canonicalJSON, err := toCanonicalJSON(fields)
	if err != nil {
		return "", fmt.Errorf("canonical hash: %w", err)
	}
	hash := sha256.Sum256(canonicalJSON)
	return hex.EncodeToString(hash[:]), nil
}

func toCanonicalJSON(v any) ([]byte, error) {
	switch val := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		buf := []byte("{")
		for i, k := range keys {
			if i > 0 {
				buf = append(buf, ',')
			}
			keyBytes, err := json.Marshal(k)
			if err != nil {
				return nil, err
			}
			buf = append(buf, keyBytes...)
			buf = append(buf, ':')

			valBytes, err := toCanonicalJSON(val[k])
			if err != nil {
				return nil, err
			}
			buf = append(buf, valBytes...)
		}
		buf = append(buf, '}')
		return buf, nil

	case []any:
		buf := []byte("[")
		for i, elem := range val {
			if i > 0 {
				buf = append(buf, ',')
			}
			elemBytes, err := toCanonicalJSON(elem)
			if err != nil {
				return nil, err
			}
			buf = append(buf, elemBytes...)
		}
		buf = append(buf, ']')
		return buf, nil

	default:
		return json.Marshal(v)
	}
}
