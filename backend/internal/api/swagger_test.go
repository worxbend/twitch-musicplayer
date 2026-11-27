package api

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

var (
	assetTag      = regexp.MustCompile(`(?s)<(?:link|script)\b[^>]*>`)
	cdnURL        = regexp.MustCompile(`(?:href|src)="(https://unpkg\.com/[^"]*)"`)
	pinnedVersion = regexp.MustCompile(`^https://unpkg\.com/swagger-ui-dist@\d+\.\d+\.\d+/`)
	sriHash       = regexp.MustCompile(`\bintegrity="sha384-[A-Za-z0-9+/]{64}"`)
	anonCORS      = regexp.MustCompile(`\bcrossorigin="anonymous"`)
)

// The Swagger page runs on the backend origin, which can write to the queue
// API, so every CDN asset must be an exact version locked by an SRI hash.
func TestSwaggerAssetsArePinnedWithIntegrity(t *testing.T) {
	rec := httptest.NewRecorder()
	ServeSwagger(rec, httptest.NewRequest(http.MethodGet, "/swagger", nil))

	cdnTags := 0
	for _, tag := range assetTag.FindAllString(rec.Body.String(), -1) {
		m := cdnURL.FindStringSubmatch(tag)
		if m == nil {
			continue
		}
		cdnTags++
		if !pinnedVersion.MatchString(m[1]) {
			t.Errorf("%s: not pinned to an exact swagger-ui-dist x.y.z", m[1])
		}
		if !sriHash.MatchString(tag) {
			t.Errorf("%s: missing integrity=\"sha384-...\"", m[1])
		}
		if !anonCORS.MatchString(tag) {
			t.Errorf("%s: missing crossorigin=\"anonymous\"", m[1])
		}
	}
	if cdnTags != 3 {
		t.Fatalf("found %d unpkg.com asset tags, want 3 (css, bundle, preset)", cdnTags)
	}
}
