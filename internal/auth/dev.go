package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// DevIssuer is the issuer recorded for development-login identities.
const DevIssuer = "pi-durable-platform:development"

// devAccounts are fixed local personas. Roles are granted once on first use,
// like production bootstrap, so demotions made while testing persist.
var devAccounts = []apitypes.DevAccount{
	{Id: "admin", Name: "Dev Platform Admin", Email: "admin@localhost", PlatformRole: RolePlatformAdmin},
	{Id: "auditor", Name: "Dev Platform Auditor", Email: "auditor@localhost", PlatformRole: RolePlatformAuditor},
	{Id: "user", Name: "Dev User", Email: "user@localhost", PlatformRole: RoleNone},
	// Extra people for trying team roles. No team membership is granted
	// automatically; add them to teams through the UI or API.
	{Id: "alex", Name: "Alex Dev", Email: "alex@localhost", PlatformRole: RoleNone},
	{Id: "blair", Name: "Blair Dev", Email: "blair@localhost", PlatformRole: RoleNone},
	{Id: "casey", Name: "Casey Dev", Email: "casey@localhost", PlatformRole: RoleNone},
}

// devGroups are the development personas' sample IdP groups, so access by
// group can be tried locally.
var devGroups = map[string][]string{
	"admin":   {"everyone", "platform-admins"},
	"auditor": {"everyone", "auditors"},
	"user":    {"everyone", "engineering"},
	"alex":    {"everyone", "engineering"},
	"blair":   {"everyone", "support"},
	"casey":   {"everyone", "support"},
}

// devPersonas are the development accounts as the dev data has them: an
// account that exists keeps its current name and email, so the sign-in list
// and the signed-in person agree, and signing in doesn't reset them.
// Without the database, the fixed list.
func (s *Service) devPersonas(ctx context.Context) []apitypes.DevAccount {
	out := append([]apitypes.DevAccount(nil), devAccounts...)
	if s.pool == nil {
		return out
	}
	rows, err := dbgen.New(s.pool).ListIssuerUsers(ctx, DevIssuer)
	if err != nil {
		s.log.WarnContext(ctx, "could not read the development personas", "err", err)
		return out
	}
	for i := range out {
		for _, row := range rows {
			if row.OIDCSubject == out[i].Id {
				out[i].Name, out[i].Email = row.DisplayName, row.Email
			}
		}
	}
	return out
}

// DevPersona returns the subject and name of the development persona with
// this email (case-insensitive).
func DevPersona(email string) (subject, name string, ok bool) {
	for _, a := range devAccounts {
		if strings.EqualFold(a.Email, strings.TrimSpace(email)) {
			return a.Id, a.Name, true
		}
	}
	return "", "", false
}

// DevLogin signs in as a local persona. It only exists when DEV_AUTH=true,
// which configuration refuses unless APP_URL is a loopback address; it also
// requires the request to be addressed to APP_URL's host from a same-origin
// page.
func (s *Service) DevLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.DevAuth {
		httpx.Error(w, http.StatusNotFound, "not_found", "Not found")
		return
	}
	if !s.cfg.LoopbackAppURL() || !s.sameHost(r) || !s.originAllowed(r) {
		httpx.Error(w, http.StatusForbidden, "origin_denied", "Development login is local-only")
		return
	}
	if !s.admitLogin(w, r) {
		return
	}
	var in apitypes.DevLoginRequest
	if !httpx.Decode(w, r, &in) {
		return
	}
	var acct *apitypes.DevAccount
	personas := s.devPersonas(r.Context())
	for i := range personas {
		if personas[i].Id == in.Account {
			acct = &personas[i]
		}
	}
	if acct == nil {
		httpx.Error(w, http.StatusBadRequest, "unknown_account", "Unknown development account")
		return
	}
	claims, _ := json.Marshal(map[string]any{"sub": acct.Id, "email": acct.Email, "name": acct.Name, "email_verified": true,
		"groups": devGroups[acct.Id]})
	li := loginIdentity{Issuer: DevIssuer, Subject: acct.Id, Email: acct.Email, Name: acct.Name, Claims: claims, Method: "dev",
		EmailVerified: true, Groups: devGroups[acct.Id]}
	if acct.PlatformRole != RoleNone {
		li.BootstrapRole = string(acct.PlatformRole)
	}
	if _, err := s.completeLogin(r.Context(), w, r, li); err != nil {
		if errors.Is(err, errSuspended) {
			httpx.Error(w, http.StatusForbidden, "user_suspended", "This account is suspended")
			return
		}
		httpx.Internal(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, apitypes.Ok{Ok: true})
}
