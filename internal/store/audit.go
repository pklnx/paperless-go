package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/paperless-go/internal/store/db"
)

// Who caused an audit entry.
const (
	ActorSession = "session" // the web UI, also for login attempts
	ActorAPIKey  = "api_key"
	ActorCLI     = "cli"
	// ActorSystem is the server acting on its own (the consume folder). Its
	// entries are by no user and from no client; the user they concern is
	// the target.
	ActorSystem = "system"
)

// What an audit entry is about. Documents, tags, correspondents and
// document types are named by their UUID.
const (
	TargetDocument      = "document"
	TargetUser          = "user"
	TargetAPIKey        = "api_key"
	TargetTag           = "tag"
	TargetCorrespondent = "correspondent"
	TargetDocumentType  = "document_type"
)

// ErrAuditUnavailable wraps every failure to write an audit entry. Whatever
// the entry was for has not happened.
var ErrAuditUnavailable = errors.New("the audit log cannot be written")

// ErrNotMigrated is returned when the database lacks the audit log, before
// the migrations ran.
var ErrNotMigrated = errors.New("the database schema is not up to date; run migrate")

// AuditEntry is one row of the audit log. The time comes from the database.
type AuditEntry struct {
	Actor      string
	UserID     *int64
	UserName   string
	APIKeyID   *int64
	APIKeyName string
	Action     string
	TargetType string
	TargetID   string
	TargetName string
	ClientIP   netip.Addr
	UserAgent  string
	Details    map[string]any
}

// SetUser records the user the entry is by.
func (e *AuditEntry) SetUser(id int64, name string) {
	e.UserID, e.UserName = &id, name
}

// SetTarget records what the entry is about.
func (e *AuditEntry) SetTarget(typ, id, name string) {
	e.TargetType, e.TargetID, e.TargetName = typ, id, name
}

// Detail adds a detail.
func (e *AuditEntry) Detail(key string, value any) {
	if e.Details == nil {
		e.Details = make(map[string]any)
	}
	e.Details[key] = value
}

type auditKey struct{}

// pendingAudit is an entry waiting for the transaction of the change it
// describes.
type pendingAudit struct {
	entry *AuditEntry
	// extra are more entries by the same actor and client that the store
	// method adds while it runs (auditExtra): the per-document entries of
	// a bulk change. They are written with entry and reset per attempt.
	extra   []*AuditEntry
	written bool
}

// WithAudit returns a context whose next store transaction also writes e,
// after the change succeeded and before the commit: the change and its
// entry are committed together or not at all. If the entry cannot be
// written, the change is rolled back and the error wraps
// ErrAuditUnavailable. The store method may complete e (the ID of a new
// user). Only one transaction writes it.
func WithAudit(ctx context.Context, e *AuditEntry) context.Context {
	return context.WithValue(ctx, auditKey{}, &pendingAudit{entry: e})
}

// auditFrom returns the entry still to be written with ctx's transaction.
func auditFrom(ctx context.Context) *AuditEntry {
	if p, ok := ctx.Value(auditKey{}).(*pendingAudit); ok && !p.written {
		return p.entry
	}
	return nil
}

// auditExtra adds entries to be written with the pending entry of ctx, in
// the same transaction. Only Action, the target and Details of each are
// used; the actor and client are the pending entry's. Without a pending
// entry it does nothing.
func auditExtra(ctx context.Context, entries ...*AuditEntry) {
	if p, ok := ctx.Value(auditKey{}).(*pendingAudit); ok && !p.written {
		p.extra = append(p.extra, entries...)
	}
}

// fillAuditTarget sets the ID of a target created in the transaction (a new
// user) if the pending entry is about such a target.
func fillAuditTarget(ctx context.Context, typ string, id int64) {
	fillAuditTargetID(ctx, typ, fmt.Sprint(id))
}

// fillAuditTargetID is fillAuditTarget for a target named by a string ID
// (a new document's UUID).
func fillAuditTargetID(ctx context.Context, typ, id string) {
	if e := auditFrom(ctx); e != nil && e.TargetType == typ && e.TargetID == "" {
		e.TargetID = id
	}
}

// Audit writes one entry outside any transaction: for failed logins and
// exports. A missing partition is created and the insert tried once more.
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	ua, err := s.auditUserAgent(ctx, &e)
	if err == nil {
		err = insertAudit(ctx, s.q, &e, ua)
	}
	if missingPartition(err) {
		if _, perr := s.EnsureAuditPartitions(ctx, 1); perr == nil {
			err = insertAudit(ctx, s.q, &e, ua)
		}
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrAuditUnavailable, err)
	}
	return nil
}

// inTx runs fn in a transaction. With an entry from WithAudit in ctx, the
// entry is written after fn succeeded, in the same transaction.
func (s *Store) inTx(ctx context.Context, fn func(*db.Queries) error) error {
	return s.inTxWith(ctx, func(_ pgx.Tx, q *db.Queries) error { return fn(q) })
}

// inTxWith is inTx for a function that also needs the transaction itself
// (for the search queries' unnamed statements).
func (s *Store) inTxWith(ctx context.Context, fn func(pgx.Tx, *db.Queries) error) error {
	p, _ := ctx.Value(auditKey{}).(*pendingAudit)
	if p != nil && p.written {
		p = nil
	}
	if p != nil {
		p.extra = nil // left over from an attempt that failed
	}
	var ua *int64
	if p != nil {
		var err error
		if ua, err = s.auditUserAgent(ctx, p.entry); err != nil {
			return fmt.Errorf("%w: %w", ErrAuditUnavailable, err)
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	if err := fn(tx, q); err != nil {
		return err
	}
	if p != nil {
		err := insertAudit(ctx, q, p.entry, ua)
		if err == nil && len(p.extra) > 0 {
			err = insertAuditEntries(ctx, q, p.entry, p.extra, ua)
		}
		if err != nil {
			// A failed statement aborts the transaction, so no retry here:
			// create the partition for the next attempt.
			_ = tx.Rollback(ctx)
			if missingPartition(err) {
				_, _ = s.EnsureAuditPartitions(ctx, 1)
			}
			return fmt.Errorf("%w: %w", ErrAuditUnavailable, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if p != nil {
		p.written = true
	}
	return nil
}

// auditUserAgent returns the id of e's user agent, or nil without one. It
// may insert on a pool connection of its own, so inTx calls it before the
// transaction starts: called with the transaction's connection held, it
// can wait for a free connection while every other one waits for a row
// lock that transaction holds.
func (s *Store) auditUserAgent(ctx context.Context, e *AuditEntry) (*int64, error) {
	v := cleanUserAgent(e.UserAgent)
	if v == "" {
		return nil, nil
	}
	id, err := s.userAgentID(ctx, v)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// insertAudit writes e with the user agent id ua through q, which may be a
// transaction's: it never uses the pool itself.
func insertAudit(ctx context.Context, q *db.Queries, e *AuditEntry, ua *int64) error {
	details, err := json.Marshal(cleanDetail(e.Details))
	if err != nil {
		return err
	}
	if string(details) == "null" {
		details = []byte("{}")
	}
	var ip *netip.Addr
	if e.ClientIP.IsValid() {
		a := e.ClientIP.Unmap()
		ip = &a
	}
	return q.InsertAuditEntry(ctx, db.InsertAuditEntryParams{
		Actor: e.Actor, UserID: e.UserID, UserName: nullString(cleanString(e.UserName)),
		ApiKeyID: e.APIKeyID, ApiKeyName: nullString(cleanString(e.APIKeyName)), Action: e.Action,
		TargetType: nullString(e.TargetType), TargetID: nullString(cleanString(e.TargetID)), TargetName: nullString(cleanString(e.TargetName)),
		ClientIp: ip, UserAgentID: ua, Details: details,
	})
}

// insertAuditEntries writes entries in one statement, each with the actor
// and client of base, through q.
func insertAuditEntries(ctx context.Context, q *db.Queries, base *AuditEntry, entries []*AuditEntry, ua *int64) error {
	p := db.InsertAuditEntriesParams{
		Actor: base.Actor, UserID: base.UserID, UserName: nullString(cleanString(base.UserName)),
		ApiKeyID: base.APIKeyID, ApiKeyName: nullString(cleanString(base.APIKeyName)), UserAgentID: ua,
	}
	if base.ClientIP.IsValid() {
		a := base.ClientIP.Unmap()
		p.ClientIp = &a
	}
	for _, e := range entries {
		details, err := json.Marshal(cleanDetail(e.Details))
		if err != nil {
			return err
		}
		if string(details) == "null" {
			details = []byte("{}")
		}
		p.Actions = append(p.Actions, e.Action)
		p.TargetTypes = append(p.TargetTypes, e.TargetType)
		p.TargetIds = append(p.TargetIds, cleanString(e.TargetID))
		p.TargetNames = append(p.TargetNames, cleanString(e.TargetName))
		p.Details = append(p.Details, details)
	}
	return q.InsertAuditEntries(ctx, p)
}

// missingPartition reports the error of a row whose month has no partition.
func missingPartition(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == "" &&
		strings.Contains(pgErr.Message, "no partition")
}

// EnsureAuditPartitions creates the audit log partitions of this month and
// the next monthsAhead months that do not exist yet, and returns how many it
// created. It fails with ErrNotMigrated before the migrations ran.
func (s *Store) EnsureAuditPartitions(ctx context.Context, monthsAhead int) (int, error) {
	n, err := s.q.EnsureAuditPartitions(ctx, clampInt32(monthsAhead))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42883" { // undefined_function
		return 0, ErrNotMigrated
	}
	return int(n), err
}

// The user agent ids already looked up. They are inserted outside any
// transaction (autocommit), so a cached id always exists.
const maxCachedUserAgents = 1000

type userAgentCache struct {
	mu  sync.Mutex
	ids map[string]int64
}

func (s *Store) userAgentID(ctx context.Context, ua string) (int64, error) {
	c := &s.userAgents
	c.mu.Lock()
	id, ok := c.ids[ua]
	c.mu.Unlock()
	if ok {
		return id, nil
	}
	var err error
	for range 2 {
		// No row: a concurrent insert of the same string won; look again.
		if id, err = s.q.AuditUserAgentID(ctx, ua); err == nil || !errors.Is(err, pgx.ErrNoRows) {
			break
		}
	}
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	if c.ids == nil || len(c.ids) >= maxCachedUserAgents {
		c.ids = make(map[string]int64)
	}
	c.ids[ua] = id
	c.mu.Unlock()
	return id, nil
}

// maxUserAgent is the longest user agent stored, in bytes.
const maxUserAgent = 256

// cleanUserAgent makes a header value storable: valid UTF-8 without NUL,
// at most maxUserAgent bytes, cut at a character boundary.
func cleanUserAgent(ua string) string {
	ua = cleanString(ua)
	if len(ua) <= maxUserAgent {
		return ua
	}
	cut := maxUserAgent
	for cut > 0 && !utf8.RuneStart(ua[cut]) {
		cut--
	}
	return ua[:cut]
}

// cleanString replaces invalid UTF-8 and NUL, which PostgreSQL refuses in
// text and jsonb.
func cleanString(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\x00", "�")
}

func cleanDetail(v any) any {
	switch v := v.(type) {
	case string:
		return cleanString(v)
	case []string:
		out := make([]string, len(v))
		for i, s := range v {
			out[i] = cleanString(s)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = cleanDetail(x)
		}
		return out
	case map[string]any:
		if v == nil {
			return nil
		}
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[cleanString(k)] = cleanDetail(x)
		}
		return out
	}
	return v
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func clampInt32(n int) int32 {
	return int32(max(min(n, math.MaxInt32), 0)) //nolint:gosec // clamped
}

// The action groups a filter may name instead of exact actions.
var auditGroups = map[string][]string{
	"login":     {"auth.%"},
	"users":     {"user.%"},
	"profile":   {"profile.%"},
	"api_keys":  {"api_key.%"},
	"export":    {"audit.export"},
	"documents": {"document.%"},
}

// AuditGroups are the names of the action groups, for help texts.
func AuditGroups() []string {
	return []string{"login", "users", "profile", "api_keys", "export", "documents"}
}

var auditActionPattern = regexp.MustCompile(`^[a-z0-9_]+\.[a-z0-9_]+$`)

// AuditActionPatterns turns action filters (groups or exact actions) into
// LIKE patterns. No filter matches every action.
func AuditActionPatterns(filters []string) ([]string, error) {
	if len(filters) == 0 {
		return []string{"%"}, nil
	}
	var out []string
	for _, f := range filters {
		if g, ok := auditGroups[f]; ok {
			out = append(out, g...)
			continue
		}
		if !auditActionPattern.MatchString(f) {
			return nil, fmt.Errorf("unknown action %q", f)
		}
		out = append(out, strings.ReplaceAll(f, "_", `\_`))
	}
	return out, nil
}

// AuditFilter selects audit entries.
type AuditFilter struct {
	Since, Until time.Time
	UserID       *int64
	// UserName selects a deleted user's entries by the name they carry.
	UserName *string
	Actions  []string // groups or actions; none means all
}

// AuditRecord is a stored audit entry.
type AuditRecord struct {
	ID         int64
	At         time.Time
	Actor      string
	UserID     *int64
	UserName   string
	APIKeyName string
	Action     string
	TargetType string
	TargetID   string
	TargetName string
	ClientIP   string
	UserAgent  string
	Details    json.RawMessage
}

// ExportAudit calls fn for every entry of f, oldest first.
func (s *Store) ExportAudit(ctx context.Context, f AuditFilter, fn func(AuditRecord) error) error {
	patterns, err := AuditActionPatterns(f.Actions)
	if err != nil {
		return err
	}
	const batch = 1000
	afterAt, afterID := f.Since, int64(0)
	for {
		rows, err := s.q.ExportAuditEntries(ctx, db.ExportAuditEntriesParams{
			Since: f.Since, Until: f.Until, UserID: f.UserID, UserName: f.UserName, Patterns: patterns,
			AfterAt: afterAt, AfterID: afterID, Lim: batch,
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			rec := AuditRecord{
				ID: r.ID, At: r.At, Actor: r.Actor, UserID: r.UserID, UserName: deref(r.UserName),
				APIKeyName: deref(r.ApiKeyName), Action: r.Action, TargetType: deref(r.TargetType),
				TargetID: deref(r.TargetID), TargetName: deref(r.TargetName), UserAgent: deref(r.UserAgent),
				Details: r.Details,
			}
			if r.ClientIp != nil {
				rec.ClientIP = r.ClientIp.String()
			}
			if err := fn(rec); err != nil {
				return err
			}
			afterAt, afterID = r.At, r.ID
		}
		if len(rows) < batch {
			return nil
		}
	}
}

// AuditPage selects a page of the log page, newest first.
type AuditPage struct {
	// Viewer is the scope of who reads the log: without All, the entries
	// by the viewer (Viewer.UserID) and the entries about them (target
	// user, by an admin or the command line) are listed, and UserID,
	// UserName and Actor are ignored; with All (admins), every user's,
	// optionally narrowed to UserID or Actor.
	Viewer Scope
	UserID *int64
	// UserName selects a deleted user's entries by the name they carry.
	UserName     *string
	Actor        string
	Since, Until time.Time
	Actions      []string // groups or actions; none means all
	// After the entry (CursorAt, CursorID) of the previous page.
	CursorAt *time.Time
	CursorID int64
	Limit    int
}

// ListAudit returns a page of entries, newest first.
func (s *Store) ListAudit(ctx context.Context, p AuditPage) ([]AuditRecord, error) {
	patterns, err := AuditActionPatterns(p.Actions)
	if err != nil {
		return nil, err
	}
	var rows []db.ListAuditEntriesRow
	if p.Viewer.All {
		rows, err = s.q.ListAuditEntries(ctx, db.ListAuditEntriesParams{
			Since: p.Since, Until: p.Until, UserID: p.UserID, UserName: p.UserName, Actor: nullString(p.Actor),
			Patterns: patterns, CursorAt: p.CursorAt, CursorID: p.CursorID, Lim: clampInt32(p.Limit),
		})
	} else {
		if p.Viewer.UserID <= 0 {
			return nil, errNoUser
		}
		var own []db.ListAuditEntriesForUserRow
		own, err = s.q.ListAuditEntriesForUser(ctx, db.ListAuditEntriesForUserParams{
			UserID: p.Viewer.UserID, Since: p.Since, Until: p.Until,
			Patterns: patterns, CursorAt: p.CursorAt, CursorID: p.CursorID, Lim: clampInt32(p.Limit),
		})
		for _, r := range own {
			rows = append(rows, db.ListAuditEntriesRow(r))
		}
	}
	if err != nil {
		return nil, err
	}
	out := make([]AuditRecord, 0, len(rows))
	for _, r := range rows {
		rec := AuditRecord{
			ID: r.ID, At: r.At, Actor: r.Actor, UserID: r.UserID, UserName: deref(r.UserName),
			APIKeyName: deref(r.ApiKeyName), Action: r.Action, TargetType: deref(r.TargetType),
			TargetID: deref(r.TargetID), TargetName: deref(r.TargetName), UserAgent: deref(r.UserAgent),
			Details: r.Details,
		}
		if r.ClientIp != nil {
			rec.ClientIP = r.ClientIp.String()
		}
		out = append(out, rec)
	}
	return out, nil
}
