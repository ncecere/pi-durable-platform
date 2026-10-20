package oauth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// App secrets: platform admins issue a secret to a trusted app (a
// metadata-document client on the trusted list) so it can call the
// app-level endpoints (GET /v1/app/events and /v1/app/usage) as itself,
// without holding any person's token. Why a secret and not private_key_jwt
// (DESIGN.md "OAuth Connect"): app-level access shows the app what every
// person who connected it does, so it is a platform admin's decision per
// app anyway (the trusted list), and a secret issued there fits the
// platform's other credentials (shown once, stored as a peppered digest,
// two at a time for rotation), with no key fetching (jwks_uri), JWT
// parsing or assertion replay cache across replicas.
//
// A secret works only while its client is on the trusted list (read on
// every use, so removing an app from the list stops it at once on every
// replica), and removing an app from the list deletes its secrets
// (UpdateTrustedClients): trusting it again needs new ones. The
// authorization code flow stays public: the secret is not used, or
// accepted, at the token endpoint.

// SecretPrefix starts every app secret.
const SecretPrefix = "pdpcs_"

// MaxClientSecrets is how many secrets an app may have at once (the
// current one and the one it replaces).
const MaxClientSecrets = 2

// secretPrefixLen is how much of a secret is kept to tell it apart.
const secretPrefixLen = len(SecretPrefix) + 4

// ClientSecret is an issued secret's record (never the secret).
type ClientSecret struct {
	ID         uuid.UUID
	ClientID   string
	Prefix     string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	// Trusted: its client is on the trusted list now (else it doesn't work).
	Trusted bool
}

// IssuedSecret is a new secret, shown once.
type IssuedSecret struct {
	ClientSecret
	Secret string
}

// AppClient is an app authenticated by its secret.
type AppClient struct {
	ClientID string
	SecretID uuid.UUID
}

// ErrInvalidClient: the secret is unknown, deleted, or its app isn't
// trusted (any more).
var ErrInvalidClient = errors.New("oauth: invalid app secret")

var errNoSecret = apperr.NotFound("secret_not_found", "App secret not found")

func secretView(row dbgen.OauthClientSecret, trusted map[string]bool) ClientSecret {
	return ClientSecret{ID: row.ID, ClientID: row.ClientID, Prefix: row.Prefix, CreatedAt: row.CreatedAt,
		LastUsedAt: row.LastUsedAt, Trusted: trusted[row.ClientID]}
}

// ListClientSecrets lists every app secret (platform admins and auditors).
func (s *Service) ListClientSecrets(ctx context.Context, a authz.Actor) ([]ClientSecret, error) {
	if !a.CanReadPlatform() {
		return nil, errPlatformRead
	}
	rows, err := s.q.ListOAuthClientSecrets(ctx)
	if err != nil {
		return nil, err
	}
	trusted, err := s.trustedIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ClientSecret, len(rows))
	for i, r := range rows {
		out[i] = secretView(r, trusted)
	}
	return out, nil
}

// CreateClientSecret issues a secret to a trusted app (platform admins): at
// most MaxClientSecrets per app (409 too_many_secrets: delete one first).
// Audited as oauth.client_secret_create (the client and the secret's
// prefix, never the secret).
func (s *Service) CreateClientSecret(ctx context.Context, a authz.Actor, clientID string) (IssuedSecret, error) {
	if !a.IsPlatformAdmin() {
		return IssuedSecret{}, errAdminOnly
	}
	secret := newToken(SecretPrefix)
	var out IssuedSecret
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		// The settings row serialises issuing (and holds the trusted list).
		cur, err := q.LockOAuthSettings(ctx)
		if err != nil {
			return err
		}
		trusted := false
		for _, c := range decodeTrusted(cur.TrustedClients) {
			trusted = trusted || c.ClientID == clientID
		}
		if !trusted {
			return apperr.New(http.StatusUnprocessableEntity, "client_not_trusted", "Only a trusted app (on the trusted list) can have a secret")
		}
		n, err := q.CountOAuthClientSecrets(ctx, clientID)
		if err != nil {
			return err
		}
		if n >= MaxClientSecrets {
			return apperr.Conflict("too_many_secrets", "This app has two secrets already: delete one first")
		}
		row, err := q.InsertOAuthClientSecret(ctx, dbgen.InsertOAuthClientSecretParams{ClientID: clientID,
			SecretHash: s.digest(secret), PepperID: s.pepperID(), Prefix: secret[:secretPrefixLen],
			CreatedBy: uuid.NullUUID{UUID: a.UserID, Valid: a.UserID != uuid.Nil}})
		if err != nil {
			return err
		}
		out = IssuedSecret{ClientSecret: secretView(row, map[string]bool{clientID: true}), Secret: secret}
		e := a.Audit("oauth.client_secret_create", "oauth_client_secret", row.ID.String())
		e.After = map[string]any{"clientId": clientID, "prefix": row.Prefix}
		e.Metadata = map[string]any{"clientId": clientID, "prefix": row.Prefix}
		return audit.Record(ctx, q, e)
	})
	return out, err
}

// DeleteClientSecret deletes an app secret (platform admins); it stops
// working at once. Audited as oauth.client_secret_delete.
func (s *Service) DeleteClientSecret(ctx context.Context, a authz.Actor, id uuid.UUID) error {
	if !a.IsPlatformAdmin() {
		if a.CanReadPlatform() {
			return errAdminOnly
		}
		return errNoSecret
	}
	return store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		row, err := q.DeleteOAuthClientSecret(ctx, id)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errNoSecret
		} else if err != nil {
			return err
		}
		e := a.Audit("oauth.client_secret_delete", "oauth_client_secret", row.ID.String())
		e.Before = map[string]any{"clientId": row.ClientID, "prefix": row.Prefix}
		e.Metadata = map[string]any{"clientId": row.ClientID, "prefix": row.Prefix}
		return audit.Record(ctx, q, e)
	})
}

// IsClientSecret reports whether a bearer value looks like an app secret.
func IsClientSecret(v string) bool { return validTokenShape(v, SecretPrefix) }

// AuthenticateClient checks an app secret: its digest (current or previous
// pepper), and that its client is on the trusted list now. Its use is
// recorded at most once a minute.
func (s *Service) AuthenticateClient(ctx context.Context, secret string) (AppClient, error) {
	if !IsClientSecret(secret) {
		return AppClient{}, ErrInvalidClient
	}
	for _, d := range s.digests(secret) {
		row, err := s.q.GetOAuthClientSecret(ctx, d)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			continue
		} else if err != nil {
			return AppClient{}, err
		}
		trusted, err := s.trustedIDs(ctx)
		if err != nil {
			return AppClient{}, err
		}
		if !trusted[row.ClientID] {
			return AppClient{}, ErrInvalidClient
		}
		if err := s.q.TouchOAuthClientSecret(ctx, row.ID); err != nil {
			s.Log.WarnContext(ctx, "could not record an app secret's use", "err", err)
		}
		return AppClient{ClientID: row.ClientID, SecretID: row.ID}, nil
	}
	return AppClient{}, ErrInvalidClient
}

// ClientStillValid re-checks an authenticated app (an open stream): its
// secret still exists and its client is still trusted. A database failure
// keeps it (the next check asks again).
func (s *Service) ClientStillValid(ctx context.Context, c AppClient) bool {
	rows, err := s.q.ListOAuthClientSecrets(ctx)
	if err != nil {
		return true
	}
	found := false
	for _, r := range rows {
		found = found || (r.ID == c.SecretID && r.ClientID == c.ClientID)
	}
	if !found {
		return false
	}
	trusted, err := s.trustedIDs(ctx)
	return err != nil || trusted[c.ClientID]
}
