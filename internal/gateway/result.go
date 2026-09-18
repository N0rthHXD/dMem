package gateway

// WriteResult carries the metadata common to every state-changing gateway
// operation. Replay is true when the result was served from a previously
// committed idempotent write rather than a new mutation.
type WriteResult struct {
	OperationID string
	Replay      bool
}
