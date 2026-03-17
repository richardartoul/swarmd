package coreutils

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/richardartoul/swarmd/pkg/sh/internal"
	lsfmt "github.com/u-root/u-root/pkg/ls"
)

func runLs(env *commandEnv, args []string) error {
	showAll := false
	human := false
	directoryOnly := false
	longForm := false
	quoted := false
	recurse := false
	classify := false
	sortBySize := false

	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "a", Names: []string{"-a"}},
		{Canonical: "h", Names: []string{"-h"}},
		{Canonical: "d", Names: []string{"-d"}},
		{Canonical: "l", Names: []string{"-l"}},
		{Canonical: "Q", Names: []string{"-Q"}},
		{Canonical: "R", Names: []string{"-R"}},
		{Canonical: "F", Names: []string{"-F"}},
		{Canonical: "S", Names: []string{"-S"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "a":
			showAll = true
		case "h":
			human = true
		case "d":
			directoryOnly = true
		case "l":
			longForm = true
		case "Q":
			quoted = true
		case "R":
			recurse = true
		case "F":
			classify = true
		case "S":
			sortBySize = true
		}
	}

	names := operands
	if len(names) == 0 {
		names = []string{"."}
	}

	var stringer lsfmt.Stringer = lsfmt.NameStringer{}
	if quoted {
		stringer = lsfmt.QuotedStringer{}
	}
	if longForm {
		stringer = lsfmt.LongStringer{Human: human, Name: stringer}
	}

	tw := tabwriter.NewWriter(env.stdout(), 0, 0, 1, ' ', 0)
	defer tw.Flush()

	type entry struct {
		name string
		info fs.FileInfo
	}
	for index, name := range names {
		resolvedPath, err := env.resolvePathArg(name)
		if err != nil {
			return err
		}
		info, err := env.hc.FileSystem.Lstat(resolvedPath)
		if err != nil {
			fmt.Fprintln(tw, err)
			continue
		}
		if len(names) > 1 && info.IsDir() && !directoryOnly && !recurse {
			if index > 0 {
				fmt.Fprintln(tw)
			}
			fmt.Fprintf(tw, "%s:\n", name)
		}

		if recurse {
			err := walkResolved(env.hc.FileSystem, resolvedPath, func(currentPath string, currentInfo fs.FileInfo, walkErr error) error {
				if walkErr != nil {
					fmt.Fprintln(tw, walkErr)
					return nil
				}
				rel, err := filepath.Rel(resolvedPath, currentPath)
				if err != nil {
					return err
				}
				displayName := joinDisplayPath(name, rel)
				printLsRecord(tw, stringer, displayName, currentInfo, showAll, classify)
				return nil
			})
			if err != nil {
				return err
			}
			continue
		}

		if !info.IsDir() || directoryOnly {
			printLsRecord(tw, stringer, name, info, showAll, classify)
			continue
		}

		entries, err := env.hc.FileSystem.ReadDir(resolvedPath)
		if err != nil {
			fmt.Fprintln(tw, err)
			continue
		}
		files := make([]entry, 0, len(entries))
		for _, dirEntry := range entries {
			childPath := filepath.Join(resolvedPath, dirEntry.Name())
			childInfo, err := env.hc.FileSystem.Lstat(childPath)
			if err != nil {
				fmt.Fprintln(tw, err)
				continue
			}
			files = append(files, entry{name: dirEntry.Name(), info: childInfo})
		}
		if sortBySize {
			sort.SliceStable(files, func(i, j int) bool {
				return files[i].info.Size() > files[j].info.Size()
			})
		}
		for _, file := range files {
			printLsRecord(tw, stringer, file.name, file.info, showAll, classify)
		}
	}
	return nil
}

func printLsRecord(writer io.Writer, stringer lsfmt.Stringer, name string, info fs.FileInfo, showAll, classify bool) {
	if !showAll && strings.HasPrefix(name, ".") {
		return
	}
	record := lsfmt.FromOSFileInfo(name, info)
	record.Name = name
	if classify {
		record.Name += lsIndicator(record)
	}
	fmt.Fprintln(writer, stringer.FileString(record))
}

func lsIndicator(info lsfmt.FileInfo) string {
	if info.Mode.IsRegular() && info.Mode&0o111 != 0 {
		return "*"
	}
	if info.Mode&os.ModeDir != 0 {
		return "/"
	}
	if info.Mode&os.ModeSymlink != 0 {
		return "@"
	}
	if info.Mode&os.ModeSocket != 0 {
		return "="
	}
	if info.Mode&os.ModeNamedPipe != 0 {
		return "|"
	}
	return ""
}

func runFind(env *commandEnv, args []string) error {
	roots, query, err := parseFindArgs(args)
	if err != nil {
		return err
	}
	longStringer := lsfmt.LongStringer{Human: true, Name: lsfmt.NameStringer{}}
	var finalErr error
	for _, rootRaw := range roots {
		rootPath, err := env.resolvePathArg(rootRaw)
		if err != nil {
			finalErr = errors.Join(finalErr, err)
			continue
		}
		err = walkResolved(env.hc.FileSystem, rootPath, func(currentPath string, info fs.FileInfo, walkErr error) error {
			rel, err := filepath.Rel(rootPath, currentPath)
			if err != nil {
				return err
			}
			if query.hasMaxDepth && findDepth(rel) > query.maxDepth {
				if info != nil && info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			displayName := joinDisplayPath(rootRaw, rel)
			if walkErr != nil {
				fmt.Fprintf(env.stderr(), "%s: %v\n", displayName, walkErr)
				return nil
			}
			if query.debug {
				fmt.Fprintf(env.stderr(), "find: checking %s\n", displayName)
			}
			match, err := query.matches(info, displayName)
			if err != nil {
				return err
			}
			if !match {
				return nil
			}
			if query.longForm {
				record := lsfmt.FromOSFileInfo(displayName, info)
				record.Name = displayName
				fmt.Fprintln(env.stdout(), longStringer.FileString(record))
				return nil
			}
			fmt.Fprintln(env.stdout(), displayName)
			return nil
		})
		if err != nil {
			finalErr = errors.Join(finalErr, err)
		}
	}
	return finalErr
}

type findPermMode uint8

const (
	findPermNone findPermMode = iota
	findPermExact
	findPermAll
)

type findMatcher func(info fs.FileInfo, displayName string) (bool, error)

type findQuery struct {
	matcher     findMatcher
	maxDepth    int
	hasMaxDepth bool
	longForm    bool
	debug       bool
}

type findParser struct {
	args  []string
	pos   int
	query *findQuery
}

func parseFindArgs(args []string) ([]string, findQuery, error) {
	var roots []string
	i := 0
	for i < len(args) && !isFindExpressionToken(args[i]) {
		roots = append(roots, args[i])
		i++
	}
	if len(roots) == 0 {
		query, consumed, err := parseFindExpression(args)
		if err == nil && consumed == len(args) {
			return []string{"."}, query, nil
		}
		if consumed > 0 && consumed < len(args) && areFindRoots(args[consumed:]) {
			return args[consumed:], query, nil
		}
		if err != nil {
			return nil, findQuery{}, err
		}
		return []string{"."}, query, nil
	}
	query, _, err := parseFindExpression(args[i:])
	return roots, query, err
}

func parseFindExpression(args []string) (findQuery, int, error) {
	query := findQuery{}
	if len(args) == 0 {
		return query, 0, nil
	}
	parser := findParser{
		args:  args,
		query: &query,
	}
	matcher, err := parser.parseOr()
	query.matcher = matcher
	return query, parser.pos, err
}

func isFindExpressionToken(arg string) bool {
	if strings.HasPrefix(arg, "-") && arg != "-" {
		return true
	}
	switch arg {
	case "(", ")", "-name", "-iname", "-type", "-path", "-ipath", "-maxdepth", "-perm", "-mode", "-l", "-d", "-a", "-and", "-o", "-or", "-print":
		return true
	default:
		return false
	}
}

func areFindRoots(args []string) bool {
	for _, arg := range args {
		if isFindExpressionToken(arg) {
			return false
		}
	}
	return len(args) > 0
}

func parseFindPerm(value string) (findPermMode, uint32, error) {
	mode := findPermExact
	if strings.HasPrefix(value, "-") {
		mode = findPermAll
		value = value[1:]
	}
	expr, err := parseModeExpression(value)
	if err != nil || !expr.absolute {
		return findPermNone, 0, fmt.Errorf("find: invalid permission %q", value)
	}
	return mode, expr.bits, nil
}

func (q findQuery) matches(info fs.FileInfo, displayName string) (bool, error) {
	if q.matcher == nil {
		return true, nil
	}
	return q.matcher(info, displayName)
}

func matchFindPath(pattern, displayName string, ignoreCase bool) (bool, error) {
	name := filepath.ToSlash(displayName)
	candidates := []string{name}
	switch name {
	case ".":
		candidates = append(candidates, "./")
	default:
		if !strings.HasPrefix(name, "./") {
			candidates = append(candidates, "./"+name)
		}
	}
	for _, candidate := range candidates {
		match, err := matchFindPattern(pattern, candidate, ignoreCase)
		if err != nil {
			return false, err
		}
		if match {
			return true, nil
		}
	}
	return false, nil
}

func matchFindPattern(pattern, candidate string, ignoreCase bool) (bool, error) {
	if ignoreCase {
		pattern = strings.ToLower(pattern)
		candidate = strings.ToLower(candidate)
	}
	return path.Match(pattern, candidate)
}

func (p *findParser) parseOr() (findMatcher, error) {
	left, err := p.parseAnd(true)
	if err != nil {
		return nil, err
	}
	for p.pos < len(p.args) {
		switch p.args[p.pos] {
		case "-o", "-or":
			p.pos++
			right, err := p.parseAnd(false)
			if err != nil {
				return nil, err
			}
			left = orFindMatchers(left, right)
		default:
			return left, nil
		}
	}
	return left, nil
}

func (p *findParser) parseAnd(allowEmpty bool) (findMatcher, error) {
	if p.pos >= len(p.args) {
		if allowEmpty {
			return nil, nil
		}
		return nil, fmt.Errorf("find: missing expression")
	}
	if p.args[p.pos] == ")" {
		if allowEmpty {
			return nil, nil
		}
		return nil, fmt.Errorf("find: missing expression before )")
	}
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for p.pos < len(p.args) {
		switch p.args[p.pos] {
		case "-o", "-or", ")":
			return left, nil
		case "-a", "-and":
			p.pos++
		}
		if p.pos >= len(p.args) {
			return nil, fmt.Errorf("find: missing expression")
		}
		if p.args[p.pos] == ")" {
			return nil, fmt.Errorf("find: missing expression before )")
		}
		right, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		left = andFindMatchers(left, right)
	}
	return left, nil
}

func (p *findParser) parsePrimary() (findMatcher, error) {
	if p.pos >= len(p.args) {
		return nil, fmt.Errorf("find: missing expression")
	}
	switch p.args[p.pos] {
	case "(":
		p.pos++
		expr, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if expr == nil {
			return nil, fmt.Errorf("find: empty expression")
		}
		if p.pos >= len(p.args) || p.args[p.pos] != ")" {
			return nil, fmt.Errorf("find: expected )")
		}
		p.pos++
		return expr, nil
	case ")":
		return nil, fmt.Errorf("find: unexpected )")
	case "-a", "-and", "-o", "-or":
		return nil, fmt.Errorf("find: missing expression before %q", p.args[p.pos])
	case "-name":
		pattern, err := p.takeValue("-name")
		if err != nil {
			return nil, err
		}
		return findNameMatcher(pattern, false), nil
	case "-iname":
		pattern, err := p.takeValue("-iname")
		if err != nil {
			return nil, err
		}
		return findNameMatcher(pattern, true), nil
	case "-type":
		fileType, err := p.takeValue("-type")
		if err != nil {
			return nil, err
		}
		return findTypeMatcher(fileType), nil
	case "-path":
		pattern, err := p.takeValue("-path")
		if err != nil {
			return nil, err
		}
		return findPathMatcher(pattern, false), nil
	case "-ipath":
		pattern, err := p.takeValue("-ipath")
		if err != nil {
			return nil, err
		}
		return findPathMatcher(pattern, true), nil
	case "-maxdepth":
		value, err := p.takeValue("-maxdepth")
		if err != nil {
			return nil, err
		}
		depth, err := parseNonNegativeCount(value)
		if err != nil {
			return nil, fmt.Errorf("find: invalid maxdepth %q", value)
		}
		p.query.maxDepth = depth
		p.query.hasMaxDepth = true
		return matchAllFind, nil
	case "-perm", "-mode":
		flag := p.args[p.pos]
		value, err := p.takeValue(flag)
		if err != nil {
			return nil, err
		}
		permMode, bits, err := parseFindPerm(value)
		if err != nil {
			return nil, err
		}
		return findPermMatcher(permMode, bits), nil
	case "-l":
		p.query.longForm = true
		p.pos++
		return matchAllFind, nil
	case "-d":
		p.query.debug = true
		p.pos++
		return matchAllFind, nil
	case "-print":
		p.pos++
		return matchAllFind, nil
	default:
		if strings.HasPrefix(p.args[p.pos], "-") && p.args[p.pos] != "-" {
			return nil, formatUtilityOptionError(&internal.UnknownOptionError{Option: p.args[p.pos]})
		}
		return nil, fmt.Errorf("find: invalid expression %q", p.args[p.pos])
	}
}

func (p *findParser) takeValue(flag string) (string, error) {
	if p.pos+1 >= len(p.args) {
		return "", fmt.Errorf("flag needs an argument: %s", flag)
	}
	value := p.args[p.pos+1]
	p.pos += 2
	return value, nil
}

func matchAllFind(fs.FileInfo, string) (bool, error) {
	return true, nil
}

func andFindMatchers(left, right findMatcher) findMatcher {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	return func(info fs.FileInfo, displayName string) (bool, error) {
		match, err := left(info, displayName)
		if err != nil || !match {
			return match, err
		}
		return right(info, displayName)
	}
}

func orFindMatchers(left, right findMatcher) findMatcher {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	return func(info fs.FileInfo, displayName string) (bool, error) {
		match, err := left(info, displayName)
		if err != nil {
			return false, err
		}
		if match {
			return true, nil
		}
		return right(info, displayName)
	}
}

func findNameMatcher(pattern string, ignoreCase bool) findMatcher {
	return func(info fs.FileInfo, displayName string) (bool, error) {
		return matchFindPattern(pattern, path.Base(filepath.ToSlash(displayName)), ignoreCase)
	}
}

func findPathMatcher(pattern string, ignoreCase bool) findMatcher {
	return func(info fs.FileInfo, displayName string) (bool, error) {
		return matchFindPath(pattern, displayName, ignoreCase)
	}
}

func findTypeMatcher(fileType string) findMatcher {
	return func(info fs.FileInfo, displayName string) (bool, error) {
		fileTypes := map[string]os.FileMode{
			"f":         0,
			"file":      0,
			"d":         os.ModeDir,
			"directory": os.ModeDir,
			"s":         os.ModeSocket,
			"p":         os.ModeNamedPipe,
			"l":         os.ModeSymlink,
			"c":         os.ModeCharDevice | os.ModeDevice,
			"b":         os.ModeDevice,
		}
		typeMode, ok := fileTypes[fileType]
		if !ok {
			validTypes := make([]string, 0, len(fileTypes))
			for key := range fileTypes {
				validTypes = append(validTypes, key)
			}
			return false, fmt.Errorf("%v is not a valid file type\n valid types are %v", fileType, strings.Join(validTypes, ","))
		}
		if info.Mode()&os.ModeType != typeMode {
			if typeMode == 0 && !info.Mode().IsRegular() {
				return false, nil
			}
			if typeMode != 0 {
				return false, nil
			}
		}
		return true, nil
	}
}

func findPermMatcher(mode findPermMode, bits uint32) findMatcher {
	return func(info fs.FileInfo, displayName string) (bool, error) {
		switch mode {
		case findPermExact:
			return modeBits(info.Mode())&0o7777 == bits, nil
		case findPermAll:
			return modeBits(info.Mode())&bits == bits, nil
		default:
			return true, nil
		}
	}
}

func findDepth(rel string) int {
	rel = filepath.ToSlash(rel)
	if rel == "" || rel == "." {
		return 0
	}
	return strings.Count(rel, "/") + 1
}

func runChmod(env *commandEnv, args []string) error {
	recursive := false
	reference := ""
	parsedOpts, operands, err := parseUtilityOptionsToFirstOperand(args, []internal.OptionSpec{
		{Canonical: "recursive", Names: []string{"-R", "--recursive"}},
		{Canonical: "reference", Names: []string{"--reference"}, ValueMode: internal.RequiredOptionValue},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "recursive":
			recursive = true
		case "reference":
			reference = opt.Value
		}
	}

	chmodFS, err := env.chmodFS()
	if err != nil {
		return err
	}

	var (
		referenceMode os.FileMode
		expr          modeExpression
		targets       []string
	)
	if reference != "" {
		if len(operands) < 1 {
			return usageError(env, 1,
				"usage: chmod [-R] mode file ...",
				"       chmod [-R] --reference=reference_file file ...",
			)
		}
		referencePath, err := env.resolvePathArg(reference)
		if err != nil {
			return err
		}
		info, err := env.hc.FileSystem.Stat(referencePath)
		if err != nil {
			return fmt.Errorf("bad reference file: %w", err)
		}
		referenceMode = info.Mode()
		targets = operands
	} else {
		if len(operands) < 2 {
			return usageError(env, 1,
				"usage: chmod [-R] mode file ...",
				"       chmod [-R] --reference=reference_file file ...",
			)
		}
		expr, err = parseModeExpression(operands[0])
		if err != nil {
			return err
		}
		targets = operands[1:]
	}

	var finalErr error
	changeMode := func(resolvedPath string) error {
		if reference != "" {
			return chmodFS.Chmod(resolvedPath, referenceMode)
		}
		info, err := env.hc.FileSystem.Stat(resolvedPath)
		if err != nil {
			return err
		}
		newMode := expr.Apply(info.Mode(), info.IsDir())
		return chmodFS.Chmod(resolvedPath, newMode)
	}

	for _, name := range targets {
		resolvedPath, err := env.resolvePathArg(name)
		if err != nil {
			return err
		}
		if recursive {
			err = walkResolved(env.hc.FileSystem, resolvedPath, func(currentPath string, info fs.FileInfo, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				return changeMode(currentPath)
			})
		} else {
			err = changeMode(resolvedPath)
		}
		if err != nil {
			finalErr = err
			fmt.Fprintln(env.stderr(), err)
		}
	}
	return finalErr
}

func runCp(env *commandEnv, args []string) error {
	recursive := false
	interactive := false
	force := false
	verbose := false
	noFollowSymlinks := false

	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "recursive", Names: []string{"-R", "-r", "--recursive", "--RECURSIVE"}},
		{Canonical: "interactive", Names: []string{"-i", "--interactive"}},
		{Canonical: "force", Names: []string{"-f", "--force"}},
		{Canonical: "verbose", Names: []string{"-v", "--verbose"}},
		{Canonical: "no-dereference", Names: []string{"-P", "--no-dereference"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "recursive":
			recursive = true
		case "interactive":
			interactive = true
		case "force":
			force = true
		case "verbose":
			verbose = true
		case "no-dereference":
			noFollowSymlinks = true
		}
	}
	if len(operands) < 2 {
		return fmt.Errorf("insufficient arguments")
	}

	inputs, err := env.resolvePaths(operands[:len(operands)-1])
	if err != nil {
		return err
	}
	dest := operands[len(operands)-1]
	destResolved, err := env.resolvePathArg(dest)
	if err != nil {
		return err
	}
	destInfo, destErr := env.hc.FileSystem.Stat(destResolved)
	destIsDir := destErr == nil && destInfo.IsDir()
	if len(inputs) > 1 && !destIsDir {
		return fmt.Errorf("target %q is not a directory", dest)
	}

	var lastErr error
	for _, src := range inputs {
		targetPath := destResolved
		if destIsDir {
			targetPath = filepath.Join(destResolved, filepath.Base(src.raw))
		}
		if recursive {
			lastErr = copyTree(env, src, targetPath, interactive, force, verbose, noFollowSymlinks)
		} else {
			lastErr = copyResolvedEntry(env, src.path, targetPath, src.raw, interactive, force, verbose, noFollowSymlinks, false)
		}
		if lastErr != nil {
			return lastErr
		}
	}
	return nil
}

func copyTree(env *commandEnv, source resolvedPath, destPath string, interactive, force, verbose, noFollowSymlinks bool) error {
	return walkResolved(env.hc.FileSystem, source.path, func(currentPath string, info fs.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source.path, currentPath)
		if err != nil {
			return err
		}
		targetPath := destPath
		if rel != "." {
			targetPath = filepath.Join(destPath, rel)
		}
		return copyResolvedEntry(env, currentPath, targetPath, joinDisplayPath(source.raw, rel), interactive, force, verbose, noFollowSymlinks, true)
	})
}

func copyResolvedEntry(env *commandEnv, sourcePath, destPath, displaySource string, interactive, force, verbose, noFollowSymlinks, recursive bool) error {
	sourceInfo, err := copyStat(env, sourcePath, noFollowSymlinks)
	if err != nil {
		return err
	}
	if sourceInfo.IsDir() && !recursive {
		fmt.Fprintf(env.stderr(), "cp: -r not specified, omitting directory %s\n", sourcePath)
		return nil
	}

	destInfo, destErr := env.hc.FileSystem.Stat(destPath)
	destExists := destErr == nil
	if destErr != nil && !errors.Is(destErr, fs.ErrNotExist) {
		return destErr
	}
	if destExists && sameFile(env.hc.FileSystem, sourcePath, sourceInfo, destPath, destInfo) {
		fmt.Fprintf(env.stderr(), "cp: %q and %q are the same file\n", sourcePath, destPath)
		return nil
	}
	if destExists && interactive && !force {
		fmt.Fprintf(env.stderr(), "cp: overwrite %q? ", destPath)
		reader := bufio.NewReader(env.stdin())
		answer, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if answer == "" || strings.ToLower(answer[:1]) != "y" {
			return nil
		}
	}

	mutableFS, err := env.mutableFS()
	if err != nil {
		return err
	}
	switch {
	case sourceInfo.IsDir():
		if err := mutableFS.MkdirAll(destPath, sourceInfo.Mode().Perm()); err != nil {
			return err
		}
	case sourceInfo.Mode().IsRegular():
		srcFile, err := env.hc.FileSystem.Open(sourcePath)
		if err != nil {
			return err
		}
		defer srcFile.Close()
		dstFile, err := env.hc.FileSystem.OpenFile(destPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, sourceInfo.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(dstFile, srcFile); err != nil {
			dstFile.Close()
			return err
		}
		if err := dstFile.Close(); err != nil {
			return err
		}
	case sourceInfo.Mode()&os.ModeSymlink == os.ModeSymlink:
		readlinkFS, err := env.readlinkFS()
		if err != nil {
			return err
		}
		if force && destExists {
			if err := env.hc.FileSystem.Remove(destPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		linkTarget, err := readlinkFS.Readlink(sourcePath)
		if err != nil {
			return err
		}
		if err := mutableFS.Symlink(linkTarget, destPath); err != nil {
			return err
		}
	default:
		return &os.PathError{
			Op:   "copy",
			Path: sourcePath,
			Err:  fmt.Errorf("unsupported file mode %s", sourceInfo.Mode()),
		}
	}
	if verbose {
		fmt.Fprintf(env.stdout(), "%q -> %q\n", sourcePath, destPath)
	}
	return nil
}

func copyStat(env *commandEnv, path string, noFollowSymlinks bool) (fs.FileInfo, error) {
	if noFollowSymlinks {
		return env.hc.FileSystem.Lstat(path)
	}
	return env.hc.FileSystem.Stat(path)
}

func runMv(env *commandEnv, args []string) error {
	update := false
	noClobber := false

	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "update", Names: []string{"-u", "--update"}},
		{Canonical: "no-clobber", Names: []string{"-n", "--no-clobber"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "update":
			update = true
		case "no-clobber":
			noClobber = true
		}
	}
	if len(operands) < 2 {
		return fmt.Errorf("insufficient arguments")
	}

	renameFS, err := env.renameFS()
	if err != nil {
		return err
	}
	inputs, err := env.resolvePaths(operands[:len(operands)-1])
	if err != nil {
		return err
	}
	destRaw := operands[len(operands)-1]
	destResolved, err := env.resolvePathArg(destRaw)
	if err != nil {
		return err
	}
	destInfo, destErr := env.hc.FileSystem.Lstat(destResolved)
	destIsDir := destErr == nil && destInfo.IsDir()
	if len(inputs) > 1 && !destIsDir {
		return fmt.Errorf("not a directory: %s", destResolved)
	}

	for _, source := range inputs {
		targetPath := destResolved
		if destIsDir {
			targetPath = filepath.Join(destResolved, filepath.Base(source.raw))
		}
		if noClobber {
			if _, err := env.hc.FileSystem.Lstat(targetPath); err == nil {
				continue
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		if update {
			sourceInfo, err := env.hc.FileSystem.Lstat(source.path)
			if err != nil {
				return err
			}
			targetInfo, err := env.hc.FileSystem.Lstat(targetPath)
			if err == nil && targetInfo.ModTime().After(sourceInfo.ModTime()) {
				continue
			}
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		if err := renameFS.Rename(source.path, targetPath); err != nil {
			return err
		}
	}
	return nil
}
