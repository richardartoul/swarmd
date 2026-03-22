package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/richardartoul/swarmd/pkg/agent"
)

func runTUICommand(ctx context.Context, opts runtimeOptions) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("tui requires an interactive terminal")
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	events := newTUIEventBus(runCtx.Done())
	queue := newTUIQueue()
	model := newAgentTUIModel(agentTUIOptions{
		events:       events.Events(),
		submitPrompt: queue.SubmitPrompt,
		cancel:       cancel,
		modelName:    opts.modelName,
		rootDir:      opts.rootDir,
		verbose:      opts.verbose,
	})

	runtimeErrCh := make(chan error, 1)
	go func() {
		err := runTUIAgent(runCtx, opts, queue, events)
		if err != nil && !errors.Is(err, context.Canceled) {
			cancel()
		}
		runtimeErrCh <- err
	}()

	// Keep terminal-native drag selection/copy working by not enabling mouse capture.
	program := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithContext(runCtx),
	)
	_, programErr := program.Run()
	cancel()
	runtimeErr := <-runtimeErrCh

	if errors.Is(programErr, tea.ErrProgramKilled) {
		switch {
		case ctx.Err() != nil:
			programErr = ctx.Err()
		case runtimeErr != nil:
			programErr = nil
		}
	}
	if errors.Is(runtimeErr, context.Canceled) && (programErr == nil || errors.Is(programErr, context.Canceled)) {
		runtimeErr = nil
	}
	if programErr != nil {
		return programErr
	}
	return runtimeErr
}

func runTUIAgent(ctx context.Context, opts runtimeOptions, queue agent.Queue, events *tuiEventBus) error {
	driver := tuiDriver{
		next:    opts.baseDriver,
		events:  events,
		verbose: opts.verbose,
	}
	handlers := tuiEventPrinter{
		events:  events,
		verbose: opts.verbose,
	}
	stdout := tuiStreamWriter{
		events: events,
		stream: transcriptKindStdout,
	}
	stderr := tuiStreamWriter{
		events: events,
		stream: transcriptKindStderr,
	}

	cfg, err := opts.agentConfig(nil, driver, handlers, handlers, stdout, stderr)
	if err != nil {
		return err
	}
	runtime, err := agent.New(cfg)
	if err != nil {
		return err
	}
	return runSessionLoop(ctx, queue, agent.NewSession(runtime))
}

type tuiEventBus struct {
	ch   chan tea.Msg
	done <-chan struct{}
}

func newTUIEventBus(done <-chan struct{}) *tuiEventBus {
	return &tuiEventBus{
		ch:   make(chan tea.Msg, 512),
		done: done,
	}
}

func (b *tuiEventBus) Events() <-chan tea.Msg {
	return b.ch
}

func (b *tuiEventBus) Send(msg tea.Msg) {
	select {
	case <-b.done:
		return
	case b.ch <- msg:
	}
}

func (b *tuiEventBus) TrySend(msg tea.Msg) {
	select {
	case <-b.done:
		return
	case b.ch <- msg:
	default:
	}
}

type tuiQueue struct {
	ch     chan agent.Trigger
	mu     sync.Mutex
	nextID int
}

func newTUIQueue() *tuiQueue {
	return &tuiQueue{
		ch: make(chan agent.Trigger, 1),
	}
}

func (q *tuiQueue) SubmitPrompt(prompt string) (agent.Trigger, bool) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return agent.Trigger{}, false
	}

	q.mu.Lock()
	q.nextID++
	trigger := makeTrigger(prompt, q.nextID)
	q.mu.Unlock()

	select {
	case q.ch <- trigger:
		return trigger, true
	default:
		return agent.Trigger{}, false
	}
}

func (q *tuiQueue) Next(ctx context.Context) (agent.Trigger, error) {
	select {
	case <-ctx.Done():
		return agent.Trigger{}, ctx.Err()
	case trigger := <-q.ch:
		return trigger, nil
	}
}

func waitForTUIEvent(events <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-events
	}
}

type tuiDecisionMsg struct {
	step    int
	thought string
	tool    string
	input   string
}

type tuiStepMsg struct {
	step agent.Step
}

type tuiResultMsg struct {
	result agent.Result
}

type tuiLiveOutputMsg struct {
	stream transcriptKind
	text   string
}

type tuiDriver struct {
	next    agent.Driver
	events  *tuiEventBus
	verbose bool
}

func (d tuiDriver) Next(ctx context.Context, req agent.Request) (agent.Decision, error) {
	decision, err := d.next.Next(ctx, req)
	if err != nil {
		return agent.Decision{}, err
	}

	if d.verbose {
		msg := tuiDecisionMsg{
			step:    req.Step,
			thought: decision.Thought,
		}
		if decision.Tool != nil {
			msg.tool = strings.TrimSpace(decision.Tool.Name)
			msg.input = strings.TrimSpace(decision.Tool.Input)
		}
		d.events.Send(msg)
	}
	return decision, nil
}

type tuiEventPrinter struct {
	events  *tuiEventBus
	verbose bool
}

func (p tuiEventPrinter) HandleStep(ctx context.Context, trigger agent.Trigger, step agent.Step) error {
	_ = ctx
	_ = trigger

	p.events.Send(tuiStepMsg{step: step})
	return nil
}

func (p tuiEventPrinter) HandleResult(ctx context.Context, result agent.Result) error {
	_ = ctx

	p.events.Send(tuiResultMsg{result: result})
	return nil
}

type tuiStreamWriter struct {
	events *tuiEventBus
	stream transcriptKind
}

func (w tuiStreamWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.events.TrySend(tuiLiveOutputMsg{
		stream: w.stream,
		text:   string(p),
	})
	return len(p), nil
}
