package main

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/richardartoul/swarmd/pkg/agent"
)

type agentTUIOptions struct {
	events       <-chan tea.Msg
	submitPrompt func(prompt string) (agent.Trigger, bool)
	cancel       context.CancelFunc
	modelName    string
	rootDir      string
	verbose      bool
}

type agentTUIFocus int

const (
	agentTUIFocusTranscript agentTUIFocus = iota
	agentTUIFocusInput
)

const inputPromptPrefix = "agent> "

func (f agentTUIFocus) label() string {
	switch f {
	case agentTUIFocusTranscript:
		return "transcript"
	case agentTUIFocusInput:
		return "input"
	default:
		return "unknown"
	}
}

type transcriptKind string

const (
	transcriptKindSystem    transcriptKind = "system"
	transcriptKindUser      transcriptKind = "user"
	transcriptKindThought   transcriptKind = "thought"
	transcriptKindCommand   transcriptKind = "command"
	transcriptKindStdout    transcriptKind = "stdout"
	transcriptKindStderr    transcriptKind = "stderr"
	transcriptKindAssistant transcriptKind = "assistant"
	transcriptKindError     transcriptKind = "error"
)

type transcriptEntry struct {
	kind  transcriptKind
	title string
	body  string
}

type agentTUIKeyMap struct {
	toggleFocus key.Binding
	history     key.Binding
	live        key.Binding
	backToInput key.Binding
	send        key.Binding
	help        key.Binding
	quit        key.Binding
}

func newAgentTUIKeyMap() agentTUIKeyMap {
	return agentTUIKeyMap{
		toggleFocus: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "toggle focus")),
		history:     key.NewBinding(key.WithKeys("up", "pgup"), key.WithHelp("up/pgup", "history")),
		live:        key.NewBinding(key.WithKeys("down", "pgdown"), key.WithHelp("down/pgdn", "prompt")),
		backToInput: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "input")),
		send:        key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send prompt")),
		help:        key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "toggle help")),
		quit:        key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

func (k agentTUIKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.toggleFocus, k.history, k.live, k.send, k.help, k.quit}
}

func (k agentTUIKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.toggleFocus, k.history, k.live},
		{k.backToInput, k.help, k.quit},
		{k.send},
	}
}

type agentTUIModel struct {
	events       <-chan tea.Msg
	submitPrompt func(prompt string) (agent.Trigger, bool)
	cancel       context.CancelFunc
	modelName    string
	rootDir      string
	verbose      bool

	transcript viewport.Model
	input      textarea.Model
	help       help.Model
	keys       agentTUIKeyMap

	focus      agentTUIFocus
	width      int
	height     int
	showHelp   bool
	running    bool
	status     string
	currentCWD string
	usage      agent.Usage
	entries    []transcriptEntry
}

func newAgentTUIModel(opts agentTUIOptions) agentTUIModel {
	transcript := viewport.New(0, 0)
	transcript.MouseWheelEnabled = true

	input := textarea.New()
	input.Prompt = inputPromptPrefix
	input.Placeholder = "Describe the task and press Enter"
	input.ShowLineNumbers = false
	input.SetHeight(3)
	input.SetPromptFunc(lipgloss.Width(inputPromptPrefix), func(displayLine int) string {
		if displayLine == 0 {
			return inputPromptPrefix
		}
		return strings.Repeat(" ", lipgloss.Width(inputPromptPrefix))
	})
	input.FocusedStyle.CursorLine = lipgloss.NewStyle()
	input.BlurredStyle.CursorLine = lipgloss.NewStyle()
	_ = input.Focus()

	model := agentTUIModel{
		events:       opts.events,
		submitPrompt: opts.submitPrompt,
		cancel:       opts.cancel,
		modelName:    opts.modelName,
		rootDir:      opts.rootDir,
		verbose:      opts.verbose,
		transcript:   transcript,
		input:        input,
		help:         help.New(),
		keys:         newAgentTUIKeyMap(),
		focus:        agentTUIFocusInput,
		currentCWD:   opts.rootDir,
	}
	model.appendEntry(transcriptEntry{
		kind:  transcriptKindSystem,
		title: "ready",
		body:  "agentrepl is ready. Enter a prompt to trigger the agent. Use ? for help or :quit to exit.",
	})
	return model
}

func (m agentTUIModel) Init() tea.Cmd {
	return tea.Batch(m.input.Focus(), waitForTUIEvent(m.events))
}

func (m *agentTUIModel) toggleFocus() {
	switch m.focus {
	case agentTUIFocusTranscript:
		m.setFocus(agentTUIFocusInput)
	default:
		m.setFocus(agentTUIFocusTranscript)
	}
}

func (m *agentTUIModel) setFocus(focus agentTUIFocus) {
	m.focus = focus
	if focus == agentTUIFocusInput {
		m.input.Focus()
		return
	}
	m.input.Blur()
}

func (m agentTUIModel) shouldFollowTranscript() bool {
	if len(m.entries) == 0 || m.transcript.Height == 0 {
		return true
	}
	return m.transcript.AtBottom()
}

func (m *agentTUIModel) resize() {
	if m.width == 0 || m.height == 0 {
		return
	}

	followTail := m.shouldFollowTranscript()
	m.help.Width = m.width
	m.help.ShowAll = m.showHelp
	transcriptStyle := transcriptPaneStyle(m.focus == agentTUIFocusTranscript)
	inputStyle := inputPaneStyle(m.focus == agentTUIFocusInput)
	innerInputWidth := max(20, m.width-inputStyle.GetHorizontalFrameSize())
	m.input.SetWidth(innerInputWidth)
	m.input.SetHeight(3)

	headerHeight := lipgloss.Height(m.headerView())
	inputHeight := lipgloss.Height(inputStyle.Width(m.width).Height(m.input.Height() + inputStyle.GetVerticalFrameSize()).Render(m.input.View()))
	footerHeight := lipgloss.Height(m.footerView())
	transcriptHeight := max(8, m.height-headerHeight-inputHeight-footerHeight)
	m.transcript.Width = max(20, m.width-transcriptStyle.GetHorizontalFrameSize())
	m.transcript.Height = max(6, transcriptHeight-transcriptStyle.GetVerticalFrameSize())
	m.refreshTranscript(followTail)
}

func (m agentTUIModel) transcriptContentWidth() int {
	if m.transcript.Width <= 0 {
		return 0
	}
	return m.transcript.Width
}
