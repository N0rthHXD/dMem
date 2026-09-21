package httpapi

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/N0rthHXD/dMem/internal/gateway"
)

// PageQuery parameter names shared by list routes.
const (
	limitQueryParam  = "limit"
	cursorQueryParam = "cursor"
)

// ParsePageRequest implements the shared pagination convention: an optional
// integer limit between 1 and 200 (default 50) and an optional opaque cursor of
// at most 512 characters. It returns a gateway page request ready for a module
// list query.
func ParsePageRequest(values url.Values) (gateway.PageRequest, error) {
	request := gateway.PageRequest{Limit: defaultPageLimit}
	if raw := values.Get(limitQueryParam); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < minPageLimit || parsed > maxPageLimit {
			return gateway.PageRequest{}, &requestError{
				status:  http.StatusUnprocessableEntity,
				code:    codeInvalidRequest,
				message: "limit must be an integer between 1 and 200",
			}
		}
		request.Limit = parsed
	}
	cursor := values.Get(cursorQueryParam)
	if len([]rune(cursor)) > maxCursorRunes {
		return gateway.PageRequest{}, &requestError{
			status:  http.StatusUnprocessableEntity,
			code:    codeInvalidRequest,
			message: "cursor is too long",
		}
	}
	request.Cursor = cursor
	return request, nil
}
