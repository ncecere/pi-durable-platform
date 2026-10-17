package httpapi_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/secrets"
	"github.com/ncecere/pi-durable-platform/internal/serviceaccounts"
)

// Service accounts: team-owned identities for services, agents and
// automation, managed by the team's owners and admins, with keys that stop
// working as soon as the account, its team or the key does.

const toolsTeam = "/v1/teams/tools"

const saBase = toolsTeam + "/service-accounts"

// teamEnv is team "tools" (owner user, editor alex, member blair) and team
// "other" (owner casey), with platform staff.
type teamEnv struct {
	t                                               *testing.T
	app                                             *testApp
	admin, auditor, owner, editor, member, outsider *session
}

func newTeamEnv(t *testing.T) *teamEnv {
	t.Helper()
	app := newTestApp(t, nil)
	e := &teamEnv{t: t, app: app}
	e.admin, e.auditor = app.signIn("admin"), app.signIn("auditor")
	e.owner, e.editor, e.member, e.outsider = app.signIn("user"), app.signIn("alex"), app.signIn("blair"), app.signIn("casey")
	createTeam(t, e.admin, "tools", "user@localhost")
	createTeam(t, e.admin, "other", "casey@localhost")
	for email, role := range map[string]string{"alex@localhost": "editor", "blair@localhost": "member"} {
		code, ec := e.owner.call("POST", toolsTeam+"/members", map[string]any{"email": email, "role": role}, nil, nil)
		mustCode(t, "add "+email, code, ec, 201, "")
	}
	return e
}

func (e *teamEnv) setTeamStatus(slug, status string) {
	e.t.Helper()
	var tv apitypes.TeamView
	if code := e.admin.get("/v1/admin/teams/"+slug, &tv); code != 200 {
		e.t.Fatalf("get team = %d", code)
	}
	code, ec := e.admin.call("PATCH", "/v1/admin/teams/"+slug, map[string]any{"status": status}, nil, ifMatch(tv.Team.Revision))
	mustCode(e.t, status+" "+slug, code, ec, 200, "")
}

func (e *teamEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.app.Pool.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func createSA(t *testing.T, s *session, body map[string]any) apitypes.ServiceAccount {
	t.Helper()
	var out apitypes.ServiceAccount
	code, ec := s.call("POST", saBase, body, &out, nil)
	mustCode(t, "create service account", code, ec, 201, "")
	return out
}

func createSAKey(t *testing.T, s *session, saID string, body map[string]any) apitypes.ServiceAccountKeyCreated {
	t.Helper()
	var out apitypes.ServiceAccountKeyCreated
	code, ec := s.call("POST", saBase+"/"+saID+"/keys", body, &out, nil)
	mustCode(t, "create key", code, ec, 201, "")
	return out
}

func patchSA(t *testing.T, s *session, saID string, body map[string]any) apitypes.ServiceAccount {
	t.Helper()
	var cur, out apitypes.ServiceAccount
	if code := s.get(saBase+"/"+saID, &cur); code != 200 {
		t.Fatalf("get service account = %d", code)
	}
	code, ec := s.call("PATCH", saBase+"/"+saID, body, &out, ifMatch(cur.Revision))
	mustCode(t, "patch service account", code, ec, 200, "")
	return out
}

func TestServiceAccountsManagement(t *testing.T) {
	e := newTeamEnv(t)
	sa := createSA(t, e.owner, map[string]any{"name": "ci-bot", "description": "Release pipeline"})
	if sa.Role != "member" || !sa.Enabled || sa.TeamSlug != "tools" || sa.ActiveKeyCount != 0 || sa.CreatedByName == nil || sa.Revision != 1 {
		t.Fatalf("service account = %+v", sa)
	}
	id := sa.Id.String()
	for _, c := range []struct {
		body map[string]any
		code int
		err  string
	}{
		{map[string]any{"name": "ci-bot"}, 409, "name_taken"},
		{map[string]any{"name": "CI Bot"}, 400, "invalid_name"},
		{map[string]any{"name": "-bot"}, 400, "invalid_name"},
		{map[string]any{"name": "ops", "role": "admin"}, 400, "invalid_role"},
		{map[string]any{"name": "ops", "role": "owner"}, 400, "invalid_role"},
	} {
		code, ec := e.owner.call("POST", saBase, c.body, nil, nil)
		mustCode(t, "create "+c.err, code, ec, c.code, c.err)
	}
	// Owners and admins manage; members and platform staff read; others
	// don't see the team.
	code, ec := e.member.call("POST", saBase, map[string]any{"name": "mine"}, nil, nil)
	mustCode(t, "member create", code, ec, 403, "forbidden")
	code, ec = e.editor.call("POST", saBase, map[string]any{"name": "mine"}, nil, nil)
	mustCode(t, "editor create", code, ec, 403, "forbidden")
	code, ec = e.admin.call("POST", saBase, map[string]any{"name": "mine"}, nil, nil)
	mustCode(t, "platform admin create", code, ec, 403, "forbidden")
	code, ec = e.outsider.call("GET", saBase, nil, nil, nil)
	mustCode(t, "outsider list", code, ec, 404, "team_not_found")
	var list []apitypes.ServiceAccount
	if code := e.member.get(saBase, &list); code != 200 || len(list) != 1 || list[0].Name != "ci-bot" {
		t.Fatalf("member list = %d %+v", code, list)
	}
	if code := e.auditor.get(saBase+"/"+id, nil); code != 200 {
		t.Errorf("auditor get = %d", code)
	}
	code, ec = e.member.call("GET", saBase+"/"+id+"/keys", nil, nil, nil)
	mustCode(t, "member keys", code, ec, 403, "forbidden")
	code, ec = e.owner.call("PATCH", saBase+"/"+id, map[string]any{"role": "editor"}, nil, nil)
	mustCode(t, "no If-Match", code, ec, 428, "")
	code, ec = e.owner.call("PATCH", saBase+"/"+id, map[string]any{"role": "editor"}, nil, ifMatch(9))
	mustCode(t, "stale", code, ec, 412, "")
	if got := patchSA(t, e.owner, id, map[string]any{"role": "editor", "description": "x"}); got.Role != "editor" || got.Revision != 2 {
		t.Fatalf("patched = %+v", got)
	}
	createSA(t, e.owner, map[string]any{"name": "other-bot"})
	code, ec = e.owner.call("PATCH", saBase+"/"+id, map[string]any{"name": "other-bot"}, nil, ifMatch(2))
	mustCode(t, "rename to a taken name", code, ec, 409, "name_taken")

	// Keys: at most two active (rotation), always expiring, shown once.
	k1 := createSAKey(t, e.owner, id, map[string]any{"name": "deploy"})
	if !strings.HasPrefix(k1.Secret, "pdp_sa_") || len(k1.Secret) != len("pdp_sa_")+43 || k1.Key.Prefix != k1.Secret[:len("pdp_sa_")+8] || k1.Key.Status != "active" ||
		k1.Key.ExpiresAt.Sub(time.Now()) < 89*24*time.Hour || k1.Key.CreatedByName == nil {
		t.Fatalf("key = %+v", k1)
	}
	for _, body := range []map[string]any{{"name": ""}, {"name": "x", "expiresInDays": 0}, {"name": "x", "expiresInDays": 366}} {
		code, ec := e.owner.call("POST", saBase+"/"+id+"/keys", body, nil, nil)
		mustCode(t, "invalid key", code, ec, 400, "")
	}
	k2 := createSAKey(t, e.owner, id, map[string]any{"name": "deploy-next", "expiresInDays": 365})
	code, ec = e.owner.call("POST", saBase+"/"+id+"/keys", map[string]any{"name": "third"}, nil, nil)
	mustCode(t, "third key", code, ec, 409, "too_many_keys")
	code, ec = e.member.call("DELETE", saBase+"/"+id+"/keys/"+k1.Key.Id.String(), nil, nil, nil)
	mustCode(t, "member revokes", code, ec, 403, "forbidden")
	code, ec = e.owner.call("DELETE", saBase+"/"+id+"/keys/"+k1.Key.Id.String(), nil, nil, nil)
	mustCode(t, "revoke", code, ec, 200, "")
	code, ec = e.owner.call("DELETE", saBase+"/"+id+"/keys/"+k1.Key.Id.String(), nil, nil, nil)
	mustCode(t, "revoke again", code, ec, 200, "")
	createSAKey(t, e.owner, id, map[string]any{"name": "rotated"})
	var keys []apitypes.ServiceAccountKey
	if code := e.auditor.get(saBase+"/"+id+"/keys", &keys); code != 200 || len(keys) != 3 || keys[0].Name != "rotated" || keys[2].Status != "revoked" {
		t.Fatalf("keys = %d %+v", code, keys)
	}
	var got apitypes.ServiceAccount
	if e.owner.get(saBase+"/"+id, &got); got.ActiveKeyCount != 2 {
		t.Errorf("active keys = %d", got.ActiveKeyCount)
	}
	if n := e.scalar(`SELECT count(*)::text FROM service_account_keys k WHERE position($1 in row_to_json(k)::text) > 0
		OR position($2 in row_to_json(k)::text) > 0`, k1.Secret, k2.Secret); n != "0" {
		t.Fatal("a secret is stored")
	}
	// A turned-off account gets no keys.
	patchSA(t, e.owner, id, map[string]any{"enabled": false})
	code, ec = e.owner.call("DELETE", saBase+"/"+id+"/keys/"+k2.Key.Id.String(), nil, nil, nil)
	mustCode(t, "revoke while off", code, ec, 200, "")
	code, ec = e.owner.call("POST", saBase+"/"+id+"/keys", map[string]any{"name": "while off"}, nil, nil)
	mustCode(t, "key while off", code, ec, 409, "disabled")
	// Archived teams are read-only.
	e.setTeamStatus("tools", "archived")
	code, ec = e.owner.call("POST", saBase, map[string]any{"name": "late"}, nil, nil)
	mustCode(t, "create in an archived team", code, ec, 409, "team_archived")
	e.setTeamStatus("tools", "active")
	code, ec = e.owner.call("DELETE", saBase+"/"+id, nil, nil, nil)
	mustCode(t, "delete", code, ec, 200, "")
	code, ec = e.owner.call("GET", saBase+"/"+id, nil, nil, nil)
	mustCode(t, "deleted", code, ec, 404, "service_account_not_found")

	// Audited with the team, never with a secret.
	for _, action := range []string{"service_account.create", "service_account.update", "service_account.delete",
		"service_account_key.create", "service_account_key.revoke"} {
		if n := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action = $1 AND team_id IS NOT NULL`, action); n == "0" {
			t.Errorf("no %s entries", action)
		}
	}
	if n := e.scalar(`SELECT count(*)::text FROM audit_log WHERE target_type = 'service_account_key'`); n != "5" { // 3 created, 2 revoked
		t.Errorf("key audits = %s", n)
	}
	var page apitypes.AuditPage
	e.owner.get(toolsTeam+"/audit?action=service_account_key.revoke", &page)
	if len(page.Items) == 0 || page.Items[0].TargetLabel == nil || !strings.HasPrefix(*page.Items[0].TargetLabel, "ci-bot · ") {
		t.Errorf("revoke entry = %+v", page.Items)
	}
	audit := e.scalar(`SELECT string_agg(row_to_json(a)::text, '') FROM audit_log a`)
	if strings.Contains(audit, k1.Secret) || strings.Contains(audit, k2.Secret) {
		t.Error("the audit log contains a key")
	}
}

// A key authenticates (serviceaccounts.Service.Authenticate) only while
// the key, its account and its team are all usable; keys made with the
// previous pepper still work and move to the current one.
func TestServiceAccountKeyAuthentication(t *testing.T) {
	e := newTeamEnv(t)
	pepper, err := secrets.ParseKey(testAPIKeyPepper)
	if err != nil {
		t.Fatal(err)
	}
	svc := serviceaccounts.New(e.app.Pool, pepper)
	ctx := context.Background()
	sa := createSA(t, e.owner, map[string]any{"name": "ci-bot"})
	id := sa.Id.String()
	key := createSAKey(t, e.owner, id, map[string]any{"name": "deploy"})
	check := func(what, secret string, ok bool) {
		t.Helper()
		p, err := svc.Authenticate(ctx, secret)
		switch {
		case ok && err != nil:
			t.Errorf("%s: %v", what, err)
		case ok && (p.Account.ID != sa.Id || p.Team.Slug != "tools"):
			t.Errorf("%s: principal = %+v", what, p)
		case !ok && !errors.Is(err, serviceaccounts.ErrInvalid):
			t.Errorf("%s: want ErrInvalid, got %v", what, err)
		}
	}
	check("fresh key", key.Secret, true)
	check("unknown key", "pdp_sa_"+strings.Repeat("x", 43), false)
	check("malformed", "pdp_sa_short", false)
	patchSA(t, e.owner, id, map[string]any{"enabled": false})
	check("turned off", key.Secret, false)
	patchSA(t, e.owner, id, map[string]any{"enabled": true})
	check("turned on again", key.Secret, true)
	e.setTeamStatus("tools", "archived")
	check("archived team", key.Secret, false)
	e.setTeamStatus("tools", "active")
	check("unarchived team", key.Secret, true)
	if _, err := e.app.Pool.Exec(ctx, `UPDATE service_account_keys SET created_at = now() - interval '2 days',
		expires_at = now() - interval '1 day' WHERE id = $1`, key.Key.Id); err != nil {
		t.Fatal(err)
	}
	check("expired", key.Secret, false)
	fresh := createSAKey(t, e.owner, id, map[string]any{"name": "fresh"})
	code, ec := e.owner.call("DELETE", saBase+"/"+id+"/keys/"+fresh.Key.Id.String(), nil, nil, nil)
	mustCode(t, "revoke", code, ec, 200, "")
	check("revoked", fresh.Secret, false)

	// Pepper rotation: a service built with a new pepper and the old one as
	// previous accepts the key and re-digests it.
	next := createSAKey(t, e.owner, id, map[string]any{"name": "next"})
	rotated := serviceaccounts.New(e.app.Pool, []byte(strings.Repeat("n", 32)))
	rotated.SetPreviousPepper(pepper)
	if _, err := rotated.Authenticate(ctx, next.Secret); err != nil {
		t.Fatalf("previous pepper: %v", err)
	}
	if _, err := svc.Authenticate(ctx, next.Secret); !errors.Is(err, serviceaccounts.ErrInvalid) {
		t.Errorf("old pepper after the rehash = %v", err)
	}
	if n := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action = 'service_account_key.pepper_rehash'`); n != "1" {
		t.Errorf("rehash audits = %s", n)
	}
	code, ec = e.owner.call("DELETE", saBase+"/"+id, nil, nil, nil)
	mustCode(t, "delete", code, ec, 200, "")
	check("deleted account", next.Secret, false)
	if _, err := rotated.Authenticate(ctx, next.Secret); !errors.Is(err, serviceaccounts.ErrInvalid) {
		t.Errorf("deleted account = %v", err)
	}
}

// The admin list of every team's accounts.
func TestServiceAccountAdminList(t *testing.T) {
	e := newTeamEnv(t)
	// Every team's accounts, for platform staff: by name, searchable, by team, paged.
	createSA(t, e.owner, map[string]any{"name": "ci-bot"})
	createSA(t, e.owner, map[string]any{"name": "a-tools-bot"})
	code, ec := e.outsider.call("POST", "/v1/teams/other/service-accounts", map[string]any{"name": "zz-other-bot"}, nil, nil)
	mustCode(t, "other team's account", code, ec, 201, "")
	names := func(s *session, query string) (int, []string, *string) {
		t.Helper()
		var page apitypes.ServiceAccountPage
		code := s.get("/v1/admin/service-accounts"+query, &page)
		out := []string{}
		for _, a := range page.Items {
			out = append(out, a.TeamSlug+"/"+a.Name)
		}
		return code, out, page.NextCursor
	}
	if code, got, next := names(e.auditor, ""); code != 200 || strings.Join(got, ",") != "tools/a-tools-bot,tools/ci-bot,other/zz-other-bot" || next != nil {
		t.Errorf("all = %d %v %v", code, got, next)
	}
	if _, got, _ := names(e.admin, "?team=other"); strings.Join(got, ",") != "other/zz-other-bot" {
		t.Errorf("team filter = %v", got)
	}
	if _, got, _ := names(e.admin, "?q=OTHER"); strings.Join(got, ",") != "other/zz-other-bot" {
		t.Errorf("search = %v", got)
	}
	if _, got, _ := names(e.admin, "?q=tools"); strings.Join(got, ",") != "tools/a-tools-bot,tools/ci-bot" {
		t.Errorf("search by team = %v", got)
	}
	if _, got, _ := names(e.admin, "?team=nope"); len(got) != 0 {
		t.Errorf("unknown team = %v", got)
	}
	_, first, next := names(e.admin, "?limit=2")
	if len(first) != 2 || next == nil {
		t.Fatalf("first page = %v %v", first, next)
	}
	if _, rest, last := names(e.admin, "?limit=2&cursor="+*next); strings.Join(rest, ",") != "other/zz-other-bot" || last != nil {
		t.Errorf("second page = %v %v", rest, last)
	}
	for _, s := range []*session{e.owner, e.member} {
		code, ec := s.call("GET", "/v1/admin/service-accounts", nil, nil, nil)
		mustCode(t, "non-staff", code, ec, 403, "forbidden")
	}
	for _, q := range []string{"?cursor=junk", "?limit=0", "?team=" + strings.Repeat("x", 101)} {
		code, _ := e.admin.call("GET", "/v1/admin/service-accounts"+q, nil, nil, nil)
		if code != 400 {
			t.Errorf("%s = %d", q, code)
		}
	}
}
