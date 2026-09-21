package tracker

import (
	"context"
	"time"
)

// Content is one current provider observation of a tracker item's readable
// body. Title, Body and URL retain the provider's own text and addressing for
// operator output. State is the same live observation StateReader returns, so
// a caller that reads content does not need a second provider round trip to
// learn the lifecycle class. UpdatedAt is the provider's last-modified time;
// the zero time means the provider did not report one.
type Content struct {
	Ref       string
	Title     string
	Body      string
	URL       string
	State     LiveState
	UpdatedAt time.Time
}

// ContentReader reads the current provider content of one tracker reference.
//
// It is optional, exactly as StateReader is: callers type-assert a Seam to
// ContentReader and a seam that does not implement it is not an error, it is
// the absence of the capability. The read verb reports that absence as
// unsupported for the configured backend rather than failing the command, and
// an error returned from ReadContent stays a real read failure for that one
// reference.
type ContentReader interface {
	ReadContent(context.Context, string) (Content, error)
}
