package slack

import "errors"

// ErrPermanent wraps errors that should not be retried and should move the
// outbox message to the dead-letter dir.
var ErrPermanent = errors.New("slack: permanent failure")

// ErrTransient wraps errors that should be retried on the next rescan.
var ErrTransient = errors.New("slack: transient failure")

// IsPermanent reports whether err is (or wraps) ErrPermanent.
func IsPermanent(err error) bool { return errors.Is(err, ErrPermanent) }

// IsTransient reports whether err is (or wraps) ErrTransient.
func IsTransient(err error) bool { return errors.Is(err, ErrTransient) }
