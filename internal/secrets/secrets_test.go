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
