package agent

import (
	"errors"
	"net/url"
	"strings"
	"unicode"
)

// SafeEndpoint is raw without the parts that can carry a credential: its
// userinfo, its query and fragment, and any path segment that reads as a key.
// ModelInfo keeps only the host so credentials in the URL never reach an
// artifact; an error message is an artifact too, through stderr, --jsonl and
// the run history. A gateway's ?api_key=... or /sk-.../v1 passed through
// with only the userinfo cleared. The path is kept otherwise: a missing /v1
// is the commonest wrong endpoint, and the message has to show it. A
// comma-separated list (SearXNG's pool) is redacted address by address.
func SafeEndpoint(raw string) string {
	if strings.Contains(raw, ",") {
		parts := strings.Split(raw, ",")
		for i, p := range parts {
			parts[i] = SafeEndpoint(strings.TrimSpace(p))
		}
		return strings.Join(parts, ",")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "[invalid endpoint URL]"
	}
	u.User = nil
	if u.RawQuery != "" || u.ForceQuery {
		u.RawQuery, u.ForceQuery = "redacted", false
	}
	u.Fragment, u.RawFragment = "", ""
	segs := strings.Split(u.Path, "/")
	for i, s := range segs {
		if secretLike(s) {
			segs[i] = "redacted"
		}
	}
	u.Path, u.RawPath = strings.Join(segs, "/"), ""
	return u.String()
}

// secretLike reports whether a path segment reads as a key or a token: a
// known key prefix, or a long run of letters and digits mixed.
func secretLike(s string) bool {
	if strings.HasPrefix(s, "sk-") || strings.HasPrefix(s, "key-") {
		return true
	}
	if len(s) < 20 {
		return false
	}
	return strings.IndexFunc(s, unicode.IsLetter) >= 0 && strings.IndexFunc(s, unicode.IsDigit) >= 0
}

// endpointFailure preserves errors.Is/As without exposing HTTP client URLs
// through the wrapped cause's Error method.
type endpointFailure struct {
	detail string
	cause  error
}

func (e *endpointFailure) Error() string { return e.detail }
func (e *endpointFailure) Unwrap() error { return e.cause }

// endpointError redacts endpoint wherever err's text names it. The HTTP
// client's own url.Error masks only the password, so its URL is redacted too.
func endpointError(endpoint string, err error) error {
	if err == nil {
		return nil
	}
	detail := strings.ReplaceAll(err.Error(), endpoint, SafeEndpoint(endpoint))
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		detail = strings.ReplaceAll(detail, urlErr.URL, SafeEndpoint(urlErr.URL))
	}
	// A request URL the error names is the endpoint with a route spliced in
	// before its query, so the endpoint is no substring of it: the secrets
	// themselves are taken out wherever they appear.
	for _, secret := range endpointSecrets(endpoint) {
		detail = strings.ReplaceAll(detail, secret, "redacted")
	}
	return &endpointFailure{detail: detail, cause: err}
}

// endpointSecrets are the parts of endpoint SafeEndpoint redacts: the
// userinfo, the query's values and the path segments that read as keys.
func endpointSecrets(endpoint string) []string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil
	}
	var out []string
	add := func(v string) {
		// A short value ("v1", "true") is no key, and replaced everywhere it
		// would garble the message.
		if len(v) >= 8 {
			out = append(out, v, url.QueryEscape(v))
		}
	}
	p, _ := u.User.Password()
	add(p)
	add(u.User.Username())
	for _, vs := range u.Query() {
		for _, v := range vs {
			add(v)
		}
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if secretLike(seg) {
			add(seg)
		}
	}
	return out
}
