package repository

import (
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
	// Verbose logs every statement; otherwise only warnings and slow queries are.
	Verbose bool
}

// Connect opens the Postgres pool the repositories run on.
func Connect(dsn string, pool Pool) (*gorm.DB, error) {
	logMode := logger.Warn
	if pool.Verbose {
		logMode = logger.Info
	}

	conn, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.NewSlogLogger(slog.Default(), logger.Config{
			LogLevel:                  logMode,
			SlowThreshold:             200 * time.Millisecond,
			IgnoreRecordNotFoundError: true,
			ParameterizedQueries:      true,
		}),
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
