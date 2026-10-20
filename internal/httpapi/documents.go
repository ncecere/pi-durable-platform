package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/agenthost/launcher"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
)

// Documents for apps (docs/custom-agents.md): a conversation document the
// agent's author exposed, read through the agent's runtime with exactly the
// access reading the thread needs. The runtime is not trusted: its answer is
// read bounded and must be a document of the kind asked for.

// documentKindRE is what an exposed document's kind may look like (the
// runtime's EXPOSED_KIND).
var documentKindRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// maxDocumentReply bounds what the platform reads of a runtime's answer: the
// largest document an author may expose (1 MiB of JSON) and its envelope.
const maxDocumentReply = 1<<20 + 16<<10

// documentPath checks the agent, thread and kind of a document route and
// returns them; false when it has written an error.
func documentPath(w http.ResponseWriter, r *http.Request) (uuid.UUID, string, string, bool) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return id, "", "", false
	}
	threadID := r.PathValue("threadId")
	if !threadIDRE.MatchString(threadID) {
		httpx.Error(w, http.StatusBadRequest, "invalid_thread_id", "Thread IDs are 1-64 letters, digits, dashes or underscores")
		return id, "", "", false
	}
	kind := r.PathValue("kind")
	if !documentKindRE.MatchString(kind) {
		httpx.Error(w, http.StatusBadRequest, "invalid_document_kind", "A document kind is 1-128 letters, digits and . _ : -, starting with a letter or digit")
		return id, "", "", false
	}
	return id, threadID, kind, true
}

// getAgentThreadDocument reads an exposed document of a thread: authorized as
// reading the thread (Spec: may use the agent; ThreadAccess: not another's
// private thread).
func (a *api) getAgentThreadDocument(w http.ResponseWriter, r *http.Request) {
	id, threadID, kind, ok := documentPath(w, r)
	if !ok {
		return
	}
	spec, err := a.Agents.Spec(r.Context(), a.actor(r), id)
	if failed(w, r, err) {
		return
	}
	if failed(w, r, a.Agents.ThreadAccess(r.Context(), a.actor(r), id, threadID)) {
		return
	}
	reply := a.relayDocument(r, spec, threadID, kind)
	reply.write(w)
}

// adminReadAgentThreadDocument is a platform admin's reasoned read of an
// exposed document of any thread: recorded as thread.admin_read (with the
// document's kind and the outcome: read, not_found or failed) in the agent's
// team's audit log before it is answered, like a read of the conversation
// itself, whether it found a document or not. A thread the agent doesn't
// have and a kind it doesn't expose (any 4xx of the runtime but a conflict)
// answer the same 404 document_not_found, so a read reveals nothing about
// which threads or kinds exist.
func (a *api) adminReadAgentThreadDocument(w http.ResponseWriter, r *http.Request) {
	id, threadID, kind, ok := documentPath(w, r)
	if !ok {
		return
	}
	var in apitypes.AdminThreadRead
	if !httpx.Decode(w, r, &in) {
		return
	}
	read, err := a.Agents.AdminRead(r.Context(), a.actor(r), id, threadID, in.Reason)
	if failed(w, r, err) {
		return
	}
	read.Entry.Metadata["document"] = kind
	reply := a.relayDocument(r, read.Spec, threadID, kind)
	outcome := "read"
	switch {
	case reply.status == http.StatusOK:
	case reply.status >= 400 && reply.status < 500 && reply.status != http.StatusConflict && reply.status != http.StatusTooManyRequests:
		outcome = "not_found"
		reply = documentReply{status: http.StatusNotFound, raw: errorJSON("document_not_found", "No such document"), contentType: "application/json"}
	default:
		outcome = "failed"
	}
	read.Entry.Metadata["outcome"] = outcome
	if failed(w, r, a.Agents.RecordAdminRead(context.WithoutCancel(r.Context()), read)) {
		return
	}
	reply.write(w)
}

// documentReply is what the platform answers for a document read.
type documentReply struct {
	status int
	doc    *apitypes.ThreadDocument
	// raw is a runtime's (or the relay's) error answer, passed on.
	raw         []byte
	contentType string
}

func (d documentReply) write(w http.ResponseWriter) {
	if d.doc != nil {
		httpx.JSON(w, d.status, d.doc)
		return
	}
	if d.raw == nil {
		httpx.Error(w, d.status, "agent_unavailable", "The agent did not answer. Try again shortly.")
		return
	}
	w.Header().Set("Content-Type", d.contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(d.status)
	_, _ = w.Write(d.raw)
}

// relayDocument asks the agent's runtime (waking it) for the thread's
// document. A 200 must be a document of that kind within the bound (else
// 502); an error answer is passed on as it came, when it is small JSON.
func (a *api) relayDocument(r *http.Request, spec launcher.Spec, threadID, kind string) documentReply {
	get := r.Clone(r.Context())
	get.Method, get.Body, get.ContentLength, get.Header = http.MethodGet, http.NoBody, 0, http.Header{}
	get.URL.RawQuery = ""
	held := &boundedResponse{header: http.Header{}, limit: maxDocumentReply}
	a.Agents.Host().Proxy(held, get, spec, "/threads/"+threadID+"/documents/"+url.PathEscape(kind))
	switch {
	case held.status == 0:
		return documentReply{status: http.StatusBadGateway}
	case held.over:
		return documentReply{status: http.StatusBadGateway, raw: errorJSON("document_too_large", "The agent's document is larger than the platform relays"),
			contentType: "application/json"}
	case held.status != http.StatusOK:
		if !json.Valid(held.body.Bytes()) {
			return documentReply{status: http.StatusBadGateway}
		}
		return documentReply{status: held.status, raw: held.body.Bytes(), contentType: "application/json"}
	}
	var body struct {
		Data *apitypes.ThreadDocument `json:"data"`
	}
	if err := json.Unmarshal(held.body.Bytes(), &body); err != nil || body.Data == nil || body.Data.Kind != kind || body.Data.Value == nil {
		return documentReply{status: http.StatusBadGateway, raw: errorJSON("agent_unavailable", "The agent's answer was not the document"), contentType: "application/json"}
	}
	return documentReply{status: http.StatusOK, doc: body.Data}
}

func errorJSON(code, message string) []byte {
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": message}})
	return b
}

// boundedResponse keeps a relayed response in memory, up to limit bytes;
// over says more came (the rest is dropped).
type boundedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
	limit  int
	over   bool
}

func (b *boundedResponse) Header() http.Header { return b.header }

func (b *boundedResponse) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}

func (b *boundedResponse) Write(p []byte) (int, error) {
	b.WriteHeader(http.StatusOK)
	if room := b.limit - b.body.Len(); len(p) > room {
		b.over = true
		if room > 0 {
			b.body.Write(p[:room])
		}
		return len(p), nil
	}
	return b.body.Write(p)
}

func (b *boundedResponse) Flush() {}
