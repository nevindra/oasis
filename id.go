package oasis

import (
	"time"

	"github.com/rs/xid"
)

// NewID generates a globally unique, time-sortable ID (20 chars, base32).
func NewID() string {
	return xid.New().String()
}

// NowUnix returns current time as Unix seconds.
func NowUnix() int64 {
	return time.Now().Unix()
}
