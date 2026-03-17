package coreutils

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/richardartoul/swarmd/pkg/sh/expand"
	"github.com/richardartoul/swarmd/pkg/sh/internal"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/syntax"
)

type byteOrLineMode uint8

const (
	modeLines byteOrLineMode = iota
	modeBytes
)

type tailCountSpec struct {
	fromStart bool
	value     int
}

type wcCounts struct {
	lines int64
	words int64
	bytes int64
	chars int64
}

type unameInfo struct {
	sysname  string
	nodename string
	release  string
	version  string
	machine  string
}

type grepMatcher struct {
	matchLine   func(line string) bool
	findMatches func(line string) []string
}

func (m grepMatcher) Match(line string) bool {
	return m.matchLine(line)
}

func (m grepMatcher) FindAll(line string) []string {
	return m.findMatches(line)
}

type grepTarget struct {
	displayName  string
	resolvedPath string
	useStdin     bool
}

type sedSubstitution struct {
	regex       *regexp.Regexp
	replacement string
	global      bool
	print       bool
}

func runGrep(env *commandEnv, args []string) error {
	fixedStrings := false
	modeSpecified := false
	ignoreCase := false
	invertMatch := false
	showLineNumbers := false
	countOnly := false
	listFiles := false
	onlyMatching := false
	quiet := false
	recursive := false
	var patterns []string

	parsedOpts, operands, err := parseUtilityOptionsToFirstOperand(args, []internal.OptionSpec{
		{Canonical: "E", Names: []string{"-E"}},
		{Canonical: "F", Names: []string{"-F"}},
		{Canonical: "R", Names: []string{"-R"}},
		{Canonical: "r", Names: []string{"-r"}},
		{Canonical: "i", Names: []string{"-i"}},
		{Canonical: "v", Names: []string{"-v"}},
		{Canonical: "n", Names: []string{"-n"}},
		{Canonical: "c", Names: []string{"-c"}},
		{Canonical: "l", Names: []string{"-l"}},
		{Canonical: "o", Names: []string{"-o"}},
		{Canonical: "q", Names: []string{"-q"}},
		{Canonical: "e", Names: []string{"-e"}, ValueMode: internal.RequiredOptionValue},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "E":
			fixedStrings = false
			modeSpecified = true
		case "F":
			fixedStrings = true
			modeSpecified = true
		case "R", "r":
			recursive = true
		case "i":
			ignoreCase = true
		case "v":
			invertMatch = true
		case "n":
			showLineNumbers = true
		case "c":
			countOnly = true
		case "l":
			listFiles = true
		case "o":
			onlyMatching = true
		case "q":
			quiet = true
		case "e":
			patterns = append(patterns, opt.Value)
		}
	}
	if len(patterns) == 0 {
		if len(operands) == 0 {
			return usageError(env, 2, "usage: grep [-E|-F] [-R|-r] [-ivncloq] [-e pattern]... [pattern] [file ...]")
		}
		patterns = append(patterns, operands[0])
		operands = operands[1:]
	}
	if !modeSpecified {
		return usageError(
			env,
			2,
			"grep: use -F for literal matches or -E for regex patterns; plain grep without -E/-F is not allowed",
			"usage: grep [-E|-F] [-R|-r] [-ivncloq] [-e pattern]... [pattern] [file ...]",
		)
	}
	if len(operands) == 0 {
		operands = []string{"-"}
	}

	matcher, err := buildGrepMatcher(patterns, fixedStrings, ignoreCase)
	if err != nil {
		return err
	}

	targets, prefixFilename, err := expandGrepTargets(env, operands, recursive)
	if err != nil {
		return err
	}
	matchedAny := false
	for _, target := range targets {
		reader, closeFn, err := openGrepTarget(env, target)
		if err != nil {
			return err
		}
		fileMatched, matchCount, err := grepReader(env.stdout(), reader, target.displayName, matcher, grepRunConfig{
			invertMatch:     invertMatch,
			showLineNumbers: showLineNumbers,
			prefixFilename:  prefixFilename,
			countOnly:       countOnly,
			listFiles:       listFiles,
			onlyMatching:    onlyMatching,
			quiet:           quiet,
		})
		if closeFn != nil {
			closeErr := closeFn()
			if err == nil {
				err = closeErr
			}
		}
		if err != nil {
			return err
		}
		if fileMatched {
			matchedAny = true
		}
		if quiet && fileMatched {
			return nil
		}
		if listFiles {
			if fileMatched {
				if _, err := fmt.Fprintln(env.stdout(), target.displayName); err != nil {
					return err
				}
			}
			continue
		}
		if countOnly {
			if prefixFilename {
				if _, err := fmt.Fprintf(env.stdout(), "%s:%d\n", target.displayName, matchCount); err != nil {
					return err
				}
			} else if _, err := fmt.Fprintf(env.stdout(), "%d\n", matchCount); err != nil {
				return err
			}
		}
	}
	if !matchedAny {
		return interp.ExitStatus(1)
	}
	return nil
}

func runSed(env *commandEnv, args []string) error {
	suppressDefaultPrint := false
	var scripts []string

	parsedOpts, operands, err := parseUtilityOptionsToFirstOperand(args, []internal.OptionSpec{
		{Canonical: "n", Names: []string{"-n"}},
		{Canonical: "e", Names: []string{"-e"}, ValueMode: internal.RequiredOptionValue},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "n":
			suppressDefaultPrint = true
		case "e":
			scripts = append(scripts, opt.Value)
		}
	}
	if len(scripts) == 0 {
		if len(operands) > 0 {
			scripts = append(scripts, operands[0])
			operands = operands[1:]
		}
	}
	if len(operands) == 0 {
		operands = []string{"-"}
	}

	commands, err := parseSedProgram(scripts)
	if err != nil {
		return err
	}

	for _, operand := range operands {
		reader, closeFn, err := openCommandInput(env, operand)
		if err != nil {
			return err
		}
		err = runSedProgram(env.stdout(), reader, commands, suppressDefaultPrint)
		if closeFn != nil {
			closeErr := closeFn()
			if err == nil {
				err = closeErr
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func runHead(env *commandEnv, args []string) error {
	args = normalizeLegacyHeadArgs(args)
	mode := modeLines
	count := 10
	quiet := false
	verbose := false
	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "n", Names: []string{"-n"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "c", Names: []string{"-c"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "q", Names: []string{"-q"}},
		{Canonical: "v", Names: []string{"-v"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "n":
			count, err = parseNonNegativeCount(opt.Value)
			if err != nil {
				return fmt.Errorf("head: %w", err)
			}
			mode = modeLines
		case "c":
			count, err = parseNonNegativeCount(opt.Value)
			if err != nil {
				return fmt.Errorf("head: %w", err)
			}
			mode = modeBytes
		case "q":
			quiet = true
		case "v":
			verbose = true
		}
	}
	if len(operands) == 0 {
		operands = []string{"-"}
	}
	printHeaders := len(operands) > 1 && !quiet
	if verbose {
		printHeaders = true
	}

	for i, operand := range operands {
		reader, closeFn, err := openCommandInput(env, operand)
		if err != nil {
			return err
		}
		if printHeaders {
			if i > 0 {
				fmt.Fprintln(env.stdout())
			}
			fmt.Fprintf(env.stdout(), "==> %s <==\n", operand)
		}
		switch mode {
		case modeLines:
			err = writeHeadLines(env.stdout(), reader, count)
		case modeBytes:
			err = writeHeadBytes(env.stdout(), reader, count)
		}
		if closeFn != nil {
			closeErr := closeFn()
			if err == nil {
				err = closeErr
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func runTail(env *commandEnv, args []string) error {
	args = normalizeLegacyTailArgs(args)
	mode := modeLines
	count := tailCountSpec{value: 10}
	quiet := false
	verbose := false
	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "n", Names: []string{"-n"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "c", Names: []string{"-c"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "q", Names: []string{"-q"}},
		{Canonical: "v", Names: []string{"-v"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "n":
			count, err = parseTailCount(opt.Value)
			if err != nil {
				return fmt.Errorf("tail: %w", err)
			}
			mode = modeLines
		case "c":
			count, err = parseTailCount(opt.Value)
			if err != nil {
				return fmt.Errorf("tail: %w", err)
			}
			mode = modeBytes
		case "q":
			quiet = true
		case "v":
			verbose = true
		}
	}
	if len(operands) == 0 {
		operands = []string{"-"}
	}
	printHeaders := len(operands) > 1 && !quiet
	if verbose {
		printHeaders = true
	}

	for i, operand := range operands {
		reader, closeFn, err := openCommandInput(env, operand)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(reader)
		if closeFn != nil {
			closeErr := closeFn()
			if err == nil {
				err = closeErr
			}
		}
		if err != nil {
			return err
		}
		if printHeaders {
			if i > 0 {
				fmt.Fprintln(env.stdout())
			}
			fmt.Fprintf(env.stdout(), "==> %s <==\n", operand)
		}
		switch mode {
		case modeLines:
			err = writeTailLines(env.stdout(), data, count)
		case modeBytes:
			err = writeTailBytes(env.stdout(), data, count)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func runWc(env *commandEnv, args []string) error {
	showLines := false
	showWords := false
	showBytes := false
	showChars := false
	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "l", Names: []string{"-l"}},
		{Canonical: "w", Names: []string{"-w"}},
		{Canonical: "c", Names: []string{"-c"}},
		{Canonical: "m", Names: []string{"-m"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "l":
			showLines = true
		case "w":
			showWords = true
		case "c":
			showBytes = true
		case "m":
			showChars = true
		}
	}
	if !showLines && !showWords && !showBytes && !showChars {
		showLines = true
		showWords = true
		showBytes = true
	}
	if len(operands) == 0 {
		counts, err := countReader(env.stdin())
		if err != nil {
			return err
		}
		return writeWcLine(env.stdout(), counts, showLines, showWords, showBytes, showChars, "")
	}

	var total wcCounts
	for _, operand := range operands {
		reader, closeFn, err := openCommandInput(env, operand)
		if err != nil {
			return err
		}
		counts, err := countReader(reader)
		if closeFn != nil {
			closeErr := closeFn()
			if err == nil {
				err = closeErr
			}
		}
		if err != nil {
			return err
		}
		total.lines += counts.lines
		total.words += counts.words
		total.bytes += counts.bytes
		total.chars += counts.chars
		if err := writeWcLine(env.stdout(), counts, showLines, showWords, showBytes, showChars, operand); err != nil {
			return err
		}
	}
	if len(operands) > 1 {
		return writeWcLine(env.stdout(), total, showLines, showWords, showBytes, showChars, "total")
	}
	return nil
}

func runEnv(env *commandEnv, args []string) error {
	nulSeparated := false
	ignoreEnv := false
	var unsetNames []string
	parsedOpts, operands, err := parseUtilityOptionsToFirstOperand(args, []internal.OptionSpec{
		{Canonical: "0", Names: []string{"-0"}},
		{Canonical: "i", Names: []string{"-i", "--ignore-environment"}},
		{Canonical: "u", Names: []string{"-u"}, ValueMode: internal.RequiredOptionValue},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "0":
			nulSeparated = true
		case "i":
			ignoreEnv = true
		case "u":
			if !syntax.ValidName(opt.Value) {
				return fmt.Errorf("env: invalid variable name %q", opt.Value)
			}
			unsetNames = append(unsetNames, opt.Value)
		}
	}

	type assignment struct {
		name  string
		value string
	}
	var assignments []assignment
	commandStart := 0
	for commandStart < len(operands) {
		name, value, ok := strings.Cut(operands[commandStart], "=")
		if !ok {
			break
		}
		if !syntax.ValidName(name) {
			return fmt.Errorf("env: invalid variable assignment %q", operands[commandStart])
		}
		assignments = append(assignments, assignment{name: name, value: value})
		commandStart++
	}

	if ignoreEnv || len(unsetNames) > 0 || len(assignments) > 0 {
		writer, ok := env.hc.Env.(expand.WriteEnviron)
		if !ok {
			return fmt.Errorf("env: current environment is not writable")
		}
		if ignoreEnv {
			var names []string
			env.hc.Env.Each(func(name string, vr expand.Variable) bool {
				if vr.Exported && vr.IsSet() && vr.Kind == expand.String {
					names = append(names, name)
				}
				return true
			})
			for _, name := range names {
				if err := writer.Set(name, expand.Variable{}); err != nil {
					return err
				}
			}
		}
		for _, name := range unsetNames {
			if err := writer.Set(name, expand.Variable{}); err != nil {
				return err
			}
		}
		for _, assignment := range assignments {
			if err := writer.Set(assignment.name, expand.Variable{
				Set:      true,
				Exported: true,
				Kind:     expand.String,
				Str:      assignment.value,
			}); err != nil {
				return err
			}
		}
		if commandStart < len(operands) {
			return env.dispatchWithOptions(operands[commandStart:], interp.RunSimpleCommandOptions{
				Env: writer,
			})
		}
	}

	if commandStart == len(operands) {
		return writeEnvironment(env.stdout(), env.hc.Env, nulSeparated)
	}
	return env.dispatch(operands[commandStart:])
}

func runUname(env *commandEnv, args []string) error {
	showAll := false
	showSysname := false
	showNodename := false
	showRelease := false
	showVersion := false
	showMachine := false
	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "a", Names: []string{"-a"}},
		{Canonical: "s", Names: []string{"-s"}},
		{Canonical: "n", Names: []string{"-n"}},
		{Canonical: "r", Names: []string{"-r"}},
		{Canonical: "v", Names: []string{"-v"}},
		{Canonical: "m", Names: []string{"-m"}},
	})
	if err != nil {
		return err
	}
	if len(operands) > 0 {
		return fmt.Errorf("uname: too many arguments")
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "a":
			showAll = true
		case "s":
			showSysname = true
		case "n":
			showNodename = true
		case "r":
			showRelease = true
		case "v":
			showVersion = true
		case "m":
			showMachine = true
		}
	}
	if showAll {
		showSysname = true
		showNodename = true
		showRelease = true
		showVersion = true
		showMachine = true
	}
	if !showSysname && !showNodename && !showRelease && !showVersion && !showMachine {
		showSysname = true
	}

	info := hostUnameInfo()
	parts := make([]string, 0, 5)
	if showSysname {
		parts = append(parts, info.sysname)
	}
	if showNodename {
		parts = append(parts, info.nodename)
	}
	if showRelease {
		parts = append(parts, info.release)
	}
	if showVersion {
		parts = append(parts, info.version)
	}
	if showMachine {
		parts = append(parts, info.machine)
	}
	_, err = fmt.Fprintln(env.stdout(), strings.Join(parts, " "))
	return err
}

func openCommandInput(env *commandEnv, operand string) (io.Reader, func() error, error) {
	if operand == "-" {
		return env.stdin(), nil, nil
	}
	resolvedPath, err := env.resolvePathArg(operand)
	if err != nil {
		return nil, nil, err
	}
	file, err := env.hc.FileSystem.Open(resolvedPath)
	if err != nil {
		return nil, nil, err
	}
	return file, file.Close, nil
}

func openGrepTarget(env *commandEnv, target grepTarget) (io.Reader, func() error, error) {
	if target.useStdin {
		return env.stdin(), nil, nil
	}
	file, err := env.hc.FileSystem.Open(target.resolvedPath)
	if err != nil {
		return nil, nil, err
	}
	return file, file.Close, nil
}

func expandGrepTargets(env *commandEnv, operands []string, recursive bool) ([]grepTarget, bool, error) {
	targets := make([]grepTarget, 0, len(operands))
	prefixFilename := false
	for _, operand := range operands {
		if operand == "-" {
			targets = append(targets, grepTarget{
				displayName: operand,
				useStdin:    true,
			})
			continue
		}
		resolvedPath, err := env.resolvePathArg(operand)
		if err != nil {
			return nil, false, err
		}
		info, err := env.hc.FileSystem.Lstat(resolvedPath)
		if err != nil {
			return nil, false, err
		}
		if !recursive || !info.IsDir() {
			targets = append(targets, grepTarget{
				displayName:  operand,
				resolvedPath: resolvedPath,
			})
			continue
		}
		prefixFilename = true
		err = walkResolvedInfo(env.hc.FileSystem, resolvedPath, info, func(currentPath string, info fs.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() || !info.Mode().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(resolvedPath, currentPath)
			if err != nil {
				return err
			}
			targets = append(targets, grepTarget{
				displayName:  joinDisplayPath(operand, rel),
				resolvedPath: currentPath,
			})
			return nil
		})
		if err != nil {
			return nil, false, err
		}
	}
	if len(targets) > 1 {
		prefixFilename = true
	}
	return targets, prefixFilename, nil
}

type grepRunConfig struct {
	invertMatch     bool
	showLineNumbers bool
	prefixFilename  bool
	countOnly       bool
	listFiles       bool
	onlyMatching    bool
	quiet           bool
}

func buildGrepMatcher(patterns []string, fixedStrings, ignoreCase bool) (grepMatcher, error) {
	regexps := make([]*regexp.Regexp, len(patterns))
	for i, pattern := range patterns {
		if fixedStrings {
			pattern = regexp.QuoteMeta(pattern)
		}
		if ignoreCase {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return grepMatcher{}, fmt.Errorf("grep: %w", err)
		}
		regexps[i] = re
	}
	return grepMatcher{
		matchLine: func(line string) bool {
			for _, re := range regexps {
				if re.MatchString(line) {
					return true
				}
			}
			return false
		},
		findMatches: func(line string) []string {
			return grepFindMatches(line, regexps)
		},
	}, nil
}

func grepReader(w io.Writer, reader io.Reader, operand string, matcher grepMatcher, cfg grepRunConfig) (bool, int, error) {
	buffered := bufio.NewReader(reader)
	matchedAny := false
	matchCount := 0
	lineNumber := 0
	for {
		rawLine, err := buffered.ReadBytes('\n')
		if len(rawLine) > 0 {
			lineNumber++
			lineText := strings.TrimSuffix(string(rawLine), "\n")
			matches := []string(nil)
			matched := matcher.Match(lineText)
			if cfg.onlyMatching && !cfg.invertMatch {
				matches = matcher.FindAll(lineText)
				matched = len(matches) > 0
			}
			if cfg.invertMatch {
				matched = !matched
			}
			if matched {
				matchedAny = true
				matchCount++
				if cfg.quiet {
					return true, matchCount, nil
				}
				if !cfg.countOnly && !cfg.listFiles {
					if cfg.onlyMatching && !cfg.invertMatch {
						for _, match := range matches {
							if err := writeGrepPrefixes(w, operand, lineNumber, cfg); err != nil {
								return matchedAny, matchCount, err
							}
							if _, err := fmt.Fprintln(w, match); err != nil {
								return matchedAny, matchCount, err
							}
						}
						continue
					}
					if err := writeGrepPrefixes(w, operand, lineNumber, cfg); err != nil {
						return matchedAny, matchCount, err
					}
					if _, err := w.Write(rawLine); err != nil {
						return matchedAny, matchCount, err
					}
				}
			}
		}
		if err == io.EOF {
			return matchedAny, matchCount, nil
		}
		if err != nil {
			return matchedAny, matchCount, err
		}
	}
}

func grepFindMatches(line string, regexps []*regexp.Regexp) []string {
	matches := make([]string, 0)
	offset := 0
	for offset <= len(line) {
		bestStart := -1
		bestEnd := -1
		for _, re := range regexps {
			loc := re.FindStringIndex(line[offset:])
			if loc == nil {
				continue
			}
			start := offset + loc[0]
			end := offset + loc[1]
			if bestStart < 0 || start < bestStart || (start == bestStart && end > bestEnd) {
				bestStart = start
				bestEnd = end
			}
		}
		if bestStart < 0 {
			return matches
		}
		if bestEnd == bestStart {
			if bestStart >= len(line) {
				return matches
			}
			offset = bestStart + 1
			continue
		}
		matches = append(matches, line[bestStart:bestEnd])
		offset = bestEnd
	}
	return matches
}

func writeGrepPrefixes(w io.Writer, operand string, lineNumber int, cfg grepRunConfig) error {
	if cfg.prefixFilename {
		if _, err := fmt.Fprintf(w, "%s:", operand); err != nil {
			return err
		}
	}
	if cfg.showLineNumbers {
		if _, err := fmt.Fprintf(w, "%d:", lineNumber); err != nil {
			return err
		}
	}
	return nil
}

func parseSedSubstitutions(scripts []string) ([]sedSubstitution, error) {
	substitutions := make([]sedSubstitution, 0, len(scripts))
	for _, script := range scripts {
		substitution, err := parseSedSubstitution(strings.TrimSpace(script))
		if err != nil {
			return nil, err
		}
		substitutions = append(substitutions, substitution)
	}
	return substitutions, nil
}

func parseSedSubstitution(script string) (sedSubstitution, error) {
	if script == "" {
		return sedSubstitution{}, fmt.Errorf("sed: empty script")
	}
	if len(script) < 2 || script[0] != 's' {
		return sedSubstitution{}, fmt.Errorf("sed: unsupported script %q", script)
	}
	separator := script[1]
	pattern, next, err := parseSedSegment(script, 2, separator)
	if err != nil {
		return sedSubstitution{}, err
	}
	replacement, next, err := parseSedSegment(script, next, separator)
	if err != nil {
		return sedSubstitution{}, err
	}

	substitution := sedSubstitution{
		replacement: translateSedReplacement(replacement),
	}
	for _, flag := range strings.TrimSpace(script[next:]) {
		switch flag {
		case 'g':
			substitution.global = true
		case 'p':
			substitution.print = true
		default:
			return sedSubstitution{}, fmt.Errorf("sed: unsupported substitute flag %q", string(flag))
		}
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return sedSubstitution{}, fmt.Errorf("sed: %w", err)
	}
	substitution.regex = re
	return substitution, nil
}

func parseSedSegment(script string, start int, separator byte) (string, int, error) {
	var builder strings.Builder
	escaped := false
	for i := start; i < len(script); i++ {
		ch := script[i]
		if escaped {
			if ch != separator {
				builder.WriteByte('\\')
			}
			builder.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == separator {
			return builder.String(), i + 1, nil
		}
		builder.WriteByte(ch)
	}
	if escaped {
		builder.WriteByte('\\')
	}
	return "", 0, fmt.Errorf("sed: unterminated substitute expression %q", script)
}

func translateSedReplacement(raw string) string {
	var builder strings.Builder
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if ch == '\\' && i+1 < len(raw) {
			next := raw[i+1]
			switch {
			case next >= '1' && next <= '9':
				builder.WriteByte('$')
				builder.WriteByte(next)
			case next == '&':
				builder.WriteByte('&')
			case next == '$':
				builder.WriteString("$$")
			default:
				if next == '$' {
					builder.WriteString("$$")
				} else {
					builder.WriteByte(next)
				}
			}
			i++
			continue
		}
		if ch == '&' {
			builder.WriteString("$0")
			continue
		}
		if ch == '$' {
			builder.WriteString("$$")
			continue
		}
		builder.WriteByte(ch)
	}
	return builder.String()
}

func runSedStream(w io.Writer, reader io.Reader, substitutions []sedSubstitution, suppressDefaultPrint bool) error {
	buffered := bufio.NewReader(reader)
	for {
		rawLine, err := buffered.ReadString('\n')
		if len(rawLine) > 0 {
			line, newline := splitTrailingNewline(rawLine)
			current := line
			for _, substitution := range substitutions {
				var changed bool
				current, changed = applySedSubstitution(substitution, current)
				if changed && substitution.print {
					if _, err := io.WriteString(w, current+newline); err != nil {
						return err
					}
				}
			}
			if !suppressDefaultPrint {
				if _, err := io.WriteString(w, current+newline); err != nil {
					return err
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func splitTrailingNewline(line string) (string, string) {
	if strings.HasSuffix(line, "\n") {
		return strings.TrimSuffix(line, "\n"), "\n"
	}
	return line, ""
}

func applySedSubstitution(substitution sedSubstitution, input string) (string, bool) {
	if substitution.global {
		if !substitution.regex.MatchString(input) {
			return input, false
		}
		return substitution.regex.ReplaceAllString(input, substitution.replacement), true
	}
	match := substitution.regex.FindStringSubmatchIndex(input)
	if match == nil {
		return input, false
	}
	replaced := substitution.regex.ExpandString(nil, substitution.replacement, input, match)
	return input[:match[0]] + string(replaced) + input[match[1]:], true
}

func parseNonNegativeCount(raw string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid count %q", raw)
	}
	return n, nil
}

func normalizeLegacyHeadArgs(args []string) []string {
	if len(args) == 0 || !isLegacyCountArg(args[0], '-') {
		return args
	}
	return append([]string{"-n", args[0][1:]}, args[1:]...)
}

func normalizeLegacyTailArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	switch {
	case isLegacyCountArg(args[0], '-'):
		return append([]string{"-n", args[0][1:]}, args[1:]...)
	case isLegacyCountArg(args[0], '+'):
		return append([]string{"-n", args[0]}, args[1:]...)
	default:
		return args
	}
}

func isLegacyCountArg(arg string, prefix byte) bool {
	if len(arg) < 2 || arg[0] != prefix {
		return false
	}
	for _, r := range arg[1:] {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func parseTailCount(raw string) (tailCountSpec, error) {
	spec := tailCountSpec{}
	if strings.HasPrefix(raw, "+") {
		spec.fromStart = true
		raw = strings.TrimPrefix(raw, "+")
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return tailCountSpec{}, fmt.Errorf("invalid count %q", raw)
	}
	spec.value = n
	return spec, nil
}

func writeHeadLines(w io.Writer, reader io.Reader, count int) error {
	if count <= 0 {
		return nil
	}
	buffered := bufio.NewReader(reader)
	for i := 0; i < count; i++ {
		line, err := buffered.ReadBytes('\n')
		if len(line) > 0 {
			if _, writeErr := w.Write(line); writeErr != nil {
				return writeErr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func writeHeadBytes(w io.Writer, reader io.Reader, count int) error {
	if count <= 0 {
		return nil
	}
	_, err := io.CopyN(w, reader, int64(count))
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return nil
	}
	return err
}

func writeTailLines(w io.Writer, data []byte, count tailCountSpec) error {
	lines := splitLines(data)
	if count.fromStart {
		start := count.value - 1
		if start < 0 {
			start = 0
		}
		if start >= len(lines) {
			return nil
		}
		for _, line := range lines[start:] {
			if _, err := w.Write(line); err != nil {
				return err
			}
		}
		return nil
	}
	if count.value <= 0 || len(lines) == 0 {
		return nil
	}
	start := len(lines) - count.value
	if start < 0 {
		start = 0
	}
	for _, line := range lines[start:] {
		if _, err := w.Write(line); err != nil {
			return err
		}
	}
	return nil
}

func writeTailBytes(w io.Writer, data []byte, count tailCountSpec) error {
	if count.fromStart {
		start := count.value - 1
		if start < 0 {
			start = 0
		}
		if start >= len(data) {
			return nil
		}
		_, err := w.Write(data[start:])
		return err
	}
	if count.value <= 0 {
		return nil
	}
	start := len(data) - count.value
	if start < 0 {
		start = 0
	}
	_, err := w.Write(data[start:])
	return err
}

func splitLines(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	lines := make([][]byte, 0, bytes.Count(data, []byte{'\n'})+1)
	start := 0
	for start < len(data) {
		if idx := bytes.IndexByte(data[start:], '\n'); idx >= 0 {
			end := start + idx + 1
			lines = append(lines, data[start:end])
			start = end
			continue
		}
		lines = append(lines, data[start:])
		break
	}
	return lines
}

func countReader(reader io.Reader) (wcCounts, error) {
	buffered := bufio.NewReader(reader)
	var (
		counts wcCounts
		inWord bool
	)
	for {
		r, size, err := buffered.ReadRune()
		if err == io.EOF {
			return counts, nil
		}
		if err != nil {
			return counts, err
		}
		counts.bytes += int64(size)
		counts.chars++
		if r == '\n' {
			counts.lines++
		}
		if unicode.IsSpace(r) {
			inWord = false
			continue
		}
		if !inWord {
			counts.words++
			inWord = true
		}
	}
}

func writeWcLine(w io.Writer, counts wcCounts, showLines, showWords, showBytes, showChars bool, name string) error {
	fields := make([]string, 0, 5)
	if showLines {
		fields = append(fields, strconv.FormatInt(counts.lines, 10))
	}
	if showWords {
		fields = append(fields, strconv.FormatInt(counts.words, 10))
	}
	if showBytes {
		fields = append(fields, strconv.FormatInt(counts.bytes, 10))
	}
	if showChars {
		fields = append(fields, strconv.FormatInt(counts.chars, 10))
	}
	if name != "" {
		fields = append(fields, name)
	}
	_, err := fmt.Fprintln(w, strings.Join(fields, " "))
	return err
}

func writeEnvironment(w io.Writer, current expand.Environ, nulSeparated bool) error {
	values := make(map[string]string)
	current.Each(func(name string, vr expand.Variable) bool {
		if vr.Exported && vr.IsSet() && vr.Kind == expand.String {
			values[name] = vr.Str
		} else {
			delete(values, name)
		}
		return true
	})
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if nulSeparated {
			if _, err := fmt.Fprintf(w, "%s=%s\x00", name, values[name]); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "%s=%s\n", name, values[name]); err != nil {
			return err
		}
	}
	return nil
}
