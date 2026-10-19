package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
)

func randomKey(t *testing.T) []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	box, err := New(randomKey(t))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := box.Seal([]byte("sk-secret"), []byte("row:1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("sk-secret")) {
		t.Fatal("plaintext visible in ciphertext")
	}
	pt, err := box.Open(ct, []byte("row:1"))
	if err != nil || string(pt) != "sk-secret" {
		t.Fatalf("open = %q %v", pt, err)
	}
	if _, err := box.Open(ct, []byte("row:2")); !errors.Is(err, ErrUndecryptable) {
		t.Fatal("ciphertext opened with different associated data")
	}
	ct[len(ct)-1] ^= 1
	if _, err := box.Open(ct, []byte("row:1")); !errors.Is(err, ErrUndecryptable) {
		t.Fatal("tampered ciphertext opened")
	}
}

func TestRotation(t *testing.T) {
	oldKey, newKey := randomKey(t), randomKey(t)
	oldBox, _ := New(oldKey)
	ct, _ := oldBox.Seal([]byte("v"), nil)

	rotated, err := New(newKey, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	if pt, err := rotated.Open(ct, nil); err != nil || string(pt) != "v" {
		t.Fatalf("previous key not usable: %v", err)
	}
	if !rotated.NeedsRotation(ct) {
		t.Fatal("old ciphertext not flagged for rotation")
	}
	fresh, _ := rotated.Seal([]byte("v"), nil)
	if rotated.NeedsRotation(fresh) {
		t.Fatal("new ciphertext flagged for rotation")
	}
	onlyNew, _ := New(newKey)
	if _, err := onlyNew.Open(ct, nil); !errors.Is(err, ErrUndecryptable) {
		t.Fatal("old ciphertext opened without the old key")
	}
}

func TestParseKey(t *testing.T) {
	k := randomKey(t)
	for _, s := range []string{base64.StdEncoding.EncodeToString(k), base64.RawURLEncoding.EncodeToString(k)} {
		if got, err := ParseKey(s); err != nil || !bytes.Equal(got, k) {
			t.Fatalf("ParseKey(%q) = %v", s, err)
		}
	}
	if _, err := ParseKey(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("short key accepted")
	}
}

// A ciphertext names its key: the prefixes the SQL filters of rekey and
// doctor compare are exactly what Seal writes, per key.
func TestKeyIDPrefixes(t *testing.T) {
	keyA, keyB, keyC := randomKey(t), randomKey(t), randomKey(t)
	a, _ := New(keyA)
	ctA, _ := a.Seal([]byte("v"), nil)
	if !bytes.HasPrefix(ctA, a.CurrentPrefix()) || len(a.CurrentPrefix()) != PrefixLen {
		t.Fatalf("a ciphertext does not start with its box's prefix: %x vs %x", ctA[:PrefixLen], a.CurrentPrefix())
	}
	if !bytes.Equal(a.CurrentPrefix(), Prefix(KeyID(keyA))) || !bytes.Equal(a.CurrentKeyID(), KeyID(keyA)) {
		t.Fatal("prefix and key id do not agree")
	}
	if got := a.PreviousPrefixes(); got == nil || len(got) != 0 {
		t.Fatalf("a box without a previous key has previous prefixes %x", got)
	}

	b, _ := New(keyB, keyA, keyA) // a repeated previous key counts once
	prefixes := b.PreviousPrefixes()
	if len(prefixes) != 1 || !bytes.Equal(prefixes[0], a.CurrentPrefix()) {
		t.Fatalf("previous prefixes = %x", prefixes)
	}
	if bytes.Equal(b.CurrentPrefix(), a.CurrentPrefix()) {
		t.Fatal("two keys share a prefix")
	}
	// A previous key equal to the current one is no previous key.
	same, _ := New(keyA, keyA)
	if same.HasPrevious() || len(same.PreviousPrefixes()) != 0 {
		t.Fatal("the current key counts as its own previous key")
	}

	// Which, from the bytes alone.
	ctB, _ := b.Seal([]byte("v"), nil)
	ctC, _ := mustBox(t, keyC).Seal([]byte("v"), nil)
	for name, tc := range map[string]struct {
		ct   []byte
		want Slot
	}{
		"current":   {ctB, SlotCurrent},
		"previous":  {ctA, SlotPrevious},
		"unknown":   {ctC, SlotUnknown},
		"empty":     {nil, SlotMalformed},
		"short":     {ctA[:PrefixLen], SlotMalformed},
		"version 2": {append([]byte{2}, ctA[1:]...), SlotMalformed},
	} {
		if got := b.Which(tc.ct); got != tc.want {
			t.Errorf("%s: Which = %s, want %s", name, got, tc.want)
		}
		if b.NeedsRotation(tc.ct) != (tc.want != SlotCurrent) {
			t.Errorf("%s: NeedsRotation disagrees with Which", name)
		}
	}
	// A key that is not configured, and a value that is no sealed value,
	// both fail as ErrUndecryptable.
	if _, err := b.Open(ctC, nil); !errors.Is(err, ErrUndecryptable) {
		t.Errorf("unknown key: %v", err)
	}
	if _, err := b.Open([]byte("not sealed at all, just text"), nil); !errors.Is(err, ErrUndecryptable) {
		t.Errorf("malformed: %v", err)
	}
	// The previous key opens its values, with their associated data.
	sealedWithAD, _ := a.Seal([]byte("v"), []byte("row-1"))
	if _, err := b.Open(sealedWithAD, []byte("row-2")); !errors.Is(err, ErrUndecryptable) {
		t.Error("a value opened for another row")
	}
	if pt, err := b.Open(sealedWithAD, []byte("row-1")); err != nil || string(pt) != "v" {
		t.Errorf("previous key: %q %v", pt, err)
	}
}

func mustBox(t *testing.T, k []byte, previous ...[]byte) *Box {
	t.Helper()
	b, err := New(k, previous...)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
