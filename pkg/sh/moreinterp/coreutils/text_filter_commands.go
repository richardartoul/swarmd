package coreutils

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	diffpkg "github.com/rogpeppe/go-internal/diff"

	"github.com/richardartoul/swarmd/pkg/sh/internal"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

func runSort(env *commandEnv, args []string) error {
	reverse := false
	unique := false
	numeric := false
	ignoreCase := false
	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "r", Names: []string{"-r"}},
		{Canonical: "u", Names: []string{"-u"}},
		{Canonical: "n", Names: []string{"-n"}},
		{Canonical: "f", Names: []string{"-f"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "r":
			reverse = true
		case "u":
			unique = true
		case "n":
			numeric = true
		case "f":
			ignoreCase = true
		}
	}

	lines := make([]string, 0)
	if err := eachCommandLine(env, operands, func(line, _ string) error {
		lines = append(lines, line)
		return nil
	}); err != nil {
		return err
	}

	sort.SliceStable(lines, func(i, j int) bool {
		compare := compareSortLines(lines[i], lines[j], numeric, ignoreCase)
		if reverse {
			return compare > 0
		}
		return compare < 0
	})
	if unique {
		lines = uniqueSortLines(lines, numeric, ignoreCase)
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(env.stdout(), line); err != nil {
			return err
		}
	}
	return nil
}

func compareSortLines(left, right string, numeric, ignoreCase bool) int {
	if numeric {
		leftValue, leftOK := parseSortNumber(left)
		rightValue, rightOK := parseSortNumber(right)
		if leftOK && rightOK {
			switch {
			case leftValue < rightValue:
				return -1
			case leftValue > rightValue:
				return 1
			default:
				return 0
			}
		}
	}
	if ignoreCase {
		left = strings.ToLower(left)
		right = strings.ToLower(right)
	}
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func parseSortNumber(line string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(line), 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func uniqueSortLines(lines []string, numeric, ignoreCase bool) []string {
	if len(lines) == 0 {
		return nil
	}
	result := []string{lines[0]}
	for _, line := range lines[1:] {
		if compareSortLines(result[len(result)-1], line, numeric, ignoreCase) != 0 {
			result = append(result, line)
		}
	}
	return result
}

type cutMode uint8

const (
	cutModeBytes cutMode = iota
	cutModeChars
	cutModeFields
)

type cutRange struct {
	start int
	end   int
}

func runCut(env *commandEnv, args []string) error {
	mode := cutModeBytes
	modeCount := 0
	list := ""
	delimiter := "\t"
	suppressNoDelimiter := false
	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "b", Names: []string{"-b"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "c", Names: []string{"-c"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "f", Names: []string{"-f"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "d", Names: []string{"-d"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "s", Names: []string{"-s"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "b":
			mode = cutModeBytes
			list = opt.Value
			modeCount++
		case "c":
			mode = cutModeChars
			list = opt.Value
			modeCount++
		case "f":
			mode = cutModeFields
			list = opt.Value
			modeCount++
		case "d":
			delimiter = opt.Value
		case "s":
			suppressNoDelimiter = true
		}
	}
	if modeCount != 1 {
		return usageError(env, 1, "usage: cut (-b list | -c list | -f list) [-d delim] [-s] [file ...]")
	}
	if mode != cutModeFields && delimiter != "\t" {
		return fmt.Errorf("cut: -d is only valid with -f")
	}
	if mode != cutModeFields && suppressNoDelimiter {
		return fmt.Errorf("cut: -s is only valid with -f")
	}
	if mode == cutModeFields && utf8.RuneCountInString(delimiter) != 1 {
		return fmt.Errorf("cut: delimiter must be a single character")
	}
	ranges, err := parseCutRanges(list)
	if err != nil {
		return fmt.Errorf("cut: %w", err)
	}

	return eachCommandLine(env, operands, func(line, newline string) error {
		var out string
		switch mode {
		case cutModeBytes:
			out = cutBytes(line, ranges)
		case cutModeChars:
			out = cutChars(line, ranges)
		case cutModeFields:
			var ok bool
			out, ok = cutFields(line, delimiter, ranges)
			if !ok {
				if suppressNoDelimiter {
					return nil
				}
				out = line
			}
		}
		_, err := io.WriteString(env.stdout(), out+newline)
		return err
	})
}

func parseCutRanges(raw string) ([]cutRange, error) {
	parts := strings.Split(raw, ",")
	ranges := make([]cutRange, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("invalid list %q", raw)
		}
		if !strings.Contains(part, "-") {
			value, err := parseCutIndex(part)
			if err != nil {
				return nil, fmt.Errorf("invalid list %q", raw)
			}
			ranges = append(ranges, cutRange{start: value, end: value})
			continue
		}
		startText, endText, _ := strings.Cut(part, "-")
		var current cutRange
		if startText == "" {
			current.start = 1
		} else {
			value, err := parseCutIndex(startText)
			if err != nil {
				return nil, fmt.Errorf("invalid list %q", raw)
			}
			current.start = value
		}
		if endText == "" {
			current.end = -1
		} else {
			value, err := parseCutIndex(endText)
			if err != nil {
				return nil, fmt.Errorf("invalid list %q", raw)
			}
			current.end = value
		}
		if current.end != -1 && current.end < current.start {
			return nil, fmt.Errorf("invalid list %q", raw)
		}
		ranges = append(ranges, current)
	}
	return ranges, nil
}

func parseCutIndex(raw string) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("invalid index %q", raw)
	}
	return value, nil
}

func cutRangeContains(ranges []cutRange, index int) bool {
	for _, r := range ranges {
		if index < r.start {
			continue
		}
		if r.end == -1 || index <= r.end {
			return true
		}
	}
	return false
}

func cutBytes(line string, ranges []cutRange) string {
	data := []byte(line)
	result := make([]byte, 0, len(data))
	for i, b := range data {
		if cutRangeContains(ranges, i+1) {
			result = append(result, b)
		}
	}
	return string(result)
}

func cutChars(line string, ranges []cutRange) string {
	runes := []rune(line)
	var builder strings.Builder
	for i, r := range runes {
		if cutRangeContains(ranges, i+1) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func cutFields(line, delimiter string, ranges []cutRange) (string, bool) {
	if !strings.Contains(line, delimiter) {
		return "", false
	}
	fields := strings.Split(line, delimiter)
	selected := make([]string, 0, len(fields))
	for i, field := range fields {
		if cutRangeContains(ranges, i+1) {
			selected = append(selected, field)
		}
	}
	return strings.Join(selected, delimiter), true
}

type uniqLine struct {
	text    string
	newline string
}

func runUniq(env *commandEnv, args []string) error {
	showCount := false
	showDuplicates := false
	showUniques := false
	ignoreCase := false
	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "c", Names: []string{"-c"}},
		{Canonical: "d", Names: []string{"-d"}},
		{Canonical: "u", Names: []string{"-u"}},
		{Canonical: "i", Names: []string{"-i"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "c":
			showCount = true
		case "d":
			showDuplicates = true
		case "u":
			showUniques = true
		case "i":
			ignoreCase = true
		}
	}
	if len(operands) > 1 {
		return usageError(env, 1, "usage: uniq [-cdui] [input]")
	}

	lines := make([]uniqLine, 0)
	if err := eachCommandLine(env, operands, func(line, newline string) error {
		lines = append(lines, uniqLine{text: line, newline: newline})
		return nil
	}); err != nil {
		return err
	}
	if len(lines) == 0 {
		return nil
	}

	current := lines[0]
	count := 1
	for _, line := range lines[1:] {
		if uniqKeysEqual(current.text, line.text, ignoreCase) {
			count++
			continue
		}
		if err := writeUniqGroup(env.stdout(), current, count, showCount, showDuplicates, showUniques); err != nil {
			return err
		}
		current = line
		count = 1
	}
	return writeUniqGroup(env.stdout(), current, count, showCount, showDuplicates, showUniques)
}

func uniqKeysEqual(left, right string, ignoreCase bool) bool {
	if ignoreCase {
		left = strings.ToLower(left)
		right = strings.ToLower(right)
	}
	return left == right
}

func writeUniqGroup(w io.Writer, line uniqLine, count int, showCount, showDuplicates, showUniques bool) error {
	if showDuplicates && count < 2 {
		return nil
	}
	if showUniques && count != 1 {
		return nil
	}
	if showCount {
		_, err := fmt.Fprintf(w, "%d %s%s", count, line.text, line.newline)
		return err
	}
	_, err := io.WriteString(w, line.text+line.newline)
	return err
}

type trSet struct {
	chars    []rune
	contains map[rune]struct{}
}

func runTr(env *commandEnv, args []string) error {
	deleteChars := false
	squeezeRepeats := false
	parsedOpts, operands, err := parseUtilityOptionsToFirstOperand(args, []internal.OptionSpec{
		{Canonical: "d", Names: []string{"-d"}},
		{Canonical: "s", Names: []string{"-s"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "d":
			deleteChars = true
		case "s":
			squeezeRepeats = true
		}
	}

	switch {
	case !deleteChars && !squeezeRepeats && len(operands) != 2:
		return usageError(env, 1, "usage: tr [-ds] string1 [string2]")
	case deleteChars && !squeezeRepeats && len(operands) != 1:
		return usageError(env, 1, "usage: tr [-ds] string1 [string2]")
	case squeezeRepeats && len(operands) != 1 && len(operands) != 2:
		return usageError(env, 1, "usage: tr [-ds] string1 [string2]")
	}

	var (
		deleteSet    trSet
		squeezeSet   trSet
		translateMap map[rune]rune
		translate    bool
	)
	if deleteChars {
		chars, err := parseTrSet(operands[0])
		if err != nil {
			return fmt.Errorf("tr: %w", err)
		}
		deleteSet = newTrSet(chars)
		if squeezeRepeats {
			squeezeChars := chars
			if len(operands) == 2 {
				squeezeChars, err = parseTrSet(operands[1])
				if err != nil {
					return fmt.Errorf("tr: %w", err)
				}
			}
			squeezeSet = newTrSet(squeezeChars)
		}
	} else if len(operands) == 1 {
		chars, err := parseTrSet(operands[0])
		if err != nil {
			return fmt.Errorf("tr: %w", err)
		}
		squeezeSet = newTrSet(chars)
	} else {
		fromChars, err := parseTrSet(operands[0])
		if err != nil {
			return fmt.Errorf("tr: %w", err)
		}
		toChars, err := parseTrSet(operands[1])
		if err != nil {
			return fmt.Errorf("tr: %w", err)
		}
		if len(toChars) == 0 {
			return fmt.Errorf("tr: empty replacement set")
		}
		translateMap = buildTrTranslation(fromChars, toChars)
		translate = true
		if squeezeRepeats {
			squeezeSet = newTrSet(toChars)
		}
	}

	buffered := bufio.NewReader(env.stdin())
	var output strings.Builder
	var (
		lastOutput rune
		haveOutput bool
	)
	for {
		r, _, err := buffered.ReadRune()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if deleteChars && deleteSet.has(r) {
			continue
		}
		if translate {
			if replacement, ok := translateMap[r]; ok {
				r = replacement
			}
		}
		if squeezeRepeats && squeezeSet.has(r) && haveOutput && lastOutput == r {
			continue
		}
		output.WriteRune(r)
		lastOutput = r
		haveOutput = true
	}
	_, err = io.WriteString(env.stdout(), output.String())
	return err
}

func newTrSet(chars []rune) trSet {
	contains := make(map[rune]struct{}, len(chars))
	for _, ch := range chars {
		contains[ch] = struct{}{}
	}
	return trSet{chars: chars, contains: contains}
}

func (set trSet) has(ch rune) bool {
	_, ok := set.contains[ch]
	return ok
}

func buildTrTranslation(fromChars, toChars []rune) map[rune]rune {
	translation := make(map[rune]rune, len(fromChars))
	lastReplacement := toChars[len(toChars)-1]
	for i, ch := range fromChars {
		if _, exists := translation[ch]; exists {
			continue
		}
		replacement := lastReplacement
		if i < len(toChars) {
			replacement = toChars[i]
		}
		translation[ch] = replacement
	}
	return translation
}

func parseTrSet(raw string) ([]rune, error) {
	chars := make([]rune, 0, len(raw))
	for i := 0; i < len(raw); {
		part, next, err := parseTrAtom(raw, i)
		if err != nil {
			return nil, err
		}
		if len(part) == 1 && next+1 < len(raw) && raw[next] == '-' {
			nextPart, rangeEnd, err := parseTrAtom(raw, next+1)
			if err == nil && len(nextPart) == 1 {
				if part[0] > nextPart[0] {
					return nil, fmt.Errorf("invalid range %q", raw[i:rangeEnd])
				}
				for ch := part[0]; ch <= nextPart[0]; ch++ {
					chars = append(chars, ch)
				}
				i = rangeEnd
				continue
			}
		}
		chars = append(chars, part...)
		i = next
	}
	return chars, nil
}

func parseTrAtom(raw string, start int) ([]rune, int, error) {
	if strings.HasPrefix(raw[start:], "[:") {
		if end := strings.Index(raw[start+2:], ":]"); end >= 0 {
			className := raw[start+2 : start+2+end]
			if chars, ok := trClassRunes(className); ok {
				return chars, start + 2 + end + 2, nil
			}
		}
	}
	if raw[start] == '\\' {
		return parseTrEscape(raw, start)
	}
	r, size := utf8.DecodeRuneInString(raw[start:])
	return []rune{r}, start + size, nil
}

func parseTrEscape(raw string, start int) ([]rune, int, error) {
	if start+1 >= len(raw) {
		return []rune{'\\'}, len(raw), nil
	}
	switch raw[start+1] {
	case 'n':
		return []rune{'\n'}, start + 2, nil
	case 'r':
		return []rune{'\r'}, start + 2, nil
	case 't':
		return []rune{'\t'}, start + 2, nil
	case 'a':
		return []rune{'\a'}, start + 2, nil
	case 'b':
		return []rune{'\b'}, start + 2, nil
	case 'f':
		return []rune{'\f'}, start + 2, nil
	case 'v':
		return []rune{'\v'}, start + 2, nil
	case '\\':
		return []rune{'\\'}, start + 2, nil
	}
	if raw[start+1] >= '0' && raw[start+1] <= '7' {
		end := start + 2
		for end < len(raw) && end < start+4 && raw[end] >= '0' && raw[end] <= '7' {
			end++
		}
		value, err := strconv.ParseInt(raw[start+1:end], 8, 32)
		if err != nil {
			return nil, 0, err
		}
		return []rune{rune(value)}, end, nil
	}
	r, size := utf8.DecodeRuneInString(raw[start+1:])
	return []rune{r}, start + 1 + size, nil
}

func trClassRunes(name string) ([]rune, bool) {
	switch name {
	case "alnum":
		return append(append([]rune{}, trRangeRunes('0', '9')...), append(trRangeRunes('A', 'Z'), trRangeRunes('a', 'z')...)...), true
	case "alpha":
		return append(append([]rune{}, trRangeRunes('A', 'Z')...), trRangeRunes('a', 'z')...), true
	case "blank":
		return []rune{' ', '\t'}, true
	case "digit":
		return trRangeRunes('0', '9'), true
	case "lower":
		return trRangeRunes('a', 'z'), true
	case "space":
		return []rune{' ', '\t', '\n', '\r', '\v', '\f'}, true
	case "upper":
		return trRangeRunes('A', 'Z'), true
	default:
		return nil, false
	}
}

func trRangeRunes(start, end rune) []rune {
	chars := make([]rune, 0, end-start+1)
	for ch := start; ch <= end; ch++ {
		chars = append(chars, ch)
	}
	return chars
}

func runComm(env *commandEnv, args []string) error {
	suppressed := [3]bool{}
	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "1", Names: []string{"-1"}},
		{Canonical: "2", Names: []string{"-2"}},
		{Canonical: "3", Names: []string{"-3"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "1":
			suppressed[0] = true
		case "2":
			suppressed[1] = true
		case "3":
			suppressed[2] = true
		}
	}
	if len(operands) != 2 {
		return usageError(env, 1, "usage: comm [-123] file1 file2")
	}

	leftLines, err := readTextLines(env, operands[0])
	if err != nil {
		return err
	}
	rightLines, err := readTextLines(env, operands[1])
	if err != nil {
		return err
	}

	leftIndex, rightIndex := 0, 0
	for leftIndex < len(leftLines) || rightIndex < len(rightLines) {
		switch {
		case leftIndex >= len(leftLines):
			if err := writeCommLine(env.stdout(), 2, suppressed, rightLines[rightIndex]); err != nil {
				return err
			}
			rightIndex++
		case rightIndex >= len(rightLines):
			if err := writeCommLine(env.stdout(), 1, suppressed, leftLines[leftIndex]); err != nil {
				return err
			}
			leftIndex++
		case leftLines[leftIndex] == rightLines[rightIndex]:
			if err := writeCommLine(env.stdout(), 3, suppressed, leftLines[leftIndex]); err != nil {
				return err
			}
			leftIndex++
			rightIndex++
		case leftLines[leftIndex] < rightLines[rightIndex]:
			if err := writeCommLine(env.stdout(), 1, suppressed, leftLines[leftIndex]); err != nil {
				return err
			}
			leftIndex++
		default:
			if err := writeCommLine(env.stdout(), 2, suppressed, rightLines[rightIndex]); err != nil {
				return err
			}
			rightIndex++
		}
	}
	return nil
}

func writeCommLine(w io.Writer, column int, suppressed [3]bool, line string) error {
	if suppressed[column-1] {
		return nil
	}
	leadingTabs := 0
	for i := 0; i < column-1; i++ {
		if !suppressed[i] {
			leadingTabs++
		}
	}
	if _, err := io.WriteString(w, strings.Repeat("\t", leadingTabs)); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w, line)
	return err
}

func runDiff(env *commandEnv, args []string) error {
	quiet := false
	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "q", Names: []string{"-q"}},
		{Canonical: "u", Names: []string{"-u"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		if opt.Canonical == "q" {
			quiet = true
		}
	}
	if len(operands) != 2 {
		return usageError(env, 1, "usage: diff [-u] [-q] file1 file2")
	}

	leftName, rightName := operands[0], operands[1]
	var (
		leftData  []byte
		rightData []byte
	)
	if leftName == "-" && rightName == "-" {
		leftData, err = io.ReadAll(env.stdin())
		if err != nil {
			return err
		}
		rightData = append([]byte(nil), leftData...)
	} else {
		leftData, err = readCommandData(env, leftName)
		if err != nil {
			return err
		}
		rightData, err = readCommandData(env, rightName)
		if err != nil {
			return err
		}
	}
	if bytes.Equal(leftData, rightData) {
		return nil
	}
	if quiet {
		if _, err := fmt.Fprintf(env.stdout(), "Files %s and %s differ\n", leftName, rightName); err != nil {
			return err
		}
		return interp.ExitStatus(1)
	}
	output := diffpkg.Diff(leftName, leftData, rightName, rightData)
	if _, err := env.stdout().Write(output); err != nil {
		return err
	}
	return interp.ExitStatus(1)
}

func eachCommandLine(env *commandEnv, operands []string, fn func(line, newline string) error) error {
	if len(operands) == 0 {
		operands = []string{"-"}
	}
	for _, operand := range operands {
		data, err := readCommandData(env, operand)
		if err != nil {
			return err
		}
		for _, rawLine := range splitLines(data) {
			line, newline := splitTrailingNewline(string(rawLine))
			if err := fn(line, newline); err != nil {
				return err
			}
		}
	}
	return nil
}

func readCommandData(env *commandEnv, operand string) ([]byte, error) {
	reader, closeFn, err := openCommandInput(env, operand)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(reader)
	if closeFn != nil {
		closeErr := closeFn()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

func readTextLines(env *commandEnv, operand string) ([]string, error) {
	data, err := readCommandData(env, operand)
	if err != nil {
		return nil, err
	}
	lines := make([]string, 0)
	for _, rawLine := range splitLines(data) {
		line, _ := splitTrailingNewline(string(rawLine))
		lines = append(lines, line)
	}
	return lines, nil
}
