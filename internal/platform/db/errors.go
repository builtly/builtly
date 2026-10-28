package db

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	sqlite3 "modernc.org/sqlite"
	sqlite3lib "modernc.org/sqlite/lib"
)

// Sentinel / structured DB errors mapped from drivers.
var (
	ErrNotFound = errors.New("db: not found")
)

// ErrUniqueViolation is returned when a unique constraint is violated.
type ErrUniqueViolation struct {
	Constraint string
}

func (e ErrUniqueViolation) Error() string {
	if e.Constraint == "" {
		return "db: unique violation"
	}
	return "db: unique violation on " + e.Constraint
}

// ErrForeignKey is returned when a foreign key constraint fails.
var ErrForeignKey = errors.New("db: foreign key violation")

// MapError translates driver-specific errors into package errors.
func MapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrUniqueViolation{Constraint: pgErr.ConstraintName}
		case "23503":
			return ErrForeignKey
		}
	}

	var se *sqlite3.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case sqlite3lib.SQLITE_CONSTRAINT_UNIQUE, sqlite3lib.SQLITE_CONSTRAINT_PRIMARYKEY:
			return ErrUniqueViolation{Constraint: se.Error()}
		case sqlite3lib.SQLITE_CONSTRAINT_FOREIGNKEY:
			return ErrForeignKey
		case sqlite3lib.SQLITE_CONSTRAINT:
			msg := se.Error()
			if strings.Contains(strings.ToLower(msg), "unique") {
				return ErrUniqueViolation{Constraint: msg}
			}
			if strings.Contains(strings.ToLower(msg), "foreign key") {
				return ErrForeignKey
			}
		}
	}
	return err
}
