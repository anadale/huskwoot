package api

import (
	"net/http"

	rootapi "github.com/anadale/huskwoot/api"
)

// OpenAPISpec returns the embedded OpenAPI specification as bytes.
// Exported for tests to verify YAML validity and coverage of all registered routes.
func OpenAPISpec() []byte {
	return rootapi.Spec()
}

// openapiHandler serves the embedded OpenAPI YAML. The route is public:
// clients and SDK generators benefit from schema access without a bearer token,
// and the specification contains no secrets.
func (s *Server) openapiHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rootapi.Spec())
}
