package coreutils

import (
	"bufio"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	stdbase64 "encoding/base64"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/richardartoul/swarmd/pkg/sh/internal"
)

func runCat(env *commandEnv, args []string) error {
	_, files, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "u", Names: []string{"-u"}},
	})
	if err != nil {
		return err
	}
	if len(files) == 0 {
		_, err = io.Copy(env.stdout(), env.stdin())
		return err
	}
	for _, file := range files {
		if file == "-" {
			if _, err := io.Copy(env.stdout(), env.stdin()); err != nil {
				return err
			}
			continue
		}
		resolvedPath, err := env.resolvePathArg(file)
		if err != nil {
			return err
		}
		reader, err := env.hc.FileSystem.Open(resolvedPath)
		if err != nil {
			return err
		}
		if _, err := io.Copy(env.stdout(), reader); err != nil {
			reader.Close()
			return err
		}
		if err := reader.Close(); err != nil {
			return err
		}
	}
	return nil
}

func runBase64(env *commandEnv, args []string) error {
	decode := false

	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "d", Names: []string{"-d"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		if opt.Canonical == "d" {
			decode = true
		}
	}

	var reader io.Reader = env.stdin()
	switch len(operands) {
	case 0:
	case 1:
		resolvedPath, err := env.resolvePathArg(operands[0])
		if err != nil {
			return err
		}
		file, err := env.hc.FileSystem.Open(resolvedPath)
		if err != nil {
			return err
		}
		defer file.Close()
		reader = file
	default:
		return errors.New("usage: base64 [-d] [file]")
	}

	if decode {
		decoder := stdbase64.NewDecoder(stdbase64.StdEncoding, reader)
		_, err := io.Copy(env.stdout(), decoder)
		return err
	}

	encoder := stdbase64.NewEncoder(stdbase64.StdEncoding, env.stdout())
	if _, err := io.Copy(encoder, reader); err != nil {
		encoder.Close()
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	_, err = fmt.Fprintln(env.stdout())
	return err
}

func runShasum(env *commandEnv, args []string) error {
	algorithm := 1

	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "algorithm", Names: []string{"-a", "--algorithm"}, ValueMode: internal.RequiredOptionValue},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		if opt.Canonical != "algorithm" {
			continue
		}
		algorithm, err = strconv.Atoi(opt.Value)
		if err != nil {
			return fmt.Errorf("invalid algorithm, only 1, 256 or 512 are valid: %w", os.ErrInvalid)
		}
	}

	sumReader := func(reader io.Reader) ([]byte, error) {
		var hasher hash.Hash
		switch algorithm {
		case 1:
			hasher = sha1.New()
		case 256:
			hasher = sha256.New()
		case 512:
			hasher = sha512.New()
		default:
			return nil, fmt.Errorf("invalid algorithm, only 1, 256 or 512 are valid: %w", os.ErrInvalid)
		}
		if _, err := io.Copy(hasher, reader); err != nil {
			return nil, err
		}
		return hasher.Sum(nil), nil
	}

	if len(operands) == 0 {
		hashBytes, err := sumReader(env.stdin())
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(env.stdout(), "%x -\n", hashBytes)
		return err
	}

	for _, file := range operands {
		resolvedPath, err := env.resolvePathArg(file)
		if err != nil {
			return err
		}
		reader, err := env.hc.FileSystem.Open(resolvedPath)
		if err != nil {
			return err
		}
		hashBytes, err := sumReader(reader)
		reader.Close()
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(env.stdout(), "%x %s\n", hashBytes, file); err != nil {
			return err
		}
	}
	return nil
}

func runTouch(env *commandEnv, args []string) error {
	access := false
	modification := false
	noCreate := false
	timestamp := time.Now()
	timestampSet := false
	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "a", Names: []string{"-a"}},
		{Canonical: "m", Names: []string{"-m"}},
		{Canonical: "c", Names: []string{"-c"}},
		{Canonical: "d", Names: []string{"-d"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "t", Names: []string{"-t"}, ValueMode: internal.RequiredOptionValue},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "a":
			access = true
		case "m":
			modification = true
		case "c":
			noCreate = true
		case "d":
			timestamp, err = time.Parse(time.RFC3339, opt.Value)
			if err != nil {
				return err
			}
			timestampSet = true
		case "t":
			timestamp, err = parseTouchTimestamp(opt.Value)
			if err != nil {
				return err
			}
			timestampSet = true
		}
	}
	if len(operands) == 0 {
		return usageError(env, 1, "usage: touch [-acm] [-c] [-d time] [-t timestamp] file ...")
	}

	changeAccess := access || (!access && !modification)
	changeModification := modification || (!access && !modification)
	mutableFS, err := env.mutableFS()
	if err != nil {
		return err
	}

	var joinedErr error
	for _, file := range operands {
		resolvedPath, err := env.resolvePathArg(file)
		if err != nil {
			return err
		}
		info, statErr := env.hc.FileSystem.Stat(resolvedPath)
		notExist := errors.Is(statErr, fs.ErrNotExist)
		if statErr != nil && !notExist {
			joinedErr = errors.Join(joinedErr, statErr)
			continue
		}
		if notExist {
			if noCreate {
				continue
			}
			createdFile, err := env.hc.FileSystem.OpenFile(resolvedPath, os.O_WRONLY|os.O_CREATE, 0o666)
			if err != nil {
				joinedErr = errors.Join(joinedErr, err)
				continue
			}
			if err := createdFile.Close(); err != nil {
				joinedErr = errors.Join(joinedErr, err)
				continue
			}
		}

		accessTime := timestamp
		modificationTime := timestamp
		if !notExist {
			accessTime, modificationTime = fileTimes(info)
			if changeAccess {
				accessTime = timestamp
			}
			if changeModification {
				modificationTime = timestamp
			}
		} else if !timestampSet {
			// Creating a missing file with -a or -m still updates both timestamps.
			accessTime = timestamp
			modificationTime = timestamp
		}
		if err := mutableFS.Chtimes(resolvedPath, accessTime, modificationTime); err != nil {
			joinedErr = errors.Join(joinedErr, err)
		}
	}
	return joinedErr
}

const (
	defaultCreationMode = 0o777
)

func parseMkdirMode(mode string) (os.FileMode, error) {
	base := modeFromBits(0, defaultCreationMode)
	if mode == "" {
		return base, nil
	}
	expr, err := parseModeExpression(mode)
	if err != nil {
		return 0, err
	}
	return expr.Apply(base, true), nil
}

func runMkdir(env *commandEnv, args []string) error {
	mode := ""
	makeAll := false
	verbose := false
	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "m", Names: []string{"-m"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "p", Names: []string{"-p"}},
		{Canonical: "v", Names: []string{"-v"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "m":
			mode = opt.Value
		case "p":
			makeAll = true
		case "v":
			verbose = true
		}
	}
	if len(operands) < 1 {
		return usageError(env, 1, "usage: mkdir [-pv] [-m mode] directory ...")
	}

	createMode, err := parseMkdirMode(mode)
	if err != nil {
		return err
	}
	mutableFS, err := env.mutableFS()
	if err != nil {
		return err
	}
	chmodFS, err := env.chmodFS()
	if err != nil {
		return err
	}

	for _, name := range operands {
		resolvedPath, err := env.resolvePathArg(name)
		if err != nil {
			return err
		}
		if makeAll {
			err = mutableFS.MkdirAll(resolvedPath, createMode)
		} else {
			err = mutableFS.Mkdir(resolvedPath, createMode)
		}
		if err != nil {
			fmt.Fprintf(env.stderr(), "%v: %v\n", name, err)
			continue
		}
		if verbose {
			fmt.Fprintf(env.stdout(), "%v\n", name)
		}
		if mode != "" {
			_ = chmodFS.Chmod(resolvedPath, createMode)
		}
	}
	return nil
}

func parseTouchTimestamp(value string) (time.Time, error) {
	base, secRaw, hasSec := strings.Cut(value, ".")
	seconds := 0
	if hasSec {
		if len(secRaw) != 2 {
			return time.Time{}, fmt.Errorf("invalid timestamp %q", value)
		}
		parsed, err := strconv.Atoi(secRaw)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid timestamp %q", value)
		}
		seconds = parsed
	}

	now := time.Now()
	year := now.Year()
	switch len(base) {
	case 8:
	case 10:
		yy, err := strconv.Atoi(base[:2])
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid timestamp %q", value)
		}
		if yy >= 69 {
			year = 1900 + yy
		} else {
			year = 2000 + yy
		}
		base = base[2:]
	case 12:
		parsedYear, err := strconv.Atoi(base[:4])
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid timestamp %q", value)
		}
		year = parsedYear
		base = base[4:]
	default:
		return time.Time{}, fmt.Errorf("invalid timestamp %q", value)
	}

	month, err := strconv.Atoi(base[:2])
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timestamp %q", value)
	}
	day, err := strconv.Atoi(base[2:4])
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timestamp %q", value)
	}
	hour, err := strconv.Atoi(base[4:6])
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timestamp %q", value)
	}
	minute, err := strconv.Atoi(base[6:8])
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timestamp %q", value)
	}

	timestamp := time.Date(year, time.Month(month), day, hour, minute, seconds, 0, time.Local)
	if timestamp.Year() != year || int(timestamp.Month()) != month || timestamp.Day() != day ||
		timestamp.Hour() != hour || timestamp.Minute() != minute || timestamp.Second() != seconds {
		return time.Time{}, fmt.Errorf("invalid timestamp %q", value)
	}
	return timestamp, nil
}

const rmUsage = "rm [-Rrvif] file..."

func runRm(env *commandEnv, args []string) error {
	interactive := false
	verbose := false
	recursive := false
	recursiveUpper := false
	force := false

	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "interactive", Names: []string{"-i"}},
		{Canonical: "verbose", Names: []string{"-v"}},
		{Canonical: "recursive", Names: []string{"-r"}},
		{Canonical: "recursive-upper", Names: []string{"-R"}},
		{Canonical: "force", Names: []string{"-f"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "interactive":
			interactive = true
		case "verbose":
			verbose = true
		case "recursive":
			recursive = true
		case "recursive-upper":
			recursiveUpper = true
		case "force":
			force = true
		}
	}
	if len(operands) == 0 {
		return fmt.Errorf("%v", rmUsage)
	}
	if force {
		interactive = false
	}

	var mutableFS interpMutableRemover
	if recursive || recursiveUpper {
		fsys, err := env.mutableFS()
		if err != nil {
			return err
		}
		mutableFS = fsys
	}

	reader := bufio.NewReader(env.stdin())
	for _, file := range operands {
		if interactive {
			fmt.Fprintf(env.stderr(), "rm: remove '%v'? ", file)
			answer, err := reader.ReadString('\n')
			if err != nil {
				return err
			}
			if answer == "" || strings.ToLower(answer[:1]) != "y" {
				continue
			}
		}

		resolvedPath, err := env.resolvePathArg(file)
		if err != nil {
			return err
		}
		if recursive || recursiveUpper {
			err = mutableFS.RemoveAll(resolvedPath)
		} else {
			err = env.hc.FileSystem.Remove(resolvedPath)
		}
		if err != nil {
			if force && isIgnorableRemoveError(err) {
				continue
			}
			return err
		}
		if verbose {
			printPath := file
			if !isRootedPath(file) {
				printPath = resolvedPath
			}
			fmt.Fprintf(env.stdout(), "removed '%v'\n", printPath)
		}
	}
	return nil
}

type interpMutableRemover interface {
	RemoveAll(path string) error
}

func isIgnorableRemoveError(err error) bool {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, fs.ErrNotExist) {
		return true
	}
	var pathErr *os.PathError
	return errors.As(err, &pathErr)
}
