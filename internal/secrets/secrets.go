// Package secrets encrypts small secrets (such as credentials an agent uses)
// for storage in PostgreSQL, using AES-256-GCM (adapted from Grounded).
//
// The key comes from ENCRYPTION_KEY (32 random bytes, base64). For rotation,
// set the new key as ENCRYPTION_KEY and the old one as
// ENCRYPTION_KEY_PREVIOUS: new writes use the new key and old values still
// decrypt. NeedsRotation tells which stored values are still on the old key
// (re-entering a value re-encrypts it; there is no bulk rotate command yet).
//
// Ciphertext layout (format version 1, the only format ever written):
//
//	version(1) = 0x01 | key id(8) | nonce(12) | sealed data + GCM tag(16)
//
// The key id is the first 8 bytes of SHA-256(key), so the right key is found
// without trial decryption and a rotation can tell, from the stored bytes
// alone, which values are still on an old key. The caller supplies
// associated data (for example "agent_secret:<id>") so a ciphertext
// cannot be moved to another row.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

const (
	version byte = 1
	// IDOffset and IDLen locate the key id inside a ciphertext (for SQL
	// filters: substring(col FROM IDOffset+1 FOR IDLen)).
	IDOffset = 1
	IDLen    = 8
	nonceLen = 12
)

// ErrUndecryptable is returned for every value that cannot be decrypted;
// the wrapped detail says why (malformed, unknown key, or authentication
// failure: wrong key material, tampering or other associated data).
var ErrUndecryptable = errors.New("secret cannot be decrypted with the configured keys")

var (
	errMalformed  = fmt.Errorf("%w: not a sealed value", ErrUndecryptable)
	errUnknownKey = fmt.Errorf("%w: sealed with a key that is neither ENCRYPTION_KEY nor ENCRYPTION_KEY_PREVIOUS", ErrUndecryptable)
	errAuth       = fmt.Errorf("%w: authentication failed", ErrUndecryptable)
)

// Slot says which configured key sealed a ciphertext.
type Slot int

const (
	SlotMalformed Slot = iota // not a version-1 sealed value
	SlotUnknown               // a key that is not configured
	SlotCurrent               // ENCRYPTION_KEY
	SlotPrevious              // ENCRYPTION_KEY_PREVIOUS
)

func (s Slot) String() string {
	return [...]string{"malformed", "unknown key", "current key", "previous key"}[s]
}

type key struct {
	id   [IDLen]byte
	aead cipher.AEAD
}

// Box encrypts with the current key and decrypts with any configured key.
type Box struct {
	current key
	keys    []key
}

// ParseKey decodes a base64 (standard or URL, padded or not) 32-byte key.
func ParseKey(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			if len(b) != 32 {
				return nil, fmt.Errorf("key must be 32 bytes, got %d", len(b))
			}
			return b, nil
		}
	}
	return nil, errors.New("key must be base64")
}

// KeyID is the id a key's ciphertexts carry: the first 8 bytes of SHA-256.
func KeyID(raw []byte) []byte {
	sum := sha256.Sum256(raw)
	return sum[:IDLen]
}

func newKey(raw []byte) (key, error) {
	block, err := aes.NewCipher(raw)
	if err != nil {
		return key{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return key{}, err
	}
	var k key
	copy(k.id[:], KeyID(raw))
	k.aead = aead
	return k, nil
}

// New builds a Box from the current key and optional previous keys. A
// previous key equal to the current one is ignored.
func New(current []byte, previous ...[]byte) (*Box, error) {
	cur, err := newKey(current)
	if err != nil {
		return nil, err
	}
	b := &Box{current: cur, keys: []key{cur}}
	for _, p := range previous {
		k, err := newKey(p)
		if err != nil {
			return nil, err
		}
		if k.id != cur.id {
			b.keys = append(b.keys, k)
		}
	}
	return b, nil
}

// CurrentKeyID is the id of ENCRYPTION_KEY, carried by every new ciphertext.
func (b *Box) CurrentKeyID() []byte { return append([]byte(nil), b.current.id[:]...) }

// HasPrevious reports whether a previous key (different from the current
// one) is configured.
func (b *Box) HasPrevious() bool { return len(b.keys) > 1 }

// Seal encrypts plaintext bound to associated data.
func (b *Box) Seal(plaintext, associated []byte) ([]byte, error) {
	nonce := make([]byte, b.current.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+IDLen+len(nonce)+len(plaintext)+b.current.aead.Overhead())
	out = append(out, version)
	out = append(out, b.current.id[:]...)
	out = append(out, nonce...)
	return b.current.aead.Seal(out, nonce, plaintext, associated), nil
}

// Which says which configured key sealed ciphertext, from its key id alone
// (Open still has to succeed for the value to be usable).
func (b *Box) Which(ciphertext []byte) Slot {
	s, _ := b.find(ciphertext)
	return s
}

func (b *Box) find(ciphertext []byte) (Slot, *key) {
	if len(ciphertext) < 1+IDLen+nonceLen+16 || ciphertext[0] != version {
		return SlotMalformed, nil
	}
	id := string(ciphertext[IDOffset : IDOffset+IDLen])
	for i := range b.keys {
		if string(b.keys[i].id[:]) == id {
			if i == 0 {
				return SlotCurrent, &b.keys[i]
			}
			return SlotPrevious, &b.keys[i]
		}
	}
	return SlotUnknown, nil
}

// Open decrypts a value produced by Seal with the same associated data,
// using the key its id names.
func (b *Box) Open(ciphertext, associated []byte) ([]byte, error) {
	slot, k := b.find(ciphertext)
	switch slot {
	case SlotMalformed:
		return nil, errMalformed
	case SlotUnknown:
		return nil, errUnknownKey
	}
	rest := ciphertext[IDOffset+IDLen:]
	plain, err := k.aead.Open(nil, rest[:nonceLen], rest[nonceLen:], associated)
	if err != nil {
		return nil, errAuth
	}
	return plain, nil
}

// NeedsRotation reports whether ciphertext was sealed with a non-current key.
func (b *Box) NeedsRotation(ciphertext []byte) bool {
	return b.Which(ciphertext) != SlotCurrent
}
