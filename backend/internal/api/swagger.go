package api

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.yaml
var openAPISpec []byte

// swaggerUIBase pins swagger-ui-dist to one exact release. The page runs on
// the backend origin, which may write to the queue API, so each asset also
// carries a Subresource Integrity hash. Bumping the version means
// recomputing all three hashes:
//
//	curl -fsSL <url> | openssl dgst -sha384 -binary | openssl base64 -A
const swaggerUIBase = "https://unpkg.com/swagger-ui-dist@5.35.0/"

const swaggerHTML = `<!DOCTYPE html>
<html>
<head>
  <title>Lofi Radio — API Docs</title>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="stylesheet" href="` + swaggerUIBase + `swagger-ui.css"
    integrity="sha384-WlYgdmXYOOBWL5apwjDEZwN+U3HmXSSu5R6V5aGxEcf7z+KuRNwBhoxKVN1i3D5I" crossorigin="anonymous">
  <style>body { margin: 0; }</style>
</head>
<body>
<div id="swagger-ui"></div>
<script src="` + swaggerUIBase + `swagger-ui-bundle.js"
  integrity="sha384-gjRm0bEPMeTmMh5jT2cHccIwCUZSTd8YC+j0d0sXw0MdGEfScduGpSyF3ZSSLkX9" crossorigin="anonymous"></script>
<script src="` + swaggerUIBase + `swagger-ui-standalone-preset.js"
  integrity="sha384-My2aDM4r2Mbm3ybHcubKm9O9U8FEjvF/O5nGvE9YK5dzqOTbWEKa79RPJ1krdMaF" crossorigin="anonymous"></script>
<script>
window.onload = function() {
  SwaggerUIBundle({
    url: "/openapi.yaml",
    dom_id: "#swagger-ui",
    presets: [SwaggerUIBundle.presets.apis, SwaggerUIStandalonePreset],
    layout: "StandaloneLayout",
    deepLinking: true,
  });
};
</script>
</body>
</html>`

// ServeSwagger serves the Swagger UI HTML page at GET /swagger.
func ServeSwagger(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(swaggerHTML)) //nolint:errcheck
}

// ServeOpenAPISpec serves the raw OpenAPI YAML at GET /openapi.yaml.
func ServeOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	w.Write(openAPISpec) //nolint:errcheck
}
