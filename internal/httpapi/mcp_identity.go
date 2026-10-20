package httpapi

import (
	"net/http"

	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/mcpidentity"
)

// The public keys MCP servers verify the platform's identity tokens with
// (docs/mcp-identity.md): a JWK Set, unauthenticated and cacheable. Like
// OAuth Connect's metadata it is a protocol document, not the /v1 envelope,
// so it is outside api/openapi.yaml (protocolRoutes).

func (a *api) mcpIdentityRoutes() []route {
	return []route{{"GET", mcpidentity.JWKSPath, http.HandlerFunc(a.mcpIdentityJWKS)}}
}

func (a *api) mcpIdentityJWKS(w http.ResponseWriter, r *http.Request) {
	if a.MCPIdentity == nil {
		httpx.Error(w, http.StatusNotFound, "not_found", "Not found")
		return
	}
	a.MCPIdentity.ServeJWKS(w, r)
}
