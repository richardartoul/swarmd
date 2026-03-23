package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/richardartoul/swarmd/pkg/agent"
	agentanthropic "github.com/richardartoul/swarmd/pkg/agent/anthropic"
)

const (
	apiKeyEnv    = "ANTHROPIC_API_KEY"
	defaultModel = "claude-sonnet-4-6"
	demoPrompt   = "Execute the required shell command and then finish."
	systemPrompt = `You are running a deterministic integration test.

Rules:
- On your first response, you must call the run_shell tool.
- The run_shell command must be exactly: echo hello from the sandbox
- Do not return a final response before the tool call.
- After the tool call succeeds, return the exact plain string "done".
- Do not add any explanation.`
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	apiKey := strings.TrimSpace(os.Getenv(apiKeyEnv))
	if apiKey == "" {
		return fmt.Errorf("%s is required; export %s and rerun this example", apiKeyEnv, apiKeyEnv)
	}

	root, err := os.MkdirTemp("", "swarmd-minimal-agent-anthropic")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)

	driver, err := agentanthropic.New(agentanthropic.Config{
		APIKey: apiKey,
		Model:  defaultModel,
	})
	if err != nil {
		return err
	}

	runtime, err := agent.New(agent.Config{
		Root:         root,
		Driver:       driver,
		MaxSteps:     4,
		SystemPrompt: systemPrompt,
	})
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := runtime.Close(); runErr == nil {
			runErr = closeErr
		}
	}()

	result, err := runtime.HandleTrigger(context.Background(), agent.Trigger{
		ID:      "prompt-1",
		Kind:    "repl.prompt",
		Payload: demoPrompt,
	})
	if err != nil {
		return err
	}

	stdout := "<no shell step>"
	if len(result.Steps) > 0 {
		stdout = strings.TrimSpace(result.Steps[0].Stdout)
	}

	fmt.Printf("status: %s\n", result.Status)
	fmt.Printf("stdout: %s\n", stdout)
	if strings.TrimSpace(result.Error) != "" {
		fmt.Printf("error: %s\n", result.Error)
	}
	if result.Status == agent.ResultStatusFinished || result.Value != nil {
		fmt.Printf("value: %s\n", agent.RenderResultValue(result.Value))
	} else {
		fmt.Printf("value: <none>\n")
	}
	return nil
}
