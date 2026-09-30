package agent

import (
	"errors"
	"net/url"
	"strings"
)

func safeEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[invalid endpoint URL]"
	}
	u.User = nil
	return u.String()
}

// endpointFailure preserves errors.Is/As without exposing HTTP client URLs
// through the wrapped cause's Error method.
type endpointFailure struct {
	detail string
	cause  error
}

func (e *endpointFailure) Error() string { return e.detail }
func (e *endpointFailure) Unwrap() error { return e.cause }

func endpointError(endpoint string, err error) error {
	if err == nil {
		return nil
	}
	detail := strings.ReplaceAll(err.Error(), endpoint, safeEndpoint(endpoint))
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		detail = strings.ReplaceAll(detail, urlErr.URL, safeEndpoint(urlErr.URL))
	}
	return &endpointFailure{detail: detail, cause: err}
}
