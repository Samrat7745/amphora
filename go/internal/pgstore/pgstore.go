// Package pgstore is the Postgres metadata store; table, column, FK and sequence names match
// what Hibernate (ddl-auto=update) creates for the Java service.
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/carbynestack/amphora-go/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is satisfied by *pgxpool.Pool and pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store wraps a connection pool.
type Store struct{ Pool *pgxpool.Pool }

const schema = `
CREATE TABLE IF NOT EXISTS amphora_secrets (
  secret_id varchar(255) NOT NULL,
  PRIMARY KEY (secret_id));
CREATE TABLE IF NOT EXISTS secret_tags (
  id int4 NOT NULL,
  tag_key varchar(255),
  tag_value varchar(255),
  tag_value_type varchar(255),
  secret varchar(255) NOT NULL,
  PRIMARY KEY (id));
CREATE SEQUENCE IF NOT EXISTS hibernate_sequence START 1 INCREMENT 1;
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk9i7dng41dtnf03ddv5wpv2qt6') THEN
    ALTER TABLE secret_tags ADD CONSTRAINT fk9i7dng41dtnf03ddv5wpv2qt6 FOREIGN KEY (secret) REFERENCES amphora_secrets;
  END IF;
END $$;`

// Open connects and ensures the (non-destructive) schema exists.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 10 // HikariCP default
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	s := &Store{Pool: pool}
	if err := s.EnsureSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// EnsureSchema creates tables, FK and sequence if missing.
func (s *Store) EnsureSchema(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, schema)
	return err
}

// Ping checks DB reachability (health).
func (s *Store) Ping(ctx context.Context) error { return s.Pool.Ping(ctx) }

// Close releases the pool.
func (s *Store) Close() { s.Pool.Close() }

// InTx runs fn in a transaction.
func (s *Store) InTx(ctx context.Context, fn func(q Querier) error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(context.Background())
		return err
	}
	return tx.Commit(ctx)
}

// DSN builds the connection string like the JDBC url.
func DSN(host string, port int, user, password, db string) string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=prefer", urlEsc(user), urlEsc(password), host, port, db)
}

func urlEsc(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			out = append(out, c)
		} else {
			out = append(out, fmt.Sprintf("%%%02X", c)...)
		}
	}
	return string(out)
}

// TagRow is a secret_tags row.
type TagRow struct {
	ID                    int32
	Key, Value, ValueType string
}

// Tag converts the row to the model type.
func (r TagRow) Tag() model.Tag {
	return model.Tag{Key: r.Key, Value: r.Value, ValueType: model.TagValueType(r.ValueType)}
}

func nz(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Exists reports whether the secret row exists.
func (s *Store) Exists(ctx context.Context, q Querier, id string) (bool, error) {
	var one int
	err := q.QueryRow(ctx, "SELECT 1 FROM amphora_secrets WHERE secret_id = $1", id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Tags returns the tags of a secret (ordered by id).
func (s *Store) Tags(ctx context.Context, q Querier, id string) ([]TagRow, error) {
	rows, err := q.Query(ctx, "SELECT id, tag_key, tag_value, tag_value_type FROM secret_tags WHERE secret = $1 ORDER BY id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TagRow{}
	for rows.Next() {
		var r TagRow
		var k, v, t *string
		if err := rows.Scan(&r.ID, &k, &v, &t); err != nil {
			return nil, err
		}
		r.Key, r.Value, r.ValueType = nz(k), nz(v), nz(t)
		out = append(out, r)
	}
	return out, rows.Err()
}

// FindSecret returns (exists, tags).
func (s *Store) FindSecret(ctx context.Context, q Querier, id string) (bool, []TagRow, error) {
	ok, err := s.Exists(ctx, q, id)
	if err != nil || !ok {
		return false, nil, err
	}
	tags, err := s.Tags(ctx, q, id)
	return true, tags, err
}

// LockSecret takes a row lock on the secret for the rest of the transaction (serialises concurrent tag
// changes of one secret: tag_key is not unique in the schema, so check-then-insert would otherwise race).
// A missing secret is not an error.
func (s *Store) LockSecret(ctx context.Context, q Querier, id string) error {
	var one int
	err := q.QueryRow(ctx, "SELECT 1 FROM amphora_secrets WHERE secret_id = $1 FOR UPDATE", id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// FindTag returns the tag row with the key or nil.
func (s *Store) FindTag(ctx context.Context, q Querier, id, key string) (*TagRow, error) {
	var r TagRow
	var k, v, t *string
	err := q.QueryRow(ctx, "SELECT id, tag_key, tag_value, tag_value_type FROM secret_tags WHERE secret = $1 AND tag_key = $2 ORDER BY id LIMIT 1", id, key).Scan(&r.ID, &k, &v, &t)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Key, r.Value, r.ValueType = nz(k), nz(v), nz(t)
	return &r, nil
}

// InsertSecret inserts the secret row.
func (s *Store) InsertSecret(ctx context.Context, q Querier, id string) error {
	_, err := q.Exec(ctx, "INSERT INTO amphora_secrets (secret_id) VALUES ($1)", id)
	return err
}

// InsertTag inserts a tag with an id from the shared hibernate_sequence.
func (s *Store) InsertTag(ctx context.Context, q Querier, id string, t model.Tag) error {
	_, err := q.Exec(ctx, "INSERT INTO secret_tags (id, tag_key, tag_value, tag_value_type, secret) VALUES (nextval('hibernate_sequence'), $1, $2, $3, $4)",
		t.Key, t.Value, string(t.ValueType), id)
	return err
}

// UpdateTag changes value and type of an existing tag row.
func (s *Store) UpdateTag(ctx context.Context, q Querier, rowID int32, t model.Tag) error {
	_, err := q.Exec(ctx, "UPDATE secret_tags SET tag_value = $1, tag_value_type = $2 WHERE id = $3", t.Value, string(t.ValueType), rowID)
	return err
}

// DeleteTagRow removes one tag.
func (s *Store) DeleteTagRow(ctx context.Context, q Querier, rowID int32) error {
	_, err := q.Exec(ctx, "DELETE FROM secret_tags WHERE id = $1", rowID)
	return err
}

// DeleteTags removes all tags of a secret except the given row ids.
func (s *Store) DeleteTags(ctx context.Context, q Querier, id string, keepRows []int32) error {
	if keepRows == nil {
		keepRows = []int32{} // a nil slice is sent as NULL and "NOT (id = ANY(NULL))" matches no row
	}
	_, err := q.Exec(ctx, "DELETE FROM secret_tags WHERE secret = $1 AND NOT (id = ANY($2))", id, keepRows)
	return err
}

// DeleteSecret removes the tags and the secret row.
func (s *Store) DeleteSecret(ctx context.Context, q Querier, id string) error {
	if _, err := q.Exec(ctx, "DELETE FROM secret_tags WHERE secret = $1", id); err != nil {
		return err
	}
	_, err := q.Exec(ctx, "DELETE FROM amphora_secrets WHERE secret_id = $1", id)
	return err
}

// ListParams describes a list request.
type ListParams struct {
	Filters  []model.TagFilter
	SortKey  string // empty: unsorted
	SortDesc bool
	Paged    bool // false: single page with everything
	Page     int  // zero based (only if Paged)
	PageSize int  // >= 1 (only if Paged)
}

const maxInt32 = 2147483647

type sqlBuilder struct {
	args []any
}

func (b *sqlBuilder) arg(v any) string {
	b.args = append(b.args, v)
	return "$" + strconv.Itoa(len(b.args))
}

func (b *sqlBuilder) where(filters []model.TagFilter) string {
	if len(filters) == 0 {
		return ""
	}
	var conds []string
	for _, f := range filters {
		c := "CAST(t.tag_key AS varchar) = " + b.arg(f.Key)
		switch f.Op {
		case model.OpEquals:
			c += " AND CAST(t.tag_value AS varchar) = " + b.arg(f.Value)
		case model.OpGreaterThan, model.OpLessThan:
			n, _ := strconv.ParseInt(f.Value, 10, 64)
			op := ">"
			if f.Op == model.OpLessThan {
				op = "<"
			}
			c += " AND CAST(t.tag_value AS int8) " + op + " " + b.arg(n)
		}
		conds = append(conds, "s.secret_id IN (SELECT t.secret FROM secret_tags t WHERE s.secret_id = t.secret AND "+c+")")
	}
	return " WHERE " + strings.Join(conds, " AND ")
}

// List returns a page of secrets with their tags.
func (s *Store) List(ctx context.Context, p ListParams) (model.MetadataPage, error) {
	var page model.MetadataPage
	sortType := ""
	if p.SortKey != "" {
		rows, err := s.Pool.Query(ctx, "SELECT tag_value_type FROM secret_tags WHERE tag_key = $1 GROUP BY tag_value_type LIMIT 2", p.SortKey)
		if err != nil {
			return page, err
		}
		var types []string
		for rows.Next() {
			var t *string
			if err := rows.Scan(&t); err != nil {
				rows.Close()
				return page, err
			}
			types = append(types, nz(t))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return page, err
		}
		if len(types) > 1 {
			return page, model.Internal("The TagValueType of the tags used for sorting have different configurations.")
		}
		if len(types) == 1 {
			sortType = types[0]
		}
	}

	var cb sqlBuilder
	countSQL := "SELECT count(s.secret_id) FROM amphora_secrets s" + cb.where(p.Filters)
	var total int64
	if err := s.Pool.QueryRow(ctx, countSQL, cb.args...).Scan(&total); err != nil {
		return page, err
	}

	var qb sqlBuilder
	sel := "SELECT s.secret_id"
	order := ""
	if sortType != "" {
		cast := "varchar"
		if sortType == "LONG" {
			cast = "int8"
		}
		sel += ", CAST((SELECT t2.tag_value FROM secret_tags t2 WHERE s.secret_id = t2.secret AND t2.tag_key = " + qb.arg(p.SortKey) + ") AS " + cast + ")"
		order = " ORDER BY 2 ASC"
		if p.SortDesc {
			order = " ORDER BY 2 DESC"
		}
	}
	sql := sel + " FROM amphora_secrets s" + qb.where(p.Filters) + order
	var offset int64
	size := int64(maxInt32)
	pageNo := int64(0)
	if p.Paged {
		size = int64(p.PageSize)
		pageNo = int64(p.Page)
	}
	offset = pageNo * size
	sql += " LIMIT " + qb.arg(size) + " OFFSET " + qb.arg(offset)

	rows, err := s.Pool.Query(ctx, sql, qb.args...)
	if err != nil {
		return page, err
	}
	var ids []string
	for rows.Next() {
		var id string
		dest := []any{&id}
		var extra any
		if sortType != "" {
			dest = append(dest, &extra)
		}
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return page, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return page, err
	}

	tagsBy := map[string][]model.Tag{}
	if len(ids) > 0 {
		trows, err := s.Pool.Query(ctx, "SELECT secret, tag_key, tag_value, tag_value_type FROM secret_tags WHERE secret = ANY($1) ORDER BY id", ids)
		if err != nil {
			return page, err
		}
		for trows.Next() {
			var sid string
			var k, v, t *string
			if err := trows.Scan(&sid, &k, &v, &t); err != nil {
				trows.Close()
				return page, err
			}
			tagsBy[sid] = append(tagsBy[sid], model.Tag{Key: nz(k), Value: nz(v), ValueType: model.TagValueType(nz(t))})
		}
		trows.Close()
		if err := trows.Err(); err != nil {
			return page, err
		}
	}
	content := make([]model.Metadata, 0, len(ids))
	for _, id := range ids {
		tags := tagsBy[id]
		if tags == nil {
			tags = []model.Tag{}
		}
		content = append(content, model.Metadata{SecretID: id, Tags: tags})
	}
	// Spring PageImpl total correction
	if len(content) > 0 && offset+size > total {
		total = offset + int64(len(content))
	}
	totalPages := int64(0)
	if size == 0 {
		totalPages = 1
	} else {
		totalPages = (total + size - 1) / size
	}
	return model.MetadataPage{Content: content, Number: int32(pageNo), Size: int32(size), TotalElements: total, TotalPages: int32(totalPages)}, nil
}
