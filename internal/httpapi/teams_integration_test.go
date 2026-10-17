package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
)

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// session is one signed-in browser.
type session struct {
	t      *testing.T
	app    *testApp
	client *http.Client
	csrf   string
	me     apitypes.Me
}

func (a *testApp) signIn(account string) *session {
	a.t.Helper()
	jar, _ := cookiejar.New(nil)
	s := &session{t: a.t, app: a, client: &http.Client{Jar: jar}}
	if code, body := s.raw("POST", "/auth/dev", map[string]string{"account": account}, nil); code != 200 {
		a.t.Fatalf("sign in %s: %d %s", account, code, body)
	}
	s.refresh()
	return s
}

func (s *session) refresh() {
	s.t.Helper()
	var me apitypes.Me
	if code := s.get("/v1/me", &me); code != 200 {
		s.t.Fatalf("/v1/me = %d", code)
	}
	s.me, s.csrf = me, me.CsrfToken
}

func (s *session) raw(method, path string, body any, headers map[string]string) (int, []byte) {
	s.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.app.URL+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set("Origin", s.app.URL)
		req.Header.Set("X-CSRF-Token", s.csrf)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := s.client.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, out
}

// call performs a request and decodes {"data": ...} into out on 2xx.
func (s *session) call(method, path string, body, out any, headers map[string]string) (int, string) {
	s.t.Helper()
	code, raw := s.raw(method, path, body, headers)
	if code >= 200 && code < 300 && out != nil {
		env := struct{ Data any }{Data: out}
		if err := json.Unmarshal(raw, &env); err != nil {
			s.t.Fatalf("decode %s %s: %v: %s", method, path, err, raw)
		}
	}
	return code, errorCode(raw)
}

func (s *session) get(path string, out any) int {
	s.t.Helper()
	code, _ := s.call("GET", path, nil, out, nil)
	return code
}

func ifMatch(rev int64) map[string]string {
	return map[string]string{"If-Match": fmt.Sprintf("%q", fmt.Sprint(rev))}
}

func mustCode(t *testing.T, what string, gotCode int, gotErr string, wantCode int, wantErr string) {
	t.Helper()
	if gotCode != wantCode || (wantErr != "" && gotErr != wantErr) {
		t.Fatalf("%s: got %d %q, want %d %q", what, gotCode, gotErr, wantCode, wantErr)
	}
}

func createTeam(t *testing.T, admin *session, slug, owner string) apitypes.TeamCreated {
	t.Helper()
	var out apitypes.TeamCreated
	code, e := admin.call("POST", "/v1/admin/teams", map[string]any{
		"slug": slug, "name": strings.ToUpper(slug), "ownerEmail": owner,
	}, &out, nil)
	mustCode(t, "create team", code, e, 201, "")
	return out
}

func TestTeamLifecycleAndMembership(t *testing.T) {
	app := newTestApp(t, nil)
	admin := app.signIn("admin")
	owner := app.signIn("user")

	created := createTeam(t, admin, "platform-tools", "user@localhost")
	if created.Owner.Status != "added" || created.Team.OwnerCount != 1 || created.Team.MemberCount != 1 {
		t.Fatalf("owner not added: %+v", created)
	}
	owner.refresh()
	if len(owner.me.Teams) != 1 || owner.me.Teams[0].Role != "owner" || owner.me.Teams[0].Slug != "platform-tools" {
		t.Fatalf("owner memberships = %+v", owner.me.Teams)
	}
	var mine []apitypes.MyTeam
	if code := owner.get("/v1/teams", &mine); code != 200 || len(mine) != 1 {
		t.Fatalf("my teams = %d %+v", code, mine)
	}
	base := "/v1/teams/platform-tools"

	// Invite someone who has never signed in; they join on first sign-in.
	var added apitypes.MemberAddResult
	code, e := owner.call("POST", base+"/members", map[string]string{"email": "Alex@Localhost", "role": "editor"}, &added, nil)
	mustCode(t, "invite alex", code, e, 201, "")
	if added.Status != "invited" || added.Invite == nil || added.Invite.Email != "alex@localhost" {
		t.Fatalf("expected invite, got %+v", added)
	}
	var invites []apitypes.Invite
	if owner.get(base+"/invites", &invites); len(invites) != 1 {
		t.Fatalf("invites = %+v", invites)
	}
	alex := app.signIn("alex")
	if len(alex.me.Teams) != 1 || alex.me.Teams[0].Role != "editor" {
		t.Fatalf("invite not accepted at sign-in: %+v", alex.me.Teams)
	}
	if owner.get(base+"/invites", &invites); len(invites) != 0 {
		t.Fatalf("accepted invite still open: %+v", invites)
	}

	// Add an existing user directly; adding again conflicts.
	app.signIn("blair")
	code, e = owner.call("POST", base+"/members", map[string]string{"email": "blair@localhost", "role": "member"}, &added, nil)
	mustCode(t, "add blair", code, e, 201, "")
	if added.Status != "added" {
		t.Fatalf("blair status = %s", added.Status)
	}
	code, e = owner.call("POST", base+"/members", map[string]string{"email": "blair@localhost", "role": "member"}, nil, nil)
	mustCode(t, "add blair again", code, e, 409, "already_member")
	code, e = owner.call("POST", base+"/members", map[string]string{"email": "not an email", "role": "member"}, nil, nil)
	// The generated Email type rejects a malformed address while decoding.
	mustCode(t, "bad email", code, e, 400, "invalid_json")
	code, e = owner.call("POST", base+"/members", map[string]string{"email": "x@example.com", "role": "superuser"}, nil, nil)
	mustCode(t, "bad role", code, e, 400, "invalid_role")
	code, e = owner.call("POST", base+"/members", map[string]any{"email": "x@example.com", "role": "member", "extra": 1}, nil, nil)
	mustCode(t, "unknown field", code, e, 400, "invalid_json")

	// Editors cannot manage members or see invites.
	code, e = alex.call("POST", base+"/members", map[string]string{"email": "casey@localhost", "role": "member"}, nil, nil)
	mustCode(t, "editor adds member", code, e, 403, "forbidden")
	code, e = alex.call("GET", base+"/invites", nil, nil, nil)
	mustCode(t, "editor lists invites", code, e, 403, "forbidden")

	// Role change needs If-Match; stale revisions are rejected.
	var members []apitypes.Member
	owner.get(base+"/members", &members)
	if len(members) != 3 || members[0].Role != "owner" {
		t.Fatalf("members (owners first) = %+v", members)
	}
	var blair apitypes.Member
	for _, m := range members {
		if m.User.Email == "blair@localhost" {
			blair = m
		}
	}
	path := base + "/members/" + blair.User.Id.String()
	code, e = owner.call("PATCH", path, map[string]string{"role": "admin"}, nil, nil)
	mustCode(t, "patch without If-Match", code, e, 428, "revision_required")
	code, e = owner.call("PATCH", path, map[string]string{"role": "admin"}, nil, ifMatch(blair.Revision+5))
	mustCode(t, "stale patch", code, e, 412, "revision_conflict")
	var updated apitypes.Member
	code, e = owner.call("PATCH", path, map[string]string{"role": "admin"}, &updated, ifMatch(blair.Revision))
	mustCode(t, "promote blair", code, e, 200, "")
	if updated.Role != "admin" || updated.Revision != blair.Revision+1 {
		t.Fatalf("updated = %+v", updated)
	}

	// Team admins cannot touch owners or create them.
	blairS := app.signIn("blair")
	ownerID := owner.me.User.Id.String()
	code, e = blairS.call("DELETE", base+"/members/"+ownerID, nil, nil, nil)
	mustCode(t, "admin removes owner", code, e, 403, "forbidden")
	code, e = blairS.call("POST", base+"/members", map[string]string{"email": "casey@localhost", "role": "owner"}, nil, nil)
	mustCode(t, "admin invites owner", code, e, 403, "forbidden")

	// The last owner can neither leave nor be demoted.
	code, e = owner.call("DELETE", base+"/members/"+ownerID, nil, nil, nil)
	mustCode(t, "last owner leaves", code, e, 409, "last_owner")
	var self apitypes.Member
	for _, m := range members {
		if m.User.Id == owner.me.User.Id {
			self = m
		}
	}
	code, e = owner.call("PATCH", base+"/members/"+ownerID, map[string]string{"role": "admin"}, nil, ifMatch(self.Revision))
	mustCode(t, "last owner demoted", code, e, 409, "last_owner")

	// Non-members cannot see the team at all; platform auditors can.
	casey := app.signIn("casey")
	code, e = casey.call("GET", base, nil, nil, nil)
	mustCode(t, "non-member reads team", code, e, 404, "team_not_found")
	code, e = casey.call("GET", base+"/members", nil, nil, nil)
	mustCode(t, "non-member lists members", code, e, 404, "team_not_found")
	auditor := app.signIn("auditor")
	var view apitypes.TeamView
	if code := auditor.get(base, &view); code != 200 || view.Role != nil {
		t.Fatalf("auditor view = %d %+v", code, view)
	}
	if code := owner.get(base, &view); code != 200 || view.Role == nil || *view.Role != "owner" {
		t.Fatalf("owner view = %d %+v", code, view)
	}
	// By ID as well as by slug.
	if code := owner.get("/v1/teams/"+view.Team.Id.String(), &view); code != 200 || view.Team.Slug != "platform-tools" {
		t.Fatalf("by id = %d %+v", code, view)
	}

	// Members can leave.
	code, e = alex.call("DELETE", base+"/members/"+alex.me.User.Id.String(), nil, nil, nil)
	mustCode(t, "editor leaves", code, e, 200, "")

	// Team audit: editors and above, members cannot.
	var page apitypes.AuditPage
	if code := owner.get(base+"/audit", &page); code != 200 {
		t.Fatalf("team audit = %d", code)
	}
	var actions []string
	for _, it := range page.Items {
		actions = append(actions, it.Action)
		if it.ClientIp != nil {
			t.Errorf("team log shows the client address: %+v", it)
		}
	}
	for _, want := range []string{"team.create", "team.owner_assign", "team.invite_create", "team.invite_accept", "team.member_add", "team.member_role_change", "team.member_leave"} {
		if !strings.Contains(strings.Join(actions, ","), want) {
			t.Errorf("team audit missing %s (have %v)", want, actions)
		}
	}
	code, e = app.signIn("casey").call("GET", base+"/audit", nil, nil, nil)
	mustCode(t, "non-member reads audit", code, e, 404, "team_not_found")

	// One entry by ID (a linked entry): the team's own only.
	first := page.Items[len(page.Items)-1]
	var entry apitypes.AuditEntry
	if code := owner.get(fmt.Sprintf(base+"/audit/%d", first.Id), &entry); code != 200 || entry.Id != first.Id || entry.Action != first.Action {
		t.Fatalf("audit entry = %d %+v, want %+v", code, entry, first)
	}
	code, e = owner.call("GET", fmt.Sprintf(base+"/audit/%d", first.Id+1_000_000), nil, nil, nil)
	mustCode(t, "missing audit entry", code, e, 404, "not_found")
	code, e = owner.call("GET", base+"/audit/abc", nil, nil, nil)
	mustCode(t, "bad audit entry id", code, e, 400, "invalid_id")
	// A platform entry (a sign-in) is not in the team's log.
	var platformLog apitypes.AuditPage
	admin.get("/v1/admin/audit?action=auth.login&limit=1", &platformLog)
	code, e = owner.call("GET", fmt.Sprintf(base+"/audit/%d", platformLog.Items[0].Id), nil, nil, nil)
	mustCode(t, "platform entry in team log", code, e, 404, "not_found")
}

func TestTeamCreationRules(t *testing.T) {
	app := newTestApp(t, nil)
	admin := app.signIn("admin")
	user := app.signIn("user")
	auditor := app.signIn("auditor")

	body := map[string]any{"slug": "ok-team", "name": "OK", "ownerEmail": "new@example.com"}
	code, e := user.call("POST", "/v1/admin/teams", body, nil, nil)
	mustCode(t, "user creates team", code, e, 403, "forbidden")
	code, e = auditor.call("POST", "/v1/admin/teams", body, nil, nil)
	mustCode(t, "auditor creates team", code, e, 403, "forbidden")

	for _, tc := range []struct{ slug, name, owner, code string }{
		{"id", "X", "x@example.com", "invalid_slug"},
		{"Bad_Slug", "X", "x@example.com", "invalid_slug"},
		{"-x", "X", "x@example.com", "invalid_slug"},
		{"123e4567-e89b-12d3-a456-426614174000", "X", "x@example.com", "invalid_slug"},
		{"good", " ", "x@example.com", "invalid_name"},
		{"good", "X", "nobody", "invalid_json"}, // the generated Email type rejects it while decoding
	} {
		b := map[string]any{"slug": tc.slug, "name": tc.name, "ownerEmail": tc.owner}
		code, e := admin.call("POST", "/v1/admin/teams", b, nil, nil)
		mustCode(t, "create "+tc.slug, code, e, 400, tc.code)
	}

	created := createTeam(t, admin, "ok-team", "new@example.com")
	if created.Owner.Status != "invited" || created.Owner.Invite.Role != "owner" || created.Team.OwnerInvites != 1 {
		t.Fatalf("unknown owner should be invited: %+v", created)
	}
	code, e = admin.call("POST", "/v1/admin/teams", body, nil, nil)
	mustCode(t, "duplicate slug", code, e, 409, "slug_taken")

	// Auditors can list; paging and search work.
	createTeam(t, admin, "b-team", "user@localhost")
	createTeam(t, admin, "c-team", "user@localhost")
	var page apitypes.TeamPage
	if code := auditor.get("/v1/admin/teams?limit=2", &page); code != 200 || len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("page 1 = %d %+v", code, page)
	}
	var page2 apitypes.TeamPage
	auditor.get("/v1/admin/teams?limit=2&cursor="+*page.NextCursor, &page2)
	if len(page2.Items) != 1 || page2.NextCursor != nil || page2.Items[0].Team.Slug != "ok-team" {
		t.Fatalf("page 2 = %+v", page2)
	}
	var found apitypes.TeamPage
	if auditor.get("/v1/admin/teams?q=C-TE", &found); len(found.Items) != 1 || found.Items[0].Team.Slug != "c-team" {
		t.Fatalf("search = %+v", found)
	}
	if auditor.get("/v1/admin/teams?q=%25", &found); len(found.Items) != 0 {
		t.Fatalf("a %% search matched everything: %+v", found)
	}
	code, e = auditor.call("GET", "/v1/admin/teams?status=gone", nil, nil, nil)
	mustCode(t, "bad status filter", code, e, 400, "invalid_status")
	code, e = auditor.call("GET", "/v1/admin/teams?limit=500", nil, nil, nil)
	mustCode(t, "limit too high", code, e, 400, "invalid_limit")

	// Auditors read but cannot update.
	var sum apitypes.TeamSummary
	admin.get("/v1/admin/teams/b-team", &sum)
	code, e = auditor.call("PATCH", "/v1/admin/teams/b-team", map[string]string{"name": "x"}, nil, ifMatch(sum.Team.Revision))
	mustCode(t, "auditor updates team", code, e, 403, "forbidden")

	// Archiving makes the team read-only.
	code, e = admin.call("PATCH", "/v1/admin/teams/b-team", map[string]string{"status": "archived"}, nil, nil)
	mustCode(t, "archive without If-Match", code, e, 428, "revision_required")
	code, e = admin.call("PATCH", "/v1/admin/teams/b-team", map[string]string{"status": "archived"}, &sum, ifMatch(sum.Team.Revision))
	mustCode(t, "archive", code, e, 200, "")
	if sum.Team.Status != "archived" || sum.Team.ArchivedAt == nil || sum.Team.Revision != 2 {
		t.Fatalf("archive result = %+v", sum.Team)
	}
	code, e = admin.call("PATCH", "/v1/admin/teams/b-team", map[string]string{"name": "late"}, nil, ifMatch(1))
	mustCode(t, "stale update", code, e, 412, "revision_conflict")
	user.refresh()
	code, e = user.call("POST", "/v1/teams/b-team/members", map[string]string{"email": "z@example.com", "role": "member"}, nil, nil)
	mustCode(t, "add to archived team", code, e, 409, "team_archived")
	var archived apitypes.TeamPage
	if auditor.get("/v1/admin/teams?status=archived", &archived); len(archived.Items) != 1 {
		t.Fatalf("archived filter = %+v", archived)
	}
	var back apitypes.TeamSummary
	code, e = admin.call("PATCH", "/v1/admin/teams/b-team", map[string]string{"status": "active", "description": "back"}, &back, ifMatch(sum.Team.Revision))
	mustCode(t, "unarchive", code, e, 200, "")
	if back.Team.Status != "active" || back.Team.ArchivedAt != nil || back.Team.Description != "back" {
		t.Fatalf("unarchive = %+v", back.Team)
	}
}

func TestAssignOwnerRecoversTeam(t *testing.T) {
	app := newTestApp(t, nil)
	admin := app.signIn("admin")
	app.signIn("alex")
	createTeam(t, admin, "lab", "user@localhost")
	user := app.signIn("user")
	code, e := user.call("POST", "/v1/teams/lab/members", map[string]string{"email": "alex@localhost", "role": "member"}, nil, nil)
	mustCode(t, "add alex", code, e, 201, "")

	var res apitypes.MemberAddResult
	code, e = admin.call("POST", "/v1/admin/teams/lab/owners", map[string]string{"email": "alex@localhost"}, &res, nil)
	mustCode(t, "assign owner", code, e, 200, "")
	if res.Status != "added" || res.Member.Role != "owner" {
		t.Fatalf("assign owner = %+v", res)
	}
	// Now the original owner is no longer the last one and can leave.
	code, e = user.call("DELETE", "/v1/teams/lab/members/"+user.me.User.Id.String(), nil, nil, nil)
	mustCode(t, "former sole owner leaves", code, e, 200, "")
	// An unknown address is invited as owner.
	code, e = admin.call("POST", "/v1/admin/teams/lab/owners", map[string]string{"email": "later@example.com"}, &res, nil)
	mustCode(t, "invite owner", code, e, 200, "")
	if res.Status != "invited" || res.Invite == nil || res.Invite.Role != "owner" {
		t.Fatalf("invite owner = %+v", res)
	}
	code, e = admin.call("POST", "/v1/admin/teams/nope/owners", map[string]string{"email": "later@example.com"}, nil, nil)
	mustCode(t, "missing team", code, e, 404, "team_not_found")
}

func TestPlatformUserAdministration(t *testing.T) {
	app := newTestApp(t, nil)
	admin := app.signIn("admin")
	user := app.signIn("user")

	var detail apitypes.UserDetail
	admin.get("/v1/admin/users/"+admin.me.User.Id.String(), &detail)
	code, e := admin.call("PATCH", "/v1/admin/users/"+admin.me.User.Id.String(), map[string]string{"platformRole": "none"}, nil, ifMatch(detail.User.Revision))
	mustCode(t, "demote last admin", code, e, 409, "last_admin")
	code, e = admin.call("PATCH", "/v1/admin/users/"+admin.me.User.Id.String(), map[string]string{"status": "suspended"}, nil, ifMatch(detail.User.Revision))
	mustCode(t, "suspend last admin", code, e, 409, "last_admin")
	code, e = admin.call("PATCH", "/v1/admin/users/"+admin.me.User.Id.String(), map[string]string{"platformRole": "root"}, nil, ifMatch(detail.User.Revision))
	mustCode(t, "invalid role", code, e, 400, "invalid_role")

	// Promote another admin; now demoting yourself is allowed.
	var ud apitypes.UserDetail
	admin.get("/v1/admin/users/"+user.me.User.Id.String(), &ud)
	var u apitypes.User
	code, e = admin.call("PATCH", "/v1/admin/users/"+user.me.User.Id.String(), map[string]string{"platformRole": "platform_admin"}, &u, ifMatch(ud.User.Revision))
	mustCode(t, "promote user", code, e, 200, "")
	if u.PlatformRole != "platform_admin" || u.Revision != ud.User.Revision+1 {
		t.Fatalf("promoted = %+v", u)
	}
	code, e = admin.call("PATCH", "/v1/admin/users/"+user.me.User.Id.String(), map[string]string{"platformRole": "none"}, nil, ifMatch(ud.User.Revision))
	mustCode(t, "stale user update", code, e, 412, "revision_conflict")
	code, e = admin.call("PATCH", "/v1/admin/users/"+admin.me.User.Id.String(), map[string]string{"platformRole": "none"}, nil, ifMatch(detail.User.Revision))
	mustCode(t, "demote self", code, e, 200, "")
	code, _ = admin.call("GET", "/v1/admin/users", nil, nil, nil)
	if code != 403 {
		t.Fatalf("demoted admin still reaches admin API: %d", code)
	}

	// Suspension ends the target's sessions immediately.
	newAdmin := user
	newAdmin.refresh()
	casey := app.signIn("casey")
	var cd apitypes.UserDetail
	newAdmin.get("/v1/admin/users/"+casey.me.User.Id.String(), &cd)
	code, e = newAdmin.call("PATCH", "/v1/admin/users/"+casey.me.User.Id.String(), map[string]string{"status": "suspended"}, nil, ifMatch(cd.User.Revision))
	mustCode(t, "suspend casey", code, e, 200, "")
	if code, _ := casey.call("GET", "/v1/me", nil, nil, nil); code != 401 {
		t.Fatalf("suspended user's session still valid: %d", code)
	}

	// Search, filters and paging.
	var page apitypes.UserPage
	if code := newAdmin.get("/v1/admin/users?q=dev%20platform&limit=1", &page); code != 200 || len(page.Items) != 1 {
		t.Fatalf("search = %d %+v", code, page)
	}
	if newAdmin.get("/v1/admin/users?status=suspended", &page); len(page.Items) != 1 || page.Items[0].Email != "casey@localhost" {
		t.Fatalf("status filter = %+v", page)
	}
	if newAdmin.get("/v1/admin/users?role=platform_admin", &page); len(page.Items) != 1 || page.Items[0].Email != "user@localhost" {
		t.Fatalf("role filter = %+v", page)
	}
	var all apitypes.UserPage
	newAdmin.get("/v1/admin/users?limit=1", &all)
	seen := 1
	for all.NextCursor != nil {
		cursor := *all.NextCursor
		all = apitypes.UserPage{}
		newAdmin.get("/v1/admin/users?limit=1&cursor="+cursor, &all)
		seen += len(all.Items)
	}
	if seen != 3 {
		t.Fatalf("paged through %d users, want 3", seen)
	}
	code, e = newAdmin.call("GET", "/v1/admin/users?cursor=garbage", nil, nil, nil)
	mustCode(t, "bad cursor", code, e, 400, "invalid_cursor")
	code, e = newAdmin.call("GET", "/v1/admin/users/00000000-0000-0000-0000-000000000000", nil, nil, nil)
	mustCode(t, "missing user", code, e, 404, "user_not_found")
	code, e = newAdmin.call("GET", "/v1/admin/users/nope", nil, nil, nil)
	mustCode(t, "bad user id", code, e, 400, "invalid_id")
}

// The user list counts each user's teams; the detail lists them.
func TestAdminUserTeams(t *testing.T) {
	app := newTestApp(t, nil)
	admin := app.signIn("admin")
	user := app.signIn("user")
	createTeam(t, admin, "one", "user@localhost")
	createTeam(t, admin, "two", "user@localhost")
	var page apitypes.UserPage
	admin.get("/v1/admin/users?q=user@", &page)
	if len(page.Items) != 1 || page.Items[0].TeamCount == nil || *page.Items[0].TeamCount != 2 {
		t.Fatalf("users = %+v", page.Items)
	}
	var d apitypes.UserDetail
	admin.get("/v1/admin/users/"+user.me.User.Id.String(), &d)
	if len(d.Teams) != 2 || d.Teams[0].Role != "owner" {
		t.Fatalf("detail teams = %+v", d.Teams)
	}
}

// Two owners removing each other at the same moment must never leave the
// team without an owner: the team row lock serialises the owner count.
func TestConcurrentOwnerRemovalKeepsAnOwner(t *testing.T) {
	app := newTestApp(t, nil)
	admin := app.signIn("admin")
	a, b := app.signIn("alex"), app.signIn("blair")
	for i := range 5 {
		slug := fmt.Sprintf("race-%d", i)
		createTeam(t, admin, slug, "alex@localhost")
		if code, e := admin.call("POST", "/v1/admin/teams/"+slug+"/owners", map[string]string{"email": "blair@localhost"}, nil, nil); code != 200 {
			t.Fatalf("second owner: %d %s", code, e)
		}
		codes := make(chan int, 2)
		go func() {
			c, _ := a.raw("DELETE", "/v1/teams/"+slug+"/members/"+b.me.User.Id.String(), nil, nil)
			codes <- c
		}()
		go func() {
			c, _ := b.raw("DELETE", "/v1/teams/"+slug+"/members/"+a.me.User.Id.String(), nil, nil)
			codes <- c
		}()
		c1, c2 := <-codes, <-codes
		// The loser is refused: 409 last_owner if it ran second while still a
		// member, or 404 if it had already been removed by the winner.
		lost := func(c int) bool { return c == 409 || c == 404 }
		if !((c1 == 200 && lost(c2)) || (lost(c1) && c2 == 200)) {
			t.Fatalf("round %d: codes %d and %d, want one 200 and one refusal", i, c1, c2)
		}
		var sum apitypes.TeamSummary
		admin.get("/v1/admin/teams/"+slug, &sum)
		if sum.OwnerCount != 1 {
			t.Fatalf("round %d: owner count %d", i, sum.OwnerCount)
		}
	}
}

// An owner invited by email counts as an owner invite on the admin team
// list, and a platform admin who isn't a member revokes it; an auditor can't.
func TestOwnerInviteOnAdminPages(t *testing.T) {
	app := newTestApp(t, nil)
	admin := app.signIn("admin")
	auditor := app.signIn("auditor")
	createTeam(t, admin, "invited", "new.owner@example.com")
	var sum apitypes.TeamSummary
	if code := admin.get("/v1/admin/teams/invited", &sum); code != 200 || sum.OwnerCount != 0 || sum.OwnerInvites != 1 {
		t.Fatalf("summary = %d %+v", code, sum)
	}
	var invites []apitypes.Invite
	if code := auditor.get("/v1/teams/invited/invites", &invites); code != 200 || len(invites) != 1 {
		t.Fatalf("invites = %d %+v", code, invites)
	}
	code, e := auditor.call("DELETE", "/v1/teams/invited/invites/"+invites[0].Id.String(), nil, nil, nil)
	mustCode(t, "auditor revokes", code, e, 403, "forbidden")
	code, e = admin.call("DELETE", "/v1/teams/invited/invites/"+invites[0].Id.String(), nil, nil, nil)
	mustCode(t, "platform admin revokes", code, e, 200, "")
	code, e = admin.call("DELETE", "/v1/teams/invited/invites/"+invites[0].Id.String(), nil, nil, nil)
	mustCode(t, "revoke again", code, e, 404, "invite_not_found")
	if admin.get("/v1/admin/teams/invited", &sum); sum.OwnerInvites != 0 {
		t.Errorf("owner invites after revoking = %d", sum.OwnerInvites)
	}
}
