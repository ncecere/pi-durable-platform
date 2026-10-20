package rekey_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/rekey"
	"github.com/ncecere/pi-durable-platform/internal/secrets"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

func key(t testing.TB) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func box(t testing.TB, current []byte, previous ...[]byte) *secrets.Box {
	t.Helper()
	b, err := secrets.New(current, previous...)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sealedValues is how many sealed values seed() stores.
const sealedValues = 15

// value is one sealed value the fixture stored.
type value struct {
	table, column string
	id            uuid.UUID
	ad            []byte
	plain         string
}

// fixture stores, sealed with sealer, two rows of every sealed table (and one
// with NULL headers), with the associated data the services use.
type fixture struct {
	pool   *pgxpool.Pool
	sealer *secrets.Box
	values []value
}

func (f *fixture) seal(t testing.TB, table, column string, id uuid.UUID, ad, plain string) []byte {
	t.Helper()
	ct, err := f.sealer.Seal([]byte(plain), []byte(ad))
	if err != nil {
		t.Fatal(err)
	}
	f.values = append(f.values, value{table, column, id, []byte(ad), plain})
	return ct
}

func (f *fixture) exec(t testing.TB, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func seed(t testing.TB, pool *pgxpool.Pool, sealer *secrets.Box) *fixture {
	t.Helper()
	f := &fixture{pool: pool, sealer: sealer}
	teamID := uuid.New()
	f.exec(t, `INSERT INTO teams (id, slug, name) VALUES ($1, 'rekey-team', 'Rekey')`, teamID)
	for i := range 2 {
		id := uuid.New()
		f.exec(t, `INSERT INTO agents (id, team_id, template_id, template_version, name, db_password_enc, runtime_token_enc)
			SELECT $1, $2, id, 1, $3, $4, $5 FROM agent_templates WHERE slug = 'assistant'`,
			id, teamID, fmt.Sprintf("agent-%d", i),
			f.seal(t, "agents", "db_password_enc", id, id.String(), fmt.Sprintf("db-password-%d", i)),
			f.seal(t, "agents", "runtime_token_enc", id, id.String(), fmt.Sprintf("runtime-token-%d", i)))
		// A thread's servers hang off its record (thread_mcp_servers_thread_fkey).
		f.exec(t, `INSERT INTO agent_threads (agent_id, thread_id, title) VALUES ($1, 't1', 'Thread')`, id)
		thread := uuid.New()
		f.exec(t, `INSERT INTO thread_mcp_servers (id, agent_id, thread_id, name, slug, url, headers_enc, expires_at)
			VALUES ($1, $2, 't1', 'Thread server', 'ts', 'https://mcp.example.com', $3, now() + interval '1 day')`,
			thread, id, f.seal(t, "thread_mcp_servers", "headers_enc", thread, "thread_mcp_server:"+thread.String(), fmt.Sprintf(`{"Authorization":"Bearer thread-%d"}`, i)))
	}
	for i := range 2 {
		// A person's stored IdP refresh token (group re-checks), keyed by the user.
		user := uuid.New()
		f.exec(t, `INSERT INTO users (id, oidc_issuer, oidc_subject, email) VALUES ($1, 'https://idp.example.com', $2, $3)`,
			user, fmt.Sprintf("subject-%d", i), fmt.Sprintf("person%d@example.com", i))
		f.exec(t, `INSERT INTO user_idp_tokens (user_id, refresh_token_enc) VALUES ($1, $2)`,
			user, f.seal(t, "user_idp_tokens", "refresh_token_enc", user, "idp_refresh_token:"+user.String(), fmt.Sprintf("refresh-%d", i)))
	}
	for i := range 2 {
		id := uuid.New()
		var headers any // the second provider has none
		if i == 0 {
			headers = f.seal(t, "model_providers", "headers_enc", id, "model_provider:"+id.String(), `{"X-Org":"acme"}`)
		}
		f.exec(t, `INSERT INTO model_providers (id, name, kind, base_url, api_key_enc, headers_enc) VALUES ($1, $2, 'openai', 'https://api.example.com/v1', $3, $4)`,
			id, fmt.Sprintf("provider-%d", i), f.seal(t, "model_providers", "api_key_enc", id, "model_provider:"+id.String(), fmt.Sprintf("sk-provider-%d", i)), headers)
	}
	for i := range 2 {
		// The MCP identity token's signing keys (the value stands in for a key).
		id := uuid.New()
		f.exec(t, `INSERT INTO mcp_identity_keys (id, kid, private_key_enc, public_jwk, signs_from) VALUES ($1, $2, $3, '{}', now())`,
			id, fmt.Sprintf("kid-%d", i), f.seal(t, "mcp_identity_keys", "private_key_enc", id, "mcp_identity_key:"+id.String(), fmt.Sprintf("private-key-%d", i)))
	}
	for i := range 2 {
		id := uuid.New()
		f.exec(t, `INSERT INTO mcp_servers (id, name, slug, url, headers_enc) VALUES ($1, $2, $3, 'https://mcp.example.com', $4)`,
			id, fmt.Sprintf("server-%d", i), fmt.Sprintf("server%d", i),
			f.seal(t, "mcp_servers", "headers_enc", id, "mcp_server:"+id.String(), fmt.Sprintf(`{"Authorization":"Bearer mcp-%d"}`, i)))
	}
	return f
}

// stored reads a value's current ciphertext.
func (f *fixture) stored(t testing.TB, v value) []byte {
	t.Helper()
	var ct []byte
	key := "id"
	if v.table == "user_idp_tokens" {
		key = "user_id"
	}
	err := f.pool.QueryRow(context.Background(), fmt.Sprintf(`SELECT %s FROM %s WHERE %s = $1`, v.column, v.table, key), v.id).Scan(&ct)
	if err != nil {
		t.Fatal(err)
	}
	return ct
}

// assertOpensWith checks every stored value opens with b and holds its plaintext.
func (f *fixture) assertOpensWith(t testing.TB, b *secrets.Box, slot secrets.Slot) {
	t.Helper()
	for _, v := range f.values {
		ct := f.stored(t, v)
		if got := b.Which(ct); got != slot {
			t.Errorf("%s.%s %s sealed under %s, want %s", v.table, v.column, v.id, got, slot)
			continue
		}
		plain, err := b.Open(ct, v.ad)
		if err != nil || string(plain) != v.plain {
			t.Errorf("%s.%s %s: opened to %q, %v; want %q", v.table, v.column, v.id, plain, err, v.plain)
		}
	}
}

func (f *fixture) snapshot(t testing.TB) [][]byte {
	t.Helper()
	var out [][]byte
	for _, v := range f.values {
		out = append(out, f.stored(t, v))
	}
	return out
}

func equalSnapshots(a, b [][]byte) bool {
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return len(a) == len(b)
}

func (f *fixture) audits(t testing.TB) []string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT metadata::text FROM audit_log WHERE action = 'platform.secrets_rekey' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func TestRekeyMovesEverythingToTheCurrentKey(t *testing.T) {
	pool, _ := testutil.NewDB(t)
	ctx := context.Background()
	keyA, keyB := key(t), key(t)
	f := seed(t, pool, box(t, keyA))
	if len(f.values) != sealedValues {
		t.Fatalf("fixture holds %d values", len(f.values))
	}
	rotated := box(t, keyB, keyA)
	f.assertOpensWith(t, rotated, secrets.SlotPrevious)

	// Counting: everything is on the previous key.
	counts, err := rekey.Count(ctx, pool, rotated)
	if err != nil {
		t.Fatal(err)
	}
	var cur, prev, bad int
	for _, c := range counts {
		cur, prev, bad = cur+c.Current, prev+c.Previous, bad+c.Unreadable
	}
	if cur != 0 || prev != sealedValues || bad != 0 {
		t.Fatalf("count = %d current, %d previous, %d unreadable", cur, prev, bad)
	}

	// A dry run reports and changes nothing, not even the audit log.
	before := f.snapshot(t)
	res, err := rekey.Run(ctx, pool, rotated, rekey.Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if c, p, u, r, _ := res.Totals(); c != 0 || p != sealedValues || u != 0 || r != 0 || !res.DryRun {
		t.Errorf("dry run = %+v", res)
	}
	if !equalSnapshots(before, f.snapshot(t)) || len(f.audits(t)) != 0 {
		t.Fatal("a dry run changed something")
	}

	// The revision and updated_at of the owning rows stay as they were.
	var rev int64
	var updated time.Time
	row := pool.QueryRow(ctx, `SELECT revision, updated_at FROM model_providers WHERE name = 'provider-0'`)
	if err := row.Scan(&rev, &updated); err != nil {
		t.Fatal(err)
	}

	res, err = rekey.Run(ctx, pool, rotated, rekey.Options{Batch: 1})
	if err != nil {
		t.Fatal(err)
	}
	if c, p, u, r, ch := res.Totals(); c != 0 || p != sealedValues || u != 0 || r != sealedValues || ch != 0 {
		t.Errorf("run = current %d previous %d unreadable %d resealed %d changed %d", c, p, u, r, ch)
	}
	f.assertOpensWith(t, box(t, keyB), secrets.SlotCurrent) // the previous key is no longer needed

	var rev2 int64
	var updated2 time.Time
	if err := pool.QueryRow(ctx, `SELECT revision, updated_at FROM model_providers WHERE name = 'provider-0'`).Scan(&rev2, &updated2); err != nil {
		t.Fatal(err)
	}
	if rev2 != rev || !updated2.Equal(updated) {
		t.Errorf("revision/updated_at changed: %d %v -> %d %v", rev, updated, rev2, updated2)
	}

	// One audit entry for the run, with counts and no values.
	audits := f.audits(t)
	if len(audits) != 1 || !strings.Contains(audits[0], `"resealed": 15`) || !strings.Contains(audits[0], `"complete": true`) {
		t.Fatalf("audit entries = %v", audits)
	}
	for _, v := range f.values {
		if strings.Contains(audits[0], v.plain) {
			t.Fatalf("the audit entry holds a value: %s", audits[0])
		}
	}

	// Idempotent: a second run finds nothing to do and leaves the bytes alone.
	after := f.snapshot(t)
	res, err = rekey.Run(ctx, pool, rotated, rekey.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if c, p, _, r, _ := res.Totals(); c != sealedValues || p != 0 || r != 0 {
		t.Errorf("second run = %+v", res)
	}
	if !equalSnapshots(after, f.snapshot(t)) {
		t.Error("a second run rewrote values")
	}
	// And it works without a previous key at all.
	if _, err := rekey.Run(ctx, pool, box(t, keyB), rekey.Options{DryRun: true}); err != nil {
		t.Errorf("without a previous key: %v", err)
	}
}

func TestRekeyRefusesValuesNoKeyOpens(t *testing.T) {
	pool, _ := testutil.NewDB(t)
	ctx := context.Background()
	keyA, keyB, keyC := key(t), key(t), key(t)
	f := seed(t, pool, box(t, keyA))

	// One value under a third key, one damaged under the previous key.
	foreign := f.values[0]
	ct, err := box(t, keyC).Seal([]byte(foreign.plain), foreign.ad)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, fmt.Sprintf(`UPDATE %s SET %s = $1 WHERE id = $2`, foreign.table, foreign.column), ct, foreign.id)
	damaged := f.values[len(f.values)-1]
	d := f.stored(t, damaged)
	d[len(d)-1] ^= 0xff
	f.exec(t, fmt.Sprintf(`UPDATE %s SET %s = $1 WHERE id = $2`, damaged.table, damaged.column), d, damaged.id)
	before := f.snapshot(t)

	for _, tc := range []struct {
		name string
		box  *secrets.Box
		bad  int
	}{
		{"previous key configured", box(t, keyB, keyA), 2},
		{"previous key missing", box(t, keyB), len(f.values)},
	} {
		for _, dry := range []bool{true, false} {
			res, err := rekey.Run(ctx, pool, tc.box, rekey.Options{DryRun: dry, Batch: 2})
			var ue *rekey.UnreadableError
			if !errors.As(err, &ue) {
				t.Fatalf("%s (dry %v): err = %v", tc.name, dry, err)
			}
			if _, _, u, r, _ := res.Totals(); u != tc.bad || r != 0 {
				t.Errorf("%s (dry %v): unreadable %d resealed %d, want %d, 0", tc.name, dry, u, r, tc.bad)
			}
			msg := err.Error()
			for _, v := range f.values {
				if strings.Contains(msg, v.plain) {
					t.Fatalf("the error holds a value: %s", msg)
				}
			}
			if !strings.Contains(msg, "nothing was changed") || (tc.bad == 2 && !strings.Contains(msg, foreign.id.String())) {
				t.Errorf("error does not say what is wrong: %s", msg)
			}
			if !equalSnapshots(before, f.snapshot(t)) {
				t.Fatalf("%s (dry %v): values changed although the run was refused", tc.name, dry)
			}
		}
	}
	if len(f.audits(t)) != 0 {
		t.Error("a refused run was audited")
	}

	// Count reports them too, by key id.
	counts, err := rekey.Count(ctx, pool, box(t, keyB, keyA))
	if err != nil {
		t.Fatal(err)
	}
	unreadable := 0
	for _, c := range counts {
		unreadable += c.Unreadable
	}
	if unreadable != 1 { // the damaged one still names key A
		t.Errorf("count unreadable = %d, want 1 (the foreign key)", unreadable)
	}
}

// A value saved while a batch is being re-sealed is not overwritten with the
// old value, re-sealed.
func TestRekeyDoesNotLoseAConcurrentSave(t *testing.T) {
	pool, _ := testutil.NewDB(t)
	ctx := context.Background()
	keyA, keyB := key(t), key(t)
	f := seed(t, pool, box(t, keyA))
	rotated := box(t, keyB, keyA)

	idx := -1
	for i, v := range f.values {
		if v.table == "model_providers" && v.column == "api_key_enc" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("no provider key in the fixture")
	}
	target := f.values[idx]
	saved := false
	restore := rekey.SetAfterBatchRead(func(column string) {
		if saved || column != "model_providers"+"."+"api_key_enc" {
			return
		}
		saved = true
		// What the platform does on a save: seal under the current key.
		ct, err := rotated.Seal([]byte("sk-saved-meanwhile"), target.ad)
		if err != nil {
			t.Error(err)
			return
		}
		if _, err := pool.Exec(ctx, `UPDATE model_providers SET api_key_enc = $1, revision = revision + 1 WHERE id = $2`, ct, target.id); err != nil {
			t.Error(err)
		}
	})
	defer restore()

	res, err := rekey.Run(ctx, pool, rotated, rekey.Options{})
	if err != nil {
		t.Fatal(err)
	}
	restore()
	if !saved {
		t.Fatal("the hook never ran")
	}
	if _, _, _, resealed, changed := res.Totals(); changed != 1 || resealed != sealedValues-1 {
		t.Errorf("resealed %d, changed %d; want %d, 1", resealed, changed, sealedValues-1)
	}
	plain, err := box(t, keyB).Open(f.stored(t, target), target.ad)
	if err != nil || string(plain) != "sk-saved-meanwhile" {
		t.Fatalf("the save was lost: %q, %v", plain, err)
	}
	var rev int64
	if err := pool.QueryRow(ctx, `SELECT revision FROM model_providers WHERE id = $1`, target.id).Scan(&rev); err != nil || rev != 2 {
		t.Errorf("revision = %d, %v", rev, err)
	}
	// Everything else moved.
	f.values = append(f.values[:idx], f.values[idx+1:]...)
	f.assertOpensWith(t, box(t, keyB), secrets.SlotCurrent)
}

// Saves and a re-seal at the same time, many rows, small batches: every save
// survives.
func TestRekeyWhileSaving(t *testing.T) {
	pool, _ := testutil.NewDB(t)
	ctx := context.Background()
	keyA, keyB := key(t), key(t)
	old, rotated := box(t, keyA), box(t, keyB, keyA)

	const n = 60
	ids := make([]uuid.UUID, n)
	for i := range ids {
		ids[i] = uuid.New()
		ct, _ := old.Seal([]byte("old"), []byte("model_provider:"+ids[i].String()))
		if _, err := pool.Exec(ctx, `INSERT INTO model_providers (id, name, kind, base_url, api_key_enc) VALUES ($1, $2, 'openai', 'https://x.example.com', $3)`,
			ids[i], fmt.Sprintf("p%d", i), ct); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := n - 1; i >= 0; i-- {
			ct, _ := rotated.Seal([]byte(fmt.Sprintf("new-%d", i)), []byte("model_provider:"+ids[i].String()))
			if _, err := pool.Exec(ctx, `UPDATE model_providers SET api_key_enc = $1 WHERE id = $2`, ct, ids[i]); err != nil {
				t.Error(err)
			}
		}
	}()
	if _, err := rekey.Run(ctx, pool, rotated, rekey.Options{Batch: 3}); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	for i, id := range ids {
		var ct []byte
		if err := pool.QueryRow(ctx, `SELECT api_key_enc FROM model_providers WHERE id = $1`, id).Scan(&ct); err != nil {
			t.Fatal(err)
		}
		plain, err := box(t, keyB).Open(ct, []byte("model_provider:"+id.String()))
		if err != nil || string(plain) != fmt.Sprintf("new-%d", i) {
			t.Errorf("row %d: %q, %v", i, plain, err)
		}
	}
}

// An interrupted run leaves every value valid, and running again finishes.
func TestRekeyResumesAfterAnInterrupt(t *testing.T) {
	pool, _ := testutil.NewDB(t)
	keyA, keyB := key(t), key(t)
	f := seed(t, pool, box(t, keyA))
	rotated := box(t, keyB, keyA)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	batches := 0
	restore := rekey.SetAfterBatchRead(func(string) {
		if batches++; batches == 4 {
			cancel() // in the middle of the run
		}
	})
	res, err := rekey.Run(ctx, pool, rotated, rekey.Options{Batch: 1})
	restore()
	if err == nil {
		t.Fatal("the interrupted run reported success")
	}
	_, _, _, first, _ := res.Totals()
	if first == 0 || first >= sealedValues {
		t.Fatalf("re-sealed %d before the interrupt", first)
	}
	// Whatever state it stopped in, every value opens with the two keys.
	for _, v := range f.values {
		if plain, err := rotated.Open(f.stored(t, v), v.ad); err != nil || string(plain) != v.plain {
			t.Fatalf("%s.%s unreadable after the interrupt: %v", v.table, v.column, err)
		}
	}

	res, err = rekey.Run(context.Background(), pool, rotated, rekey.Options{Batch: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, second, _ := res.Totals(); first+second != sealedValues {
		t.Errorf("re-sealed %d + %d, want %d", first, second, sealedValues)
	}
	f.assertOpensWith(t, box(t, keyB), secrets.SlotCurrent)
	audits := f.audits(t)
	if len(audits) != 2 || !strings.Contains(audits[0], `"complete": false`) || !strings.Contains(audits[1], `"complete": true`) {
		t.Errorf("audit entries = %v", audits)
	}
}

func TestRekeyBatchBounds(t *testing.T) {
	pool, _ := testutil.NewDB(t)
	for _, n := range []int{-1, rekey.MaxBatch + 1} {
		if _, err := rekey.Run(context.Background(), pool, box(t, key(t)), rekey.Options{Batch: n}); err == nil {
			t.Errorf("batch %d accepted", n)
		}
	}
}

// The associated data of each column is the one its service uses. The
// services keep theirs unexported, so this reads their sources.
func TestAssociatedDataMatchesTheServices(t *testing.T) {
	for file, snippets := range map[string][]string{
		"../llm/service.go":             {`[]byte("model_provider:" + id.String())`},
		"../mcp/service.go":             {`[]byte("mcp_server:" + id.String())`, `[]byte("thread_mcp_server:" + id.String())`},
		"../agents/agents.go":           {`ad := []byte(p.ID.String())`, `[]byte(ag.ID.String())`},
		"../auth/grouprecheck.go":       {`[]byte("idp_refresh_token:" + userID.String())`},
		"../mcpidentity/mcpidentity.go": {`[]byte("mcp_identity_key:" + id.String())`},
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range snippets {
			if !strings.Contains(string(src), s) {
				t.Errorf("%s no longer contains %s: update the associated data in internal/rekey", file, s)
			}
		}
	}
}

// Every column the schema names *_enc holds sealed values: a new one must be added to the columns a run re-seals, or
// dropping the previous key would leave its values unreadable.
func TestEverySealedColumnIsRekeyed(t *testing.T) {
	pool, _ := testutil.NewDB(t)
	rows, err := pool.Query(context.Background(), `SELECT table_name || '.' || column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND column_name LIKE '%\_enc' ESCAPE '\' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	covered := map[string]bool{}
	for _, c := range rekey.SealedColumns() {
		covered[c] = true
	}
	n := 0
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		n++
		if !covered[c] {
			t.Errorf("%s looks sealed (*_enc) but internal/rekey does not re-seal it", c)
		}
		delete(covered, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for c := range covered {
		t.Errorf("internal/rekey re-seals %s, which the schema does not have", c)
	}
	if n == 0 {
		t.Fatal("found no *_enc columns: the query is wrong")
	}
}
