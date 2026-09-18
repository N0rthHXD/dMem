package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// CanonicalHash returns a stable, protocol-independent hash of a command or
// query value. The value must be a transport-free command payload: it must not
// contain a CallContext, an idempotency key, or a request identifier.
//
// encoding/json emits struct fields in declaration order and sorts map keys, so
// the same semantic value produces the same hash regardless of HTTP method,
// route, header order, or field construction order.
func CanonicalHash(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("gateway: canonical hash: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
