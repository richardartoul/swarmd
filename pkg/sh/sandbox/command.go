// See LICENSE for licensing information

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/richardartoul/swarmd/pkg/sh/expand"
	"github.com/richardartoul/swarmd/pkg/sh/internal"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/moreinterp/coreutils"
	"github.com/richardartoul/swarmd/pkg/sh/syntax"
)

const (
	sandboxCommandExecName  = "__sandbox_command__"
	sandboxShellCommandName = "sh"
)

type sandboxRuntimeConfig struct {
	networkEnabled   bool
	programValidator func(syntax.Node) error
	customCommands   []CustomCommand
	execHandlers     []func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc
	callHandlers     []interp.CallHandlerFunc
}

type sandboxCommandKind uint8

const (
	sandboxBuiltinCommand sandboxCommandKind = iota
	sandboxCoreutilsCommand
	sandboxSyntheticCommand
	sandboxCustomCommand
)

type sandboxCommandInfo struct {
	kind sandboxCommandKind
}

func rewriteSandboxCommand(args []string) []string {
	rewritten := make([]string, 1, len(args))
	rewritten[0] = sandboxCommandExecName
	if len(args) > 1 {
		rewritten = append(rewritten, args[1:]...)
	}
	return rewritten
}

func sandboxExecHandler(cfg sandboxRuntimeConfig) func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			switch args[0] {
			case sandboxCommandExecName:
				return runSandboxCommand(ctx, args[1:], cfg)
			case sandboxShellCommandName:
				return runSandboxShell(ctx, cfg, args[1:])
			default:
				return next(ctx, args)
			}
		}
	}
}

func sandboxCommandCatalog(cfg sandboxRuntimeConfig) map[string]sandboxCommandInfo {
	catalog := make(map[string]sandboxCommandInfo, len(allowedBuiltins)+len(coreutils.Commands())+len(cfg.customCommands)+1)
	for name := range allowedBuiltins {
		catalog[name] = sandboxCommandInfo{kind: sandboxBuiltinCommand}
	}
	for _, command := range coreutils.Commands() {
		if command.RequiresNetwork && !cfg.networkEnabled {
			continue
		}
		catalog[command.Name] = sandboxCommandInfo{kind: sandboxCoreutilsCommand}
	}
	for _, command := range cfg.customCommands {
		if command.Info.RequiresNetwork && !cfg.networkEnabled {
			continue
		}
		catalog[command.Info.Name] = sandboxCommandInfo{kind: sandboxCustomCommand}
	}
	catalog[sandboxShellCommandName] = sandboxCommandInfo{kind: sandboxSyntheticCommand}
	return catalog
}

func sandboxShellEnv(policy FileSystem, env expand.Environ, pwd string) (expand.Environ, error) {
	pairs, err := sandboxBaseEnvPairs(policy, pwd)
	if err != nil {
		return nil, err
	}
	if env == nil {
		return expand.ListEnviron(pairs...), nil
	}
	env.Each(func(name string, vr expand.Variable) bool {
		if !vr.Exported || !vr.IsSet() || vr.Kind != expand.String {
			return true
		}
		switch name {
		case "PWD", "TMPDIR", "UID", "EUID", "GID":
			return true
		}
		pairs = append(pairs, name+"="+vr.String())
		return true
	})
	return expand.ListEnviron(pairs...), nil
}

func newSandboxShellRunner(hc interp.HandlerContext, cfg sandboxRuntimeConfig) (*interp.Runner, error) {
	policy, ok := hc.FileSystem.(FileSystem)
	if !ok {
		return nil, fmt.Errorf("sandbox shell requires sandbox filesystem")
	}
	var networkDialer interp.NetworkDialer
	if cfg.networkEnabled {
		networkDialer = hc.NetworkDialer
	}
	runner, err := NewRunnerWithConfig(policy, RunnerConfig{
		Stdin:             hc.Stdin,
		Stdout:            hc.Stdout,
		Stderr:            hc.Stderr,
		NetworkDialer:     networkDialer,
		HTTPClientFactory: hc.HTTPClientFactory,
		NetworkEnabled:    cfg.networkEnabled,
		ProgramValidator:  cfg.programValidator,
		CustomCommands:    cfg.customCommands,
		ExecHandlers:      cfg.execHandlers,
		CallHandlers:      cfg.callHandlers,
	})
	if err != nil {
		return nil, err
	}
	env, err := sandboxShellEnv(policy, hc.Env, hc.Dir)
	if err != nil {
		return nil, err
	}
	if err := interp.Env(env)(runner); err != nil {
		return nil, err
	}
	if err := interp.Dir(hc.Dir)(runner); err != nil {
		return nil, err
	}
	return runner, nil
}

func parseSandboxShellProgram(hc interp.HandlerContext, stderr io.Writer, args []string) (*syntax.File, []string, error) {
	parseProgram := func(reader io.Reader, name string) (*syntax.File, error) {
		if reader == nil {
			reader = strings.NewReader("")
		}
		file, err := syntax.NewParser(syntax.Variant(syntax.LangPOSIX)).Parse(reader, name)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", sandboxShellCommandName, err)
			return nil, interp.ExitStatus(2)
		}
		return file, nil
	}
	parseProgramFile := func(rawPath string, params []string) (*syntax.File, []string, error) {
		path, err := ResolvePath(hc.FileSystem, hc.Dir, rawPath)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", sandboxShellCommandName, err)
			return nil, nil, interp.ExitStatus(1)
		}
		fileReader, err := hc.FileSystem.Open(path)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", sandboxShellCommandName, err)
			return nil, nil, interp.ExitStatus(1)
		}
		defer fileReader.Close()
		file, err := parseProgram(fileReader, rawPath)
		if err != nil {
			return nil, nil, err
		}
		return file, append([]string(nil), params...), nil
	}

	if len(args) == 0 {
		file, err := parseProgram(hc.Stdin, sandboxShellCommandName)
		return file, nil, err
	}
	switch args[0] {
	case "-c":
		if len(args) < 2 {
			fmt.Fprintf(stderr, "%s: option requires an argument -- c\n", sandboxShellCommandName)
			return nil, nil, interp.ExitStatus(2)
		}
		name := sandboxShellCommandName
		params := []string(nil)
		if len(args) > 2 {
			name = args[2]
			params = args[3:]
		}
		file, err := parseProgram(strings.NewReader(args[1]), name)
		return file, append([]string(nil), params...), err
	case "-", "-s":
		file, err := parseProgram(hc.Stdin, sandboxShellCommandName)
		return file, append([]string(nil), args[1:]...), err
	case "--":
		if len(args) == 1 {
			file, err := parseProgram(hc.Stdin, sandboxShellCommandName)
			return file, nil, err
		}
		return parseProgramFile(args[1], args[2:])
	default:
		if strings.HasPrefix(args[0], "-") {
			fmt.Fprintf(stderr, "%s: unsupported option %q\n", sandboxShellCommandName, args[0])
			return nil, nil, interp.ExitStatus(2)
		}
		return parseProgramFile(args[0], args[1:])
	}
}

func runSandboxShell(ctx context.Context, cfg sandboxRuntimeConfig, args []string) error {
	hc := interp.HandlerCtx(ctx)
	stderr := hc.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	file, params, err := parseSandboxShellProgram(hc, stderr, args)
	if err != nil {
		return err
	}
	if cfg.programValidator != nil {
		if err := cfg.programValidator(file); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", sandboxShellCommandName, err)
			return interp.ExitStatus(1)
		}
	}
	runner, err := newSandboxShellRunner(hc, cfg)
	if err != nil {
		return err
	}
	runner.Params = append([]string(nil), params...)
	return runner.Run(ctx, file)
}

func runSandboxCommand(ctx context.Context, args []string, cfg sandboxRuntimeConfig) error {
	hc := interp.HandlerCtx(ctx)
	stdout := hc.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	stderr := hc.Stderr
	if stderr == nil {
		stderr = io.Discard
	}

	parsedOpts, commandArgs, err := internal.ParseOptions(args, []internal.OptionSpec{
		{Canonical: "p", Names: []string{"-p"}},
		{Canonical: "v", Names: []string{"-v"}},
		{Canonical: "V", Names: []string{"-V"}},
	}, internal.ParseOptionsConfig{
		StopAtOperand: true,
	})
	if err != nil {
		var unknown *internal.UnknownOptionError
		if errors.As(err, &unknown) {
			fmt.Fprintf(stderr, "command: invalid option %q\n", unknown.Option)
		} else {
			fmt.Fprintf(stderr, "command: %v\n", err)
		}
		return interp.ExitStatus(2)
	}

	showMode := ""
	useDefaultPath := false
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "p":
			useDefaultPath = true
		case "v":
			showMode = "-v"
		case "V":
			showMode = "-V"
		}
	}
	if len(commandArgs) == 0 {
		return nil
	}
	if useDefaultPath {
		fmt.Fprintf(stderr, "command: builtin disabled in sandbox\n")
		return interp.ExitStatus(1)
	}
	if showMode == "" {
		return hc.RunSimpleCommandWithOptions(ctx, commandArgs, interp.RunSimpleCommandOptions{
			SuppressFunctions: true,
		})
	}

	catalog := sandboxCommandCatalog(cfg)
	last := uint8(0)
	for _, arg := range commandArgs {
		last = 0
		info, ok := catalog[arg]
		switch {
		case syntax.IsKeyword(arg):
			if showMode == "-V" {
				fmt.Fprintf(stdout, "%s is a shell keyword\n", arg)
			} else {
				fmt.Fprintf(stdout, "%s\n", arg)
			}
		case hc.IsFunction(arg):
			if showMode == "-V" {
				fmt.Fprintf(stdout, "%s is a function\n", arg)
			} else {
				fmt.Fprintf(stdout, "%s\n", arg)
			}
		case ok && info.kind == sandboxBuiltinCommand:
			if showMode == "-V" {
				fmt.Fprintf(stdout, "%s is a shell builtin\n", arg)
			} else {
				fmt.Fprintf(stdout, "%s\n", arg)
			}
		case ok && (info.kind == sandboxCoreutilsCommand || info.kind == sandboxSyntheticCommand || info.kind == sandboxCustomCommand):
			if showMode == "-V" {
				fmt.Fprintf(stdout, "%s is a sandbox command\n", arg)
			} else {
				fmt.Fprintf(stdout, "%s\n", arg)
			}
		default:
			last = 1
		}
	}
	if last != 0 {
		return interp.ExitStatus(last)
	}
	return nil
}
