package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"

	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/oauth"
	"github.com/ncecere/pi-durable-platform/internal/secrets"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// Re-checking IdP group membership (DESIGN.md "Sign-in and group
// membership").
//
// OIDC_ALLOWED_GROUPS is applied at sign-in. Afterwards the platform keeps
// the refresh token the IdP issued (sealed; needs the offline_access scope)
// and, every OIDC_GROUP_RECHECK_INTERVAL for each recently active person,
// exchanges it for fresh claims (the ID token, else userinfo) and applies the
// same rule. A person who is no longer in an allowed group, or whom the IdP
// refuses to vouch for, is denied: users.group_denied_at is set (separate
// from the admin's suspension), their sessions and connected apps are
// revoked, and everywhere the platform asks whether a person is active
// (store.IsUserActive) they now are not: sessions, API keys, app tokens.
// Their data and teams stay; signing in again (which applies the rule
// afresh) clears the flag. If the IdP can't be asked (unreachable, or the
// groups claim is missing from its answer) nothing changes until
// OIDC_GROUP_RECHECK_GRACE has passed since its last answer.

// Why a person was denied (users.group_denied_reason).
const (
	DeniedNotInGroup  = "not_in_group"
	DeniedIdPRefused  = "idp_refused"
	DeniedUnreachable = "idp_unreachable"
)

const (
	recheckBatch        = 50
	recheckWorkers      = 4
	recheckTimeout      = 20 * time.Second
	recheckActiveWindow = 30 * 24 * time.Hour
)

// SetSecretBox gives the service the box that seals the IdP refresh tokens it
// keeps (ENCRYPTION_KEY). Without one no token is kept and nobody is
// re-checked.
func (s *Service) SetSecretBox(b *secrets.Box) { s.box = b }

func idpTokenAD(userID uuid.UUID) []byte { return []byte("idp_refresh_token:" + userID.String()) }

// isBootstrap: the person is the bootstrap admin, exempt from the group
// rule (the platform can't be locked out of its first admin).
func (s *Service) isBootstrap(subject string) bool {
	return s.cfg.OIDC.BootstrapAdminSubject != "" && subject == s.cfg.OIDC.BootstrapAdminSubject
}

// groupAllowed reports whether groups include one of OIDC_ALLOWED_GROUPS.
func (s *Service) groupAllowed(groups []string) bool {
	return slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(s.cfg.OIDC.AllowedGroups, g) })
}

// groupRecheckOn: groups are re-checked (OIDC with allowed groups, an
// interval, and a box to seal tokens with).
func (s *Service) groupRecheckOn() bool {
	o := s.cfg.OIDC
	return o.Enabled() && len(o.AllowedGroups) > 0 && o.GroupRecheckInterval > 0 && s.box != nil
}

// keepIdPToken stores the refresh token of an OIDC sign-in (sealed), or
// forgets the previous one when this sign-in brought none. The bootstrap
// admin is not re-checked, so no token is kept for them.
func (s *Service) keepIdPToken(ctx context.Context, q *dbgen.Queries, user dbgen.User, li loginIdentity) error {
	if li.Method != "oidc" || !s.groupRecheckOn() {
		return nil
	}
	if li.RefreshToken == "" || s.isBootstrap(li.Subject) {
		return q.DeleteIdPToken(ctx, user.ID)
	}
	enc, err := s.box.Seal([]byte(li.RefreshToken), idpTokenAD(user.ID))
	if err != nil {
		return err
	}
	return q.UpsertIdPToken(ctx, dbgen.UpsertIdPTokenParams{UserID: user.ID, RefreshTokenEnc: enc})
}

// restoreGroupAccess clears a person's group denial (they signed in again,
// or the group rule was removed) and audits user.group_restored.
func (s *Service) restoreGroupAccess(ctx context.Context, q *dbgen.Queries, user dbgen.User, via, reqID, ip string) (dbgen.User, error) {
	before := ""
	if user.GroupDeniedReason != nil {
		before = *user.GroupDeniedReason
	}
	out, err := q.RestoreUserGroup(ctx, user.ID)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return user, nil
	} else if err != nil {
		return user, err
	}
	e := audit.Entry{ActorUserID: user.ID, Action: "user.group_restored", TargetType: "user", TargetID: user.ID.String(),
		Metadata: map[string]any{"via": via, "deniedReason": before}, RequestID: reqID, ClientIP: ip}
	if via != "sign_in" {
		e.ActorKind, e.ActorUserID = audit.ActorSystem, uuid.Nil
	}
	return out, audit.Record(ctx, q, e)
}

// denyGroupAccess denies a person (in the transaction q belongs to): the
// flag, their sessions and connected apps, the refresh token kept for them,
// and the audit entry user.group_denied. Already denied: nothing.
func (s *Service) denyGroupAccess(ctx context.Context, q *dbgen.Queries, userID uuid.UUID, reason string) error {
	if _, err := q.DenyUserGroup(ctx, dbgen.DenyUserGroupParams{ID: userID, Reason: &reason}); errors.Is(store.NotFound(err), store.ErrNotFound) {
		return q.DeleteIdPToken(ctx, userID)
	} else if err != nil {
		return err
	}
	if err := q.DeleteUserSessions(ctx, userID); err != nil {
		return err
	}
	apps, err := oauth.RevokeUserGrants(ctx, q, userID, oauth.ReasonDenied, "")
	if err != nil {
		return err
	}
	if err := q.DeleteIdPToken(ctx, userID); err != nil {
		return err
	}
	return audit.Record(ctx, q, audit.Entry{ActorKind: audit.ActorSystem, Action: "user.group_denied", TargetType: "user", TargetID: userID.String(),
		Metadata: map[string]any{"reason": deniedWords[reason], "code": reason, "connectedAppsRevoked": apps}})
}

// deniedWords say why, for the audit log's reason line.
var deniedWords = map[string]string{
	DeniedNotInGroup:  "No longer in a group allowed to sign in",
	DeniedIdPRefused:  "The identity provider no longer vouches for them",
	DeniedUnreachable: "The identity provider could not confirm their groups within the grace period",
}

// RunGroupRecheck re-checks people's groups until ctx ends. Without
// OIDC_ALLOWED_GROUPS it only lifts the denials a removed rule left behind.
func (s *Service) RunGroupRecheck(ctx context.Context) {
	o := s.cfg.OIDC
	if !o.Enabled() {
		return
	}
	if len(o.AllowedGroups) == 0 {
		if err := s.liftGroupDenials(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("group re-check: lifting denials of a removed group rule failed", "err", err)
		}
		return
	}
	if o.GroupRecheckInterval <= 0 {
		s.log.Info("group re-check is off (OIDC_GROUP_RECHECK_INTERVAL=0): OIDC_ALLOWED_GROUPS applies at sign-in only")
		return
	}
	if s.box == nil {
		return
	}
	if !slices.Contains(o.Scopes, "offline_access") {
		s.log.Warn("OIDC_ALLOWED_GROUPS is set but OIDC_SCOPES lacks offline_access: without a refresh token from the "+
			"identity provider, people removed from the group keep access until they sign in again", "interval", o.GroupRecheckInterval)
	}
	tick := min(max(o.GroupRecheckInterval/10, time.Second), time.Minute)
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if _, err := s.RecheckGroups(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("group re-check failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// liftGroupDenials restores everyone the group rule denied (the rule was
// removed from the configuration).
func (s *Service) liftGroupDenials(ctx context.Context) error {
	return store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ids, err := q.RestoreAllGroupDenied(ctx)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := audit.Record(ctx, q, audit.Entry{ActorKind: audit.ActorSystem, Action: "user.group_restored", TargetType: "user",
				TargetID: id.String(), Metadata: map[string]any{"via": "rule_removed"}}); err != nil {
				return err
			}
		}
		return nil
	})
}

// RecheckGroups checks everyone due now and returns how many it asked the
// IdP about. It is safe to run on several replicas at once.
func (s *Service) RecheckGroups(ctx context.Context) (int, error) {
	if !s.groupRecheckOn() {
		return 0, nil
	}
	total := 0
	for {
		due, err := s.claimDueChecks(ctx)
		if err != nil || len(due) == 0 {
			return total, err
		}
		total += len(due)
		work := make(chan dbgen.ListDueGroupChecksRow)
		var wg sync.WaitGroup
		for range min(recheckWorkers, len(due)) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for row := range work {
					s.checkOne(ctx, row)
				}
			}()
		}
		for _, row := range due {
			work <- row
		}
		close(work)
		wg.Wait()
		if len(due) < recheckBatch || ctx.Err() != nil {
			return total, ctx.Err()
		}
	}
}

// claimDueChecks takes the next batch of people due a check, marking them
// checked now (so other replicas skip them, and a failing IdP is retried
// after an interval, not in a loop).
func (s *Service) claimDueChecks(ctx context.Context) ([]dbgen.ListDueGroupChecksRow, error) {
	now := s.now()
	var rows []dbgen.ListDueGroupChecksRow
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		var err error
		rows, err = q.ListDueGroupChecks(ctx, dbgen.ListDueGroupChecksParams{DueBefore: now.Add(-s.cfg.OIDC.GroupRecheckInterval),
			Issuer: s.cfg.OIDC.Issuer, ActiveSince: now.Add(-recheckActiveWindow), MaxRows: recheckBatch})
		if err != nil || len(rows) == 0 {
			return err
		}
		ids := make([]uuid.UUID, len(rows))
		for i, r := range rows {
			ids[i] = r.UserID
		}
		return q.TouchGroupChecks(ctx, ids)
	})
	return rows, err
}

// answer is what the IdP said about one person.
type answer struct {
	verdict verdict
	reason  string   // denied: why
	groups  []string // allowed: their groups now
	refresh string   // the refresh token to keep ("": the same)
	why     string   // unknown: what went wrong (for the log)
}

type verdict int

const (
	allowed verdict = iota
	denied
	unknown // the IdP couldn't say
)

func (s *Service) checkOne(ctx context.Context, row dbgen.ListDueGroupChecksRow) {
	ctx, cancel := context.WithTimeout(ctx, recheckTimeout)
	defer cancel()
	if s.isBootstrap(row.OIDCSubject) {
		return
	}
	log := s.log.With("user", row.UserID)
	refresh, err := s.box.Open(row.RefreshTokenEnc, idpTokenAD(row.UserID))
	if err != nil {
		log.Error("group re-check: the stored refresh token can't be opened (ENCRYPTION_KEY changed?)", "err", err)
		return
	}
	c, err := s.client(ctx)
	if err != nil {
		log.Warn("group re-check: the identity provider is unavailable", "err", err)
		s.apply(ctx, log, row, answer{verdict: unknown, why: "discovery failed"})
		return
	}
	s.apply(ctx, log, row, s.ask(ctx, c, string(refresh), row.OIDCSubject, log))
}

// ask exchanges the refresh token for fresh claims and applies the group
// rule to them.
func (s *Service) ask(ctx context.Context, c *oidcClient, refresh, subject string, log *slog.Logger) answer {
	tok, err := c.oauth.TokenSource(ctx, &oauth2.Token{RefreshToken: refresh}).Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			switch re.ErrorCode {
			case "invalid_grant", "access_denied", "unauthorized_client", "invalid_token", "consent_required", "login_required", "interaction_required":
				return answer{verdict: denied, reason: DeniedIdPRefused}
			}
			status := 0
			if re.Response != nil {
				status = re.Response.StatusCode
			}
			return answer{verdict: unknown, why: fmt.Sprintf("token endpoint answered %d %s", status, re.ErrorCode)}
		}
		return answer{verdict: unknown, why: "token endpoint unreachable"}
	}
	out := answer{}
	if tok.RefreshToken != "" && tok.RefreshToken != refresh {
		out.refresh = tok.RefreshToken
	}
	claimName := s.cfg.OIDC.GroupsClaim
	var claims map[string]any
	if raw, _ := tok.Extra("id_token").(string); raw != "" {
		idt, err := c.verifier.Verify(ctx, raw)
		if err != nil {
			return answer{verdict: unknown, why: "the ID token did not verify"}
		}
		if idt.Subject != subject {
			log.Error("group re-check: the identity provider answered for a different subject")
			return answer{verdict: unknown, why: "subject mismatch"}
		}
		if err := idt.Claims(&claims); err != nil {
			return answer{verdict: unknown, why: "the ID token's claims are unreadable"}
		}
	}
	if _, ok := claims[claimName]; !ok {
		claims = s.userinfoClaims(ctx, c, tok, subject, claimName)
	}
	if _, ok := claims[claimName]; !ok {
		return answer{verdict: unknown, why: "the groups claim " + claimName + " is in neither the ID token nor userinfo"}
	}
	out.groups = Groups(claims, claimName)
	if s.groupAllowed(out.groups) {
		out.verdict = allowed
	} else {
		out.verdict, out.reason = denied, DeniedNotInGroup
	}
	return out
}

// userinfoClaims reads the groups claim from the userinfo endpoint (nil when
// the endpoint can't say).
func (s *Service) userinfoClaims(ctx context.Context, c *oidcClient, tok *oauth2.Token, subject, claimName string) map[string]any {
	ui, err := c.provider.UserInfo(ctx, oauth2.StaticTokenSource(tok))
	if err != nil || ui.Subject != subject {
		return nil
	}
	var claims map[string]any
	if ui.Claims(&claims) != nil {
		return nil
	}
	v, ok := claims[claimName]
	if !ok {
		return nil
	}
	return map[string]any{claimName: v}
}

// apply records what the IdP said about one person. A person who signed in
// again since the check began (their token changed) is left alone: that
// sign-in applied the rule afresh.
func (s *Service) apply(ctx context.Context, log *slog.Logger, row dbgen.ListDueGroupChecksRow, a answer) {
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		cur, err := q.LockIdPToken(ctx, row.UserID)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		if !bytes.Equal(cur.RefreshTokenEnc, row.RefreshTokenEnc) {
			return nil
		}
		switch a.verdict {
		case allowed:
			enc := cur.RefreshTokenEnc
			if a.refresh != "" {
				if enc, err = s.box.Seal([]byte(a.refresh), idpTokenAD(row.UserID)); err != nil {
					return err
				}
			}
			if err := q.MarkGroupVerified(ctx, dbgen.MarkGroupVerifiedParams{UserID: row.UserID, RefreshTokenEnc: enc}); err != nil {
				return err
			}
			return q.SetUserGroups(ctx, dbgen.SetUserGroupsParams{UserID: row.UserID, Groups: boundGroups(a.groups)})
		case denied:
			log.Info("group re-check: access ended", "reason", a.reason)
			return s.denyGroupAccess(ctx, q, row.UserID, a.reason)
		}
		if grace := s.cfg.OIDC.GroupRecheckGrace; s.now().Sub(cur.VerifiedAt) > grace {
			log.Warn("group re-check: the identity provider has not vouched for this person within the grace period: access ended",
				"why", a.why, "grace", grace)
			return s.denyGroupAccess(ctx, q, row.UserID, DeniedUnreachable)
		}
		log.Warn("group re-check: the identity provider could not say; access kept for now", "why", a.why,
			"lastVerified", cur.VerifiedAt.Format(time.RFC3339))
		return nil
	})
	if err != nil && ctx.Err() == nil {
		log.Error("group re-check: recording the answer failed", "err", err)
	}
}
