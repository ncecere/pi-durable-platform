package provision_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/agenthost/provision"
)

// The provisioner runs as a CREATEROLE role that is not a superuser, owner
// of the agents database: as deployed. Needs PDP_TEST_DATABASE_URL (a
// superuser connection where it may create a database and a role).
func TestProvisionAsCreateRole(t *testing.T) {
	adminURL := os.Getenv("PDP_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PDP_TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	suffix := fmt.Sprint(time.Now().UnixNano())
	db, role, password := "pdp_prov_"+suffix, "pdp_prov_admin_"+suffix, "prov-test-"+suffix
	su, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer su.Close(ctx)
	for _, stmt := range []string{
		fmt.Sprintf("CREATE ROLE %s LOGIN CREATEROLE PASSWORD '%s'", role, password),
		fmt.Sprintf("CREATE DATABASE %s OWNER %s", db, role),
	} {
		if _, err := su.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	agentIDs := []string{"11111111-2222-3333-4444-555555555555", "66666666-7777-8888-9999-000000000000"}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), adminURL)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
		for _, id := range agentIDs {
			name, _ := provision.NameFor(id)
			_, _ = c.Exec(context.Background(), "DROP ROLE IF EXISTS "+name)
		}
		_, _ = c.Exec(context.Background(), "DROP ROLE IF EXISTS "+role)
	})

	u, _ := url.Parse(adminURL)
	u.User, u.Path = url.UserPassword(role, password), "/"+db
	agentsURL := u.String()
	pool, err := pgxpool.New(ctx, agentsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	p := provision.New(pool)
	if err := p.Prepare(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	creds := make([]provision.Credentials, len(agentIDs))
	for i, id := range agentIDs {
		if creds[i], err = p.Create(ctx, id); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	// Running Create again rotates the password.
	if creds[0], err = p.Create(ctx, agentIDs[0]); err != nil {
		t.Fatalf("create again: %v", err)
	}

	// Each agent uses its own schema and cannot read the other's.
	connect := func(c provision.Credentials) *pgx.Conn {
		dsn, err := c.DSN(agentsURL, "")
		if err != nil {
			t.Fatal(err)
		}
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatalf("connect as %s: %v", c.Role, err)
		}
		return conn
	}
	a, b := connect(creds[0]), connect(creds[1])
	defer b.Close(ctx)
	if _, err := a.Exec(ctx, fmt.Sprintf("CREATE TABLE %s.notes (body text)", creds[0].Schema)); err != nil {
		t.Fatalf("agent creates a table in its schema: %v", err)
	}
	if _, err := b.Exec(ctx, fmt.Sprintf("SELECT * FROM %s.notes", creds[0].Schema)); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("another agent read the table: %v", err)
	}
	if _, err := b.Exec(ctx, "CREATE TABLE public.leak (x int)"); err == nil {
		t.Fatal("an agent created a table in schema public")
	}

	// Evict ends the agent's sessions; Delete removes its schema and role.
	if err := p.Evict(ctx, agentIDs[0]); err != nil {
		t.Fatalf("evict: %v", err)
	}
	if err := a.Ping(ctx); err == nil {
		t.Fatal("the evicted session still answers")
	}
	for _, id := range agentIDs {
		if err := p.Delete(ctx, id); err != nil {
			t.Fatalf("delete %s: %v", id, err)
		}
	}
	if names, err := p.Names(ctx); err != nil || len(names) != 0 {
		t.Fatalf("left after delete: %v %v", names, err)
	}
}
