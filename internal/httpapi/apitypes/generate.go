// Package apitypes contains Go types generated from api/openapi.yaml.
// Handlers build responses from these types so the server cannot drift from
// the published contract. Regenerate with `go generate ./...`.
package apitypes

//go:generate go tool oapi-codegen -config oapi-codegen.yaml ../../../api/openapi.yaml
