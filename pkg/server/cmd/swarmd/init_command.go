package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	initDefaultNamespace   = "default"
	initHeartbeatAgentID   = "server-log-heartbeat"
	initHeartbeatAgentFile = initHeartbeatAgentID + ".yaml"
	initHeartbeatAgentSpec = `version: 1
agent_id: server-log-heartbeat
name: Server Log Heartbeat
description: Emits a heartbeat message to the server logs once a minute.
model:
  provider: openai
  name: gpt-5
prompt: |
  You are a server log heartbeat agent.

  When triggered, write exactly one log entry using the structured server_log tool.

  If the payload includes a string field named "level", use it. Otherwise use "info".
  If the payload includes a string field named "message", use it. Otherwise use "scheduled heartbeat from server-log-heartbeat".

  After writing the log entry, finish with a short confirmation that includes the level and message you used.
tools:
  - server_log
runtime:
  max_steps: 2
  step_timeout: 20s
  max_output_bytes: 16384
  retry_delay: 10s
  max_attempts: 3
schedules:
  - id: server-log-heartbeat-every-minute
    cron: "* * * * *"
    timezone: UTC
    payload:
      level: info
      message: scheduled heartbeat from server-log-heartbeat
`
)

func runInitCommand(_ context.Context, args []string, streams commandIO) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(streams.stderr)

	configRoot := envOr("SWARMD_SERVER_CONFIG_ROOT", defaultConfigRoot)
	fs.StringVar(&configRoot, "config-root", configRoot, "Config root containing namespaces/<namespace>/agents/*.yaml")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := ensureNoExtraArgs(fs, "init"); err != nil {
		return err
	}

	configRoot = strings.TrimSpace(configRoot)
	if err := requireFlag("config-root", configRoot); err != nil {
		return err
	}
	configRoot = filepath.Clean(configRoot)

	agentsDir := filepath.Join(configRoot, "namespaces", initDefaultNamespace, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return fmt.Errorf("ensure agent config dir %q: %w", agentsDir, err)
	}

	agentPath := filepath.Join(agentsDir, initHeartbeatAgentFile)
	if err := writeInitAgentConfig(agentPath, initHeartbeatAgentSpec); err != nil {
		return err
	}

	fmt.Fprintf(streams.stdout, "init> config_root=%s namespace=%s agent=%s\n", configRoot, initDefaultNamespace, initHeartbeatAgentID)
	fmt.Fprintf(streams.stdout, "created> %s\n", agentPath)
	fmt.Fprintf(streams.stdout, "next> swarmd config validate --config-root %q\n", configRoot)
	fmt.Fprintf(streams.stdout, "next> swarmd server --config-root %q\n", configRoot)
	return nil
}

func writeInitAgentConfig(path, contents string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("agent config %q already exists", path)
		}
		return fmt.Errorf("create agent config %q: %w", path, err)
	}

	if _, err := file.WriteString(contents); err != nil {
		file.Close()
		return fmt.Errorf("write agent config %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close agent config %q: %w", path, err)
	}
	return nil
}
