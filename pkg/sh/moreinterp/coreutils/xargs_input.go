package coreutils

import (
	"fmt"
	"strings"
	"unicode"
)

func splitXargsInput(input string) ([]string, error) {
	var (
		args         []string
		current      strings.Builder
		inSingle     bool
		inDouble     bool
		escaped      bool
		tokenStarted bool
	)
	flush := func() {
		if tokenStarted {
			args = append(args, current.String())
			current.Reset()
			tokenStarted = false
		}
	}

	for _, r := range input {
		switch {
		case inSingle:
			if r == '\'' {
				inSingle = false
			} else {
				current.WriteRune(r)
			}
			tokenStarted = true
		case inDouble:
			if escaped {
				switch r {
				case '"', '\\':
					current.WriteRune(r)
				case '\n':
					// line continuation inside double quotes
				default:
					current.WriteRune('\\')
					current.WriteRune(r)
				}
				escaped = false
				tokenStarted = true
				continue
			}
			switch r {
			case '"':
				inDouble = false
			case '\\':
				escaped = true
			default:
				current.WriteRune(r)
			}
			tokenStarted = true
		case escaped:
			if r != '\n' {
				current.WriteRune(r)
			}
			escaped = false
			tokenStarted = true
		case unicode.IsSpace(r):
			flush()
		case r == '\'':
			inSingle = true
			tokenStarted = true
		case r == '"':
			inDouble = true
			tokenStarted = true
		case r == '\\':
			escaped = true
			tokenStarted = true
		default:
			current.WriteRune(r)
			tokenStarted = true
		}
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("xargs: unmatched quote")
	}
	if escaped {
		return nil, fmt.Errorf("xargs: trailing escape")
	}
	flush()
	return args, nil
}
