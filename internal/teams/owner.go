package teams

import (
	"context"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// A team's own services (model providers, MCP servers) name the team that
// owns them; the platform's own have no owner.

// Owner is the team that owns a team's own service.
type Owner struct {
	ID               uuid.UUID
	Slug, Name, Kind string
}

// OwnerOf is the owner from a list row's joined team columns (nil: a
// platform service).
func OwnerOf(id uuid.NullUUID, slug, name, kind *string) *Owner {
	if !id.Valid {
		return nil
	}
	o := &Owner{ID: id.UUID}
	for _, f := range []struct {
		dst *string
		src *string
	}{{&o.Slug, slug}, {&o.Name, name}, {&o.Kind, kind}} {
		if f.src != nil {
			*f.dst = *f.src
		}
	}
	return o
}

// OwnerFromTeam is team t as an owner.
func OwnerFromTeam(t dbgen.Team) *Owner {
	return &Owner{ID: t.ID, Slug: t.Slug, Name: t.Name, Kind: t.Kind}
}

// OwnerByID is the owner with ID id (nil for a platform service).
func OwnerByID(ctx context.Context, q *dbgen.Queries, id uuid.NullUUID) (*Owner, error) {
	if !id.Valid {
		return nil, nil
	}
	t, err := q.GetTeamByID(ctx, id.UUID)
	if err != nil {
		return nil, err
	}
	return OwnerFromTeam(t), nil
}

var errBadOwner = apperr.Invalid("invalid_owner", "owner is all, platform, or a team's slug or ID")

// OwnerFilter turns an admin list's ?owner= (all, platform, or a team's
// slug or ID) into list filters.
func OwnerFilter(ctx context.Context, q *dbgen.Queries, owner string) (platformOnly bool, team uuid.NullUUID, err error) {
	switch owner {
	case "", "all":
		return false, uuid.NullUUID{}, nil
	case "platform":
		return true, uuid.NullUUID{}, nil
	}
	if len(owner) > 100 {
		return false, team, errBadOwner
	}
	t, err := Resolve(ctx, q, owner)
	if err != nil {
		return false, team, err
	}
	return false, uuid.NullUUID{UUID: t.ID, Valid: true}, nil
}
