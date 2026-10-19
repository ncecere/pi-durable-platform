package doctor

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/auth"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// groupRecheck reports whether the group re-check (DESIGN.md "Sign-in and
// group membership") has anyone to check: with OIDC_ALLOWED_GROUPS and the
// re-check on, the recently active people (the ones the re-check considers)
// with and without a stored IdP refresh token. A person without one is never
// re-checked: they keep access after leaving the allowed group until they sign
// in again. Nothing is reported when the group rule or the re-check is off.
func (r *runner) groupRecheck(ctx context.Context, pool *pgxpool.Pool) {
	o := r.cfg.OIDC
	if !o.Enabled() || len(o.AllowedGroups) == 0 {
		return
	}
	c := Check{Group: "auth", Name: "group re-check", Status: Info}
	if o.GroupRecheckInterval <= 0 {
		c.Detail = "off (OIDC_GROUP_RECHECK_INTERVAL=0): OIDC_ALLOWED_GROUPS applies at sign-in only"
		r.add(c)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, r.opts.Timeout)
	defer cancel()
	start := time.Now()
	with, without, err := auth.RecheckPeople(ctx, dbgen.New(pool), o, time.Now())
	c.DurationMs = millis(time.Since(start))
	switch {
	case err != nil:
		c.Detail = "skipped: the people the re-check applies to can't be counted: " + err.Error()
	case without > 0:
		c.Status = Warn
		c.Detail = fmt.Sprintf("%d of %d recently active people have no stored identity provider refresh token (%d have one): "+
			"they are never re-checked and keep access after leaving the allowed group until they sign in again; %s",
			without, with+without, with, r.noTokenCause())
		c.Fix = "Add offline_access to OIDC_SCOPES and allow refresh tokens for this client in the identity provider " +
			"(authentik 2024.2+: add the offline_access scope mapping to the provider; Keycloak: keep the offline_access " +
			"scope on the client). Then people sign in again: each sign-in stores a token"
	case with == 0:
		c.Detail = "no recently active people yet (a sign-in, session, API key or connected app in use within 30 days)"
	default:
		c.Detail = fmt.Sprintf("all %d recently active people have a stored identity provider refresh token, so every one is re-checked every %s", with, o.GroupRecheckInterval)
	}
	r.add(c)
}

// noTokenCause says why people may lack a token.
func (r *runner) noTokenCause() string {
	if !slices.Contains(r.cfg.OIDC.Scopes, "offline_access") {
		return "OIDC_SCOPES does not include offline_access, so the provider issues no refresh token"
	}
	return "OIDC_SCOPES has offline_access, so either the provider issues no refresh token to this client " +
		"or these people signed in before the re-check was on"
}
