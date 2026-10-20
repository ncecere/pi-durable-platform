package doctor_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/doctor"
	"github.com/ncecere/pi-durable-platform/internal/mcpidentity"
	"github.com/ncecere/pi-durable-platform/internal/secrets"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// keys mcp-identity: none yet (serve makes it), the key that signs, too old
// (rotate), too old with a successor published, and none signing.
func TestDoctorMCPIdentityKey(t *testing.T) {
	pool, dbURL := testutil.NewDB(t)
	ctx := context.Background()
	cfg := devConfig(dbURL)
	check := func(what string, want doctor.Status, detail string) {
		t.Helper()
		rep := doctor.Run(ctx, cfg, doctor.Options{Timeout: 10 * time.Second})
		c, ok := find(rep, "keys", "mcp-identity")
		if !ok || c.Status != want || !strings.Contains(c.Detail, detail) {
			t.Errorf("%s: %+v, want %s with %q", what, c, want, detail)
		}
	}
	check("no key", doctor.Warn, "no key signs")

	key, err := secrets.ParseKey(cfg.EncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	keys := mcpidentity.New(pool, box, cfg.AppURL)
	if _, err := keys.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	check("a key", doctor.OK, "signs, since")

	if _, err := pool.Exec(ctx, `UPDATE mcp_identity_keys SET signs_from = now() - interval '100 days'`); err != nil {
		t.Fatal(err)
	}
	check("an old key", doctor.Warn, "more than 90 days")
	if _, err := keys.Rotate(ctx, mcpidentity.RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	check("an old key with its successor published", doctor.Info, "1 key(s) published")

	if _, err := pool.Exec(ctx, `DELETE FROM mcp_identity_keys WHERE signs_from < now()`); err != nil {
		t.Fatal(err)
	}
	check("only a key waiting to sign", doctor.Fail, "no MCP identity key signs now")
}
