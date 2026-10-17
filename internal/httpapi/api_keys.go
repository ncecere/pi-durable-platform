package httpapi

import (
	"net/http"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/apikeys"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// The caller's personal API keys (api/openapi.yaml, tag api-keys).
// internal/apikeys authorizes every call; a key's secret is only ever
// returned by create.

// apiKeyRoutes: the caller's API keys.
func (a *api) apiKeyRoutes() []route {
	return []route{
		{"GET", "/v1/me/api-keys", a.session(a.listMyApiKeys)},
		{"POST", "/v1/me/api-keys", a.session(a.createMyApiKey)},
		{"DELETE", "/v1/me/api-keys/{keyId}", a.session(a.revokeMyApiKey)},
	}
}

func toAPIKey(k dbgen.ApiKey, now time.Time) apitypes.ApiKey {
	exp := k.ExpiresAt
	return apitypes.ApiKey{
		Id: k.ID, Name: k.Name, Prefix: k.Prefix, Status: apitypes.ApiKeyStatus(apikeys.Status(k, now)),
		CreatedAt: k.CreatedAt, ExpiresAt: &exp, LastUsedAt: k.LastUsedAt, RevokedAt: k.RevokedAt,
	}
}

func (a *api) listMyApiKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := a.APIKeys.List(r.Context(), a.actor(r))
	now := time.Now()
	writeList(w, r, keys, err, func(k dbgen.ApiKey) apitypes.ApiKey { return toAPIKey(k, now) })
}

func (a *api) createMyApiKey(w http.ResponseWriter, r *http.Request) {
	var in apitypes.ApiKeyCreate
	if !httpx.Decode(w, r, &in) {
		return
	}
	days := int32(0)
	setIf(&days, in.ExpiresInDays)
	if in.ExpiresInDays != nil && days == 0 {
		days = -1 // explicit 0 is invalid, not the default
	}
	c, err := a.APIKeys.Create(r.Context(), a.actor(r), apikeys.CreateInput{Name: in.Name, Days: days})
	if failed(w, r, err) {
		return
	}
	httpx.JSON(w, http.StatusCreated, apitypes.ApiKeyCreated{Key: toAPIKey(c.Key, time.Now()), Secret: c.Secret})
}

func (a *api) revokeMyApiKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "keyId")
	if !ok {
		return
	}
	writeOK(w, r, a.APIKeys.Revoke(r.Context(), a.actor(r), id))
}
