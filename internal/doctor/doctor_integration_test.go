package doctor_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/agenthost/provision"
	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/doctor"
	"github.com/ncecere/pi-durable-platform/internal/rekey"
	"github.com/ncecere/pi-durable-platform/internal/secrets"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

func find(rep doctor.Report, group, name string) (doctor.Check, bool) {
	for _, c := range rep.Checks {
		if c.Group == group && c.Name == name {
			return c, true
		}
	}
	return doctor.Check{}, false
}

func devConfig(dbURL string) config.Config {
	c := config.Defaults()
	c.DatabaseURL, c.AppURL, c.DevAuth = dbURL, "http://localhost:8098", true
	c.EncryptionKey = "MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE="
	c.APIKeyPepper = c.EncryptionKey
	c.Agents.DatabaseURL = dbURL
	return c
}

func TestDoctorHealthyInstall(t *testing.T) {
	_, dbURL := testutil.NewDB(t)
	rep := doctor.Run(context.Background(), devConfig(dbURL), doctor.Options{Timeout: 10 * time.Second})
	if !rep.OK {
		t.Fatalf("report not ok: %+v", rep.Checks)
	}
	if c, _ := find(rep, "postgres", "migrations"); c.Status != doctor.OK || !strings.Contains(c.Detail, "current (version 26)") {
		t.Errorf("migrations = %+v", c)
	}
	if c, _ := find(rep, "safety", "production"); c.Status != doctor.Info {
		t.Errorf("safety = %+v", c)
	}
	for _, name := range []string{"pepper"} {
		if c, ok := find(rep, "keys", name); !ok || c.Status != doctor.OK {
			t.Errorf("keys %s = %+v", name, c)
		}
	}
	for _, name := range []string{"database", "provisioning"} {
		if c, ok := find(rep, "agents", name); !ok || c.Status != doctor.OK {
			t.Errorf("agents %s = %+v", name, c)
		}
	}
	var buf bytes.Buffer
	if err := rep.WriteText(&buf); err != nil || !strings.Contains(buf.String(), "all checks passed") {
		t.Errorf("text = %s %v", buf.String(), err)
	}
	buf.Reset()
	if err := rep.WriteJSON(&buf); err != nil || !strings.Contains(buf.String(), `"ok": true`) {
		t.Errorf("json = %s %v", buf.String(), err)
	}
}

// The agents database's role: reachable but unable to provision, a database that
// doesn't exist (yet), and no agents database at all.
func TestDoctorAgentsDatabase(t *testing.T) {
	pool, dbURL := testutil.NewDB(t)
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())
	role, password := "pdp_doctor_"+suffix, "doctor-test-"+suffix
	if _, err := pool.Exec(ctx, fmt.Sprintf("CREATE ROLE %s LOGIN NOCREATEROLE PASSWORD '%s'", role, password)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), testutil.DatabaseURL())
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), "DROP ROLE IF EXISTS "+role)
	})
	u, _ := url.Parse(dbURL)
	u.User = url.UserPassword(role, password)
	cfg := devConfig(dbURL)
	cfg.Agents.DatabaseURL = u.String()
	rep := doctor.Run(ctx, cfg, doctor.Options{})
	if c, _ := find(rep, "agents", "database"); c.Status != doctor.OK || !strings.Contains(c.Detail, "as "+role) {
		t.Errorf("database = %+v", c)
	}
	c, _ := find(rep, "agents", "provisioning")
	if rep.OK || c.Status != doctor.Fail || !strings.Contains(c.Detail, "lacks CREATEROLE and CREATE on database") || c.Fix == "" {
		t.Errorf("provisioning without privileges = %+v", c)
	}
	if strings.Contains(fmt.Sprintf("%+v", rep), password) {
		t.Error("the report shows the agents database password")
	}

	// A database that doesn't exist: broken, unless serve creates it.
	u, _ = url.Parse(dbURL)
	u.Path = "/pdp_doctor_missing_" + suffix
	cfg = devConfig(dbURL)
	cfg.Agents.DatabaseURL = u.String()
	if c, _ := find(doctor.Run(ctx, cfg, doctor.Options{}), "agents", "database"); c.Status != doctor.Fail {
		t.Errorf("missing database = %+v", c)
	}
	cfg.Agents.CreateDatabase = true
	rep = doctor.Run(ctx, cfg, doctor.Options{})
	if c, _ := find(rep, "agents", "database"); !rep.OK || c.Status != doctor.Info || !strings.Contains(c.Detail, "serve creates it") {
		t.Errorf("missing database with AGENTS_CREATE_DATABASE = %+v", c)
	}

	// No agents database (migrate needs none): skipped.
	cfg = devConfig(dbURL)
	cfg.Agents.DatabaseURL = ""
	rep = doctor.Run(ctx, cfg, doctor.Options{Mode: "migrate"})
	if c, _ := find(rep, "agents", "database"); !rep.OK || c.Status != doctor.Info || !strings.HasPrefix(c.Detail, "skipped") {
		t.Errorf("no agents database = %+v (%+v)", c, rep.Checks)
	}
	if _, ok := find(rep, "agents", "provisioning"); ok {
		t.Error("provisioning checked without an agents database")
	}
}

// Agent roles are server-wide, so a dropped agents database leaves them behind:
// doctor lists the ones that belong to no agent and no schema, and says how to
// remove them. The provisioning role is not a superuser, so only roles it made
// are considered (the server's other roles named like agents' are left alone).
func TestDoctorOrphanedAgentRoles(t *testing.T) {
	pool, dbURL := testutil.NewDB(t)
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())
	role, password := "pdp_doctor_prov_"+suffix, "doctor-test-"+suffix
	u, _ := url.Parse(dbURL)
	db := strings.TrimPrefix(u.Path, "/")
	for _, stmt := range []string{
		fmt.Sprintf("CREATE ROLE %s LOGIN CREATEROLE PASSWORD '%s'", role, password),
		fmt.Sprintf("GRANT CREATE ON DATABASE %s TO %s", db, role),
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	agentID := "99999999-aaaa-bbbb-cccc-" + fmt.Sprintf("%012d", time.Now().UnixNano()%1e12)
	orphan, _ := provision.NameFor(agentID)
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), testutil.DatabaseURL())
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), "DROP ROLE IF EXISTS "+orphan)
		_, _ = c.Exec(context.Background(), "DROP ROLE IF EXISTS "+role)
	})
	u.User = url.UserPassword(role, password)
	cfg := devConfig(dbURL)
	cfg.Agents.DatabaseURL = u.String()

	// An agent's storage whose schema is gone (the agents database was recreated) and that no agent has.
	agents, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer agents.Close()
	if _, err := provision.New(agents).Create(ctx, agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DROP SCHEMA "+orphan+" CASCADE"); err != nil {
		t.Fatal(err)
	}
	rep := doctor.Run(ctx, cfg, doctor.Options{})
	c, ok := find(rep, "agents", "roles")
	if !ok || !rep.OK || c.Status != doctor.Info || !strings.Contains(c.Detail, orphan) || !strings.Contains(c.Fix, "prune-roles") {
		t.Fatalf("orphaned role not reported: %+v (ok %v, report ok %v)", c, ok, rep.OK)
	}
	if strings.Contains(fmt.Sprintf("%+v", rep), password) {
		t.Error("the report shows the agents database password")
	}

	// Pruned: no longer listed.
	if _, _, err := provision.New(agents).PruneRoles(ctx, nil, false); err != nil {
		t.Fatal(err)
	}
	if c, _ := find(doctor.Run(ctx, cfg, doctor.Options{}), "agents", "roles"); strings.Contains(c.Detail, orphan) {
		t.Errorf("pruned role still listed: %+v", c)
	}
}

func TestDoctorPendingMigrations(t *testing.T) {
	pool, dbURL := testutil.NewDB(t)
	if _, err := pool.Exec(context.Background(), "DELETE FROM goose_db_version WHERE version_id = 7"); err != nil {
		t.Fatal(err)
	}
	rep := doctor.Run(context.Background(), devConfig(dbURL), doctor.Options{})
	if c, _ := find(rep, "postgres", "migrations"); rep.OK || c.Status != doctor.Fail || !strings.Contains(c.Detail, "pending") {
		t.Fatalf("pending migrations not reported: %+v", rep.Checks)
	}
}

func TestDoctorUnreachableAndUnsafe(t *testing.T) {
	c := config.Defaults()
	c.DatabaseURL = "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=1"
	c.AppURL, c.DevAuth = "http://agents.example.com", true
	c.Agents.Launcher, c.Agents.Image = config.LauncherKubernetes, "registry.example.com/runtime:1"
	rep := doctor.Run(context.Background(), c, doctor.Options{Timeout: 3 * time.Second})
	if rep.OK {
		t.Fatal("unreachable database and unsafe config reported ok")
	}
	if ch, _ := find(rep, "postgres", "connect"); ch.Status != doctor.Fail {
		t.Errorf("connect = %+v", ch)
	}
	var unsafe int
	for _, ch := range rep.Checks {
		if ch.Group == "safety" && ch.Status == doctor.Fail {
			unsafe++
		}
	}
	if unsafe != 2 {
		t.Errorf("safety failures = %d, want 2 (DEV_AUTH, http): %+v", unsafe, rep.Checks)
	}
	if r := doctor.ConfigLoadFailed("serve", errors.Join(errors.New("A: bad"), errors.New("B: bad"))); r.OK || len(r.Checks) != 2 {
		t.Errorf("load failure = %+v", r)
	}
}

// Stored secrets by the key they are sealed with: all current, some still on
// ENCRYPTION_KEY_PREVIOUS (a warning that doesn't fail doctor, until rekey),
// previous key removable, and sealed with a key nobody configured.
func TestDoctorEncryptionKeys(t *testing.T) {
	pool, dbURL := testutil.NewDB(t)
	ctx := context.Background()
	keyA := []byte("0123456789abcdef0123456789abcdeA")
	keyB := []byte("0123456789abcdef0123456789abcdeB")
	keyC := []byte("0123456789abcdef0123456789abcdeC")
	enc := base64.StdEncoding.EncodeToString
	boxOf := func(k []byte) *secrets.Box {
		b, err := secrets.New(k)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	insert := func(name string, b *secrets.Box) uuid.UUID {
		id := uuid.New()
		ct, err := b.Seal([]byte("sk-"+name), []byte("model_provider:"+id.String()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO model_providers (id, name, kind, base_url, api_key_enc) VALUES ($1, $2, 'openai', 'https://x.example.com', $3)`, id, name, ct); err != nil {
			t.Fatal(err)
		}
		return id
	}
	run := func(current []byte, previous []byte) doctor.Check {
		cfg := devConfig(dbURL)
		cfg.EncryptionKey = enc(current)
		if previous != nil {
			cfg.EncryptionKeyPrevious = enc(previous)
		}
		rep := doctor.Run(ctx, cfg, doctor.Options{Timeout: 10 * time.Second})
		c, ok := find(rep, "keys", "encryption")
		if !ok {
			t.Fatalf("no encryption check: %+v", rep.Checks)
		}
		if (c.Status == doctor.Fail) == rep.OK { // only a failing check fails the report here
			t.Errorf("a %s check and a report with ok=%v: %+v", c.Status, rep.OK, rep.Checks)
		}
		return c
	}

	// Nothing stored: fine with or without a previous key.
	if c := run(keyA, nil); c.Status != doctor.OK {
		t.Errorf("empty = %+v", c)
	}
	if c := run(keyB, keyA); c.Status != doctor.Info || !strings.Contains(c.Detail, "can be removed") {
		t.Errorf("empty with a previous key = %+v", c)
	}

	// Sealed under A; B is current and A previous.
	insert("a1", boxOf(keyA))
	insert("a2", boxOf(keyA))
	if c := run(keyA, nil); c.Status != doctor.OK {
		t.Errorf("all current = %+v", c)
	}
	c := run(keyB, keyA)
	if c.Status != doctor.Warn || !strings.Contains(c.Detail, "2 stored secret(s) are still sealed with ENCRYPTION_KEY_PREVIOUS") ||
		!strings.Contains(c.Detail, "model_providers.api_key_enc 2") || !strings.Contains(c.Fix, "rekey") {
		t.Errorf("previous = %+v", c)
	}
	if strings.Contains(c.Detail+c.Fix, "sk-a1") {
		t.Error("the check shows a value")
	}
	// The text report says so and doctor still exits zero (a warning).
	rep := doctor.Run(ctx, func() config.Config {
		cfg := devConfig(dbURL)
		cfg.EncryptionKey, cfg.EncryptionKeyPrevious = enc(keyB), enc(keyA)
		return cfg
	}(),
		doctor.Options{Timeout: 10 * time.Second})
	var buf bytes.Buffer
	if err := rep.WriteText(&buf); err != nil || !rep.OK || !strings.Contains(buf.String(), "warn  keys encryption") {
		t.Errorf("report ok=%v err=%v:\n%s", rep.OK, err, buf.String())
	}

	// After rekey: nothing left on the previous key.
	b, _ := secrets.New(keyB, keyA)
	if _, err := rekey.Run(ctx, pool, b, rekey.Options{}); err != nil {
		t.Fatal(err)
	}
	if c := run(keyB, keyA); c.Status != doctor.Info || !strings.Contains(c.Detail, "can be removed") {
		t.Errorf("after rekey = %+v", c)
	}
	if c := run(keyB, nil); c.Status != doctor.OK {
		t.Errorf("after rekey without a previous key = %+v", c)
	}

	// A value under a key nobody configured fails the check.
	insert("c1", boxOf(keyC))
	c = run(keyB, keyA)
	if c.Status != doctor.Fail || !strings.Contains(c.Detail, "1 stored secret(s) are sealed with neither") {
		t.Errorf("unknown key = %+v", c)
	}
}
