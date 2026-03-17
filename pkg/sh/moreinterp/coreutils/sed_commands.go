package coreutils

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type sedCommandKind uint8

const (
	sedCommandSubstitute sedCommandKind = iota
	sedCommandPrint
	sedCommandDelete
)

type sedAddressKind uint8

const (
	sedAddressNone sedAddressKind = iota
	sedAddressLine
	sedAddressLast
	sedAddressRegexp
)

type sedAddress struct {
	kind  sedAddressKind
	line  int
	regex *regexp.Regexp
}

type sedCommand struct {
	start        sedAddress
	end          sedAddress
	kind         sedCommandKind
	substitution sedSubstitution
}

func parseSedProgram(scripts []string) ([]sedCommand, error) {
	commands := make([]sedCommand, 0, len(scripts))
	for _, script := range scripts {
		parsed, err := parseSedScript(strings.TrimSpace(script))
		if err != nil {
			return nil, err
		}
		commands = append(commands, parsed...)
	}
	return commands, nil
}

func parseSedScript(script string) ([]sedCommand, error) {
	if script == "" {
		return nil, fmt.Errorf("sed: empty script")
	}
	var commands []sedCommand
	for i := 0; i < len(script); {
		i = skipSedCommandSeparators(script, i)
		if i >= len(script) {
			break
		}
		command, next, err := parseSedCommand(script, i)
		if err != nil {
			return nil, err
		}
		commands = append(commands, command)
		i = skipSedInlineSpace(script, next)
		if i >= len(script) {
			break
		}
		switch script[i] {
		case ';', '\n':
			i++
		default:
			return nil, fmt.Errorf("sed: unsupported script %q", script)
		}
	}
	return commands, nil
}

func skipSedCommandSeparators(script string, start int) int {
	for start < len(script) {
		if script[start] == ';' || unicode.IsSpace(rune(script[start])) {
			start++
			continue
		}
		break
	}
	return start
}

func skipSedInlineSpace(script string, start int) int {
	for start < len(script) && script[start] != '\n' && unicode.IsSpace(rune(script[start])) {
		start++
	}
	return start
}

func parseSedCommand(script string, start int) (sedCommand, int, error) {
	var command sedCommand
	i := start

	addr, next, ok, err := parseSedAddress(script, i)
	if err != nil {
		return sedCommand{}, 0, err
	}
	if ok {
		command.start = addr
		i = skipSedInlineSpace(script, next)
		if i < len(script) && script[i] == ',' {
			i++
			i = skipSedInlineSpace(script, i)
			addr, next, ok, err = parseSedAddress(script, i)
			if err != nil {
				return sedCommand{}, 0, err
			}
			if !ok {
				return sedCommand{}, 0, fmt.Errorf("sed: missing second address in %q", script[start:])
			}
			command.end = addr
			i = next
		} else {
			i = next
		}
	}

	i = skipSedInlineSpace(script, i)
	if i >= len(script) {
		return sedCommand{}, 0, fmt.Errorf("sed: unsupported script %q", script[start:])
	}

	switch script[i] {
	case 'p':
		command.kind = sedCommandPrint
		i++
	case 'd':
		command.kind = sedCommandDelete
		i++
	case 's':
		command.kind = sedCommandSubstitute
		substitution, next, err := parseSedSubstitutionAt(script, i)
		if err != nil {
			return sedCommand{}, 0, err
		}
		command.substitution = substitution
		i = next
	default:
		return sedCommand{}, 0, fmt.Errorf("sed: unsupported script %q", script[start:])
	}

	return command, i, nil
}

func parseSedAddress(script string, start int) (sedAddress, int, bool, error) {
	if start >= len(script) {
		return sedAddress{}, start, false, nil
	}
	switch script[start] {
	case '$':
		return sedAddress{kind: sedAddressLast}, start + 1, true, nil
	case '/':
		pattern, next, err := parseSedSegment(script, start+1, '/')
		if err != nil {
			return sedAddress{}, 0, false, err
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return sedAddress{}, 0, false, fmt.Errorf("sed: %w", err)
		}
		return sedAddress{kind: sedAddressRegexp, regex: re}, next, true, nil
	}

	if script[start] < '0' || script[start] > '9' {
		return sedAddress{}, start, false, nil
	}
	next := start + 1
	for next < len(script) && script[next] >= '0' && script[next] <= '9' {
		next++
	}
	line, err := strconv.Atoi(script[start:next])
	if err != nil || line < 1 {
		return sedAddress{}, 0, false, fmt.Errorf("sed: invalid line address %q", script[start:next])
	}
	return sedAddress{kind: sedAddressLine, line: line}, next, true, nil
}

func parseSedSubstitutionAt(script string, start int) (sedSubstitution, int, error) {
	if start >= len(script) || script[start] != 's' {
		return sedSubstitution{}, 0, fmt.Errorf("sed: unsupported script %q", script[start:])
	}
	if start+1 >= len(script) {
		return sedSubstitution{}, 0, fmt.Errorf("sed: unsupported script %q", script[start:])
	}
	separator := script[start+1]
	pattern, next, err := parseSedSegment(script, start+2, separator)
	if err != nil {
		return sedSubstitution{}, 0, err
	}
	replacement, next, err := parseSedSegment(script, next, separator)
	if err != nil {
		return sedSubstitution{}, 0, err
	}

	substitution := sedSubstitution{
		replacement: translateSedReplacement(replacement),
	}
	for next < len(script) {
		ch := script[next]
		if ch == ';' || ch == '\n' {
			break
		}
		if unicode.IsSpace(rune(ch)) {
			next++
			continue
		}
		switch ch {
		case 'g':
			substitution.global = true
		case 'p':
			substitution.print = true
		default:
			return sedSubstitution{}, 0, fmt.Errorf("sed: unsupported substitute flag %q", string(ch))
		}
		next++
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return sedSubstitution{}, 0, fmt.Errorf("sed: %w", err)
	}
	substitution.regex = re
	return substitution, next, nil
}

func runSedProgram(w io.Writer, reader io.Reader, commands []sedCommand, suppressDefaultPrint bool) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	lines := splitLines(data)
	rangeState := make([]bool, len(commands))

	for i, rawLine := range lines {
		current, newline := splitTrailingNewline(string(rawLine))
		lineNumber := i + 1
		isLast := i == len(lines)-1
		deleted := false

	commands:
		for j, command := range commands {
			applies, inRange := command.matches(rangeState[j], current, lineNumber, isLast)
			rangeState[j] = inRange
			if !applies {
				continue
			}
			switch command.kind {
			case sedCommandSubstitute:
				var changed bool
				current, changed = applySedSubstitution(command.substitution, current)
				if changed && command.substitution.print {
					if _, err := io.WriteString(w, current+newline); err != nil {
						return err
					}
				}
			case sedCommandPrint:
				if _, err := io.WriteString(w, current+newline); err != nil {
					return err
				}
			case sedCommandDelete:
				deleted = true
				break commands
			}
		}

		if !deleted && !suppressDefaultPrint {
			if _, err := io.WriteString(w, current+newline); err != nil {
				return err
			}
		}
	}
	return nil
}

func (command sedCommand) matches(inRange bool, current string, lineNumber int, isLast bool) (bool, bool) {
	if command.start.kind == sedAddressNone {
		return true, false
	}
	if command.end.kind == sedAddressNone {
		return command.start.matches(current, lineNumber, isLast), false
	}
	if inRange {
		if command.end.matches(current, lineNumber, isLast) {
			return true, false
		}
		return true, true
	}
	if !command.start.matches(current, lineNumber, isLast) {
		return false, false
	}
	if command.end.kind == sedAddressLine && command.end.line <= lineNumber {
		return true, false
	}
	if command.end.kind == sedAddressRegexp {
		return true, true
	}
	if command.end.matches(current, lineNumber, isLast) {
		return true, false
	}
	return true, true
}

func (address sedAddress) matches(current string, lineNumber int, isLast bool) bool {
	switch address.kind {
	case sedAddressLine:
		return lineNumber == address.line
	case sedAddressLast:
		return isLast
	case sedAddressRegexp:
		return address.regex.MatchString(current)
	default:
		return false
	}
}
