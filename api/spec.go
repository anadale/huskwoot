// Package api provides the embedded OpenAPI specification for Huskwoot.
// A separate package is required because go:embed cannot ascend above
// the source file's directory: the spec lives at the repository root
// so it can be published and linted by external tools.
package api

import _ "embed"

//go:embed openapi.yaml
var spec []byte

// Spec returns the raw OpenAPI specification contents.
// The returned slice must not be mutated.
func Spec() []byte {
	return spec
}
