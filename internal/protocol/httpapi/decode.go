package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

// decodeBody enforces the route body policy and strictly decodes one JSON
// object into dst.
func (ex *exchange) decodeBody(dst any) error {
	if err := requireJSONContentType(ex.request); err != nil {
		return err
	}
	body, err := readBoundedBody(ex.writer, ex.request, ex.route.MaxBodyBytes)
	if err != nil {
		return err
	}
	return decodeStrict(body, dst)
}

func (ex *exchange) decodeContractBody(schema string, dst any) error {
	if err := requireJSONContentType(ex.request); err != nil {
		return err
	}
	body, err := readBoundedBody(ex.writer, ex.request, ex.route.MaxBodyBytes)
	if err != nil {
		return err
	}
	if err := toolcontract.DecodeStrict(schema, body, dst); err != nil {
		return invalidContractBody()
	}
	return nil
}

func invalidContractBody() error {
	return &requestError{status: http.StatusBadRequest, code: codeInvalidJSON, message: "request body does not match the contract"}
}

// readBoundedBody reads the request body under a hard byte limit. The limit is
// the route's frozen body limit; a route that accepts no body is rejected before
// any read happens.
func readBoundedBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, &requestError{
			status:  http.StatusBadRequest,
			code:    codeUnexpectedBody,
			message: "request body is not accepted",
		}
	}
	if r.ContentLength > limit {
		return nil, &requestError{
			status:  http.StatusRequestEntityTooLarge,
			code:    codeRequestTooLarge,
			message: "request body exceeds the route limit",
		}
	}
	limited := http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, &requestError{
				status:  http.StatusRequestEntityTooLarge,
				code:    codeRequestTooLarge,
				message: "request body exceeds the route limit",
			}
		}
		return nil, &requestError{
			status:  http.StatusBadRequest,
			code:    codeInvalidJSON,
			message: "request body could not be read",
		}
	}
	return body, nil
}

// decodeStrict decodes exactly one top-level JSON object. It rejects an empty
// body, a non-object root value, unknown fields, field type mismatches, and a
// second trailing JSON value. Numbers are decoded with json.Number so integer
// values are never rounded through float64.
func decodeStrict(body []byte, dst any) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errNotSingleObject
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		return &requestError{
			status:  http.StatusBadRequest,
			code:    codeInvalidJSON,
			message: jsonDecodeMessage(err),
		}
	}
	if err := decoder.Decode(new(struct{})); !errors.Is(err, io.EOF) {
		return &requestError{
			status:  http.StatusBadRequest,
			code:    codeInvalidJSON,
			message: "request body must contain a single JSON value",
		}
	}
	return nil
}

// errNotSingleObject is shared by the empty-body and non-object cases.
var errNotSingleObject = &requestError{
	status:  http.StatusBadRequest,
	code:    codeInvalidJSON,
	message: "request body must be a single JSON object",
}

// jsonDecodeMessage maps a decoder failure to a bounded, safe message. It never
// echoes the request body or a raw decoder error string.
func jsonDecodeMessage(err error) string {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		if typeErr.Field != "" {
			return "field " + typeErr.Field + " has an invalid type"
		}
		return "request body has a value of an invalid type"
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return "request body is not valid JSON"
	}
	return "request body is not valid JSON"
}

// decodeRawValue decodes an arbitrary embedded JSON value with json.Number
// semantics. It is used for user-data fields such as content.value.
func decodeRawValue(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, &requestError{
			status:  http.StatusBadRequest,
			code:    codeInvalidJSON,
			message: "content.value is not valid JSON",
		}
	}
	return value, nil
}
