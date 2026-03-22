package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/richardartoul/swarmd/pkg/agent"
	agentopenai "github.com/richardartoul/swarmd/pkg/agent/openai"
)

const (
	apiKeyEnv    = "OPENAI_API_KEY"
	defaultModel = "gpt-4o-mini"
	demoPrompt   = "Run `echo hello from the sandbox` using the shell. After the command succeeds, finish with the exact plain string `done`."
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	apiKey := strings.TrimSpace(os.Getenv(apiKeyEnv))
	if apiKey == "" {
		return fmt.Errorf("%s is required; export %s and rerun this example", apiKeyEnv, apiKeyEnv)
	}

	root, err := os.MkdirTemp("", "swarmd-minimal-agent-openai")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)

	driver, err := agentopenai.New(agentopenai.Config{
		APIKey: apiKey,
		Model:  defaultModel,
	})
	if err != nil {
		return err
	}

	runtime, err := agent.New(agent.Config{
		Root:     root,
		Driver:   driver,
		MaxSteps: 4,
		SystemPrompt: agent.ComposeSystemPrompt(
			`This is a minimal embedding demo. Use exactly one run_shell step to execute the requested shell command. After the command succeeds, finish with the exact plain string "done".`,
			false,
		),
	})
	if err != nil {
		return err
	}

	result, err := runtime.HandleTrigger(context.Background(), agent.Trigger{
		ID:      "prompt-1",
		Kind:    "repl.prompt",
		Payload: demoPrompt,
	})
	if err != nil {
		return err
	}
	if len(result.Steps) == 0 {
		return fmt.Errorf("expected at least one step")
	}

	fmt.Printf("status: %s\n", result.Status)
	fmt.Printf("stdout: %s\n", strings.TrimSpace(result.Steps[0].Stdout))
	fmt.Printf("value: %s\n", agent.RenderResultValue(result.Value))
	return nil
}
