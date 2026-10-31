package image

import "fmt"

// Client-visible reasons a resize failed. Detail stays in Failure.Err and goes
// to logs only.
const (
	ReasonFetch    = "fetch failed"
	ReasonInvalid  = "invalid image"
	ReasonTooLarge = "image too large"
)

// Failure is the error returned for every failed resize. Permanent failures
// won't succeed on retry; the others might.
type Failure struct {
	Reason    string
	Permanent bool
	Err       error
}

func (f *Failure) Error() string {
	if f.Err == nil {
		return f.Reason
	}

	return fmt.Sprintf("%s: %v", f.Reason, f.Err)
}

func (f *Failure) Unwrap() error {
	return f.Err
}

func permanent(reason string, err error) *Failure {
	return &Failure{Reason: reason, Permanent: true, Err: err}
}

func transient(reason string, err error) *Failure {
	return &Failure{Reason: reason, Err: err}
}
