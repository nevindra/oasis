package oasis

import (
	"time"

	"github.com/google/uuid"
)

// NewID generates a globally unique, time-sortable UUIDv7 (RFC 9562).
func NewID() string {
	return uuid.Must(uuid.NewV7()).String()
}

// NowUnix returns current time as Unix seconds.
func NowUnix() int64 {
	return time.Now().Unix()
}
