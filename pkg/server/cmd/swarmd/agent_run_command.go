package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/richardartoul/swarmd/pkg/server"
)

func runAgentRun(ctx context.Context, args []string, streams commandIO) error {
	fs := flag.NewFlagSet("agent run", flag.ContinueOnError)
	fs.SetOutput(streams.stderr)
	configPath := fs.String("config", "", "path to a single agent YAML spec (required)")
	rootDir := fs.String("root", "", "sandbox root directory; defaults to a throwaway temp dir")
	modelOverride := fs.String("model", "", "override the spec model name")
	maxSteps := fs.Int("max-steps", 0, "override max steps; 0 uses the spec runtime value or a default")
	stepTimeout := fs.Duration("step-timeout", 0, "override per-step timeout; 0 uses the spec runtime value or a default")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*configPath) == "" {
		return fmt.Errorf("agent run requires -config <agent.yaml>")
	}

	spec, err := server.LoadAgentSpecFile(strings.TrimSpace(*configPath))
	if err != nil {
		return err
	}
	return executeAgent(ctx, spec, agentExecOptions{
		configPath:    strings.TrimSpace(*configPath),
		rootDir:       strings.TrimSpace(*rootDir),
		modelOverride: strings.TrimSpace(*modelOverride),
		maxSteps:      *maxSteps,
		stepTimeout:   *stepTimeout,
	}, streams)
}
