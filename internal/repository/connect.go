// Package repository is the GORM implementation of the internal/db contracts. Only
// cmd/server, the composition root, imports it; everything else depends on the db
// interfaces.
package repository

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Pool is how Connect sizes and logs its connection pool.
type Pool struct {
	MaxOpenConns int
	Verbose      bool
}

// Connect opens the Postgres pool the repositories run on.
func Connect(dsn string, pool Pool) (*gorm.DB, error) {
	conn, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: newGormLogger(slog.Default(), pool.Verbose),
	})
	if err != nil {
		return nil, fmt.Errorf("connect to the database: %w", err)
	}

	sqlDB, err := conn.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}

	sqlDB.SetMaxOpenConns(pool.MaxOpenConns)
	sqlDB.SetMaxIdleConns(min(10, pool.MaxOpenConns))
	sqlDB.SetConnMaxLifetime(time.Hour)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)

	return conn, nil
}

const slowQueryThreshold = 200 * time.Millisecond

// gormLogger logs slow queries, and every query when verbose, but no failure outside
// verbose mode: every failed query is returned to a caller that logs it once, with
// the context it has, and records it on its span. GORM's own logger also wrote each
// at ERROR, a closed session's context.Canceled included - three records per error.
type gormLogger struct {
	// GORM's own messages (the migrator's, a callback's) still go through its logger.
	logger.Interface

	log     *slog.Logger
	verbose bool
}

func newGormLogger(log *slog.Logger, verbose bool) gormLogger {
	level := logger.Warn
	if verbose {
		level = logger.Info
	}
	return gormLogger{
		Interface: logger.NewSlogLogger(log, logger.Config{LogLevel: level, ParameterizedQueries: true}),
		log:       log,
		verbose:   verbose,
	}
}

// LogMode is what db.Debug() and db.Session(&gorm.Session{Logger: ...}) call.
func (l gormLogger) LogMode(level logger.LogLevel) logger.Interface {
	l.Interface = l.Interface.LogMode(level)
	l.verbose = level >= logger.Info
	return l
}

// Trace logs one executed statement.
func (l gormLogger) Trace(ctx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error) {
	elapsed := time.Since(begin)
	level, msg := slog.LevelInfo, "query"
	switch {
	case err == nil && elapsed > slowQueryThreshold:
		level, msg = slog.LevelWarn, "slow query"
	case !l.verbose:
		return
	}
	if !l.log.Enabled(ctx, level) {
		return
	}
	sql, rows := fc()
	attrs := []slog.Attr{
		slog.Float64("duration_seconds", elapsed.Seconds()),
		slog.String("sql", sql),
		slog.Int64("rows", rows),
	}
	if err != nil {
		attrs = append(attrs, slog.String("error", err.Error()))
	}
	l.log.LogAttrs(ctx, level, msg, attrs...)
}

// ParamsFilter keeps bound values out of the logged SQL: GORM inlines them otherwise,
// and they include key fingerprints and usernames.
func (gormLogger) ParamsFilter(_ context.Context, sql string, _ ...any) (string, []any) {
	return sql, nil
}
