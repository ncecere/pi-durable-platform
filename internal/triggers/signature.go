package triggers

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Signatures, secrets and IDs (docs/triggers.md).
//
// A webhook delivery and a completion callback are signed the same way:
// X-PDP-Signature: t=<unix seconds>,v1=<hex HMAC-SHA256(secret, "<t>." + body)>.

// SignatureHeader carries a signature.
const SignatureHeader = "X-PDP-Signature"

// Tolerance is how far a webhook's signature time may be from the
// platform's clock, either way.
const Tolerance = 5 * time.Minute

// Secret prefixes: webhook secrets and callback secrets.
const (
	webhookSecretPrefix  = "pdpwh_"
	callbackSecretPrefix = "pdpcb_"
	// shownPrefix is how many of a secret's characters are shown again.
	shownPrefix = 12
)

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the platform cannot make secrets without randomness
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// newSecret is a secret (256 bits) with its kind's prefix.
func newSecret(prefix string) string { return prefix + randomToken(32) }

// newHookID is a webhook's address: 128 random bits.
func newHookID() string { return "h_" + randomToken(16) }

// newThreadID is a platform-chosen thread ID (as agents.CreateThread's).
func newThreadID() string { return "t_" + randomToken(16) }

// Sign is the signature header's value for body at t.
func Sign(secret string, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	return "t=" + ts + ",v1=" + mac(secret, ts, body)
}

func mac(secret, ts string, body []byte) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(ts))
	h.Write([]byte("."))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// verify checks a signature header against the body and the secret, and its
// time against now (Tolerance either way). It returns the v1 signature that
// matched.
func verify(header, secret string, body []byte, now time.Time) (string, bool) {
	if len(header) > 1024 {
		return "", false
	}
	var ts string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(sigs) == 0 {
		return "", false
	}
	if d := now.Sub(time.Unix(sec, 0)); d > Tolerance || d < -Tolerance {
		return "", false
	}
	want := []byte(mac(secret, ts, body))
	for _, s := range sigs {
		if hmac.Equal([]byte(strings.ToLower(s)), want) {
			return string(want), true
		}
	}
	return "", false
}

// Fire IDs: the run's unique key and the message's pi-durable requestId.

// scheduleFireID is the same on every replica for one scheduled time.
func scheduleFireID(trigger uuid.UUID, at time.Time) string {
	return "sc_" + strings.ReplaceAll(trigger.String(), "-", "") + "_" + strconv.FormatInt(at.Unix(), 10)
}

// webhookFireID is derived from the delivery's signature: a replay has the
// same one.
func webhookFireID(trigger uuid.UUID, signature string) string {
	sum := sha256.Sum256([]byte(trigger.String() + ":" + signature))
	return "wh_" + base64.RawURLEncoding.EncodeToString(sum[:])[:32]
}

// manualFireID is a new fire every time.
func manualFireID() string { return "mn_" + randomToken(16) }
