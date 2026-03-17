package coreutils

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	goawkinterp "github.com/benhoyt/goawk/interp"
	goawkparser "github.com/benhoyt/goawk/parser"
	"github.com/itchyny/gojq"

	"github.com/richardartoul/swarmd/pkg/sh/expand"
	"github.com/richardartoul/swarmd/pkg/sh/internal"
	shinterp "github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/syntax"
)

const awkRestrictedFileBoundaryPrefix = "\x00__swarmd_awk_boundary__\x00"

func runJq(env *commandEnv, args []string) error {
	rawOutput := false
	compactOutput := false
	nullInput := false
	exitStatusMode := false

	parsedOpts, operands, err := parseUtilityOptionsToFirstOperand(args, []internal.OptionSpec{
		{Canonical: "r", Names: []string{"-r", "--raw-output"}},
		{Canonical: "c", Names: []string{"-c", "--compact-output"}},
		{Canonical: "n", Names: []string{"-n", "--null-input"}},
		{Canonical: "e", Names: []string{"-e", "--exit-status"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "r":
			rawOutput = true
		case "c":
			compactOutput = true
		case "n":
			nullInput = true
		case "e":
			exitStatusMode = true
		}
	}
	filter := "."
	if len(operands) > 0 {
		filter = operands[0]
		operands = operands[1:]
	}
	if nullInput && len(operands) > 0 {
		return fmt.Errorf("jq: file operands are not supported with -n")
	}
	if !nullInput && len(operands) == 0 {
		operands = []string{"-"}
	}

	query, err := gojq.Parse(filter)
	if err != nil {
		return fmt.Errorf("jq: %w", err)
	}
	code, err := gojq.Compile(query)
	if err != nil {
		return fmt.Errorf("jq: %w", err)
	}

	var (
		hadOutput bool
		lastValue any
	)
	runQuery := func(input any) error {
		iter := code.RunWithContext(env.ctx, input)
		for {
			value, ok := iter.Next()
			if !ok {
				return nil
			}
			if err, ok := value.(error); ok {
				var haltErr *gojq.HaltError
				if errors.As(err, &haltErr) && haltErr.Value() == nil {
					return nil
				}
				return fmt.Errorf("jq: %w", err)
			}
			hadOutput = true
			lastValue = value
			if err := writeJqValue(env.stdout(), value, rawOutput, compactOutput); err != nil {
				return err
			}
		}
	}

	if nullInput {
		if err := runQuery(nil); err != nil {
			return err
		}
	} else {
		for _, operand := range operands {
			reader, closeFn, err := openCommandInput(env, operand)
			if err != nil {
				return err
			}
			err = runJqReader(env, code, reader, rawOutput, compactOutput, &hadOutput, &lastValue)
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
	}

	if exitStatusMode {
		switch {
		case !hadOutput:
			return shinterp.ExitStatus(4)
		case lastValue == nil:
			return shinterp.ExitStatus(1)
		case lastValue == false:
			return shinterp.ExitStatus(1)
		}
	}
	return nil
}

func runAwk(env *commandEnv, args []string) error {
	var (
		sourceFragments []string
		vars            []string
	)

	parsedOpts, operands, err := parseUtilityOptionsToFirstOperand(args, []internal.OptionSpec{
		{Canonical: "F", Names: []string{"-F"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "v", Names: []string{"-v"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "f", Names: []string{"-f"}, ValueMode: internal.RequiredOptionValue},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "F":
			vars = append(vars, "FS", opt.Value)
		case "v":
			name, value, ok := strings.Cut(opt.Value, "=")
			if !ok || !syntax.ValidName(name) {
				return fmt.Errorf("awk: invalid variable assignment %q", opt.Value)
			}
			vars = append(vars, name, value)
		case "f":
			source, err := readCommandText(env, opt.Value)
			if err != nil {
				return err
			}
			sourceFragments = append(sourceFragments, source)
		}
	}

	if len(sourceFragments) == 0 {
		if len(operands) == 0 {
			return usageError(env, 1, "usage: awk [-F fs] [-v var=value] [-f progfile | 'prog']")
		}
		sourceFragments = append(sourceFragments, operands[0])
		operands = operands[1:]
	}
	source := strings.Join(sourceFragments, "\n")
	program, err := goawkparser.ParseProgram([]byte(source), nil)
	if err != nil {
		return fmt.Errorf("awk: %w", err)
	}

	stdin := env.stdin()
	var closeInput func() error
	if len(operands) > 0 {
		if hasAwkVarAssignments(operands) {
			return fmt.Errorf("awk: var=value operands are not supported with file operands in restricted mode; use -v")
		}
		if err := validateAwkRestrictedFileOperands(program); err != nil {
			return err
		}
		wrappedSource := wrapAwkSourceForRestrictedFileOperands(source, operands)
		program, err = goawkparser.ParseProgram([]byte(wrappedSource), nil)
		if err != nil {
			return fmt.Errorf("awk: %w", err)
		}
		restrictedInput := newRestrictedAwkInputReader(env, operands)
		stdin = restrictedInput
		closeInput = restrictedInput.Close
	}
	status, err := goawkinterp.ExecProgram(program, &goawkinterp.Config{
		Argv0:        "awk",
		Stdin:        stdin,
		Output:       env.stdout(),
		Error:        env.stderr(),
		Environ:      currentEnvironmentPairs(env.hc.Env),
		Vars:         vars,
		NoExec:       true,
		NoFileReads:  true,
		NoFileWrites: true,
	})
	if closeInput != nil {
		closeErr := closeInput()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		return fmt.Errorf("awk: %w", err)
	}
	if status != 0 {
		return shinterp.ExitStatus(status)
	}
	return nil
}

func hasAwkVarAssignments(operands []string) bool {
	for _, operand := range operands {
		name, _, ok := strings.Cut(operand, "=")
		if ok && syntax.ValidName(name) {
			return true
		}
	}
	return false
}

func validateAwkRestrictedFileOperands(program *goawkparser.Program) error {
	disassembly, err := awkProgramDisassembly(program)
	if err != nil {
		return fmt.Errorf("awk: %w", err)
	}
	switch {
	case strings.Contains(disassembly, "Nextfile"):
		return fmt.Errorf("awk: nextfile is not supported with file operands in restricted mode")
	case strings.Contains(disassembly, "Getline"):
		return fmt.Errorf("awk: getline is not supported with file operands in restricted mode")
	default:
		return nil
	}
}

func awkProgramDisassembly(program *goawkparser.Program) (string, error) {
	var builder strings.Builder
	if err := program.Disassemble(&builder); err != nil {
		return "", err
	}
	return builder.String(), nil
}

func wrapAwkSourceForRestrictedFileOperands(source string, operands []string) string {
	var builder strings.Builder
	builder.WriteString("BEGIN {\n")
	for index, operand := range operands {
		fmt.Fprintf(&builder, "    ARGV[%d] = %s\n", index+1, strconv.Quote(operand))
		fmt.Fprintf(&builder, "    __swarmd_awkrf_files[%d] = %s\n", index+1, strconv.Quote(operand))
	}
	fmt.Fprintf(&builder, "    __swarmd_awkrf_boundary = %s\n", strconv.Quote(awkRestrictedFileBoundaryPrefix))
	builder.WriteString("}\n")
	builder.WriteString("index($0, __swarmd_awkrf_boundary) == 1 {\n")
	builder.WriteString("    __swarmd_awkrf_index = substr($0, length(__swarmd_awkrf_boundary) + 1) + 0\n")
	builder.WriteString("    FILENAME = __swarmd_awkrf_files[__swarmd_awkrf_index]\n")
	builder.WriteString("    NR = NR - 1\n")
	builder.WriteString("    FNR = 0\n")
	builder.WriteString("    next\n")
	builder.WriteString("}\n\n")
	builder.WriteString(source)
	return builder.String()
}

type restrictedAwkInputReader struct {
	env      *commandEnv
	operands []string

	operandIndex int
	pending      []byte

	currentReader              io.Reader
	currentClose               func() error
	currentSourceHadData       bool
	currentSourceEndedNewline  bool
	previousSourceHadData      bool
	previousSourceEndedNewline bool
	deferredErr                error
}

func newRestrictedAwkInputReader(env *commandEnv, operands []string) *restrictedAwkInputReader {
	return &restrictedAwkInputReader{
		env:      env,
		operands: append([]string(nil), operands...),
	}
}

func (r *restrictedAwkInputReader) Read(p []byte) (int, error) {
	for {
		if len(r.pending) > 0 {
			n := copy(p, r.pending)
			r.pending = r.pending[n:]
			return n, nil
		}
		if r.deferredErr != nil {
			err := r.deferredErr
			r.deferredErr = nil
			return 0, err
		}
		if r.currentReader == nil {
			if r.operandIndex >= len(r.operands) {
				return 0, io.EOF
			}
			if err := r.openNextSource(); err != nil {
				return 0, err
			}
			continue
		}

		n, err := r.currentReader.Read(p)
		if n > 0 {
			r.currentSourceHadData = true
			r.currentSourceEndedNewline = p[n-1] == '\n'
		}
		switch {
		case errors.Is(err, io.EOF):
			closeErr := r.finishCurrentSource()
			if closeErr != nil {
				r.deferredErr = closeErr
			}
			if n > 0 {
				return n, nil
			}
		case err != nil:
			_ = r.finishCurrentSource()
			if n > 0 {
				r.deferredErr = err
				return n, nil
			}
			return 0, err
		case n > 0:
			return n, nil
		}
	}
}

func (r *restrictedAwkInputReader) Close() error {
	var err error
	if r.currentReader != nil {
		err = r.finishCurrentSource()
	}
	if err == nil && r.deferredErr != nil {
		err = r.deferredErr
		r.deferredErr = nil
	}
	return err
}

func (r *restrictedAwkInputReader) openNextSource() error {
	reader, closeFn, err := openCommandInput(r.env, r.operands[r.operandIndex])
	if err != nil {
		return err
	}
	if r.operandIndex > 0 && r.previousSourceHadData && !r.previousSourceEndedNewline {
		r.pending = append(r.pending, '\n')
	}
	r.pending = append(r.pending, awkRestrictedFileBoundaryPrefix...)
	r.pending = strconv.AppendInt(r.pending, int64(r.operandIndex+1), 10)
	r.pending = append(r.pending, '\n')
	r.currentReader = reader
	r.currentClose = closeFn
	r.currentSourceHadData = false
	r.currentSourceEndedNewline = false
	r.operandIndex++
	return nil
}

func (r *restrictedAwkInputReader) finishCurrentSource() error {
	r.previousSourceHadData = r.currentSourceHadData
	r.previousSourceEndedNewline = r.currentSourceEndedNewline
	r.currentSourceHadData = false
	r.currentSourceEndedNewline = false
	r.currentReader = nil
	closeFn := r.currentClose
	r.currentClose = nil
	if closeFn != nil {
		return closeFn()
	}
	return nil
}

func runJqReader(env *commandEnv, code *gojq.Code, reader io.Reader, rawOutput, compactOutput bool, hadOutput *bool, lastValue *any) error {
	decoder := json.NewDecoder(reader)
	for {
		var input any
		if err := decoder.Decode(&input); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("jq: %w", err)
		}
		iter := code.RunWithContext(env.ctx, input)
		for {
			value, ok := iter.Next()
			if !ok {
				break
			}
			if err, ok := value.(error); ok {
				var haltErr *gojq.HaltError
				if errors.As(err, &haltErr) && haltErr.Value() == nil {
					break
				}
				return fmt.Errorf("jq: %w", err)
			}
			*hadOutput = true
			*lastValue = value
			if err := writeJqValue(env.stdout(), value, rawOutput, compactOutput); err != nil {
				return err
			}
		}
	}
}

func writeJqValue(w io.Writer, value any, rawOutput, compactOutput bool) error {
	if rawOutput {
		if s, ok := value.(string); ok {
			_, err := fmt.Fprintln(w, s)
			return err
		}
	}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	if !compactOutput {
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(value)
}

func readCommandText(env *commandEnv, operand string) (string, error) {
	reader, closeFn, err := openCommandInput(env, operand)
	if err != nil {
		return "", err
	}
	data, readErr := io.ReadAll(reader)
	if closeFn != nil {
		closeErr := closeFn()
		if readErr == nil {
			readErr = closeErr
		}
	}
	if readErr != nil {
		return "", readErr
	}
	return string(data), nil
}

func currentEnvironmentPairs(current expand.Environ) []string {
	if current == nil {
		return []string{}
	}
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
	pairs := make([]string, 0, len(names)*2)
	for _, name := range names {
		pairs = append(pairs, name, values[name])
	}
	return pairs
}
