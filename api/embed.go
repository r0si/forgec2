// Package api holds the OpenAPI specification and embeds it in the server binary.
//
// The docs endpoints used to resolve "api/openapi.yaml" relative to the process working
// directory (or the executable's directory), so they only worked when the server happened to
// be started from a checkout - and the release archives ship the binary, config and start
// script but NOT the api/ directory, so GET /api/docs/openapi.yaml answered 500 in every
// packaged build. Embedding removes the filesystem from the question.
package api

import _ "embed"

// OpenAPISpec is api/openapi.yaml, the single source that cmd/checkopenapi and the generated
// frontend schema also read.
//
//go:embed openapi.yaml
var OpenAPISpec []byte
