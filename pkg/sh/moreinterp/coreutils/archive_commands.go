package coreutils

import (
	"archive/tar"
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/pgzip"
	"github.com/richardartoul/swarmd/pkg/sh/internal"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
	pkggzip "github.com/u-root/u-root/pkg/gzip"
)

type gzipInput struct {
	raw      string
	resolved string
	stdin    bool
}

func runMktemp(env *commandEnv, args []string) error {
	makeDir := false
	dryRun := false
	quiet := false
	compatPrefix := ""
	prefix := ""
	suffix := ""
	tmpDir := ""

	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "directory", Names: []string{"-d", "--directory"}},
		{Canonical: "dry-run", Names: []string{"-u", "--dry-run"}},
		{Canonical: "quiet", Names: []string{"-q", "--quiet"}},
		{Canonical: "compat-prefix", Names: []string{"-t"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "prefix", Names: []string{"-s", "--prefix"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "suffix", Names: []string{"--suffix"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "tmpdir", Names: []string{"-p", "--tmpdir"}, ValueMode: internal.RequiredOptionValue},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "directory":
			makeDir = true
		case "dry-run":
			dryRun = true
		case "quiet":
			quiet = true
		case "compat-prefix":
			compatPrefix = opt.Value
		case "prefix":
			prefix = opt.Value
		case "suffix":
			suffix = opt.Value
		case "tmpdir":
			tmpDir = opt.Value
		}
	}
	if compatPrefix != "" {
		prefix = compatPrefix + prefix
	}
	switch len(operands) {
	case 0:
	case 1:
		prefix = prefix + strings.Split(operands[0], "X")[0] + suffix
	default:
		return fmt.Errorf("too many arguments")
	}
	if tmpDir != "" {
		tmpDir, err = env.resolvePathArg(tmpDir)
		if err != nil {
			return err
		}
	} else if tempDirFS, ok := env.hc.FileSystem.(interp.TempDirFileSystem); ok {
		tmpDir = tempDirFS.TempDir()
	} else if envValue := env.hc.Env.Get("TMPDIR"); envValue.Set {
		if filepath.IsAbs(envValue.Str) {
			tmpDir = filepath.Clean(envValue.Str)
		} else {
			tmpDir, err = env.resolvePathArg(envValue.Str)
			if err != nil {
				return err
			}
		}
	} else {
		tmpDir = filepath.Clean(os.TempDir())
	}

	fileName := ""
	if dryRun {
		if !quiet {
			fmt.Fprintln(env.stderr(), "mktemp: dry-run requested; no file or directory created")
		}
	} else if makeDir {
		fileName, err = createTempDir(env, tmpDir, prefix)
	} else {
		fileName, err = createTempFile(env, tmpDir, prefix)
	}
	if err != nil {
		if quiet {
			return nil
		}
		return err
	}
	_, err = fmt.Fprintf(env.stdout(), "%s\n", fileName)
	return err
}

func createTempFile(env *commandEnv, dir, prefix string) (string, error) {
	for attempts := 0; attempts < 10000; attempts++ {
		path := filepath.Join(dir, prefix+randomTempSuffix())
		file, err := env.hc.FileSystem.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if closeErr := file.Close(); closeErr != nil {
				return "", closeErr
			}
			return path, nil
		}
		if errors.Is(err, fs.ErrExist) || errors.Is(err, os.ErrExist) {
			continue
		}
		return "", err
	}
	return "", fmt.Errorf("mktemp: too many collisions")
}

func createTempDir(env *commandEnv, dir, prefix string) (string, error) {
	mutableFS, err := env.mutableFS()
	if err != nil {
		return "", err
	}
	for attempts := 0; attempts < 10000; attempts++ {
		path := filepath.Join(dir, prefix+randomTempSuffix())
		err := mutableFS.Mkdir(path, 0o700)
		if err == nil {
			return path, nil
		}
		if errors.Is(err, fs.ErrExist) || errors.Is(err, os.ErrExist) {
			continue
		}
		return "", err
	}
	return "", fmt.Errorf("mktemp: too many collisions")
}

func randomTempSuffix() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

func runGzip(env *commandEnv, args []string) error {
	return runGzipProgram(env, "gzip", args)
}

func runGunzip(env *commandEnv, args []string) error {
	return runGzipProgram(env, "gunzip", args)
}

func runGzcat(env *commandEnv, args []string) error {
	return runGzipProgram(env, "gzcat", args)
}

func runGzipProgram(env *commandEnv, prog string, args []string) error {
	opts := pkggzip.Options{
		Suffix:    ".gz",
		Blocksize: 128,
		Level:     pgzip.DefaultCompression,
		Processes: runtime.NumCPU(),
	}
	help := false
	level := 0
	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "b", Names: []string{"-b"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "d", Names: []string{"-d"}},
		{Canonical: "f", Names: []string{"-f"}},
		{Canonical: "h", Names: []string{"-h"}},
		{Canonical: "k", Names: []string{"-k"}},
		{Canonical: "p", Names: []string{"-p"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "q", Names: []string{"-q"}},
		{Canonical: "c", Names: []string{"-c"}},
		{Canonical: "S", Names: []string{"-S"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "t", Names: []string{"-t"}},
		{Canonical: "v", Names: []string{"-v"}},
		{Canonical: "1", Names: []string{"-1"}},
		{Canonical: "2", Names: []string{"-2"}},
		{Canonical: "3", Names: []string{"-3"}},
		{Canonical: "4", Names: []string{"-4"}},
		{Canonical: "5", Names: []string{"-5"}},
		{Canonical: "6", Names: []string{"-6"}},
		{Canonical: "7", Names: []string{"-7"}},
		{Canonical: "8", Names: []string{"-8"}},
		{Canonical: "9", Names: []string{"-9"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "b":
			opts.Blocksize, err = strconv.Atoi(opt.Value)
			if err != nil {
				return fmt.Errorf("%s: invalid value for -b: %q", prog, opt.Value)
			}
		case "d":
			opts.Decompress = true
		case "f":
			opts.Force = true
		case "h":
			help = true
		case "k":
			opts.Keep = true
		case "p":
			opts.Processes, err = strconv.Atoi(opt.Value)
			if err != nil {
				return fmt.Errorf("%s: invalid value for -p: %q", prog, opt.Value)
			}
		case "q":
			opts.Quiet = true
		case "c":
			opts.Stdout = true
		case "S":
			opts.Suffix = opt.Value
		case "t":
			opts.Test = true
		case "v":
			opts.Verbose = true
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			if level != 0 {
				return fmt.Errorf("error: multiple compression levels specified")
			}
			level, err = strconv.Atoi(opt.Canonical)
			if err != nil {
				return err
			}
		}
	}
	if level != 0 {
		opts.Level = level
	}
	if help {
		_, err := fmt.Fprintf(env.stderr(), "Usage of %s:\n", prog)
		return err
	}
	moreArgs := len(operands) > 0
	if !moreArgs && !opts.Force {
		return fmt.Errorf("%s: %w", prog, pkggzip.ErrStdoutNoForce)
	}
	if opts.Test {
		opts.Decompress = true
	}
	switch filepath.Base(prog) {
	case "gunzip":
		opts.Decompress = true
	case "gzcat":
		opts.Decompress = true
		opts.Stdout = true
	}
	if !moreArgs {
		opts.Stdin = true
		opts.Stdout = true
	}

	var inputs []gzipInput
	if len(operands) == 0 {
		inputs = append(inputs, gzipInput{stdin: true})
	} else {
		for _, arg := range operands {
			resolvedPath, err := env.resolvePathArg(arg)
			if err != nil {
				return err
			}
			inputs = append(inputs, gzipInput{raw: arg, resolved: resolvedPath})
		}
	}

	for _, input := range inputs {
		if err := processGzipInput(env, prog, &opts, input); err != nil {
			if !opts.Quiet {
				fmt.Fprintln(env.stderr(), err)
			}
		}
	}
	return nil
}

func processGzipInput(env *commandEnv, prog string, opts *pkggzip.Options, input gzipInput) error {
	if !input.stdin {
		if _, err := env.hc.FileSystem.Stat(input.resolved); err != nil {
			return err
		}
		if !opts.Force {
			if opts.Decompress && !strings.HasSuffix(input.resolved, opts.Suffix) {
				return fmt.Errorf("%q does not have %q suffix", input.resolved, opts.Suffix)
			}
			if !opts.Decompress && strings.HasSuffix(input.resolved, opts.Suffix) {
				return fmt.Errorf("%q already has %q suffix", input.resolved, opts.Suffix)
			}
		}
	}

	if opts.Stdout && !opts.Decompress && !opts.Force {
		if file, ok := env.stdout().(interface{ Stat() (fs.FileInfo, error) }); ok {
			if info, err := file.Stat(); err == nil && (info.Mode()&os.ModeDevice) != 0 {
				return fmt.Errorf("can not write compressed data to a terminal/device (use -f to force)")
			}
		}
	}

	outputPath := gzipOutputPath(input.resolved, opts)
	if !opts.Stdout && !opts.Test && !opts.Force {
		if _, err := env.hc.FileSystem.Stat(outputPath); err == nil {
			return fmt.Errorf("%s already exists", outputPath)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}

	var (
		inputReader io.Reader
		inputClose  io.Closer
		inputName   = input.resolved
	)
	if input.stdin {
		inputReader = env.stdin()
		inputName = "stdin"
	} else {
		file, err := env.hc.FileSystem.Open(input.resolved)
		if err != nil {
			return err
		}
		inputReader = file
		inputClose = file
	}

	var (
		outputWriter io.Writer
		outputClose  io.Closer
		outputName   = outputPath
	)
	switch {
	case opts.Test:
		outputWriter = io.Discard
		outputName = "discard"
	case opts.Stdout:
		outputWriter = env.stdout()
		outputName = "stdout"
	default:
		file, err := env.hc.FileSystem.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o666)
		if err != nil {
			if inputClose != nil {
				inputClose.Close()
			}
			return err
		}
		outputWriter = file
		outputClose = file
	}

	if opts.Verbose && !opts.Quiet {
		fmt.Fprintf(env.stderr(), "%s to %s\n", inputName, outputName)
	}

	var err error
	if opts.Decompress {
		err = pkggzip.Decompress(inputReader, outputWriter, opts.Blocksize, opts.Processes)
	} else {
		err = pkggzip.Compress(inputReader, outputWriter, opts.Level, opts.Blocksize, opts.Processes)
	}
	if inputClose != nil {
		inputClose.Close()
	}
	if outputClose != nil {
		closeErr := outputClose.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		return err
	}
	if !input.stdin && !opts.Keep && !opts.Stdout && !opts.Test {
		return env.hc.FileSystem.Remove(input.resolved)
	}
	return nil
}

func gzipOutputPath(inputPath string, opts *pkggzip.Options) string {
	if opts.Stdout || opts.Test {
		return inputPath
	}
	if opts.Decompress {
		return strings.TrimSuffix(inputPath, opts.Suffix)
	}
	return inputPath + opts.Suffix
}

func runTar(env *commandEnv, args []string) error {
	create := false
	extract := false
	list := false
	fileName := ""
	noRecursion := false
	verbose := false

	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "create", Names: []string{"-c", "--create"}},
		{Canonical: "extract", Names: []string{"-x", "--extract"}},
		{Canonical: "file", Names: []string{"-f", "--file"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "list", Names: []string{"-t", "--list"}},
		{Canonical: "no-recursion", Names: []string{"--no-recursion"}},
		{Canonical: "verbose", Names: []string{"-v", "--verbose"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "create":
			create = true
		case "extract":
			extract = true
		case "file":
			fileName = opt.Value
		case "list":
			list = true
		case "no-recursion":
			noRecursion = true
		case "verbose":
			verbose = true
		}
	}
	if create && extract {
		return fmt.Errorf("cannot supply both -c and -x")
	}
	if create && list {
		return fmt.Errorf("cannot supply both -c and -t")
	}
	if extract && list {
		return fmt.Errorf("cannot supply both -x and -t")
	}
	if !create && !extract && !list {
		return fmt.Errorf("must supply at least one of: -c, -x, -t")
	}
	if fileName == "" {
		return fmt.Errorf("file is required")
	}

	archivePath, err := env.resolvePathArg(fileName)
	if err != nil {
		return err
	}
	switch {
	case create:
		return tarCreate(env, archivePath, operands, noRecursion, verbose)
	case extract:
		if len(operands) != 1 {
			return fmt.Errorf("args length should be 1")
		}
		return tarExtract(env, archivePath, operands[0], verbose)
	case list:
		return tarList(env, archivePath)
	default:
		return nil
	}
}

func tarCreate(env *commandEnv, archivePath string, args []string, noRecursion, verbose bool) error {
	archiveFile, err := env.hc.FileSystem.OpenFile(archivePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	defer archiveFile.Close()

	writer := tar.NewWriter(archiveFile)
	defer writer.Close()

	inputs, err := env.resolvePaths(args)
	if err != nil {
		return err
	}
	for _, input := range inputs {
		err := addTarInput(env, writer, input, noRecursion, verbose)
		if err != nil {
			return err
		}
	}
	return writer.Close()
}

func addTarInput(env *commandEnv, writer *tar.Writer, input resolvedPath, noRecursion, verbose bool) error {
	addPath := func(currentPath string, info fs.FileInfo) error {
		rel, err := filepath.Rel(input.path, currentPath)
		if err != nil {
			return err
		}
		headerName := joinDisplayPath(input.raw, rel)
		if input.raw == "" {
			headerName = filepath.ToSlash(currentPath)
		}
		linkTarget := ""
		if info.Mode()&os.ModeSymlink == os.ModeSymlink {
			readlinkFS, err := env.readlinkFS()
			if err != nil {
				return err
			}
			linkTarget, err = readlinkFS.Readlink(currentPath)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, linkTarget)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(headerName)
		if verbose {
			fmt.Fprintln(env.stdout(), header.Name)
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := env.hc.FileSystem.Open(currentPath)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(writer, file)
		return err
	}

	if noRecursion {
		info, err := env.hc.FileSystem.Lstat(input.path)
		if err != nil {
			return err
		}
		return addPath(input.path, info)
	}
	return walkResolved(env.hc.FileSystem, input.path, func(currentPath string, info fs.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return addPath(currentPath, info)
	})
}

func tarExtract(env *commandEnv, archivePath, destRaw string, verbose bool) error {
	destPath, err := env.resolvePathArg(destRaw)
	if err != nil {
		return err
	}
	archiveFile, err := env.hc.FileSystem.Open(archivePath)
	if err != nil {
		return err
	}
	defer archiveFile.Close()

	mutableFS, err := env.mutableFS()
	if err != nil {
		return err
	}
	if _, err := env.hc.FileSystem.Stat(destPath); errors.Is(err, fs.ErrNotExist) {
		if err := mutableFS.MkdirAll(destPath, os.ModePerm); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	reader := tar.NewReader(archiveFile)
	chmodFS, _ := env.chmodFS()
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if verbose {
			fmt.Fprintln(env.stdout(), header.Name)
		}
		targetPath, err := safeJoin(destPath, header.Name)
		if err != nil {
			fmt.Fprintf(env.stderr(), "Warning: Skipping file %q due to: %v\n", header.Name, err)
			continue
		}
		mode := os.FileMode(header.Mode)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := mutableFS.MkdirAll(targetPath, mode.Perm()); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := mutableFS.MkdirAll(filepath.Dir(targetPath), os.ModePerm); err != nil {
				return err
			}
			file, err := env.hc.FileSystem.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(file, reader); err != nil {
				file.Close()
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := mutableFS.MkdirAll(filepath.Dir(targetPath), os.ModePerm); err != nil {
				return err
			}
			if err := mutableFS.Symlink(header.Linkname, targetPath); err != nil {
				return err
			}
		case tar.TypeLink:
			linkTarget, err := safeJoin(destPath, header.Linkname)
			if err != nil {
				return err
			}
			if err := mutableFS.Link(linkTarget, targetPath); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%q: unknown type %#o", targetPath, header.Typeflag)
		}
		if chmodFS != nil && header.Typeflag != tar.TypeSymlink {
			if err := chmodFS.Chmod(targetPath, mode); err != nil {
				return err
			}
		}
		if !header.ModTime.IsZero() {
			if err := mutableFS.Chtimes(targetPath, header.ModTime, header.ModTime); err != nil && header.Typeflag != tar.TypeSymlink {
				return err
			}
		}
	}
}

func tarList(env *commandEnv, archivePath string) error {
	archiveFile, err := env.hc.FileSystem.Open(archivePath)
	if err != nil {
		return err
	}
	defer archiveFile.Close()

	reader := tar.NewReader(archiveFile)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(env.stdout(), header.Name)
	}
}

func safeJoin(root, name string) (string, error) {
	cleanRoot := filepath.Clean(root)
	target := filepath.Clean(filepath.Join(cleanRoot, filepath.FromSlash(name)))
	rel, err := filepath.Rel(cleanRoot, target)
	if err != nil {
		return "", err
	}
	parentPrefix := ".." + string(os.PathSeparator)
	if rel == ".." || strings.HasPrefix(rel, parentPrefix) {
		return "", fmt.Errorf("path escapes extraction root")
	}
	return target, nil
}

func runXargs(env *commandEnv, args []string) error {
	maxArgs := 5000
	trace := false
	prompt := false
	nullDelimited := false
	parsedOpts, commandArgs, err := parseUtilityOptionsToFirstOperand(args, []internal.OptionSpec{
		{Canonical: "n", Names: []string{"-n"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "t", Names: []string{"-t"}},
		{Canonical: "p", Names: []string{"-p"}},
		{Canonical: "0", Names: []string{"-0"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "n":
			maxArgs, err = strconv.Atoi(opt.Value)
			if err != nil || maxArgs <= 0 {
				return fmt.Errorf("xargs: invalid value for -n: %q", opt.Value)
			}
		case "t":
			trace = true
		case "p":
			prompt = true
		case "0":
			nullDelimited = true
		}
	}
	if prompt {
		trace = true
	}
	if len(commandArgs) == 0 {
		commandArgs = []string{"echo"}
	}

	var xargs []string
	if nullDelimited {
		reader := bufio.NewReader(env.stdin())
		for {
			chunk, err := reader.ReadBytes(0x00)
			if err != nil && err != io.EOF {
				return err
			}
			if len(chunk) > 0 {
				if chunk[len(chunk)-1] == 0x00 {
					chunk = chunk[:len(chunk)-1]
				}
				xargs = append(xargs, string(chunk))
			}
			if err == io.EOF {
				break
			}
		}
	} else {
		input, err := io.ReadAll(env.stdin())
		if err != nil {
			return err
		}
		xargs, err = splitXargsInput(string(input))
		if err != nil {
			return err
		}
	}

	var ttyScanner *bufio.Scanner
	if prompt {
		ttyFile, err := env.hc.FileSystem.Open("/dev/tty")
		if err != nil {
			return err
		}
		defer ttyFile.Close()
		ttyScanner = bufio.NewScanner(ttyFile)
	}

	baseLen := len(commandArgs)
	for i, ran := 0, false; i < len(xargs) || !ran; {
		ran = true
		m := min(i+maxArgs, len(xargs))
		currentArgs := append(append([]string(nil), commandArgs...), xargs[i:m]...)
		if prompt {
			fmt.Fprintf(env.stderr(), "%s...?", strings.Join(currentArgs, " "))
		} else if trace {
			fmt.Fprintln(env.stderr(), strings.Join(currentArgs, " "))
		}
		if prompt && ttyScanner.Scan() {
			answer := ttyScanner.Text()
			if !strings.HasPrefix(answer, "y") && !strings.HasPrefix(answer, "Y") {
				continue
			}
		}
		if err := env.dispatch(currentArgs); err != nil {
			return err
		}
		commandArgs = commandArgs[:baseLen]
		if len(xargs) == 0 {
			break
		}
		i = m
	}
	return nil
}
