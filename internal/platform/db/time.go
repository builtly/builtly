package db

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// Time is a UTC time that scans from both TIMESTAMPTZ (Postgres) and RFC3339 TEXT (SQLite).
type Time time.Time

// Now returns the current UTC time.
func Now() Time { return Time(time.Now().UTC()) }

// Time converts to time.Time.
func (t Time) Time() time.Time { return time.Time(t).UTC() }

// Scan implements sql.Scanner.
func (t *Time) Scan(src any) error {
	if src == nil {
		*t = Time{}
		return nil
	}
	switch v := src.(type) {
	case time.Time:
		*t = Time(v.UTC())
		return nil
	case string:
		return t.parse(v)
	case []byte:
		return t.parse(string(v))
	default:
		return fmt.Errorf("db: cannot scan %T into Time", src)
	}
}

func (t *Time) parse(s string) error {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, s); err == nil {
			*t = Time(parsed.UTC())
			return nil
		}
	}
	return fmt.Errorf("db: cannot parse time %q", s)
}

// Value implements driver.Valuer.
// Returns RFC3339Nano text so SQLite TEXT and Postgres TIMESTAMPTZ both accept it.
func (t Time) Value() (driver.Value, error) {
	tt := time.Time(t)
	if tt.IsZero() {
		return nil, nil
	}
	return tt.UTC().Format(time.RFC3339Nano), nil
}

// MarshalJSON encodes as RFC3339.
func (t Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(t).UTC())
}

// UnmarshalJSON decodes RFC3339.
func (t *Time) UnmarshalJSON(b []byte) error {
	var tt time.Time
	if err := json.Unmarshal(b, &tt); err != nil {
		return err
	}
	*t = Time(tt.UTC())
	return nil
}

// NullTime is an optional Time.
type NullTime struct {
	Time  Time
	Valid bool
}

// Scan implements sql.Scanner.
func (n *NullTime) Scan(src any) error {
	if src == nil {
		n.Time, n.Valid = Time{}, false
		return nil
	}
	n.Valid = true
	return n.Time.Scan(src)
}

// Value implements driver.Valuer.
func (n NullTime) Value() (driver.Value, error) {
	if !n.Valid {
		return nil, nil
	}
	return n.Time.Value()
}
