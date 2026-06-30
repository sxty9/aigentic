package secret

import (
	"errors"
	"regexp"
)

// ErrBadSubject means a Subject can't be turned into a safe path segment.
var ErrBadSubject = errors.New("invalid subject")

// subjectRe matches a holistic/Linux username used as a single path segment: must start with
// an alphanumeric, then [A-Za-z0-9_-], length 1..64. No dots, slashes, or "." / ".." — so it
// can never traverse out of the per-user credential root.
var subjectRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// SafeSubject validates a server-stamped Subject (the holistic username) for use as a
// directory name under the per-user credential root. Subjects are already server-authoritative
// and tame, but credentials-on-disk demand defense in depth.
func SafeSubject(subject string) (string, error) {
	if !subjectRe.MatchString(subject) {
		return "", ErrBadSubject
	}
	return subject, nil
}
