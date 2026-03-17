package common

import "regexp"

var safeIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func IsSafeIdentifier(value string) bool {
	return safeIdentifierPattern.MatchString(value)
}
