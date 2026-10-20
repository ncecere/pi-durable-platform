package mcp

import (
	"context"
	"errors"
	"regexp"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/mcpidentity"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// threadIDRE is a thread ID (internal/agents).
var threadIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// identityToken signs the identity token of a request the agent sends
// target t (docs/mcp-identity.md). Who the call is for comes from the
// platform's records only: thread is just a name the runtime gave, looked up
// among this agent's threads, so it can't name another agent's thread (that
// is an unknown thread: shared, nobody), and nothing the runtime sends
// becomes a person.
//
//   - A personal workspace's agent: the workspace's owner.
//   - Otherwise a private thread a person started: that person.
//   - A shared thread, an unknown thread, no thread, a private thread a
//     service account started: nobody.
//
// The person must be active (not suspended, not denied by the group
// re-check) and, outside a personal workspace, still a member of the
// agent's team; otherwise nobody is named.
func (s *Service) identityToken(ctx context.Context, ag Agent, t target, thread, tool string) (string, error) {
	c, err := s.q.MCPIdentityCaller(ctx, dbgen.MCPIdentityCallerParams{AgentID: ag.ID, ThreadID: thread})
	if err != nil {
		return "", err
	}
	claims := mcpidentity.Claims{Agent: ag.ID, AgentName: c.AgentName, Tool: tool,
		Team: mcpidentity.Team{ID: c.TeamID, Slug: c.TeamSlug, Kind: c.TeamKind}, ThreadVisibility: "none"}
	if thread != "" {
		// A thread the platform has no record of is shared (Threads, DESIGN.md).
		claims.ThreadVisibility = "shared"
		if c.ThreadVisibility != nil {
			claims.ThreadVisibility = *c.ThreadVisibility
		}
	}
	personal := c.TeamKind == "personal"
	var who uuid.NullUUID
	switch {
	case personal:
		who = c.PersonalOwner
	case claims.ThreadVisibility == "private":
		who = c.ThreadCreator // NULL when a service account started it
	}
	if who.Valid {
		p, err := s.q.MCPIdentityPerson(ctx, dbgen.MCPIdentityPersonParams{UserID: who.UUID, TeamID: c.TeamID})
		switch {
		case errors.Is(store.NotFound(err), store.ErrNotFound):
		case err != nil:
			return "", err
		case p.Status == store.UserActive && p.GroupDeniedAt == nil && (personal || p.Member):
			claims.Person = &mcpidentity.Person{Issuer: p.OIDCIssuer, Subject: p.OIDCSubject}
			if t.email {
				claims.Person.Email = p.Email
			}
			// Work outside any thread (background, a future trigger) is
			// work nobody is present for.
			claims.PersonPresent = thread != ""
		}
	}
	aud := t.audience
	if aud == "" {
		aud = t.url
	}
	return s.Identity.Sign(ctx, aud, claims)
}
