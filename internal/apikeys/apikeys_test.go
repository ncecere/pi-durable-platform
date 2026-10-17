package apikeys

import (
	"strings"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

func TestKeyShapeAndDigest(t *testing.T) {
	good := Prefix + strings.Repeat("aZ0-_", 8) + "abc"
	if !LooksLikeKey(good) {
		t.Fatal("valid key refused")
	}
	for _, bad := range []string{"", "pdp_short", "sk_" + strings.Repeat("a", 45), good + "x", strings.Replace(good, "a", "!", 1)} {
		if LooksLikeKey(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	d1, d2 := Digest([]byte("pepper-1"), good), Digest([]byte("pepper-2"), good)
	if d1 == d2 || len(d1) != 64 || strings.Contains(d1, good) {
		t.Fatal("digest does not depend on the pepper")
	}
}

func TestStatus(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	if s := Status(dbgen.ApiKey{ExpiresAt: future}, now); s != StatusActive {
		t.Error(s)
	}
	if s := Status(dbgen.ApiKey{ExpiresAt: past}, now); s != StatusExpired {
		t.Error(s)
	}
	if s := Status(dbgen.ApiKey{ExpiresAt: future, RevokedAt: &past}, now); s != StatusRevoked {
		t.Error(s)
	}
}
