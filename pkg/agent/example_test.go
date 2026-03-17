package agent_test

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/richardartoul/swarmd/pkg/agent"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
)

func ExampleAgent_HandleTrigger() {
	root, err := os.MkdirTemp("", "agent-example")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)

	a, err := agent.New(agent.Config{
		FileSystem: mustSandboxFS(root),
		Driver: agent.DriverFunc(func(ctx context.Context, req agent.Request) (agent.Decision, error) {
			switch req.Step {
			case 1:
				return agent.Decision{
					Shell: &agent.ShellAction{Source: "echo hello"},
				}, nil
			default:
				return agent.Decision{
					Finish: &agent.FinishAction{Value: "done"},
				}, nil
			}
		}),
	})
	if err != nil {
		panic(err)
	}

	result, err := a.HandleTrigger(context.Background(), agent.Trigger{Kind: "message"})
	if err != nil {
		panic(err)
	}

	fmt.Println(strings.TrimSpace(result.Steps[0].Stdout))
	fmt.Println(result.Value)
	// Output:
	// hello
	// done
}

func mustSandboxFS(root string) sandbox.FileSystem {
	fsys, err := sandbox.NewFS(root)
	if err != nil {
		panic(err)
	}
	return fsys
}
