// Package rekey re-seals every secret the platform stores under the current
// ENCRYPTION_KEY (`pi-durable-platform rekey`), and counts which key each
// stored value is sealed with (`pi-durable-platform doctor`).
//
// A sealed value names its key (internal/secrets: format version, then the
// first 8 bytes of SHA-256 of the key), so which values are still on the
// previous key is read from the stored bytes, in SQL, without opening them,
// and no migration or marker column is needed.
//
// Run works column by column, in batches of rows by primary key:
//
//  1. Inspect: every value not under the current key is opened with the
//     keys the box holds. A value that opens with neither (another key,
//     damage, a missing ENCRYPTION_KEY_PREVIOUS) fails the run before
//     anything is written.
//  2. Re-seal, unless it is a dry run: each batch is one transaction that
//     opens, re-seals and writes its values back. A write is a
//     compare-and-swap on the old ciphertext (UPDATE ... WHERE key = $1 AND
//     col = $old), so a row that someone saved in the meantime (always under
//     the current key, which is what the platform seals with) is left as
//     they wrote it and counted as changed during the run. No row is locked
//     while its value is opened and no revision or updated_at changes.
//  3. One audit entry for the run, with counts and no values.
//
// Values under the current key are never touched, so a run is idempotent and
// a crashed one resumes by running again: only the batch in flight is lost,
// and it is retried.
package rekey

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/secrets"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// Batch sizes: the default, and the largest accepted.
const (
	DefaultBatch = 200
	MaxBatch     = 5000
)

// Audit entry of a run (target type platform).
const (
	auditAction   = "platform.secrets_rekey"
	auditTargetID = "secrets"
)

// afterBatchRead is a test hook: called in a batch's transaction after its
// rows of a column (table.column) were read and before they are re-sealed,
// where a concurrent save or an interrupt is injected.
var afterBatchRead func(column string)

// column is one sealed column. Its associated data binds each value to its
// row; it must be what the owning service uses (internal/llm, internal/mcp,
// internal/agents), or the value won't open.
type column struct {
	table, name string
	ad          func(id uuid.UUID) []byte
	// key is the table's uuid primary key, "id" when empty.
	key string
}

func (c column) pk() string {
	if c.key == "" {
		return "id"
	}
	return c.key
}

func (c column) label() string { return c.table + "." + c.name }

var columns = []column{
	{table: "agents", name: "db_password_enc", ad: func(id uuid.UUID) []byte { return []byte(id.String()) }},
	{table: "agents", name: "runtime_token_enc", ad: func(id uuid.UUID) []byte { return []byte(id.String()) }},
	{table: "model_providers", name: "api_key_enc", ad: func(id uuid.UUID) []byte { return []byte("model_provider:" + id.String()) }},
	{table: "model_providers", name: "headers_enc", ad: func(id uuid.UUID) []byte { return []byte("model_provider:" + id.String()) }},
	{table: "mcp_servers", name: "headers_enc", ad: func(id uuid.UUID) []byte { return []byte("mcp_server:" + id.String()) }},
	{table: "thread_mcp_servers", name: "headers_enc", ad: func(id uuid.UUID) []byte { return []byte("thread_mcp_server:" + id.String()) }},
	// Triggers' secrets and runs' messages until delivered (internal/triggers).
	{table: "agent_triggers", name: "webhook_secret_enc", ad: func(id uuid.UUID) []byte { return []byte("trigger_webhook:" + id.String()) }},
	{table: "agent_triggers", name: "callback_secret_enc", ad: func(id uuid.UUID) []byte { return []byte("trigger_callback:" + id.String()) }},
	{table: "trigger_runs", name: "message_enc", ad: func(id uuid.UUID) []byte { return []byte("trigger_run:" + id.String()) }},
	// The refresh token of a person's last sign-in (group re-checks, internal/auth).
	{table: "user_idp_tokens", name: "refresh_token_enc", key: "user_id", ad: func(id uuid.UUID) []byte { return []byte("idp_refresh_token:" + id.String()) }},
	// The MCP identity token's signing keys (internal/mcpidentity).
	{table: "mcp_identity_keys", name: "private_key_enc", ad: func(id uuid.UUID) []byte { return []byte("mcp_identity_key:" + id.String()) }},
}

// ColumnResult is what is stored in one sealed column.
type ColumnResult struct {
	Table  string
	Column string
	// Current values are sealed with ENCRYPTION_KEY (left alone), Previous
	// ones with a previous key and open with it. Unreadable ones don't open
	// with the configured keys (Count tells only by key id; a run also opens
	// them). BadIDs are some of their rows' IDs (never values).
	Current, Previous, Unreadable int
	BadIDs                        []string
	// Resealed values were moved to the current key by this run; Changed
	// rows were saved by someone else meanwhile and left as they wrote them.
	Resealed, Changed int
}

// Label is table.column.
func (r ColumnResult) Label() string { return r.Table + "." + r.Column }

// Result is the outcome of a run, one entry per sealed column.
type Result struct {
	DryRun  bool
	Columns []ColumnResult
}

// Totals adds up the columns.
func (r Result) Totals() (current, previous, unreadable, resealed, changed int) {
	for _, c := range r.Columns {
		current += c.Current
		previous += c.Previous
		unreadable += c.Unreadable
		resealed += c.Resealed
		changed += c.Changed
	}
	return
}

// UnreadableError is returned when values don't open with the configured
// keys. Nothing was written.
type UnreadableError struct {
	Result Result
}

func (e *UnreadableError) Error() string {
	var parts []string
	n := 0
	for _, c := range e.Result.Columns {
		if c.Unreadable == 0 {
			continue
		}
		n += c.Unreadable
		p := fmt.Sprintf("%s: %d", c.Label(), c.Unreadable)
		if len(c.BadIDs) > 0 {
			p += " (rows " + strings.Join(c.BadIDs, ", ") + ")"
		}
		parts = append(parts, p)
	}
	return fmt.Sprintf("%d value(s) cannot be opened with ENCRYPTION_KEY or ENCRYPTION_KEY_PREVIOUS, so nothing was changed. "+
		"Set ENCRYPTION_KEY_PREVIOUS to the key they were sealed with, or enter those secrets again. %s", n, strings.Join(parts, "; "))
}

// Options of Run.
type Options struct {
	// DryRun counts and checks without writing.
	DryRun bool
	// Batch is the rows per transaction (default DefaultBatch).
	Batch int
}

// queryer is a pool or a transaction.
type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Count tells, for every sealed column, how many values are under the
// current key, under a previous key, and under neither (or not sealed
// values at all), from their key ids alone. It opens nothing and writes
// nothing.
func Count(ctx context.Context, db queryer, box *secrets.Box) ([]ColumnResult, error) {
	out := make([]ColumnResult, 0, len(columns))
	for _, c := range columns {
		r, err := c.count(ctx, db, box)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.label(), err)
		}
		out = append(out, r)
	}
	return out, nil
}

func (c column) count(ctx context.Context, db queryer, box *secrets.Box) (ColumnResult, error) {
	r := ColumnResult{Table: c.table, Column: c.name}
	var total int
	// The names are this file's constants, never input.
	err := db.QueryRow(ctx, fmt.Sprintf(`SELECT
		count(*) FILTER (WHERE substring(%[2]s FROM 1 FOR %[3]d) = $1),
		count(*) FILTER (WHERE substring(%[2]s FROM 1 FOR %[3]d) = ANY($2::bytea[])),
		count(%[2]s) FROM %[1]s`, c.table, c.name, secrets.PrefixLen),
		box.CurrentPrefix(), box.PreviousPrefixes()).Scan(&r.Current, &r.Previous, &total)
	r.Unreadable = total - r.Current - r.Previous
	return r, err
}

type sealedRow struct {
	id         uuid.UUID
	ciphertext []byte
}

// pending returns up to limit rows after the given id (by id) whose value is
// not under the current key.
func (c column) pending(ctx context.Context, db queryer, box *secrets.Box, after uuid.NullUUID, limit int) ([]sealedRow, error) {
	rows, err := db.Query(ctx, fmt.Sprintf(`SELECT %[4]s, %[2]s FROM %[1]s
		WHERE %[2]s IS NOT NULL AND substring(%[2]s FROM 1 FOR %[3]d) <> $1 AND ($2::uuid IS NULL OR %[4]s > $2)
		ORDER BY %[4]s LIMIT $3`, c.table, c.name, secrets.PrefixLen, c.pk()), box.CurrentPrefix(), after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sealedRow
	for rows.Next() {
		var r sealedRow
		if err := rows.Scan(&r.id, &r.ciphertext); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// inspect opens every value not under the current key.
func (c column) inspect(ctx context.Context, pool *pgxpool.Pool, box *secrets.Box, batch int) (ColumnResult, error) {
	r, err := c.count(ctx, pool, box)
	if err != nil {
		return r, err
	}
	r.Previous, r.Unreadable = 0, 0
	var after uuid.NullUUID
	for {
		rows, err := c.pending(ctx, pool, box, after, batch)
		if err != nil {
			return r, err
		}
		if len(rows) == 0 {
			return r, nil
		}
		for _, row := range rows {
			if _, err := box.Open(row.ciphertext, c.ad(row.id)); err != nil {
				r.Unreadable++
				if len(r.BadIDs) < 5 {
					r.BadIDs = append(r.BadIDs, row.id.String())
				}
				continue
			}
			r.Previous++
		}
		after = uuid.NullUUID{UUID: rows[len(rows)-1].id, Valid: true}
	}
}

// reseal moves the column's values to the current key, a transaction per
// batch. It returns what it re-sealed even when it fails part way.
func (c column) reseal(ctx context.Context, pool *pgxpool.Pool, box *secrets.Box, batch int, r *ColumnResult) error {
	var after uuid.NullUUID
	for {
		var resealed, changed int
		var last uuid.UUID
		var n int
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			rows, err := c.pending(ctx, tx, box, after, batch)
			if err != nil {
				return err
			}
			n = len(rows)
			if afterBatchRead != nil && n > 0 {
				afterBatchRead(c.label())
			}
			for _, row := range rows {
				last = row.id
				plain, err := box.Open(row.ciphertext, c.ad(row.id))
				if err != nil {
					return fmt.Errorf("row %s: %w", row.id, err)
				}
				sealed, err := box.Seal(plain, c.ad(row.id))
				if err != nil {
					return err
				}
				tag, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %[1]s SET %[2]s = $1 WHERE %[3]s = $2 AND %[2]s = $3`, c.table, c.name, c.pk()),
					sealed, row.id, row.ciphertext)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 1 {
					resealed++
				} else {
					changed++
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("%s: %w", c.label(), err)
		}
		r.Resealed += resealed
		r.Changed += changed
		if n == 0 {
			return nil
		}
		after = uuid.NullUUID{UUID: last, Valid: true}
	}
}

// Run re-seals every sealed value under the box's current key (or, with
// DryRun, only reports). It returns *UnreadableError, before writing
// anything, when a value opens with neither key. The Result is filled in as
// far as the run got, also with an error.
func Run(ctx context.Context, pool *pgxpool.Pool, box *secrets.Box, opts Options) (Result, error) {
	batch := opts.Batch
	if batch == 0 {
		batch = DefaultBatch
	}
	if batch < 1 || batch > MaxBatch {
		return Result{}, fmt.Errorf("batch must be from 1 to %d", MaxBatch)
	}
	res := Result{DryRun: opts.DryRun}
	for _, c := range columns {
		r, err := c.inspect(ctx, pool, box, batch)
		if err != nil {
			return res, fmt.Errorf("%s: %w", c.label(), err)
		}
		res.Columns = append(res.Columns, r)
	}
	if _, _, unreadable, _, _ := res.Totals(); unreadable > 0 {
		return res, &UnreadableError{Result: res}
	}
	if opts.DryRun {
		return res, nil
	}

	var runErr error
	for i, c := range columns {
		if runErr = c.reseal(ctx, pool, box, batch, &res.Columns[i]); runErr != nil {
			break
		}
	}
	// Once something was written the run is audited, finished or not.
	if _, _, _, resealed, _ := res.Totals(); runErr == nil || resealed > 0 {
		if err := recordRun(ctx, pool, res, batch, runErr == nil); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("audit entry: %w", err))
		}
	}
	return res, runErr
}

// recordRun writes the run's single audit entry: counts per column, no
// values.
func recordRun(ctx context.Context, pool *pgxpool.Pool, res Result, batch int, complete bool) error {
	// Also after an interrupt: the writes that happened are audited.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_, _, _, resealed, changed := res.Totals()
	byColumn := map[string]int{}
	for _, c := range res.Columns {
		if c.Resealed > 0 {
			byColumn[c.Label()] = c.Resealed
		}
	}
	return store.InTx(ctx, pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		return audit.Record(ctx, q, audit.Entry{
			ActorKind: audit.ActorSystem, Action: auditAction, TargetType: "platform", TargetID: auditTargetID,
			Metadata: map[string]any{"resealed": resealed, "changedDuringRun": changed, "complete": complete, "batch": batch, "columns": byColumn},
		})
	})
}
