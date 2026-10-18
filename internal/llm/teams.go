package llm

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/netguard"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/teams"
)

// Team model providers: a team's owners and admins add providers and models
// for their own team (authz.CheckTeamProvider*), as platform admins do in
// Admin › Models. Their models are the team's only: availability ignores
// their visibility, teams and personal flag, platform templates can't use
// them, and a template using one can't go to the catalog.
//
// A team provider's URL is the team's choice, so it is proxy-only (direct
// mode would need egress rules a team can't grant) and every request to it
// (discovery, the model proxy) goes through the address guard
// (internal/netguard): public addresses, private ones only inside
// model_settings.private_networks, loopback, link-local and metadata
// addresses never; resolved and pinned at each connection; no redirects; no
// proxy from the environment. Its URL must be https (plain http only on a
// loopback host with the development allowance) and is checked when it is
// saved, and again at every connection (DNS rebinding). Platform providers
// keep the unguarded client: platform admins may point them at internal
// services.

var (
	errTeamProxyOnly = apperr.Invalid("invalid_access_mode", "A team's model providers are proxy-only: agents reach them through the platform")
	errTeamURL       = apperr.Invalid("invalid_base_url", "A team's model provider needs an https URL without credentials, query or fragment")
	errTeamModelUse  = apperr.Invalid("team_model_visibility",
		"A team's own models are for that team only: visibility, teams and personal don't apply to them")
	errAddressNotAllowed = apperr.New(http.StatusUnprocessableEntity, "address_not_allowed",
		"The provider's address is on a network teams can't use. Ask a platform admin to allow it.")
	errAddressUnresolved = apperr.New(http.StatusUnprocessableEntity, "address_unresolved", "The provider's host name could not be resolved")
	errBadNetworks       = apperr.Invalid("invalid_private_networks",
		"Private networks are at most 50 private CIDRs (such as 10.20.0.0/16); loopback, link-local and public ranges can't be listed")

	// ErrTeamModel refuses a team provider's model where only platform
	// models may go (platform templates, the catalog).
	ErrTeamModel = apperr.Conflict("team_model", "That model is of a team's own provider, which only that team can use")
)

// maxPrivateNetworks bounds model_settings.private_networks.
const maxPrivateNetworks = 50

// TeamNetwork is how the platform reaches team providers.
type TeamNetwork struct {
	// DevAllowance accepts every address, and plain http on a loopback host
	// (DEV_AUTH on a loopback APP_URL). Never in production.
	DevAllowance bool
	// Lookup resolves a host and Connect dials an approved address (nil: the
	// system's). Tests replace them; the guard's rule stays.
	Lookup  func(ctx context.Context, host string) ([]netip.Addr, error)
	Connect func(ctx context.Context, network, address string) (net.Conn, error)
	// TLS is the TLS configuration (nil: the system's; tests trust a test
	// server).
	TLS *tls.Config
}

type teamNet struct {
	cfg    TeamNetwork
	client *http.Client
}

// SetTeamNetwork sets how team providers are reached (at start-up; tests
// replace the resolver, dialer and TLS configuration).
func (s *Service) SetTeamNetwork(n TeamNetwork) {
	tn := &teamNet{cfg: n}
	t := &http.Transport{
		Proxy: nil, // the guard must see the real destination
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d, err := s.teamDialer(ctx, n)
			if err != nil {
				return nil, err
			}
			return d.DialContext(ctx, network, addr)
		},
		TLSClientConfig:       n.TLS,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Minute,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   8,
		// Short, so a network removed from private_networks stops being
		// reached soon on every replica (this one closes them at once).
		IdleConnTimeout: 30 * time.Second,
	}
	tn.client = &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if old := s.team.Swap(tn); old != nil {
		old.client.CloseIdleConnections()
	}
}

// teamDialer is the address guard with the private networks platform admins
// allow now.
func (s *Service) teamDialer(ctx context.Context, n TeamNetwork) (*netguard.Dialer, error) {
	settings, err := s.q.GetModelSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("model settings: %w", err)
	}
	return &netguard.Dialer{AllowPrivate: n.DevAllowance, Allow: parseNetworks(settings.PrivateNetworks),
		Timeout: 10 * time.Second, Lookup: n.Lookup, Connect: n.Connect}, nil
}

func parseNetworks(raw []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(raw))
	for _, r := range raw {
		if p, err := netip.ParsePrefix(r); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// clientFor is the client that calls a provider: through the address guard
// for a team's.
func (s *Service) clientFor(p dbgen.ModelProvider) *http.Client {
	if p.TeamID.Valid {
		return s.team.Load().client
	}
	return s.client
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// checkTeamAddress checks a team provider's (normalised) base URL: https
// (http only on a loopback host with the development allowance), on an
// address the guard allows now.
func (s *Service) checkTeamAddress(ctx context.Context, base string) error {
	n := s.team.Load().cfg
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return errTeamURL
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && n.DevAllowance && loopbackHost(u.Hostname())) {
		return errTeamURL
	}
	d, err := s.teamDialer(ctx, n)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	switch err := d.Check(ctx, u.Hostname()); {
	case errors.Is(err, netguard.ErrBlockedAddress):
		return errAddressNotAllowed
	case err != nil:
		return errAddressUnresolved
	}
	return nil
}

// checkTeamModelScope refuses publishing settings on a team provider's
// model: absent, visibility teams with no teams and personal false are
// accepted (and ignored).
func checkTeamModelScope(visibility *string, teamRefs *[]string, personal *bool) error {
	if (visibility != nil && *visibility != "" && *visibility != "teams") || (teamRefs != nil && len(*teamRefs) > 0) ||
		(personal != nil && *personal) {
		return errTeamModelUse
	}
	return nil
}

// providerSubject is a team's providers as the actor sees them; with
// settings, the platform settings that decide whether it may add one.
func providerSubject(ctx context.Context, q *dbgen.Queries, a authz.Actor, t dbgen.Team, settings bool) (authz.TeamProviderSubject, error) {
	sub := authz.TeamProviderSubject{
		Session:  a.Service == nil && a.APIKeyID == uuid.Nil && a.OAuthGrantID == uuid.Nil,
		Staff:    a.CanReadPlatform(),
		Personal: t.Kind == teams.KindPersonal,
		Archived: t.Status != teams.StatusActive,
	}
	if a.Service != nil {
		if a.Service.TeamID == t.ID {
			sub.Role = a.Service.Role
		}
	} else {
		role, err := teams.MemberRole(ctx, q, t.ID, a.UserID)
		if err != nil {
			return sub, err
		}
		sub.Role = role
	}
	if settings {
		ms, err := q.GetModelSettings(ctx)
		if err != nil {
			return sub, err
		}
		ps, err := q.GetPersonalSettings(ctx)
		if err != nil {
			return sub, err
		}
		sub.TeamsMayAdd, sub.PersonalMayAdd = ms.TeamsMayAddProviders, ps.OwnProviders
	}
	return sub, nil
}

// teamAccess resolves a team route's team and checks rule (create: with the
// platform settings). Someone who may not read the team's providers gets
// team not found.
func (s *Service) teamAccess(ctx context.Context, q *dbgen.Queries, a authz.Actor, teamRef string,
	rule func(authz.TeamProviderSubject) error, create bool) (scope, error) {
	t, err := teams.Resolve(ctx, q, teamRef)
	if err != nil {
		return scope{}, err
	}
	sub, err := providerSubject(ctx, q, a, t, create)
	if err != nil {
		return scope{}, err
	}
	if authz.CheckTeamProviderRead(sub) != nil {
		return scope{}, errTeamNotFound
	}
	if err := rule(sub); err != nil {
		return scope{}, err
	}
	return scope{team: &t}, nil
}

// CanAddProviders reports whether the actor may add a model provider for
// team t now (the team's limits, for the UI).
func (s *Service) CanAddProviders(ctx context.Context, a authz.Actor, t dbgen.Team) (bool, error) {
	sub, err := providerSubject(ctx, s.q, a, t, true)
	if err != nil {
		return false, err
	}
	return authz.CheckTeamProviderCreate(sub) == nil, nil
}

// TeamProviders lists a team's own providers (its members, platform staff).
func (s *Service) TeamProviders(ctx context.Context, a authz.Actor, teamRef string) ([]ProviderView, error) {
	sc, err := s.teamAccess(ctx, s.q, a, teamRef, authz.CheckTeamProviderRead, false)
	if err != nil {
		return nil, err
	}
	return s.listProviders(ctx, false, uuid.NullUUID{UUID: sc.team.ID, Valid: true})
}

// TeamProvider returns one of a team's providers (its members, platform
// staff); another team's or the platform's is not found.
func (s *Service) TeamProvider(ctx context.Context, a authz.Actor, teamRef string, id uuid.UUID) (ProviderView, error) {
	sc, err := s.teamAccess(ctx, s.q, a, teamRef, authz.CheckTeamProviderRead, false)
	if err != nil {
		return ProviderView{}, err
	}
	return s.provider(ctx, sc, id)
}

// CreateTeamProvider adds a provider for the team (its owners and admins,
// while platform admins let teams): proxy only, its URL through the address
// guard.
func (s *Service) CreateTeamProvider(ctx context.Context, a authz.Actor, teamRef string, in ProviderInput) (ProviderView, error) {
	sc, err := s.teamAccess(ctx, s.q, a, teamRef, authz.CheckTeamProviderCreate, true)
	if err != nil {
		return ProviderView{}, err
	}
	return s.createProvider(ctx, a, sc, in)
}

// UpdateTeamProvider changes one of the team's providers (its owners and
// admins, If-Match).
func (s *Service) UpdateTeamProvider(ctx context.Context, a authz.Actor, teamRef string, id uuid.UUID, in ProviderUpdate, expectedRevision int64) (ProviderView, error) {
	sc, err := s.teamAccess(ctx, s.q, a, teamRef, authz.CheckTeamProviderManage, false)
	if err != nil {
		return ProviderView{}, err
	}
	return s.updateProvider(ctx, a, sc, id, in, expectedRevision)
}

// DeleteTeamProvider removes one of the team's providers and its models
// (its owners and admins); refused while templates or agents use them.
func (s *Service) DeleteTeamProvider(ctx context.Context, a authz.Actor, teamRef string, id uuid.UUID) error {
	sc, err := s.teamAccess(ctx, s.q, a, teamRef, authz.CheckTeamProviderManage, false)
	if err != nil {
		return err
	}
	return s.deleteProvider(ctx, a, sc, id)
}

// DiscoverTeam asks one of the team's providers which models it offers (its
// owners and admins).
func (s *Service) DiscoverTeam(ctx context.Context, a authz.Actor, teamRef string, id uuid.UUID) ([]Discovered, error) {
	sc, err := s.teamAccess(ctx, s.q, a, teamRef, authz.CheckTeamProviderManage, false)
	if err != nil {
		return nil, err
	}
	return s.discover(ctx, sc, id)
}

// TeamProviderModels lists one of the team's providers' models (its
// members, platform staff).
func (s *Service) TeamProviderModels(ctx context.Context, a authz.Actor, teamRef string, providerID uuid.UUID) ([]ModelView, error) {
	sc, err := s.teamAccess(ctx, s.q, a, teamRef, authz.CheckTeamProviderRead, false)
	if err != nil {
		return nil, err
	}
	return s.models(ctx, sc, &providerID, false, uuid.NullUUID{})
}

// CreateTeamModel adds a model to one of the team's providers (its owners
// and admins); it is the team's only.
func (s *Service) CreateTeamModel(ctx context.Context, a authz.Actor, teamRef string, providerID uuid.UUID, in ModelInput) (ModelView, error) {
	sc, err := s.teamAccess(ctx, s.q, a, teamRef, authz.CheckTeamProviderManage, false)
	if err != nil {
		return ModelView{}, err
	}
	return s.createModel(ctx, a, sc, providerID, in)
}

// UpdateTeamModel changes a model of one of the team's providers (its
// owners and admins, If-Match).
func (s *Service) UpdateTeamModel(ctx context.Context, a authz.Actor, teamRef string, id uuid.UUID, in ModelUpdate, expectedRevision int64) (ModelView, error) {
	sc, err := s.teamAccess(ctx, s.q, a, teamRef, authz.CheckTeamProviderManage, false)
	if err != nil {
		return ModelView{}, err
	}
	return s.updateModel(ctx, a, sc, id, in, expectedRevision)
}

// DeleteTeamModel removes a model of one of the team's providers (its
// owners and admins); refused while templates or agents use it.
func (s *Service) DeleteTeamModel(ctx context.Context, a authz.Actor, teamRef string, id uuid.UUID) error {
	sc, err := s.teamAccess(ctx, s.q, a, teamRef, authz.CheckTeamProviderManage, false)
	if err != nil {
		return err
	}
	return s.deleteModel(ctx, a, sc, id)
}

// CheckTeamKinds checks the actor may see the provider types on a team's
// routes (whoever may read its providers).
func (s *Service) CheckTeamKinds(ctx context.Context, a authz.Actor, teamRef string) error {
	_, err := s.teamAccess(ctx, s.q, a, teamRef, authz.CheckTeamProviderRead, false)
	return err
}

// Settings are the platform's model settings.
type Settings = dbgen.ModelSetting

// Settings returns the model settings (platform staff).
func (s *Service) Settings(ctx context.Context, a authz.Actor) (Settings, error) {
	if !a.CanReadPlatform() {
		return Settings{}, apperr.Forbidden("Only platform admins and auditors can see platform settings")
	}
	return s.q.GetModelSettings(ctx)
}

// SettingsInput replaces the model settings.
type SettingsInput struct {
	TeamsMayAddProviders bool
	PrivateNetworks      []string
}

// checkNetworks normalises private networks: CIDRs of private addresses
// (RFC 1918, CGNAT, unique-local), masked, each once. Ranges of blocked
// addresses (loopback, link-local …) or public ones are refused: the guard
// never allows the first and always allows the second.
func checkNetworks(in []string) ([]string, error) {
	if len(in) > maxPrivateNetworks {
		return nil, errBadNetworks
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for i, raw := range in {
		p, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || netguard.Classify(p.Masked().Addr()) != netguard.Private {
			e := *errBadNetworks
			e.Details = map[string]any{"index": i}
			return nil, &e
		}
		p = p.Masked()
		if !seen[p.String()] {
			seen[p.String()] = true
			out = append(out, p.String())
		}
	}
	return out, nil
}

// UpdateSettings replaces the model settings (platform admins, If-Match).
// Turning team providers off stops new ones; existing ones keep working.
// Connections this replica keeps to team providers are closed, so a network
// taken out of private_networks is not reached again (other replicas' close
// within 30 seconds).
func (s *Service) UpdateSettings(ctx context.Context, a authz.Actor, in SettingsInput, expectedRevision int64) (Settings, error) {
	if !a.IsPlatformAdmin() {
		return Settings{}, apperr.Forbidden("Only platform admins can change platform settings")
	}
	nets, err := checkNetworks(in.PrivateNetworks)
	if err != nil {
		return Settings{}, err
	}
	var out Settings
	err = store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		cur, err := q.LockModelSettings(ctx)
		if err != nil {
			return err
		}
		if cur.Revision != expectedRevision {
			return apperr.Stale()
		}
		if out, err = q.UpdateModelSettings(ctx, dbgen.UpdateModelSettingsParams{TeamsMayAddProviders: in.TeamsMayAddProviders,
			PrivateNetworks: nets, UpdatedBy: uuid.NullUUID{UUID: a.UserID, Valid: a.UserID != uuid.Nil}}); err != nil {
			return err
		}
		e := a.Audit("platform.model_settings", "platform", "models")
		e.Before, e.After = settingsSnapshot(cur), settingsSnapshot(out)
		return audit.Record(ctx, q, e)
	})
	if err == nil {
		s.team.Load().client.CloseIdleConnections()
	}
	return out, err
}

func settingsSnapshot(m Settings) map[string]any {
	return map[string]any{"teamsMayAddProviders": m.TeamsMayAddProviders, "privateNetworks": m.PrivateNetworks}
}
