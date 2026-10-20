package oauth

import (
	"context"
	"encoding/base64"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// The app-level views a trusted app reads with its secret (secrets.go):
// usage of the people who connected it, by person (their subject) and day.
// The event stream is internal/agents (SubscribeAppThreads).

// Subject is a person's app-stable pseudonymous ID for a client (made on
// first use): what the app-level endpoints name them by.
func (s *Service) Subject(ctx context.Context, clientID string, userID uuid.UUID) (uuid.UUID, error) {
	return s.q.EnsureOAuthClientSubject(ctx, dbgen.EnsureOAuthClientSubjectParams{ClientID: clientID, UserID: userID})
}

// AppSubjects maps those of userIDs who are a client's people now (an
// active connection, as on the app-level event stream) to their subjects:
// how the stream names a thread's creator and participants. Anyone else is
// left out (the stream shows them without an identifier).
func (s *Service) AppSubjects(ctx context.Context, clientID string, userIDs []uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
	out := map[uuid.UUID]uuid.UUID{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := s.q.AppStreamSubjects(ctx, dbgen.AppStreamSubjectsParams{ClientID: clientID, Resource: s.Resource(), UserIds: userIDs})
	for _, r := range rows {
		out[r.UserID] = r.Subject
	}
	return out, err
}

// Bounds of an app usage request.
const (
	MaxAppUsageDays     = 92
	DefaultAppUsageDays = 7
	DefaultAppUsageRows = 100
	MaxAppUsageRows     = 1000
)

// AppUsageQuery is a page of an app's usage: the days From to To (UTC,
// inclusive), Limit rows after Cursor.
type AppUsageQuery struct {
	From, To time.Time
	Limit    int
	Cursor   string
}

// AppUsageRow is one person's usage on one day, through the app.
type AppUsageRow struct {
	Day                                            time.Time
	Subject                                        uuid.UUID
	Requests, Input, Output, CacheRead, CacheWrite int64
	CostUSD                                        float64
	UnpricedRequests, ToolCalls                    int64
}

// AppUsagePage is a page of rows ordered by day and subject; NextCursor is
// empty on the last page.
type AppUsagePage struct {
	From, To   time.Time
	Rows       []AppUsageRow
	NextCursor string
}

var (
	errAppUsageRange  = apperr.Invalid("invalid_range", "from and to are dates (YYYY-MM-DD), from not after to, at most 92 days apart")
	errAppUsageCursor = apperr.Invalid("invalid_cursor", "The cursor isn't one this endpoint returned")
	errAppUsageLimit  = apperr.Invalid("invalid_limit", "limit is 1 to 1000")
)

func encodeUsageCursor(day time.Time, subject uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(day.Format(time.DateOnly) + "/" + subject.String()))
}

func decodeUsageCursor(c string) (time.Time, uuid.UUID, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	d, sub, ok := strings.Cut(string(raw), "/")
	day, err1 := time.Parse(time.DateOnly, d)
	id, err2 := uuid.Parse(sub)
	return day, id, ok && err1 == nil && err2 == nil
}

// AppUsage is a page of the usage of the people who connected the app, by
// person and day: model requests and tokens (with a cost estimate) and tool
// calls attributed to the app's grants (attribution.go), including people
// who have since disconnected it (the usage was the app's).
func (s *Service) AppUsage(ctx context.Context, c AppClient, q AppUsageQuery) (AppUsagePage, error) {
	today := s.now().UTC().Truncate(24 * time.Hour)
	if q.To.IsZero() {
		q.To = today
	}
	if q.From.IsZero() {
		q.From = q.To.AddDate(0, 0, -(DefaultAppUsageDays - 1))
	}
	q.From, q.To = q.From.UTC().Truncate(24*time.Hour), q.To.UTC().Truncate(24*time.Hour)
	if q.From.After(q.To) || q.To.Sub(q.From) > (MaxAppUsageDays-1)*24*time.Hour {
		return AppUsagePage{}, errAppUsageRange
	}
	if q.Limit == 0 {
		q.Limit = DefaultAppUsageRows
	}
	if q.Limit < 1 || q.Limit > MaxAppUsageRows {
		return AppUsagePage{}, errAppUsageLimit
	}
	p := dbgen.AppUsageByPersonDayParams{ClientID: c.ClientID, Since: q.From, Until: q.To.AddDate(0, 0, 1), Lim: int32(q.Limit + 1)}
	if q.Cursor != "" {
		day, sub, ok := decodeUsageCursor(q.Cursor)
		if !ok {
			return AppUsagePage{}, errAppUsageCursor
		}
		p.AfterDay = pgtype.Date{Time: day, Valid: true}
		p.AfterSubject = uuid.NullUUID{UUID: sub, Valid: true}
	}
	rows, err := s.q.AppUsageByPersonDay(ctx, p)
	if err != nil {
		return AppUsagePage{}, err
	}
	out := AppUsagePage{From: q.From, To: q.To, Rows: make([]AppUsageRow, 0, min(len(rows), q.Limit))}
	for i, r := range rows {
		if i == q.Limit {
			last := out.Rows[len(out.Rows)-1]
			out.NextCursor = encodeUsageCursor(last.Day, last.Subject)
			break
		}
		out.Rows = append(out.Rows, AppUsageRow{Day: r.Day.Time.UTC(), Subject: r.Subject, Requests: r.Requests, Input: r.InputTokens,
			Output: r.OutputTokens, CacheRead: r.CacheReadTokens, CacheWrite: r.CacheWriteTokens, CostUSD: r.CostUsd,
			UnpricedRequests: r.UnpricedRequests, ToolCalls: r.ToolCalls})
	}
	return out, nil
}

// ConnectedClient is an app people have connected now.
type ConnectedClient struct {
	ClientID, Name, Kind string
	// People with an active connection.
	People       int64
	LastActiveAt time.Time
	Trusted      bool
}

// ConnectedClients lists the apps people have connected now, most people
// first (platform admins and auditors): what the usage filter by app and the
// trusted list choose from.
func (s *Service) ConnectedClients(ctx context.Context, a authz.Actor) ([]ConnectedClient, error) {
	if !a.CanReadPlatform() {
		return nil, errPlatformRead
	}
	rows, err := s.q.ListConnectedOAuthClients(ctx)
	if err != nil {
		return nil, err
	}
	trusted, err := s.trustedIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ConnectedClient, len(rows))
	for i, r := range rows {
		out[i] = ConnectedClient{ClientID: r.ClientID, Name: r.ClientName, Kind: r.ClientKind, People: r.People, LastActiveAt: r.LastActiveAt,
			Trusted: trusted[r.ClientID] && r.ClientKind == KindMetadata}
	}
	return out, nil
}
