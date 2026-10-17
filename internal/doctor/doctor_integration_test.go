package doctor_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/doctor"
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
	if c, _ := find(rep, "postgres", "migrations"); c.Status != doctor.OK || !strings.Contains(c.Detail, "current (version 5)") {
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
	var buf bytes.Buffer
	if err := rep.WriteText(&buf); err != nil || !strings.Contains(buf.String(), "all checks passed") {
		t.Errorf("text = %s %v", buf.String(), err)
	}
	buf.Reset()
	if err := rep.WriteJSON(&buf); err != nil || !strings.Contains(buf.String(), `"ok": true`) {
		t.Errorf("json = %s %v", buf.String(), err)
	}
}

func TestDoctorPendingMigrations(t *testing.T) {
	pool, dbURL := testutil.NewDB(t)
	if _, err := pool.Exec(context.Background(), "DELETE FROM goose_db_version WHERE version_id = 5"); err != nil {
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
