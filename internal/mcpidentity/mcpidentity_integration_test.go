package mcpidentity_test

import (
	"context"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/mcpidentity"
	"github.com/ncecere/pi-durable-platform/internal/secrets"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

const issuer = "https://agents.example.com"

func box(t *testing.T, seed byte) *secrets.Box {
	t.Helper()
	k := make([]byte, 32)
	for i := range k {
		k[i] = seed
	}
	b, err := secrets.New(k)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// clock is a settable time for Keys.Now.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newKeys(t *testing.T, pool *pgxpool.Pool, b *secrets.Box) (*mcpidentity.Keys, *clock) {
	c := &clock{t: time.Now()}
	k := mcpidentity.New(pool, b, issuer)
	k.Now = c.now
	return k, c
}

var claims = mcpidentity.Claims{Agent: uuid.New(), AgentName: "Mail", ThreadVisibility: "private", PersonPresent: true, Tool: "search",
	Team:   mcpidentity.Team{ID: uuid.New(), Slug: "personal-abc", Kind: "personal"},
	Person: &mcpidentity.Person{Issuer: "https://idp.example.com", Subject: "ada"}}

type own struct {
	PrincipalType    string              `json:"principal_type"`
	Name             string              `json:"name"`
	Team             mcpidentity.Team    `json:"team"`
	ThreadVisibility string              `json:"thread_visibility"`
	Person           *mcpidentity.Person `json:"person"`
	PersonPresent    bool                `json:"person_present"`
	Tool             string              `json:"tool"`
}

// verify checks raw against the JWKS k publishes, at time at.
func verify(t *testing.T, k *mcpidentity.Keys, raw, aud string, at time.Time) (jwt.Claims, own, error) {
	t.Helper()
	var std jwt.Claims
	var c own
	set, err := k.JWKS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return std, c, err
	}
	keys := set.Key(tok.Headers[0].KeyID)
	if len(keys) == 0 {
		return std, c, jose.ErrUnsupportedKeyType
	}
	if err := tok.Claims(keys[0].Key, &std, &c); err != nil {
		return std, c, err
	}
	return std, c, std.ValidateWithLeeway(jwt.Expected{Issuer: issuer, AnyAudience: jwt.Audience{aud}, Time: at}, 30*time.Second)
}

func kid(t *testing.T, raw string) string {
	t.Helper()
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatal(err)
	}
	return tok.Headers[0].KeyID
}

func kids(t *testing.T, k *mcpidentity.Keys) []string {
	t.Helper()
	set, err := k.JWKS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, key := range set.Keys {
		out = append(out, key.KeyID)
	}
	return out
}

// The first key is made once, however many replicas ask at once; a token
// says what it was given, for one audience, for five minutes, and verifies
// with the published key; the stored key is sealed.
func TestSignAndVerify(t *testing.T) {
	pool, _ := testutil.NewDB(t)
	b := box(t, 1)
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, _ := newKeys(t, pool, b)
			if _, err := k.Ensure(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n, _ := dbgen.New(pool).CountMCPIdentityKeys(ctx); n != 1 {
		t.Fatalf("%d keys", n)
	}
	k, clk := newKeys(t, pool, b)
	raw, err := k.Sign(ctx, "https://mail.example.com/mcp", claims)
	if err != nil {
		t.Fatal(err)
	}
	std, c, err := verify(t, k, raw, "https://mail.example.com/mcp", clk.now())
	if err != nil {
		t.Fatal(err)
	}
	if std.Subject != claims.Agent.String() || std.ID == "" || std.Expiry.Time().Sub(std.IssuedAt.Time()) != mcpidentity.TokenTTL ||
		std.IssuedAt.Time().Sub(std.NotBefore.Time()) != mcpidentity.NotBeforeSkew {
		t.Errorf("std %+v", std)
	}
	want := own{PrincipalType: "agent", Name: "Mail", Team: claims.Team, ThreadVisibility: "private", Person: claims.Person, PersonPresent: true, Tool: "search"}
	if got, _ := json.Marshal(c); string(got) != string(must(json.Marshal(want))) {
		t.Errorf("claims %s, want %s", got, must(json.Marshal(want)))
	}
	// The header: ES256, typ JWT, a kid that is the key's thumbprint.
	header, _ := base64.RawURLEncoding.DecodeString(strings.Split(raw, ".")[0])
	if !strings.Contains(string(header), `"alg":"ES256"`) || !strings.Contains(string(header), `"typ":"JWT"`) {
		t.Errorf("header %s", header)
	}
	set, _ := k.JWKS(ctx)
	thumb, _ := set.Keys[0].Thumbprint(crypto.SHA256)
	if set.Keys[0].KeyID != base64.RawURLEncoding.EncodeToString(thumb) || !set.Keys[0].IsPublic() || set.Keys[0].Use != "sig" {
		t.Errorf("published %+v", set.Keys[0])
	}
	if _, _, err := verify(t, k, raw, "https://other.example.com/mcp", clk.now()); err == nil {
		t.Error("verified for another audience")
	}
	if _, _, err := verify(t, k, raw, "https://mail.example.com/mcp", clk.now().Add(mcpidentity.TokenTTL+time.Minute)); err == nil {
		t.Error("verified after it expired")
	}
	if _, _, err := verify(t, k, raw[:len(raw)-4]+"AAAA", "https://mail.example.com/mcp", clk.now()); err == nil {
		t.Error("a changed signature verified")
	}
	// Nobody named: no person claim at all.
	raw, _ = k.Sign(ctx, "https://mail.example.com/mcp", mcpidentity.Claims{Agent: claims.Agent, ThreadVisibility: "none"})
	if _, c, err := verify(t, k, raw, "https://mail.example.com/mcp", clk.now()); err != nil || c.Person != nil || c.PersonPresent {
		t.Errorf("nobody: %+v %v", c, err)
	}
	// Sealed: the stored value is no key, and another ENCRYPTION_KEY can't sign with it.
	var enc []byte
	if err := pool.QueryRow(ctx, `SELECT private_key_enc FROM mcp_identity_keys`).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(enc), "PRIVATE") || len(enc) < 100 {
		t.Errorf("stored %d bytes", len(enc))
	}
	other, _ := newKeys(t, pool, box(t, 2))
	if _, err := other.Sign(ctx, "https://mail.example.com/mcp", claims); err == nil || !strings.Contains(err.Error(), "ENCRYPTION_KEY") {
		t.Errorf("signed with a key it can't open: %v", err)
	}
	if got := scalar(t, pool, `SELECT count(*)::text FROM audit_log WHERE action = 'platform.mcp_identity_key_create'`); got != "1" {
		t.Errorf("creations audited: %s", got)
	}
}

// Sign makes the first key when there is none (the platform never asked
// serve to), and the JWKS is empty before.
func TestSignCreatesTheFirstKey(t *testing.T) {
	pool, _ := testutil.NewDB(t)
	k := mcpidentity.New(pool, box(t, 1), issuer) // the real clock: the key is made after Sign began
	if got := kids(t, k); len(got) != 0 {
		t.Fatalf("kids before any key: %v", got)
	}
	// Another replica makes the first key: the JWKS shows it at once.
	if _, err := mcpidentity.New(pool, box(t, 1), issuer).Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := kids(t, k); len(got) != 1 {
		t.Fatalf("kids after another replica made one: %v", got)
	}
	if _, err := pool.Exec(context.Background(), `DELETE FROM mcp_identity_keys`); err != nil {
		t.Fatal(err)
	}
	k = mcpidentity.New(pool, box(t, 1), issuer)
	raw, err := k.Sign(context.Background(), "aud", claims)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := verify(t, k, raw, "aud", time.Now()); err != nil {
		t.Fatal(err)
	}
}

// Rotation: a new key is published before it signs; once it signs, the old
// one stays published for RetiredPublished, so tokens of both verify; then
// it leaves the set, and a later rotation deletes it. --now --drop-previous
// replaces every key at once.
func TestRotation(t *testing.T) {
	pool, _ := testutil.NewDB(t)
	ctx := context.Background()
	k, clk := newKeys(t, pool, box(t, 1))
	if _, err := k.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	oldRaw, _ := k.Sign(ctx, "aud", claims)
	oldKid := kid(t, oldRaw)
	if _, _, err := verify(t, k, oldRaw, "aud", clk.now()); err != nil {
		t.Fatal(err)
	}

	if _, err := k.Rotate(ctx, mcpidentity.RotateOptions{Prepublish: time.Minute}); err == nil {
		t.Error("a key published only a minute before it signs")
	}
	if _, err := k.Rotate(ctx, mcpidentity.RotateOptions{DropPrevious: true}); err == nil {
		t.Error("dropping the previous keys while they still sign")
	}
	rot, err := k.Rotate(ctx, mcpidentity.RotateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if d := rot.SignsFrom.Sub(clk.now()); d != mcpidentity.DefaultPrepublish {
		t.Errorf("signs in %s", d)
	}
	if got := kids(t, k); len(got) != 2 {
		t.Fatalf("published before it signs: %v", got)
	}
	if raw, _ := k.Sign(ctx, "aud", claims); kid(t, raw) != oldKid {
		t.Error("the new key signed before its time")
	}
	st, err := mcpidentity.Inspect(ctx, dbgen.New(pool), clk.now())
	if err != nil || st.Signing == nil || st.Signing.Kid != oldKid || len(st.Upcoming) != 1 || st.Total != 2 {
		t.Errorf("status %+v %v", st, err)
	}

	// A token of the old key just before the new one's time, then the new
	// key signs; a server verifying both a moment later accepts both.
	clk.add(mcpidentity.DefaultPrepublish - time.Minute)
	lastOld, _ := k.Sign(ctx, "aud", claims)
	clk.add(time.Minute + time.Second)
	newRaw, _ := k.Sign(ctx, "aud", claims)
	if kid(t, lastOld) != oldKid || kid(t, newRaw) != rot.Kid {
		t.Fatalf("signed with %s then %s, want %s then %s", kid(t, lastOld), kid(t, newRaw), oldKid, rot.Kid)
	}
	for _, raw := range []string{lastOld, newRaw} {
		if _, _, err := verify(t, k, raw, "aud", clk.now().Add(time.Minute)); err != nil {
			t.Errorf("a token doesn't verify during the overlap: %v", err)
		}
	}
	if got := kids(t, k); len(got) != 2 {
		t.Errorf("during the overlap: %v", got)
	}
	// An hour later the old key leaves the set; a day later a rotation deletes it.
	clk.add(mcpidentity.RetiredPublished + time.Minute)
	if got := kids(t, k); len(got) != 1 || got[0] != rot.Kid {
		t.Errorf("after the overlap: %v", got)
	}
	clk.add(mcpidentity.RetiredDeleteAfter)
	third, err := k.Rotate(ctx, mcpidentity.RotateOptions{Prepublish: mcpidentity.MinPrepublish})
	if err != nil || third.Deleted != 1 {
		t.Fatalf("rotation %+v %v", third, err)
	}

	// A leaked key: the new one signs now and is the only one left.
	leak, err := k.Rotate(ctx, mcpidentity.RotateOptions{Now: true, DropPrevious: true})
	if err != nil || leak.Deleted != 2 {
		t.Fatalf("drop %+v %v", leak, err)
	}
	if got := kids(t, k); len(got) != 1 || got[0] != leak.Kid {
		t.Errorf("after dropping: %v", got)
	}
	if raw, _ := k.Sign(ctx, "aud", claims); kid(t, raw) != leak.Kid {
		t.Error("not signed with the new key")
	}
	if got := scalar(t, pool, `SELECT count(*)::text FROM audit_log WHERE action = 'platform.mcp_identity_key_rotate'`); got != "3" {
		t.Errorf("rotations audited: %s", got)
	}
}

func scalar(t *testing.T, pool *pgxpool.Pool, sql string) string {
	t.Helper()
	var out string
	if err := pool.QueryRow(context.Background(), sql).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}
