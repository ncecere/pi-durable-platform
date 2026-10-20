package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/metrics"
	"github.com/ncecere/pi-durable-platform/internal/secrets"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/teamnet"
	"github.com/ncecere/pi-durable-platform/internal/teams"
)

var (
	errProviderNotFound = apperr.NotFound("model_provider_not_found", "Model provider not found")
	errModelNotFound    = apperr.NotFound("model_not_found", "Model not found")
	errTeamNotFound     = apperr.NotFound("team_not_found", "Team not found")
	errProviderName     = apperr.Conflict("provider_name_taken", "A provider with that name already exists")
	errModelTaken       = apperr.Conflict("model_taken", "This provider already has that model")
	errModelInUse       = apperr.Conflict("model_in_use", "Templates or agents use this model: disable it instead, or move them to another model first")
	errBadName          = apperr.Invalid("invalid_name", "Name must be 1-100 characters on one line")
	errBadKind          = apperr.Invalid("invalid_kind", "Unknown provider type")
	errBadURL           = apperr.Invalid("invalid_base_url", "The base URL must be an http or https URL without credentials, query or fragment")
	errBadKey           = apperr.Invalid("invalid_api_key", "The API key must be 1-4096 characters on one line")
	errBadMode          = apperr.Invalid("invalid_access_mode", "Access mode is proxy or direct")
	errBadHeaders       = apperr.Invalid("invalid_headers", "Headers: at most 20, valid names (not authentication, host or hop-by-hop headers), values up to 1024 characters on one line")
	errBadUpstream      = apperr.Invalid("invalid_upstream_id", "The model ID is 1-200 characters without spaces")
	errBadLimits        = apperr.Invalid("invalid_limits", "Context window must be 1024-100000000 tokens and max output 1-10000000")
	errBadPrice         = apperr.Invalid("invalid_price", "Prices are zero or more (USD per million tokens)")
	errBadVisibility    = apperr.Invalid("invalid_visibility", "Visibility is all or teams")
	// ErrUnavailable means a model is disabled, its provider is, or it is
	// not published to the team.
	ErrUnavailable = apperr.Conflict("model_unavailable", "That model is not available to this team (disabled, or not published to it)")

	headerNameRE = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_|~-]{1,100}$`)
	// Headers the platform sets itself, or that would change how the request travels.
	reservedHeaders = map[string]bool{"authorization": true, "x-api-key": true, "host": true, "content-length": true,
		"content-type": true, "cookie": true, "connection": true, "transfer-encoding": true, "te": true, "upgrade": true,
		"proxy-authorization": true, "keep-alive": true, "trailer": true, "accept-encoding": true}
)

// Service manages providers and models.
type Service struct {
	pool   *pgxpool.Pool
	q      *dbgen.Queries
	box    *secrets.Box
	client *http.Client // discovery and the proxy, for platform providers
	// team reaches team providers, through the address guard.
	team *teamnet.Guard
	// settingsChanged run after the model settings change.
	settingsChanged []func()
	// Metrics counts the model proxy's requests and tokens (nil: none).
	Metrics *metrics.Metrics
}

// New returns a Service. Team providers are reached through the address
// guard without the development allowance (see SetTeamNetwork).
func New(pool *pgxpool.Pool, box *secrets.Box) *Service {
	q := dbgen.New(pool)
	return &Service{pool: pool, q: q, box: box, client: newUpstreamClient(), team: teamnet.New(teamnet.SettingsNetworks(q), 10*time.Minute)}
}

func providerAD(id uuid.UUID) []byte { return []byte("model_provider:" + id.String()) }

// Owner is the team that owns a team provider.
type Owner = teams.Owner

func ownerOf(id uuid.NullUUID, slug, name, kind *string) *Owner {
	return teams.OwnerOf(id, slug, name, kind)
}

func teamOwner(t dbgen.Team) *Owner { return teams.OwnerFromTeam(t) }

// ProviderView is a provider without its secrets.
type ProviderView struct {
	Provider   dbgen.ModelProvider
	HasHeaders bool
	ModelCount int64
	// Team owns a team provider (nil: a platform provider).
	Team *Owner
}

// ModelView is a model with its provider's name and kind, and (for admins)
// the teams it is published to.
type ModelView struct {
	Model              dbgen.Model
	ProviderName       string
	ProviderKind       string
	ProviderEnabled    bool
	ProviderAccessMode string
	// ProviderTeam owns the model's provider (nil: a platform provider).
	ProviderTeam *Owner
	Teams        []dbgen.ListModelTeamsRow
}

func requireStaff(a authz.Actor) error {
	if !a.CanReadPlatform() {
		return apperr.Forbidden("Only platform admins and auditors can see model providers")
	}
	return nil
}

func requireAdmin(a authz.Actor) error {
	if !a.IsPlatformAdmin() {
		return apperr.Forbidden("Only platform admins can manage models")
	}
	return nil
}

// scope is where a call comes from: the admin routes (team nil: every
// provider, platform admins and auditors) or a team's routes (team: that
// team's own providers only, checked by teamAccess).
type scope struct {
	team *dbgen.Team
}

var adminScope = scope{}

// reaches reports errProviderNotFound unless the scope reaches provider p: a
// team's routes reach only the team's own providers.
func (sc scope) reaches(p dbgen.ModelProvider) error {
	if sc.team != nil && (!p.TeamID.Valid || p.TeamID.UUID != sc.team.ID) {
		return errProviderNotFound
	}
	return nil
}

// ownerFilter turns ?owner= (all, platform, or a team's slug or ID) into
// list filters.
func (s *Service) ownerFilter(ctx context.Context, owner string) (platformOnly bool, team uuid.NullUUID, err error) {
	return teams.OwnerFilter(ctx, s.q, owner)
}

// Providers lists providers (platform staff): every one (owner "" or all),
// the platform's (platform), or one team's (its slug or ID).
func (s *Service) Providers(ctx context.Context, a authz.Actor, owner string) ([]ProviderView, error) {
	if err := requireStaff(a); err != nil {
		return nil, err
	}
	platformOnly, team, err := s.ownerFilter(ctx, owner)
	if err != nil {
		return nil, err
	}
	return s.listProviders(ctx, platformOnly, team)
}

func (s *Service) listProviders(ctx context.Context, platformOnly bool, team uuid.NullUUID) ([]ProviderView, error) {
	rows, err := s.q.ListModelProviders(ctx, dbgen.ListModelProvidersParams{PlatformOnly: platformOnly, TeamID: team})
	if err != nil {
		return nil, err
	}
	out := make([]ProviderView, len(rows))
	for i, r := range rows {
		out[i] = ProviderView{Provider: dbgen.ModelProvider{ID: r.ID, Name: r.Name, Kind: r.Kind, BaseURL: r.BaseURL,
			AccessMode: r.AccessMode, Enabled: r.Enabled, Revision: r.Revision, CreatedBy: r.CreatedBy,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, TeamID: r.TeamID}, HasHeaders: len(r.HeadersEnc) > 0, ModelCount: r.ModelCount,
			Team: ownerOf(r.TeamID, r.TeamSlug, r.TeamName, r.TeamKind)}
	}
	return out, nil
}

func (s *Service) providerView(ctx context.Context, q *dbgen.Queries, p dbgen.ModelProvider) (ProviderView, error) {
	rows, err := q.ListModels(ctx, dbgen.ListModelsParams{ProviderID: uuid.NullUUID{UUID: p.ID, Valid: true}})
	if err != nil {
		return ProviderView{}, err
	}
	v := ProviderView{HasHeaders: len(p.HeadersEnc) > 0, ModelCount: int64(len(rows))}
	if p.TeamID.Valid {
		t, err := q.GetTeamByID(ctx, p.TeamID.UUID)
		if err != nil {
			return ProviderView{}, err
		}
		v.Team = teamOwner(t)
	}
	p.APIKeyEnc, p.HeadersEnc = nil, nil
	v.Provider = p
	return v, nil
}

// getProvider returns a provider the scope reaches.
func getProvider(ctx context.Context, q *dbgen.Queries, sc scope, id uuid.UUID, lock bool) (dbgen.ModelProvider, error) {
	get := q.GetModelProvider
	if lock {
		get = q.LockModelProvider
	}
	p, err := get(ctx, id)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return p, errProviderNotFound
	} else if err != nil {
		return p, err
	}
	return p, sc.reaches(p)
}

// Provider returns one provider (platform staff).
func (s *Service) Provider(ctx context.Context, a authz.Actor, id uuid.UUID) (ProviderView, error) {
	if err := requireStaff(a); err != nil {
		return ProviderView{}, err
	}
	return s.provider(ctx, adminScope, id)
}

func (s *Service) provider(ctx context.Context, sc scope, id uuid.UUID) (ProviderView, error) {
	p, err := getProvider(ctx, s.q, sc, id, false)
	if err != nil {
		return ProviderView{}, err
	}
	return s.providerView(ctx, s.q, p)
}

// ProviderInput is a new provider.
type ProviderInput struct {
	Name, Kind, BaseURL, APIKey, AccessMode string
	Headers                                 map[string]string
	Enabled                                 *bool
}

func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 100 || strings.ContainsAny(name, "\r\n\x00") {
		return "", errBadName
	}
	return name, nil
}

// checkBaseURL normalises a provider URL (no trailing slash).
func checkBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || len(raw) > 2048 {
		return "", errBadURL
	}
	return strings.TrimRight(raw, "/"), nil
}

func checkKey(key string) error {
	if n := len(key); n < 1 || n > 4096 || strings.ContainsAny(key, "\r\n\x00") || strings.TrimSpace(key) != key {
		return errBadKey
	}
	return nil
}

func checkHeaders(h map[string]string) error {
	if len(h) > 20 {
		return errBadHeaders
	}
	for k, v := range h {
		if !headerNameRE.MatchString(k) || reservedHeaders[strings.ToLower(k)] || len(v) > 1024 || strings.ContainsAny(v, "\r\n\x00") {
			return errBadHeaders
		}
	}
	return nil
}

func (s *Service) sealHeaders(id uuid.UUID, h map[string]string) ([]byte, error) {
	if len(h) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	return s.box.Seal(b, providerAD(id))
}

func providerSnapshot(p dbgen.ModelProvider) map[string]any {
	return map[string]any{"name": p.Name, "kind": p.Kind, "baseUrl": p.BaseURL, "accessMode": p.AccessMode, "enabled": p.Enabled,
		"hasHeaders": len(p.HeadersEnc) > 0}
}

// providerAudit is an audit entry about a provider, in its team's log when
// a team owns it.
func providerAudit(a authz.Actor, action string, p dbgen.ModelProvider) audit.Entry {
	e := a.Audit(action, "model_provider", p.ID.String())
	if p.TeamID.Valid {
		e.TeamID = p.TeamID.UUID
	}
	return e
}

// CreateProvider adds a platform provider (platform admins).
func (s *Service) CreateProvider(ctx context.Context, a authz.Actor, in ProviderInput) (ProviderView, error) {
	if err := requireAdmin(a); err != nil {
		return ProviderView{}, err
	}
	return s.createProvider(ctx, a, adminScope, in)
}

// createProvider adds a provider: the platform's (admin scope) or the
// scope's team's (proxy only, its URL through the address guard).
func (s *Service) createProvider(ctx context.Context, a authz.Actor, sc scope, in ProviderInput) (ProviderView, error) {
	kind, ok := KindByID(in.Kind)
	if !ok {
		return ProviderView{}, errBadKind
	}
	name, err := checkName(in.Name)
	if err != nil {
		return ProviderView{}, err
	}
	if strings.TrimSpace(in.BaseURL) == "" {
		in.BaseURL = kind.DefaultBaseURL
	}
	base, err := checkBaseURL(in.BaseURL)
	if err != nil {
		return ProviderView{}, err
	}
	if err := checkKey(in.APIKey); err != nil {
		return ProviderView{}, err
	}
	if in.AccessMode == "" {
		in.AccessMode = ModeProxy
	}
	if in.AccessMode != ModeProxy && in.AccessMode != ModeDirect {
		return ProviderView{}, errBadMode
	}
	if err := checkHeaders(in.Headers); err != nil {
		return ProviderView{}, err
	}
	var teamID uuid.NullUUID
	if sc.team != nil {
		teamID = uuid.NullUUID{UUID: sc.team.ID, Valid: true}
		if in.AccessMode != ModeProxy {
			return ProviderView{}, errTeamProxyOnly
		}
		if err := s.checkTeamAddress(ctx, base); err != nil {
			return ProviderView{}, err
		}
	}
	id := uuid.New()
	keyEnc, err := s.box.Seal([]byte(in.APIKey), providerAD(id))
	if err != nil {
		return ProviderView{}, err
	}
	headersEnc, err := s.sealHeaders(id, in.Headers)
	if err != nil {
		return ProviderView{}, err
	}
	enabled := in.Enabled == nil || *in.Enabled
	var out ProviderView
	err = store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		if sc.team != nil {
			// The rule again, in the transaction (settings may have changed).
			if _, err := s.teamAccess(ctx, q, a, sc.team.ID.String(), authz.CheckTeamProviderCreate, true); err != nil {
				return err
			}
		}
		p, err := q.InsertModelProvider(ctx, dbgen.InsertModelProviderParams{ID: id, Name: name, Kind: kind.ID, BaseURL: base,
			APIKeyEnc: keyEnc, HeadersEnc: headersEnc, AccessMode: in.AccessMode, Enabled: enabled,
			CreatedBy: uuid.NullUUID{UUID: a.UserID, Valid: a.UserID != uuid.Nil}, TeamID: teamID})
		if apperr.IsUniqueViolation(err, "model_providers_name_key") {
			return errProviderName
		} else if err != nil {
			return err
		}
		e := providerAudit(a, "model_provider.create", p)
		e.After = providerSnapshot(p)
		if err := audit.Record(ctx, q, e); err != nil {
			return err
		}
		out, err = s.providerView(ctx, q, p)
		return err
	})
	return out, err
}

// ProviderUpdate holds optional changes. APIKey replaces the key; Headers
// replaces every extra header (an empty map removes them).
type ProviderUpdate struct {
	Name, BaseURL, APIKey, AccessMode *string
	Headers                           *map[string]string
	Enabled                           *bool
}

// UpdateProvider changes any provider (platform admins, If-Match). Running
// agents of a direct-mode provider keep the old URL and key until they next
// start; proxied agents use the new ones at once. A team provider stays
// proxy-only, and a new URL goes through the address guard.
func (s *Service) UpdateProvider(ctx context.Context, a authz.Actor, id uuid.UUID, in ProviderUpdate, expectedRevision int64) (ProviderView, error) {
	if err := requireAdmin(a); err != nil {
		return ProviderView{}, err
	}
	return s.updateProvider(ctx, a, adminScope, id, in, expectedRevision)
}

func (s *Service) updateProvider(ctx context.Context, a authz.Actor, sc scope, id uuid.UUID, in ProviderUpdate, expectedRevision int64) (ProviderView, error) {
	// A team provider's new URL is checked before the transaction (the
	// check resolves its host); a provider's owner never changes.
	cur, err := getProvider(ctx, s.q, sc, id, false)
	if err != nil {
		return ProviderView{}, err
	}
	var base string
	if in.BaseURL != nil {
		if base, err = checkBaseURL(*in.BaseURL); err != nil {
			return ProviderView{}, err
		}
		if cur.TeamID.Valid {
			if err := s.checkTeamAddress(ctx, base); err != nil {
				return ProviderView{}, err
			}
		}
	}
	var out ProviderView
	err = store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		p, err := getProvider(ctx, q, sc, id, true)
		if err != nil {
			return err
		}
		if p.Revision != expectedRevision {
			return apperr.Stale()
		}
		u := dbgen.UpdateModelProviderParams{ID: id, Name: p.Name, BaseURL: p.BaseURL, APIKeyEnc: p.APIKeyEnc,
			HeadersEnc: p.HeadersEnc, AccessMode: p.AccessMode, Enabled: p.Enabled}
		keyChanged := false
		if in.Name != nil {
			if u.Name, err = checkName(*in.Name); err != nil {
				return err
			}
		}
		if in.BaseURL != nil {
			u.BaseURL = base
		}
		if in.APIKey != nil {
			if err := checkKey(*in.APIKey); err != nil {
				return err
			}
			if u.APIKeyEnc, err = s.box.Seal([]byte(*in.APIKey), providerAD(id)); err != nil {
				return err
			}
			keyChanged = true
		}
		if in.AccessMode != nil {
			if *in.AccessMode != ModeProxy && *in.AccessMode != ModeDirect {
				return errBadMode
			}
			if p.TeamID.Valid && *in.AccessMode != ModeProxy {
				return errTeamProxyOnly
			}
			u.AccessMode = *in.AccessMode
		}
		if in.Headers != nil {
			if err := checkHeaders(*in.Headers); err != nil {
				return err
			}
			if u.HeadersEnc, err = s.sealHeaders(id, *in.Headers); err != nil {
				return err
			}
		}
		if in.Enabled != nil {
			u.Enabled = *in.Enabled
		}
		updated, err := q.UpdateModelProvider(ctx, u)
		if apperr.IsUniqueViolation(err, "model_providers_name_key") {
			return errProviderName
		} else if err != nil {
			return err
		}
		e := providerAudit(a, "model_provider.update", p)
		e.Before, e.After = providerSnapshot(p), providerSnapshot(updated)
		if keyChanged {
			e.Metadata = mergeMeta(e.Metadata, map[string]any{"apiKeyReplaced": true})
		}
		if err := audit.Record(ctx, q, e); err != nil {
			return err
		}
		out, err = s.providerView(ctx, q, updated)
		return err
	})
	return out, err
}

// mergeMeta adds extra to an audit entry's metadata (the actor's, if any).
func mergeMeta(m, extra map[string]any) map[string]any {
	if m == nil {
		return extra
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// DeleteProvider removes any provider and its models (platform admins).
// Refused while templates or agents use one of its models.
func (s *Service) DeleteProvider(ctx context.Context, a authz.Actor, id uuid.UUID) error {
	if err := requireAdmin(a); err != nil {
		return err
	}
	return s.deleteProvider(ctx, a, adminScope, id)
}

func (s *Service) deleteProvider(ctx context.Context, a authz.Actor, sc scope, id uuid.UUID) error {
	return store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		p, err := getProvider(ctx, q, sc, id, true)
		if err != nil {
			return err
		}
		if err := q.DeleteModelProvider(ctx, id); apperr.IsForeignKeyViolation(err, "") {
			return errModelInUse
		} else if err != nil {
			return err
		}
		e := providerAudit(a, "model_provider.delete", p)
		e.Before = providerSnapshot(p)
		return audit.Record(ctx, q, e)
	})
}

// Models lists models (platform staff), with the teams each is published
// to: one provider's (providerID), or every provider's of an owner (owner:
// "" or all, platform, or a team's slug or ID).
func (s *Service) Models(ctx context.Context, a authz.Actor, providerID *uuid.UUID, owner string) ([]ModelView, error) {
	if err := requireStaff(a); err != nil {
		return nil, err
	}
	platformOnly, team, err := s.ownerFilter(ctx, owner)
	if err != nil {
		return nil, err
	}
	return s.models(ctx, adminScope, providerID, platformOnly, team)
}

func (s *Service) models(ctx context.Context, sc scope, providerID *uuid.UUID, platformOnly bool, team uuid.NullUUID) ([]ModelView, error) {
	filter := uuid.NullUUID{}
	if providerID != nil {
		if _, err := getProvider(ctx, s.q, sc, *providerID, false); err != nil {
			return nil, err
		}
		filter = uuid.NullUUID{UUID: *providerID, Valid: true}
	}
	rows, err := s.q.ListModels(ctx, dbgen.ListModelsParams{ProviderID: filter, PlatformOnly: platformOnly, TeamID: team})
	if err != nil {
		return nil, err
	}
	out := make([]ModelView, len(rows))
	for i, r := range rows {
		out[i] = ModelView{Model: r.Model, ProviderName: r.ProviderName, ProviderKind: r.ProviderKind,
			ProviderEnabled: r.ProviderEnabled, ProviderAccessMode: r.ProviderAccessMode,
			ProviderTeam: ownerOf(r.ProviderTeamID, r.TeamSlug, r.TeamName, r.TeamKind)}
		if r.Model.Visibility == "teams" && !r.ProviderTeamID.Valid {
			if out[i].Teams, err = s.q.ListModelTeams(ctx, r.Model.ID); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func (s *Service) modelView(ctx context.Context, q *dbgen.Queries, m dbgen.Model) (ModelView, error) {
	p, err := q.GetModelProvider(ctx, m.ProviderID)
	if err != nil {
		return ModelView{}, err
	}
	v := ModelView{Model: m, ProviderName: p.Name, ProviderKind: p.Kind, ProviderEnabled: p.Enabled, ProviderAccessMode: p.AccessMode}
	if p.TeamID.Valid {
		t, err := q.GetTeamByID(ctx, p.TeamID.UUID)
		if err != nil {
			return ModelView{}, err
		}
		v.ProviderTeam = teamOwner(t)
		return v, nil
	}
	if m.Visibility == "teams" {
		if v.Teams, err = q.ListModelTeams(ctx, m.ID); err != nil {
			return ModelView{}, err
		}
	}
	return v, nil
}

// ModelInput is a new model of a provider.
type ModelInput struct {
	UpstreamID, Name         string
	ContextWindow, MaxTokens *int32
	Images, Reasoning        bool
	InputPrice, OutputPrice  *float64
	Enabled                  *bool
	// Visibility, Teams and Personal publish a platform provider's model;
	// a team provider's models are its team's only (see checkTeamModelScope).
	Visibility string
	Teams      []string // slugs or IDs, for visibility teams
	// Personal: available in personal workspaces (default: when visibility is all).
	Personal *bool
}

func price(p *float64) (pgtype.Float8, error) {
	if p == nil {
		return pgtype.Float8{}, nil
	}
	if *p < 0 || *p > 1e6 {
		return pgtype.Float8{}, errBadPrice
	}
	return pgtype.Float8{Float64: *p, Valid: true}, nil
}

func checkLimits(ctxw, max int32) error {
	if ctxw < 1024 || ctxw > 100000000 || max < 1 || max > 10000000 {
		return errBadLimits
	}
	return nil
}

func modelSnapshot(m dbgen.Model, teamSlugs []string) map[string]any {
	out := map[string]any{"upstreamId": m.UpstreamID, "name": m.Name, "contextWindow": m.ContextWindow, "maxTokens": m.MaxTokens,
		"images": m.Images, "reasoning": m.Reasoning, "enabled": m.Enabled, "visibility": m.Visibility, "personal": m.Personal}
	if m.InputPrice.Valid {
		out["inputPrice"] = m.InputPrice.Float64
	}
	if m.OutputPrice.Valid {
		out["outputPrice"] = m.OutputPrice.Float64
	}
	if m.Visibility == "teams" {
		out["teams"] = teamSlugs
	}
	return out
}

// modelAudit is an audit entry about a model, in its provider's team's log
// when a team owns the provider.
func modelAudit(a authz.Actor, action string, id uuid.UUID, p dbgen.ModelProvider) audit.Entry {
	e := a.Audit(action, "model", id.String())
	if p.TeamID.Valid {
		e.TeamID = p.TeamID.UUID
	}
	return e
}

// setTeams replaces a model's teams; returns their slugs.
func setTeams(ctx context.Context, q *dbgen.Queries, modelID uuid.UUID, refs []string) ([]string, error) {
	if err := q.ClearModelTeams(ctx, modelID); err != nil {
		return nil, err
	}
	slugs := []string{}
	for _, ref := range refs {
		t, err := teams.Resolve(ctx, q, ref)
		if err != nil {
			return nil, err
		}
		if err := q.AddModelTeam(ctx, dbgen.AddModelTeamParams{ModelID: modelID, TeamID: t.ID}); err != nil {
			return nil, err
		}
		slugs = append(slugs, t.Slug)
	}
	return slugs, nil
}

// CreateModel adds a model to any provider (platform admins).
func (s *Service) CreateModel(ctx context.Context, a authz.Actor, providerID uuid.UUID, in ModelInput) (ModelView, error) {
	if err := requireAdmin(a); err != nil {
		return ModelView{}, err
	}
	return s.createModel(ctx, a, adminScope, providerID, in)
}

func (s *Service) createModel(ctx context.Context, a authz.Actor, sc scope, providerID uuid.UUID, in ModelInput) (ModelView, error) {
	in.UpstreamID = strings.TrimSpace(in.UpstreamID)
	if n := len(in.UpstreamID); n < 1 || n > 200 || strings.ContainsAny(in.UpstreamID, " \t\r\n\x00") {
		return ModelView{}, errBadUpstream
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = in.UpstreamID
		if utf8.RuneCountInString(in.Name) > 100 {
			in.Name = string([]rune(in.Name)[:100])
		}
	}
	name, err := checkName(in.Name)
	if err != nil {
		return ModelView{}, err
	}
	p := dbgen.InsertModelParams{ProviderID: providerID, UpstreamID: in.UpstreamID, Name: name, ContextWindow: 128000, MaxTokens: 16000,
		Images: in.Images, Reasoning: in.Reasoning, Enabled: in.Enabled == nil || *in.Enabled, Visibility: in.Visibility}
	if in.ContextWindow != nil {
		p.ContextWindow = *in.ContextWindow
	}
	if in.MaxTokens != nil {
		p.MaxTokens = *in.MaxTokens
	}
	if err := checkLimits(p.ContextWindow, p.MaxTokens); err != nil {
		return ModelView{}, err
	}
	if p.InputPrice, err = price(in.InputPrice); err != nil {
		return ModelView{}, err
	}
	if p.OutputPrice, err = price(in.OutputPrice); err != nil {
		return ModelView{}, err
	}
	if p.Visibility == "" {
		p.Visibility = "all"
	}
	if p.Visibility != "all" && p.Visibility != "teams" {
		return ModelView{}, errBadVisibility
	}
	p.Personal = p.Visibility == "all"
	if in.Personal != nil {
		p.Personal = *in.Personal
	}
	var out ModelView
	err = store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		prov, err := getProvider(ctx, q, sc, providerID, true)
		if err != nil {
			return err
		}
		if prov.TeamID.Valid {
			var teams *[]string
			if in.Teams != nil {
				teams = &in.Teams
			}
			if err := checkTeamModelScope(&in.Visibility, teams, in.Personal); err != nil {
				return err
			}
			p.Visibility, p.Personal = "teams", false
		}
		m, err := q.InsertModel(ctx, p)
		if apperr.IsUniqueViolation(err, "models_provider_upstream_key") {
			return errModelTaken
		} else if err != nil {
			return err
		}
		slugs := []string{}
		if m.Visibility == "teams" && !prov.TeamID.Valid {
			if slugs, err = setTeams(ctx, q, m.ID, in.Teams); err != nil {
				return err
			}
		}
		e := modelAudit(a, "model.create", m.ID, prov)
		e.After = modelSnapshot(m, slugs)
		e.Metadata = mergeMeta(e.Metadata, map[string]any{"providerId": providerID.String()})
		if err := audit.Record(ctx, q, e); err != nil {
			return err
		}
		out, err = s.modelView(ctx, q, m)
		return err
	})
	return out, err
}

// ModelUpdate holds optional changes. Teams replaces the teams (with
// visibility teams).
type ModelUpdate struct {
	Name                     *string
	ContextWindow, MaxTokens *int32
	Images, Reasoning        *bool
	// InputPrice and OutputPrice: a negative value clears the price.
	InputPrice, OutputPrice *float64
	Enabled                 *bool
	Visibility              *string
	Teams                   *[]string
	Personal                *bool
}

// UpdateModel changes any model (platform admins, If-Match). Disabling it
// stops agents using it from starting (running ones are not stopped).
func (s *Service) UpdateModel(ctx context.Context, a authz.Actor, id uuid.UUID, in ModelUpdate, expectedRevision int64) (ModelView, error) {
	if err := requireAdmin(a); err != nil {
		return ModelView{}, err
	}
	return s.updateModel(ctx, a, adminScope, id, in, expectedRevision)
}

// lockModel locks a model the scope reaches, and returns its provider.
func lockModel(ctx context.Context, q *dbgen.Queries, sc scope, id uuid.UUID) (dbgen.Model, dbgen.ModelProvider, error) {
	m, err := q.LockModel(ctx, id)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return m, dbgen.ModelProvider{}, errModelNotFound
	} else if err != nil {
		return m, dbgen.ModelProvider{}, err
	}
	p, err := q.GetModelProvider(ctx, m.ProviderID)
	if err != nil {
		return m, p, err
	}
	if sc.reaches(p) != nil {
		return m, p, errModelNotFound
	}
	return m, p, nil
}

func (s *Service) updateModel(ctx context.Context, a authz.Actor, sc scope, id uuid.UUID, in ModelUpdate, expectedRevision int64) (ModelView, error) {
	var out ModelView
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		m, prov, err := lockModel(ctx, q, sc, id)
		if err != nil {
			return err
		}
		if m.Revision != expectedRevision {
			return apperr.Stale()
		}
		if prov.TeamID.Valid {
			if err := checkTeamModelScope(in.Visibility, in.Teams, in.Personal); err != nil {
				return err
			}
			in.Visibility, in.Teams, in.Personal = nil, nil, nil
		}
		before, err := q.ListModelTeams(ctx, id)
		if err != nil {
			return err
		}
		beforeSlugs := make([]string, len(before))
		for i, t := range before {
			beforeSlugs[i] = t.Slug
		}
		u := dbgen.UpdateModelParams{ID: id, Name: m.Name, ContextWindow: m.ContextWindow, MaxTokens: m.MaxTokens, Images: m.Images,
			Reasoning: m.Reasoning, InputPrice: m.InputPrice, OutputPrice: m.OutputPrice, Enabled: m.Enabled, Visibility: m.Visibility, Personal: m.Personal}
		if in.Personal != nil {
			u.Personal = *in.Personal
		}
		if in.Name != nil {
			if u.Name, err = checkName(*in.Name); err != nil {
				return err
			}
		}
		if in.ContextWindow != nil {
			u.ContextWindow = *in.ContextWindow
		}
		if in.MaxTokens != nil {
			u.MaxTokens = *in.MaxTokens
		}
		if err := checkLimits(u.ContextWindow, u.MaxTokens); err != nil {
			return err
		}
		if in.Images != nil {
			u.Images = *in.Images
		}
		if in.Reasoning != nil {
			u.Reasoning = *in.Reasoning
		}
		for _, pr := range []struct {
			in  *float64
			out *pgtype.Float8
		}{{in.InputPrice, &u.InputPrice}, {in.OutputPrice, &u.OutputPrice}} {
			if pr.in == nil {
				continue
			}
			if *pr.in < 0 {
				*pr.out = pgtype.Float8{}
			} else if *pr.out, err = price(pr.in); err != nil {
				return err
			}
		}
		if in.Enabled != nil {
			u.Enabled = *in.Enabled
		}
		if in.Visibility != nil {
			if *in.Visibility != "all" && *in.Visibility != "teams" {
				return errBadVisibility
			}
			u.Visibility = *in.Visibility
		}
		updated, err := q.UpdateModel(ctx, u)
		if err != nil {
			return err
		}
		slugs := beforeSlugs
		switch {
		case updated.Visibility == "all":
			if err := q.ClearModelTeams(ctx, id); err != nil {
				return err
			}
			slugs = nil
		case in.Teams != nil:
			if slugs, err = setTeams(ctx, q, id, *in.Teams); err != nil {
				return err
			}
		}
		e := modelAudit(a, "model.update", id, prov)
		e.Before, e.After = modelSnapshot(m, beforeSlugs), modelSnapshot(updated, slugs)
		if err := audit.Record(ctx, q, e); err != nil {
			return err
		}
		out, err = s.modelView(ctx, q, updated)
		return err
	})
	return out, err
}

// DeleteModel removes any model (platform admins); refused while templates
// or agents use it.
func (s *Service) DeleteModel(ctx context.Context, a authz.Actor, id uuid.UUID) error {
	if err := requireAdmin(a); err != nil {
		return err
	}
	return s.deleteModel(ctx, a, adminScope, id)
}

func (s *Service) deleteModel(ctx context.Context, a authz.Actor, sc scope, id uuid.UUID) error {
	return store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		m, prov, err := lockModel(ctx, q, sc, id)
		if err != nil {
			return err
		}
		if err := q.DeleteModel(ctx, id); apperr.IsForeignKeyViolation(err, "") {
			return errModelInUse
		} else if err != nil {
			return err
		}
		e := modelAudit(a, "model.delete", id, prov)
		e.Before = modelSnapshot(m, nil)
		return audit.Record(ctx, q, e)
	})
}

// TeamModels lists the models a team may use now (its members and service
// accounts, and platform staff): its own providers' and the platform's
// published to it.
func (s *Service) TeamModels(ctx context.Context, a authz.Actor, teamRef string) ([]ModelView, error) {
	t, err := teams.Resolve(ctx, s.q, teamRef)
	if err != nil {
		return nil, err
	}
	allowed := a.CanReadPlatform()
	if a.Service != nil {
		allowed = a.Service.TeamID == t.ID
	} else if !allowed {
		role, err := teams.MemberRole(ctx, s.q, t.ID, a.UserID)
		if err != nil {
			return nil, err
		}
		allowed = role != ""
	}
	if !allowed {
		return nil, errTeamNotFound
	}
	rows, err := s.q.ListAvailableModels(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	out := make([]ModelView, len(rows))
	for i, r := range rows {
		out[i] = ModelView{Model: r.Model, ProviderName: r.ProviderName, ProviderKind: r.ProviderKind, ProviderEnabled: true}
		if r.ProviderTeamID.Valid {
			out[i].ProviderTeam = teamOwner(t)
		}
	}
	return out, nil
}

// CheckUsable reports ErrUnavailable unless the team may use the model now.
func CheckUsable(ctx context.Context, q *dbgen.Queries, modelID, teamID uuid.UUID) error {
	_, err := q.GetAvailableModel(ctx, dbgen.GetAvailableModelParams{ModelID: modelID, TeamID: teamID})
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return ErrUnavailable
	}
	return err
}

// CheckEnabled reports ErrUnavailable unless the model and its provider are
// enabled, and ErrTeamModel for a team provider's model (platform templates:
// which teams may use it is checked when one deploys it; a team's own
// models are never theirs).
func CheckEnabled(ctx context.Context, q *dbgen.Queries, modelID uuid.UUID) error {
	m, err := q.GetModelSummary(ctx, modelID)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return errModelNotFound
	} else if err != nil {
		return err
	}
	if m.ProviderTeamID.Valid {
		return ErrTeamModel
	}
	if !m.Enabled || !m.ProviderEnabled {
		return ErrUnavailable
	}
	return nil
}

// IsTeamModel reports whether a model is of a team's own provider (false
// for a model that doesn't exist).
func IsTeamModel(ctx context.Context, q *dbgen.Queries, modelID uuid.UUID) (bool, error) {
	m, err := q.GetModelSummary(ctx, modelID)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return false, nil
	}
	return m.ProviderTeamID.Valid, err
}

// Summary is a model's name and its provider's, for showing which model a
// template or agent uses.
type Summary = dbgen.GetModelSummaryRow

// ModelSummary returns a model's summary.
func (s *Service) ModelSummary(ctx context.Context, id uuid.UUID) (Summary, error) {
	return s.q.GetModelSummary(ctx, id)
}

// Resolved is everything needed to call a model: its provider's URL, key and
// headers (decrypted).
type Resolved struct {
	Model    dbgen.Model
	Provider dbgen.ModelProvider
	Kind     Kind
	APIKey   string
	Headers  map[string]string
}

// Resolve returns a model the team may use now, with its provider's
// credentials, or ErrUnavailable.
func (s *Service) Resolve(ctx context.Context, modelID, teamID uuid.UUID) (Resolved, error) {
	r, err := s.q.GetAvailableModel(ctx, dbgen.GetAvailableModelParams{ModelID: modelID, TeamID: teamID})
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return Resolved{}, ErrUnavailable
	} else if err != nil {
		return Resolved{}, err
	}
	kind, ok := KindByID(r.ModelProvider.Kind)
	if !ok {
		return Resolved{}, errBadKind
	}
	key, err := s.box.Open(r.ModelProvider.APIKeyEnc, providerAD(r.ModelProvider.ID))
	if err != nil {
		return Resolved{}, err
	}
	out := Resolved{Model: r.Model, Provider: r.ModelProvider, Kind: kind, APIKey: string(key)}
	if len(r.ModelProvider.HeadersEnc) > 0 {
		raw, err := s.box.Open(r.ModelProvider.HeadersEnc, providerAD(r.ModelProvider.ID))
		if err != nil {
			return Resolved{}, err
		}
		if err := json.Unmarshal(raw, &out.Headers); err != nil {
			return Resolved{}, err
		}
	}
	out.Provider.APIKeyEnc, out.Provider.HeadersEnc = nil, nil
	return out, nil
}

// Usage is one model call's token counts.
type Usage struct {
	Input, Output, CacheRead, CacheWrite int64
}

// Empty reports whether nothing was counted.
func (u Usage) Empty() bool {
	return u.Input == 0 && u.Output == 0 && u.CacheRead == 0 && u.CacheWrite == 0
}

// RecordUsage stores a call's usage, attributed to a connected app's grant
// when grant is valid (oauth.Attribution).
func (s *Service) RecordUsage(ctx context.Context, teamID, agentID, modelID uuid.UUID, source string, u Usage, grant uuid.NullUUID) error {
	return s.q.InsertModelUsage(ctx, dbgen.InsertModelUsageParams{TeamID: teamID, AgentID: uuid.NullUUID{UUID: agentID, Valid: agentID != uuid.Nil},
		ModelID: uuid.NullUUID{UUID: modelID, Valid: modelID != uuid.Nil}, Source: source,
		InputTokens: u.Input, OutputTokens: u.Output, CacheReadTokens: u.CacheRead, CacheWriteTokens: u.CacheWrite, OauthGrantID: grant})
}
