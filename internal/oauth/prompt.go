package oauth

import (
	"strings"
	"unicode"

	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// prompt and login_hint (OpenID Connect Core §3.1.2.1, as an OAuth
// authorization server that issues no ID tokens):
//
//   - prompt=none: the endpoint never shows a page. Not signed in:
//     login_required; signed in but the platform would show a page that
//     isn't sign-in or consent (the account can't use the platform):
//     interaction_required; no grant of this person for this client at all
//     (and the client isn't trusted): consent_required, before login_hint is
//     compared (else the answer would tell any app whether the browser is
//     signed in as a given email); signed in as someone other than
//     login_hint: login_required (the hinted person must sign in); a grant
//     that doesn't cover the scope: consent_required.
//     Each goes to the redirect URI with the state and iss. With a grant (or
//     a trusted client) the code comes back at once, as without prompt.
//   - prompt=consent: show the consent page even when the person allowed the
//     app before, or it is trusted.
//   - login_hint: passed to the identity provider when the person has to
//     sign in. A signed-in person who isn't the hinted one is never connected
//     silently: with prompt=none the app gets login_required; otherwise the
//     consent page says whom the app expected and offers to sign out and
//     sign in as them (or to cancel: access_denied). Allowing is refused
//     while they differ. The hint matches the person's email
//     (case-insensitively) or their identity provider subject.

// Prompt values.
const (
	PromptNone    = "none"
	PromptConsent = "consent"
)

// maxLoginHintLen bounds login_hint (an email address, or a subject).
const maxLoginHintLen = 320

// checkPrompt validates prompt: none or consent (OpenID Connect's login and
// select_account aren't supported); none can't be combined.
func checkPrompt(raw string) (none, consent bool, err *Error) {
	values := strings.Fields(raw)
	for _, v := range values {
		switch v {
		case PromptNone:
			none = true
		case PromptConsent:
			consent = true
		default:
			return false, false, oauthError("invalid_request", "prompt may be none or consent.")
		}
	}
	if none && len(values) > 1 {
		return false, false, oauthError("invalid_request", "prompt=none can't be combined with another value.")
	}
	return none, consent, nil
}

func validLoginHint(h string) bool {
	return len(h) <= maxLoginHintLen && strings.IndexFunc(h, func(r rune) bool { return unicode.IsControl(r) }) < 0
}

// HintMatches reports whether a login_hint names the person: their email
// (case-insensitively) or their identity provider subject. An empty hint
// matches anyone.
func HintMatches(u dbgen.User, hint string) bool {
	hint = strings.TrimSpace(hint)
	return hint == "" || strings.EqualFold(hint, u.Email) || (u.OIDCSubject != "" && hint == u.OIDCSubject)
}

// PromptError is the error prompt=none sends to the app: login_required,
// consent_required or interaction_required.
func PromptError(code string) *Error {
	switch code {
	case "login_required":
		return oauthError(code, "The person isn't signed in to the platform as the account the app expects; ask again without prompt=none.")
	case "consent_required":
		return oauthError(code, "The person hasn't allowed this app (for this scope); ask again without prompt=none.")
	}
	return oauthError("interaction_required", "The platform needs to show the person a page; ask again without prompt=none.")
}
