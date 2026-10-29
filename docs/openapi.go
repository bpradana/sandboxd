// Package docs embeds the generated OpenAPI specification served by the API.
package docs

import _ "embed"

//go:embed swagger.json
var SwaggerSpec []byte
