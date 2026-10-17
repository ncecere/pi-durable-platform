package httpapi

import (
	"net/http"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
)

// The request and response plumbing that handlers share. Success responses
// are {"data": …}; errors are {"error": {"code", "message"}} (httpx).

// decodeRevised reads the If-Match revision of a write, then the JSON body.
// A missing revision is 428, a malformed one 400 (see ifMatch), and a bad
// body 400.
func decodeRevised[T any](w http.ResponseWriter, r *http.Request) (T, int64, bool) {
	var in T
	rev, ok := ifMatch(w, r)
	if !ok || !httpx.Decode(w, r, &in) {
		return in, 0, false
	}
	return in, rev, true
}

// failed writes err as the response, if there is one, and reports whether
// it did.
func failed(w http.ResponseWriter, r *http.Request, err error) bool {
	if err != nil {
		httpx.Fail(w, r, err)
		return true
	}
	return false
}

// writeRevised writes a resource with its revision as the ETag, which
// clients send back in If-Match.
func writeRevised(w http.ResponseWriter, status int, revision int64, body any) {
	setETag(w, revision)
	httpx.JSON(w, status, body)
}

// writeList writes the items converted for the API (an empty list, never
// null), or err.
func writeList[T, R any](w http.ResponseWriter, r *http.Request, items []T, err error, conv func(T) R) {
	if failed(w, r, err) {
		return
	}
	out := make([]R, len(items))
	for i, v := range items {
		out[i] = conv(v)
	}
	httpx.JSON(w, http.StatusOK, out)
}

// writeOK writes {"ok": true}, or err.
func writeOK(w http.ResponseWriter, r *http.Request, err error) {
	if failed(w, r, err) {
		return
	}
	httpx.JSON(w, http.StatusOK, apitypes.Ok{Ok: true})
}
