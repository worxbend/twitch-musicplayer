package server

import (
	"net/http"
	"net/url"
	"strings"
)

// newOriginPolicy reports whether a browser origin may use the API: one of
// allowed, or the origin of baseURL, where the backend serves its own pages
// such as /swagger. Entries are expected lowercase, as config normalizes them.
func newOriginPolicy(allowed []string, baseURL string) func(origin string) bool {
	set := make(map[string]struct{}, len(allowed)+1)
	for _, origin := range allowed {
		set[origin] = struct{}{}
	}
	if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
		set[strings.ToLower(u.Scheme+"://"+u.Host)] = struct{}{}
	}
	return func(origin string) bool {
		_, ok := set[strings.ToLower(origin)]
		return ok
	}
}

// rejectForeignWrites answers 403 to a state-changing request sent from a
// page at a refused origin. CORS cannot stop these: a no-cors POST still
// reaches the handler, and the browser only hides the response. Requests
// without an Origin header come from non-browser clients and pass.
func rejectForeignWrites(allowOrigin func(origin string) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if isWrite(r.Method) && origin != "" && !allowOrigin(origin) {
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isWrite reports whether method can change server state.
func isWrite(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}
