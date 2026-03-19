package coreutils

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	strftime "github.com/ncruces/go-strftime"

	"github.com/richardartoul/swarmd/pkg/sh/expand"
	"github.com/richardartoul/swarmd/pkg/sh/internal"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

const dateDefaultFormat = "%a %b %e %H:%M:%S %Z %Y"

var dateNow = time.Now

func runDate(env *commandEnv, args []string) error {
	forceUTC, operands, err := parseDateArguments(args)
	if err != nil {
		return err
	}
	if len(operands) > 1 {
		return usageError(env, 1, "usage: date [-u] [+format]")
	}
	format := dateDefaultFormat
	if len(operands) == 1 {
		if !strings.HasPrefix(operands[0], "+") {
			return usageError(env, 1, "usage: date [-u] [+format]")
		}
		format = operands[0][1:]
	}

	current, err := resolveDateTime(dateNow(), env.hc.Env, forceUTC)
	if err != nil {
		if _, writeErr := fmt.Fprintf(env.stderr(), "date: %v\n", err); writeErr != nil {
			return writeErr
		}
		return interp.ExitStatus(1)
	}
	_, err = fmt.Fprintln(env.stdout(), formatPOSIXDate(current, format))
	return err
}

func parseDateArguments(args []string) (bool, []string, error) {
	forceUTC := false
	var operands []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			operands = append(operands, args[i+1:]...)
			return forceUTC, operands, nil
		case strings.HasPrefix(arg, "+"):
			operands = append(operands, args[i:]...)
			return forceUTC, operands, nil
		case len(arg) >= 2 && arg[0] == '-':
			if strings.HasPrefix(arg, "--") {
				return false, nil, formatUtilityOptionError(&internal.UnknownOptionError{Option: arg})
			}
			for j := 1; j < len(arg); j++ {
				if arg[j] != 'u' {
					return false, nil, formatUtilityOptionError(&internal.UnknownOptionError{Option: "-" + arg[j:j+1]})
				}
				forceUTC = true
			}
		default:
			operands = append(operands, args[i:]...)
			return forceUTC, operands, nil
		}
	}
	return forceUTC, operands, nil
}

func resolveDateTime(now time.Time, env expand.Environ, forceUTC bool) (time.Time, error) {
	location, err := resolveDateLocation(now, env, forceUTC)
	if err != nil {
		return time.Time{}, err
	}
	return now.In(location), nil
}

func resolveDateLocation(now time.Time, env expand.Environ, forceUTC bool) (*time.Location, error) {
	if forceUTC {
		return time.FixedZone("UTC", 0), nil
	}
	tz := env.Get("TZ")
	if !tz.IsSet() || tz.String() == "" || tz.Kind != expand.String {
		return time.Local, nil
	}

	raw := tz.String()
	trimmed := strings.TrimPrefix(raw, ":")
	if trimmed == "" {
		return time.Local, nil
	}
	if location, err := time.LoadLocation(trimmed); err == nil {
		return location, nil
	}
	if name, offset, ok := lookupPOSIXTZ(trimmed, now); ok {
		return time.FixedZone(name, offset), nil
	}
	return nil, fmt.Errorf("invalid time zone %q", raw)
}

// formatPOSIXDate intentionally applies C/POSIX locale semantics only.
// A future locale layer can replace this formatter when LC_TIME support expands.
func formatPOSIXDate(t time.Time, format string) string {
	buf := make([]byte, 0, len(format)+16)
	for i := 0; i < len(format); {
		if format[i] != '%' {
			buf = append(buf, format[i])
			i++
			continue
		}

		start := i
		i++
		if i >= len(format) {
			buf = append(buf, '%')
			break
		}

		flag := byte(0)
		width := -1
		switch format[i] {
		case '+':
			if i+1 < len(format) && isASCIIDigit(format[i+1]) {
				flag = format[i]
				i++
				widthStart := i
				for i < len(format) && isASCIIDigit(format[i]) {
					i++
				}
				width, _ = strconv.Atoi(format[widthStart:i])
			}
		case '0':
			if i+1 < len(format) && isASCIIDigit(format[i+1]) {
				flag = format[i]
				i++
				widthStart := i
				for i < len(format) && isASCIIDigit(format[i]) {
					i++
				}
				width, _ = strconv.Atoi(format[widthStart:i])
			}
		case '-', ':':
			flag = format[i]
			i++
			if i < len(format) && isASCIIDigit(format[i]) {
				for i < len(format) && isASCIIDigit(format[i]) {
					i++
				}
				if i < len(format) {
					i++
				}
				buf = append(buf, format[start:i]...)
				continue
			}
		}

		modifier := byte(0)
		if i < len(format) && (format[i] == 'E' || format[i] == 'O') {
			modifier = format[i]
			i++
		}
		if i >= len(format) {
			buf = append(buf, format[start:]...)
			break
		}

		spec := format[i]
		i++
		if width >= 0 {
			if modifier != 0 {
				buf = append(buf, format[start:i]...)
				continue
			}
			if appendPOSIXWidthDirective(&buf, flag, width, spec, t) {
				continue
			}
			buf = append(buf, format[start:i]...)
			continue
		}

		if modifier != 0 {
			if !dateModifierAllowed(modifier, spec) {
				buf = append(buf, format[start:i]...)
				continue
			}
		}

		switch {
		case spec == '+':
			buf = strftime.AppendFormat(buf, "%+", t)
		case flag == 0:
			buf = strftime.AppendFormat(buf, "%"+string(spec), t)
		case flag == '-' || flag == ':':
			buf = strftime.AppendFormat(buf, "%"+string(flag)+string(spec), t)
		default:
			buf = append(buf, format[start:i]...)
		}
	}
	return string(buf)
}

func appendPOSIXWidthDirective(dst *[]byte, flag byte, width int, spec byte, t time.Time) bool {
	if flag != '0' && flag != '+' {
		return false
	}
	switch spec {
	case 'C':
		*dst = append(*dst, formatDateWideCentury(t.Year(), flag, width)...)
	case 'Y':
		*dst = append(*dst, formatDateWideYear(t.Year(), flag, width)...)
	case 'G':
		isoYear, _ := t.ISOWeek()
		*dst = append(*dst, formatDateWideYear(isoYear, flag, width)...)
	case 'F':
		if width < 6 {
			width = 6
		}
		*dst = append(*dst, formatDateWideYear(t.Year(), flag, width-6)...)
		*dst = append(*dst, '-')
		*dst = append(*dst, smallDecimal2(int(t.Month()))...)
		*dst = append(*dst, '-')
		*dst = append(*dst, smallDecimal2(t.Day())...)
	default:
		return false
	}
	return true
}

func formatDateWideCentury(year int, flag byte, width int) string {
	return formatPOSIXWideInt(year/100, flag, width, 2)
}

func formatDateWideYear(year int, flag byte, width int) string {
	return formatPOSIXWideInt(year, flag, width, 4)
}

func formatPOSIXWideInt(value int, flag byte, width int, signThreshold int) string {
	if width < 0 {
		width = 0
	}
	sign := ""
	magnitude := int64(value)
	if magnitude < 0 {
		sign = "-"
		magnitude = -magnitude
	}
	digits := strconv.FormatInt(magnitude, 10)
	if sign == "" && flag == '+' && (width > signThreshold || len(digits) > signThreshold) {
		sign = "+"
	}
	padWidth := width - len(sign)
	if padWidth < len(digits) {
		padWidth = len(digits)
	}
	return sign + strings.Repeat("0", padWidth-len(digits)) + digits
}

func smallDecimal2(value int) string {
	if value >= 0 && value < 10 {
		return "0" + strconv.Itoa(value)
	}
	if value >= 10 && value < 100 {
		return strconv.Itoa(value)
	}
	return fmt.Sprintf("%02d", value)
}

func dateModifierAllowed(modifier, spec byte) bool {
	switch modifier {
	case 'E':
		return strings.ContainsRune("cCxXyY", rune(spec))
	case 'O':
		return strings.ContainsRune("bBdeHImMSuUVwWy", rune(spec))
	default:
		return false
	}
}

func isASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func lookupPOSIXTZ(spec string, now time.Time) (string, int, bool) {
	stdName, rest, ok := parsePOSIXTZName(spec)
	if !ok {
		return "", 0, false
	}
	stdOffset, rest, ok := parsePOSIXTZOffset(rest)
	if !ok {
		return "", 0, false
	}
	stdOffset = -stdOffset
	if len(rest) == 0 || rest[0] == ',' {
		return stdName, stdOffset, true
	}

	dstName, rest, ok := parsePOSIXTZName(rest)
	if !ok {
		return "", 0, false
	}
	dstOffset := stdOffset + secondsPerHour
	if len(rest) > 0 && rest[0] != ',' {
		dstOffset, rest, ok = parsePOSIXTZOffset(rest)
		if !ok {
			return "", 0, false
		}
		dstOffset = -dstOffset
	}
	if len(rest) == 0 {
		rest = ",M3.2.0,M11.1.0"
	}
	if rest[0] != ',' && rest[0] != ';' {
		return "", 0, false
	}
	rest = rest[1:]

	startRule, rest, ok := parsePOSIXTZRule(rest)
	if !ok || len(rest) == 0 || rest[0] != ',' {
		return "", 0, false
	}
	endRule, rest, ok := parsePOSIXTZRule(rest[1:])
	if !ok || len(rest) > 0 {
		return "", 0, false
	}

	utcNow := now.UTC()
	year := utcNow.Year()
	yearStart := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	ysec := int(utcNow.Sub(yearStart) / time.Second)
	startSec := posixTZRuleTime(year, startRule, stdOffset)
	endSec := posixTZRuleTime(year, endRule, dstOffset)
	dstIsDST, stdIsDST := true, false
	if endSec < startSec {
		startSec, endSec = endSec, startSec
		stdName, dstName = dstName, stdName
		stdOffset, dstOffset = dstOffset, stdOffset
		stdIsDST, dstIsDST = dstIsDST, stdIsDST
	}
	if ysec < startSec || ysec >= endSec {
		_ = stdIsDST
		return stdName, stdOffset, true
	}
	_ = dstIsDST
	return dstName, dstOffset, true
}

func parsePOSIXTZName(spec string) (string, string, bool) {
	if spec == "" {
		return "", "", false
	}
	if spec[0] != '<' {
		for i, r := range spec {
			switch r {
			case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', ',', '-', '+':
				if i < 3 {
					return "", "", false
				}
				return spec[:i], spec[i:], true
			}
		}
		if len(spec) < 3 {
			return "", "", false
		}
		return spec, "", true
	}
	for i := 1; i < len(spec); i++ {
		if spec[i] == '>' {
			return spec[1:i], spec[i+1:], true
		}
	}
	return "", "", false
}

func parsePOSIXTZOffset(spec string) (int, string, bool) {
	if spec == "" {
		return 0, "", false
	}
	neg := false
	switch spec[0] {
	case '+':
		spec = spec[1:]
	case '-':
		spec = spec[1:]
		neg = true
	}

	hours, rest, ok := parsePOSIXTZNum(spec, 0, 24*7)
	if !ok {
		return 0, "", false
	}
	offset := hours * secondsPerHour
	if len(rest) == 0 || rest[0] != ':' {
		if neg {
			offset = -offset
		}
		return offset, rest, true
	}

	minutes, rest, ok := parsePOSIXTZNum(rest[1:], 0, 59)
	if !ok {
		return 0, "", false
	}
	offset += minutes * secondsPerMinute
	if len(rest) == 0 || rest[0] != ':' {
		if neg {
			offset = -offset
		}
		return offset, rest, true
	}

	seconds, rest, ok := parsePOSIXTZNum(rest[1:], 0, 59)
	if !ok {
		return 0, "", false
	}
	offset += seconds
	if neg {
		offset = -offset
	}
	return offset, rest, true
}

type posixTZRuleKind uint8

const (
	posixTZRuleJulian posixTZRuleKind = iota
	posixTZRuleDayOfYear
	posixTZRuleMonthWeekDay
)

type posixTZRule struct {
	kind posixTZRuleKind
	day  int
	week int
	mon  int
	time int
}

func parsePOSIXTZRule(spec string) (posixTZRule, string, bool) {
	if spec == "" {
		return posixTZRule{}, "", false
	}
	var (
		rule posixTZRule
		ok   bool
	)
	switch spec[0] {
	case 'J':
		rule.day, spec, ok = parsePOSIXTZNum(spec[1:], 1, 365)
		if !ok {
			return posixTZRule{}, "", false
		}
		rule.kind = posixTZRuleJulian
	case 'M':
		rule.mon, spec, ok = parsePOSIXTZNum(spec[1:], 1, 12)
		if !ok || spec == "" || spec[0] != '.' {
			return posixTZRule{}, "", false
		}
		rule.week, spec, ok = parsePOSIXTZNum(spec[1:], 1, 5)
		if !ok || spec == "" || spec[0] != '.' {
			return posixTZRule{}, "", false
		}
		rule.day, spec, ok = parsePOSIXTZNum(spec[1:], 0, 6)
		if !ok {
			return posixTZRule{}, "", false
		}
		rule.kind = posixTZRuleMonthWeekDay
	default:
		rule.day, spec, ok = parsePOSIXTZNum(spec, 0, 365)
		if !ok {
			return posixTZRule{}, "", false
		}
		rule.kind = posixTZRuleDayOfYear
	}

	if spec == "" || spec[0] != '/' {
		rule.time = 2 * secondsPerHour
		return rule, spec, true
	}
	offset, rest, ok := parsePOSIXTZOffset(spec[1:])
	if !ok {
		return posixTZRule{}, "", false
	}
	rule.time = offset
	return rule, rest, true
}

func parsePOSIXTZNum(spec string, minValue, maxValue int) (int, string, bool) {
	if spec == "" {
		return 0, "", false
	}
	value := 0
	for i := 0; i < len(spec); i++ {
		if !isASCIIDigit(spec[i]) {
			if i == 0 || value < minValue {
				return 0, "", false
			}
			return value, spec[i:], true
		}
		value *= 10
		value += int(spec[i] - '0')
		if value > maxValue {
			return 0, "", false
		}
	}
	if value < minValue {
		return 0, "", false
	}
	return value, "", true
}

func posixTZRuleTime(year int, rule posixTZRule, offset int) int {
	var seconds int
	switch rule.kind {
	case posixTZRuleJulian:
		seconds = (rule.day - 1) * secondsPerDay
		if isLeapYear(year) && rule.day >= 60 {
			seconds += secondsPerDay
		}
	case posixTZRuleDayOfYear:
		seconds = rule.day * secondsPerDay
	case posixTZRuleMonthWeekDay:
		monthFirstWeekday := int(time.Date(year, time.Month(rule.mon), 1, 0, 0, 0, 0, time.UTC).Weekday())
		day := rule.day - monthFirstWeekday
		if day < 0 {
			day += 7
		}
		daysInMonth := daysInMonth(time.Month(rule.mon), year)
		for week := 1; week < rule.week; week++ {
			if day+7 >= daysInMonth {
				break
			}
			day += 7
		}
		seconds = (daysBeforeMonth(time.Month(rule.mon), year) + day) * secondsPerDay
	}
	return seconds + rule.time - offset
}

func daysBeforeMonth(month time.Month, year int) int {
	days := 0
	for current := time.January; current < month; current++ {
		days += daysInMonth(current, year)
	}
	return days
}

func daysInMonth(month time.Month, year int) int {
	switch month {
	case time.April, time.June, time.September, time.November:
		return 30
	case time.February:
		if isLeapYear(year) {
			return 29
		}
		return 28
	default:
		return 31
	}
}

func isLeapYear(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}

const (
	secondsPerMinute = 60
	secondsPerHour   = 60 * secondsPerMinute
	secondsPerDay    = 24 * secondsPerHour
)
