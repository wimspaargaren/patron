// Package sql provides a client with included tracing capabilities.
package sql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"regexp"
	"strconv"
	"time"

	"github.com/beatlabs/patron/trace"
	opentracing "github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	component = "sql"
	dbtype    = "RDBMS"
)

var opDurationMetrics *prometheus.HistogramVec

func init() {
	opDurationMetrics = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "client",
			Subsystem: "sql",
			Name:      "cmd_duration_seconds",
			Help:      "SQL commands completed by the client.",
		},
		[]string{"op", "success"},
	)
	prometheus.MustRegister(opDurationMetrics)
}

type connInfo struct {
	instance, user string
}

func (c *connInfo) startSpan(ctx context.Context, opName, stmt string, tags ...opentracing.Tag) (opentracing.Span, context.Context) {
	sp, ctx := opentracing.StartSpanFromContext(ctx, opName)
	ext.Component.Set(sp, component)
	ext.DBType.Set(sp, dbtype)
	ext.DBInstance.Set(sp, c.instance)
	ext.DBUser.Set(sp, c.user)
	ext.DBStatement.Set(sp, stmt)
	for _, t := range tags {
		sp.SetTag(t.Key, t.Value)
	}
	sp.SetTag(trace.VersionTag, trace.Version)
	return sp, ctx
}

// Conn represents a single database connection.
type Conn struct {
	connInfo
	conn *sql.Conn
}

// DSNInfo contains information extracted from a valid
// connection string. Additional parameters provided are discarded.
type DSNInfo struct {
	Driver   string
	DBName   string
	Address  string
	User     string
	Protocol string
}

// BeginTx starts a transaction.
func (c *Conn) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	op := "conn.BeginTx"
	sp, _ := c.startSpan(ctx, op, "")
	start := time.Now()
	tx, err := c.conn.BeginTx(ctx, opts)
	observeDuration(sp, start, op, err)
	if err != nil {
		return nil, err
	}

	return &Tx{tx: tx}, nil
}

// Close returns the connection to the connection pool.
func (c *Conn) Close(ctx context.Context) error {
	op := "conn.Close"
	sp, _ := c.startSpan(ctx, op, "")
	start := time.Now()
	err := c.conn.Close()
	observeDuration(sp, start, op, err)
	return err
}

// Exec executes a query without returning any rows.
func (c *Conn) Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	op := "conn.Exec"
	sp, _ := c.startSpan(ctx, op, query)
	start := time.Now()
	res, err := c.conn.ExecContext(ctx, query, args...)
	observeDuration(sp, start, op, err)
	return res, err
}

// Ping verifies the connection to the database is still alive.
func (c *Conn) Ping(ctx context.Context) error {
	op := "conn.Ping"
	sp, _ := c.startSpan(ctx, op, "")
	start := time.Now()
	err := c.conn.PingContext(ctx)
	observeDuration(sp, start, op, err)
	return err
}

// Prepare creates a prepared statement for later queries or executions.
func (c *Conn) Prepare(ctx context.Context, query string) (*Stmt, error) {
	op := "conn.Prepare"
	sp, _ := c.startSpan(ctx, op, query)
	start := time.Now()
	stmt, err := c.conn.PrepareContext(ctx, query)
	observeDuration(sp, start, op, err)
	if err != nil {
		return nil, err
	}
	return &Stmt{stmt: stmt}, nil
}

// Query executes a query that returns rows.
func (c *Conn) Query(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	op := "conn.Query"
	sp, _ := c.startSpan(ctx, op, query)
	start := time.Now()
	rows, err := c.conn.QueryContext(ctx, query, args...)
	observeDuration(sp, start, op, err)
	if err != nil {
		return nil, err
	}

	return rows, nil
}

// QueryRow executes a query that is expected to return at most one row.
func (c *Conn) QueryRow(ctx context.Context, query string, args ...interface{}) *sql.Row {
	op := "conn.QueryRow"
	sp, _ := c.startSpan(ctx, op, query)
	start := time.Now()
	row := c.conn.QueryRowContext(ctx, query, args...)
	observeDuration(sp, start, op, nil)
	return row
}

// DB contains the underlying db to be traced.
type DB struct {
	connInfo
	db *sql.DB
}

// Open opens a database.
func Open(driverName, dataSourceName string) (*DB, error) {
	db, err := sql.Open(driverName, dataSourceName)
	if err != nil {
		return nil, err
	}
	info := parseDSN(dataSourceName)

	return &DB{connInfo: connInfo{info.DBName, info.User}, db: db}, nil
}

// OpenDB opens a database.
func OpenDB(c driver.Connector) *DB {
	db := sql.OpenDB(c)
	return &DB{db: db}
}

// FromDB wraps an opened db. This allows to support libraries that provide
// *sql.DB like sqlmock.
func FromDB(db *sql.DB) *DB {
	return &DB{db: db}
}

// DB returns the underlying db. This is useful for SQL code that does not
// require tracing.
func (db *DB) DB() *sql.DB {
	return db.db
}

// BeginTx starts a transaction.
func (db *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	op := "db.BeginTx"
	sp, _ := db.startSpan(ctx, op, "")
	start := time.Now()
	tx, err := db.db.BeginTx(ctx, opts)
	observeDuration(sp, start, op, err)
	if err != nil {
		return nil, err
	}
	return &Tx{tx: tx, connInfo: connInfo{instance: db.instance, user: db.user}}, nil
}

// Close closes the database, releasing any open resources.
func (db *DB) Close(ctx context.Context) error {
	op := "db.Close"
	sp, _ := db.startSpan(ctx, op, "")
	start := time.Now()
	err := db.db.Close()
	observeDuration(sp, start, op, err)
	return err
}

// Conn returns a connection.
func (db *DB) Conn(ctx context.Context) (*Conn, error) {
	op := "db.Conn"
	sp, _ := db.startSpan(ctx, op, "")
	start := time.Now()
	conn, err := db.db.Conn(ctx)
	observeDuration(sp, start, op, err)
	if err != nil {
		return nil, err
	}

	return &Conn{conn: conn, connInfo: db.connInfo}, nil
}

// Driver returns the database's underlying driver.
func (db *DB) Driver(ctx context.Context) driver.Driver {
	op := "db.Driver"
	sp, _ := db.startSpan(ctx, op, "")
	start := time.Now()
	drv := db.db.Driver()
	observeDuration(sp, start, op, nil)
	return drv
}

// Exec executes a query without returning any rows.
func (db *DB) Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	op := "db.Exec"
	sp, _ := db.startSpan(ctx, op, query)
	start := time.Now()
	res, err := db.db.ExecContext(ctx, query, args...)
	observeDuration(sp, start, op, err)
	if err != nil {
		return nil, err
	}

	return res, nil
}

// Ping verifies a connection to the database is still alive, establishing a connection if necessary.
func (db *DB) Ping(ctx context.Context) error {
	op := "db.Ping"
	sp, _ := db.startSpan(ctx, op, "")
	start := time.Now()
	err := db.db.PingContext(ctx)
	observeDuration(sp, start, op, err)
	return err
}

// Prepare creates a prepared statement for later queries or executions.
func (db *DB) Prepare(ctx context.Context, query string) (*Stmt, error) {
	op := "db.Prepare"
	sp, _ := db.startSpan(ctx, op, query)
	start := time.Now()
	stmt, err := db.db.PrepareContext(ctx, query)
	observeDuration(sp, start, op, err)
	if err != nil {
		return nil, err
	}

	return &Stmt{stmt: stmt, connInfo: db.connInfo, query: query}, nil
}

// Query executes a query that returns rows.
func (db *DB) Query(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	op := "db.Query"
	sp, _ := db.startSpan(ctx, op, query)
	start := time.Now()
	rows, err := db.db.QueryContext(ctx, query, args...)
	observeDuration(sp, start, op, err)
	if err != nil {
		return nil, err
	}

	return rows, err
}

// QueryRow executes a query that is expected to return at most one row.
func (db *DB) QueryRow(ctx context.Context, query string, args ...interface{}) *sql.Row {
	op := "db.QueryRow"
	sp, _ := db.startSpan(ctx, op, query)
	start := time.Now()
	row := db.db.QueryRowContext(ctx, query, args...)
	observeDuration(sp, start, op, nil)
	return row
}

// SetConnMaxLifetime sets the maximum amount of time a connection may be reused.
func (db *DB) SetConnMaxLifetime(d time.Duration) {
	db.db.SetConnMaxLifetime(d)
}

// SetMaxIdleConns sets the maximum number of connections in the idle connection pool.
func (db *DB) SetMaxIdleConns(n int) {
	db.db.SetMaxIdleConns(n)
}

// SetMaxOpenConns sets the maximum number of open connections to the database.
func (db *DB) SetMaxOpenConns(n int) {
	db.db.SetMaxOpenConns(n)
}

// Stats returns database statistics.
func (db *DB) Stats(ctx context.Context) sql.DBStats {
	op := "db.Stats"
	sp, _ := db.startSpan(ctx, op, "")
	start := time.Now()
	stats := db.db.Stats()
	observeDuration(sp, start, op, nil)
	return stats
}

// Stmt is a prepared statement.
type Stmt struct {
	connInfo
	query string
	stmt  *sql.Stmt
}

// Close closes the statement.
func (s *Stmt) Close(ctx context.Context) error {
	op := "stmt.Close"
	sp, _ := s.startSpan(ctx, op, "")
	start := time.Now()
	err := s.stmt.Close()
	observeDuration(sp, start, op, err)
	return err
}

// Exec executes a prepared statement.
func (s *Stmt) Exec(ctx context.Context, args ...interface{}) (sql.Result, error) {
	op := "stmt.Exec"
	sp, _ := s.startSpan(ctx, op, s.query)
	start := time.Now()
	res, err := s.stmt.ExecContext(ctx, args...)
	observeDuration(sp, start, op, err)
	if err != nil {
		return nil, err
	}

	return res, nil
}

// Query executes a prepared query statement.
func (s *Stmt) Query(ctx context.Context, args ...interface{}) (*sql.Rows, error) {
	op := "stmt.Query"
	sp, _ := s.startSpan(ctx, op, s.query)
	start := time.Now()
	rows, err := s.stmt.QueryContext(ctx, args...)
	observeDuration(sp, start, op, err)
	if err != nil {
		return nil, err
	}

	return rows, nil
}

// QueryRow executes a prepared query statement.
func (s *Stmt) QueryRow(ctx context.Context, args ...interface{}) *sql.Row {
	op := "stmt.QueryRow"
	sp, _ := s.startSpan(ctx, op, s.query)
	start := time.Now()
	row := s.stmt.QueryRowContext(ctx, args...)
	observeDuration(sp, start, op, nil)
	return row
}

// Tx is an in-progress database transaction.
type Tx struct {
	connInfo
	tx *sql.Tx
}

// Commit commits the transaction.
func (tx *Tx) Commit(ctx context.Context) error {
	op := "tx.Commit"
	sp, _ := tx.startSpan(ctx, op, "")
	start := time.Now()
	err := tx.tx.Commit()
	observeDuration(sp, start, op, err)
	return err
}

// Exec executes a query that doesn't return rows.
func (tx *Tx) Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	op := "tx.Exec"
	sp, _ := tx.startSpan(ctx, op, query)
	start := time.Now()
	res, err := tx.tx.ExecContext(ctx, query, args...)
	observeDuration(sp, start, op, err)
	if err != nil {
		return nil, err
	}

	return res, nil
}

// Prepare creates a prepared statement for use within a transaction.
func (tx *Tx) Prepare(ctx context.Context, query string) (*Stmt, error) {
	op := "tx.Prepare"
	sp, _ := tx.startSpan(ctx, op, query)
	start := time.Now()
	stmt, err := tx.tx.PrepareContext(ctx, query)
	observeDuration(sp, start, op, err)
	if err != nil {
		return nil, err
	}

	return &Stmt{stmt: stmt}, nil
}

// Query executes a query that returns rows.
func (tx *Tx) Query(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	op := "tx.Query"
	sp, _ := tx.startSpan(ctx, op, query)
	start := time.Now()
	rows, err := tx.tx.QueryContext(ctx, query, args...)
	observeDuration(sp, start, op, err)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// QueryRow executes a query that is expected to return at most one row.
func (tx *Tx) QueryRow(ctx context.Context, query string, args ...interface{}) *sql.Row {
	op := "tx.QueryRow"
	sp, _ := tx.startSpan(ctx, op, query)
	start := time.Now()
	row := tx.tx.QueryRowContext(ctx, query, args...)
	observeDuration(sp, start, op, nil)
	return row
}

// Rollback aborts the transaction.
func (tx *Tx) Rollback(ctx context.Context) error {
	op := "tx.Rollback"
	sp, _ := tx.startSpan(ctx, op, "")
	start := time.Now()
	err := tx.tx.Rollback()
	observeDuration(sp, start, op, err)
	return err
}

// Stmt returns a transaction-specific prepared statement from an existing statement.
func (tx *Tx) Stmt(ctx context.Context, stmt *Stmt) *Stmt {
	op := "tx.Stmt"
	sp, _ := tx.startSpan(ctx, op, stmt.query)
	start := time.Now()
	st := &Stmt{stmt: tx.tx.StmtContext(ctx, stmt.stmt), connInfo: tx.connInfo, query: stmt.query}
	observeDuration(sp, start, op, nil)
	return st
}

func parseDSN(dsn string) DSNInfo {
	res := DSNInfo{}

	dsnPattern := regexp.MustCompile(
		`^(?P<driver>.*:\/\/)?(?:(?P<username>.*?)(?::(.*))?@)?` + // [driver://][user[:password]@]
			`(?:(?P<protocol>[^\(]*)(?:\((?P<address>[^\)]*)\))?)?` + // [net[(addr)]]
			`\/(?P<dbname>.*?)` + // /dbname
			`(?:\?(?P<params>[^\?]*))?$`) // [?param1=value1&paramN=valueN]

	matches := dsnPattern.FindStringSubmatch(dsn)
	fields := dsnPattern.SubexpNames()

	for i, match := range matches {
		switch fields[i] {
		case "driver":
			res.Driver = match
		case "username":
			res.User = match
		case "protocol":
			res.Protocol = match
		case "address":
			res.Address = match
		case "dbname":
			res.DBName = match
		}
	}

	return res
}

func observeDuration(span opentracing.Span, start time.Time, op string, err error) {
	trace.SpanComplete(span, err)
	opDurationMetrics.WithLabelValues(op, strconv.FormatBool(err != nil)).Observe(time.Since(start).Seconds())
}
