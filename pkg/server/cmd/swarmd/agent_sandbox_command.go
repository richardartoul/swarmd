package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/richardartoul/swarmd/pkg/server"
)

func runAgentSandbox(ctx context.Context, args []string, streams commandIO) error {
	fs := flag.NewFlagSet("agent sandbox", flag.ContinueOnError)
	fs.SetOutput(streams.stderr)
	configPath := fs.String("config", "", "path to a single agent YAML spec (required)")
	var stripToolsList multiFlag
	fs.Var(&stripToolsList, "strip-tools", "tool ID to remove (repeatable; also accepts comma-separated values)")
	promptSuffix := fs.String("prompt-suffix", "", "text appended to the composed system prompt")
	memoryDir := fs.String("memory-dir", "", "local .memory directory to copy in before run; written back on success")
	jsonSteps := fs.Bool("json-steps", false, "emit NDJSON step and result records on stdout")
	modelOverride := fs.String("model", "", "override the spec model name")
	maxSteps := fs.Int("max-steps", 0, "override max steps; 0 uses the spec runtime value or a default")
	stepTimeout := fs.Duration("step-timeout", 0, "override per-step timeout; 0 uses the spec runtime value or a default")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*configPath) == "" {
		return fmt.Errorf("agent sandbox requires -config <agent.yaml>")
	}

	spec, err := server.LoadAgentSpecFile(strings.TrimSpace(*configPath))
	if err != nil {
		return err
	}
	return executeAgent(ctx, spec, agentExecOptions{
		configPath:    strings.TrimSpace(*configPath),
		modelOverride: strings.TrimSpace(*modelOverride),
		maxSteps:      *maxSteps,
		stepTimeout:   *stepTimeout,
		promptSuffix:  strings.TrimSpace(*promptSuffix),
		stripTools:    append([]string(nil), stripToolsList...),
		memoryDir:     strings.TrimSpace(*memoryDir),
		jsonSteps:     *jsonSteps,
		tempRoot:      true,
	}, streams)
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(value string) error {
	*m = append(*m, parseStripToolsFlag([]string{value})...)
	return nil
}
