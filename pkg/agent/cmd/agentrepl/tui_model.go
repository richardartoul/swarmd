package main

import (
	"context"
	"fmt"
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

	focus        agentTUIFocus
	width        int
	height       int
	showHelp     bool
	running      bool
	status       string
	currentCWD   string
	cachedTokens int
	entries      []transcriptEntry
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

func (m agentTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		return m, nil
	case tuiDecisionMsg:
		m.handleDecision(msg)
		return m, waitForTUIEvent(m.events)
	case tuiStepMsg:
		m.handleStep(msg.step)
		return m, waitForTUIEvent(m.events)
	case tuiResultMsg:
		m.handleResult(msg.result)
		return m, waitForTUIEvent(m.events)
	case tuiLiveOutputMsg:
		m.handleLiveOutput(msg)
		return m, waitForTUIEvent(m.events)
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		var cmd tea.Cmd
		m.transcript, cmd = m.transcript.Update(msg)
		return m, cmd
	default:
		return m, nil
	}
}

func (m agentTUIModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading agentrepl..."
	}

	header := m.headerView()
	transcriptStyle := transcriptPaneStyle(m.focus == agentTUIFocusTranscript)
	transcriptPane := transcriptStyle.
		Width(m.width).
		Height(m.transcript.Height + transcriptStyle.GetVerticalFrameSize()).
		Render(m.transcript.View())
	inputStyle := inputPaneStyle(m.focus == agentTUIFocusInput)
	inputPane := inputStyle.
		Width(m.width).
		Height(m.input.Height() + inputStyle.GetVerticalFrameSize()).
		Render(m.input.View())
	footer := m.footerView()

	return lipgloss.JoinVertical(lipgloss.Left, header, transcriptPane, inputPane, footer)
}

func (m *agentTUIModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.quit):
		m.cancel()
		return m, tea.Quit
	case key.Matches(msg, m.keys.help):
		m.showHelp = !m.showHelp
		m.status = ""
		m.resize()
		return m, nil
	case key.Matches(msg, m.keys.toggleFocus):
		m.toggleFocus()
		return m, nil
	}

	if m.focus == agentTUIFocusTranscript {
		if key.Matches(msg, m.keys.backToInput) {
			m.setFocus(agentTUIFocusInput)
			return m, nil
		}
		var cmd tea.Cmd
		m.transcript, cmd = m.transcript.Update(msg)
		if key.Matches(msg, m.keys.live) && m.transcript.AtBottom() {
			m.setFocus(agentTUIFocusInput)
		}
		return m, cmd
	}

	if key.Matches(msg, m.keys.history) {
		m.setFocus(agentTUIFocusTranscript)
		var cmd tea.Cmd
		m.transcript, cmd = m.transcript.Update(msg)
		return m, cmd
	}

	if key.Matches(msg, m.keys.send) {
		if cmd := m.handleSubmit(); cmd != nil {
			return m, cmd
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *agentTUIModel) handleSubmit() tea.Cmd {
	value := strings.TrimSpace(m.input.Value())
	switch value {
	case "":
		m.status = "enter a prompt before sending"
		return nil
	case ":help":
		m.showHelp = !m.showHelp
		m.input.SetValue("")
		m.status = ""
		m.resize()
		return nil
	case ":quit", ":exit":
		m.cancel()
		return tea.Quit
	}

	if m.running {
		m.status = "the agent is still running; wait for the current result before sending another prompt"
		return nil
	}

	trigger, ok := m.submitPrompt(value)
	if !ok {
		m.status = "unable to queue the prompt right now"
		return nil
	}

	m.running = true
	m.status = ""
	m.appendEntry(transcriptEntry{
		kind:  transcriptKindUser,
		title: trigger.ID,
		body:  value,
	})
	m.input.SetValue("")
	return nil
}

func (m *agentTUIModel) handleDecision(msg tuiDecisionMsg) {
	if msg.thought != "" {
		m.appendEntry(transcriptEntry{
			kind:  transcriptKindThought,
			title: fmt.Sprintf("step %d thinking", msg.step),
			body:  msg.thought,
		})
	}
	if msg.tool != "" {
		m.appendEntry(transcriptEntry{
			kind:  transcriptKindCommand,
			title: fmt.Sprintf("step %d tool", msg.step),
			body:  msg.tool,
		})
	}
	if msg.input != "" {
		m.appendEntry(transcriptEntry{
			kind:  transcriptKindCommand,
			title: fmt.Sprintf("step %d input", msg.step),
			body:  msg.input,
		})
	}
}

func (m *agentTUIModel) handleStep(step agent.Step) {
	if strings.TrimSpace(step.CWDAfter) != "" {
		m.currentCWD = step.CWDAfter
	}
	if !m.verbose {
		return
	}

	if output := renderStepActionOutput(step); output != "" {
		m.appendEntry(transcriptEntry{
			kind:  transcriptKindStdout,
			title: fmt.Sprintf("step %d output", step.Index),
			body:  output,
		})
	}

	title := fmt.Sprintf("step %d done", step.Index)
	body := fmt.Sprintf("[%s]", step.Status)
	if step.Status == agent.StepStatusExitStatus {
		body = fmt.Sprintf("[%s, exit=%d]", step.Status, step.ExitStatus)
	}
	if step.Error != "" {
		body += "\n" + step.Error
	}
	m.appendEntry(transcriptEntry{
		kind:  transcriptKindSystem,
		title: title,
		body:  body,
	})
}

func (m *agentTUIModel) handleResult(result agent.Result) {
	m.running = false
	m.cachedTokens = result.Usage.CachedTokens
	if strings.TrimSpace(result.CWD) != "" {
		m.currentCWD = result.CWD
	}

	if !m.verbose {
		for _, step := range result.Steps {
			if step.Status == agent.StepStatusOK {
				continue
			}
			body := string(step.Status)
			if step.Status == agent.StepStatusExitStatus {
				body = fmt.Sprintf("%s (exit=%d)", body, step.ExitStatus)
			}
			if step.Error != "" {
				body += "\n" + step.Error
			}
			m.appendEntry(transcriptEntry{
				kind:  transcriptKindError,
				title: fmt.Sprintf("step %d", step.Index),
				body:  body,
			})
		}
	}

	switch result.Status {
	case agent.ResultStatusFinished:
		m.appendEntry(transcriptEntry{
			kind:  transcriptKindAssistant,
			title: "assistant",
			body:  agent.RenderResultValue(result.Value),
		})
		m.status = ""
	default:
		body := string(result.Status)
		if strings.TrimSpace(result.Error) != "" {
			body += "\n" + result.Error
		}
		m.appendEntry(transcriptEntry{
			kind:  transcriptKindError,
			title: "result",
			body:  body,
		})
		m.status = string(result.Status)
	}
}

func (m *agentTUIModel) handleLiveOutput(msg tuiLiveOutputMsg) {
	if msg.text == "" {
		return
	}
	m.appendStream(msg.stream, msg.text)
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

func (m *agentTUIModel) appendEntry(entry transcriptEntry) {
	followTail := m.shouldFollowTranscript()
	m.entries = append(m.entries, entry)
	m.refreshTranscript(followTail)
}

func (m *agentTUIModel) appendStream(kind transcriptKind, text string) {
	followTail := m.shouldFollowTranscript()
	if len(m.entries) > 0 {
		last := &m.entries[len(m.entries)-1]
		if last.kind == kind {
			last.body += text
			m.refreshTranscript(followTail)
			return
		}
	}
	m.entries = append(m.entries, transcriptEntry{
		kind:  kind,
		title: string(kind),
		body:  text,
	})
	m.refreshTranscript(followTail)
}

func (m agentTUIModel) shouldFollowTranscript() bool {
	if len(m.entries) == 0 || m.transcript.Height == 0 {
		return true
	}
	return m.transcript.AtBottom()
}

func (m *agentTUIModel) refreshTranscript(followTail bool) {
	offset := m.transcript.YOffset
	m.transcript.SetContent(m.renderTranscript())
	if followTail {
		m.transcript.GotoBottom()
		return
	}
	maxOffset := maxInt(0, m.transcript.TotalLineCount()-m.transcript.Height)
	m.transcript.SetYOffset(minInt(offset, maxOffset))
}

func (m agentTUIModel) renderTranscript() string {
	if len(m.entries) == 0 {
		return mutedStyle.Render("No prompts yet.")
	}

	parts := make([]string, 0, len(m.entries))
	for _, entry := range m.entries {
		parts = append(parts, m.renderEntry(entry))
	}
	return strings.Join(parts, "\n\n")
}

func (m agentTUIModel) renderEntry(entry transcriptEntry) string {
	contentWidth := m.transcriptContentWidth()
	label := entryLabelStyle(entry.kind).Render(entry.title)
	body := strings.TrimRight(entry.body, "\n")
	if body == "" {
		return label
	}
	bodyStyle := entryBodyStyle(entry.kind)
	if contentWidth > 0 {
		bodyStyle = bodyStyle.Width(contentWidth).PaddingLeft(2)
		return label + "\n" + bodyStyle.Render(body)
	}
	return label + "\n" + bodyStyle.Render(prefixLines("  ", body))
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
	innerInputWidth := maxInt(20, m.width-inputStyle.GetHorizontalFrameSize())
	m.input.SetWidth(innerInputWidth)
	m.input.SetHeight(3)

	headerHeight := lipgloss.Height(m.headerView())
	inputHeight := lipgloss.Height(inputStyle.Width(m.width).Height(m.input.Height() + inputStyle.GetVerticalFrameSize()).Render(m.input.View()))
	footerHeight := lipgloss.Height(m.footerView())
	transcriptHeight := maxInt(8, m.height-headerHeight-inputHeight-footerHeight)
	m.transcript.Width = maxInt(20, m.width-transcriptStyle.GetHorizontalFrameSize())
	m.transcript.Height = maxInt(6, transcriptHeight-transcriptStyle.GetVerticalFrameSize())
	m.refreshTranscript(followTail)
}

func (m agentTUIModel) transcriptContentWidth() int {
	if m.transcript.Width <= 0 {
		return 0
	}
	return m.transcript.Width
}

func (m agentTUIModel) headerView() string {
	title := titleStyle.Render("agentrepl")
	meta := mutedStyle.Render(fmt.Sprintf("root: %s", m.rootDir))
	return title + "\n" + meta
}

func (m agentTUIModel) footerView() string {
	m.help.Width = m.width
	m.help.ShowAll = m.showHelp

	status := mutedStyle.Render(m.statusLine())
	helpView := m.help.View(m.keys)
	if strings.TrimSpace(helpView) == "" {
		return status
	}
	return status + "\n" + helpView
}

func (m agentTUIModel) statusLine() string {
	state := "idle"
	if m.running {
		state = "running"
	}

	parts := []string{
		fmt.Sprintf("model=%s", strings.TrimSpace(m.modelName)),
		fmt.Sprintf("cwd=%s", strings.TrimSpace(m.currentCWD)),
		fmt.Sprintf("state=%s", state),
		fmt.Sprintf("focus=%s", m.focus.label()),
		fmt.Sprintf("cached_tokens=%d", m.cachedTokens),
	}
	if strings.TrimSpace(m.status) != "" {
		parts = append(parts, m.status)
	}
	return strings.Join(parts, " | ")
}

func entryLabelStyle(kind transcriptKind) lipgloss.Style {
	switch kind {
	case transcriptKindUser:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	case transcriptKindAssistant:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	case transcriptKindThought:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("221"))
	case transcriptKindCommand:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("110"))
	case transcriptKindStderr, transcriptKindError:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("203"))
	case transcriptKindStdout:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	default:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("244"))
	}
}

func entryBodyStyle(kind transcriptKind) lipgloss.Style {
	switch kind {
	case transcriptKindThought:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	case transcriptKindCommand:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("153"))
	case transcriptKindStderr, transcriptKindError:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	default:
		return lipgloss.NewStyle()
	}
}

func transcriptPaneStyle(focused bool) lipgloss.Style {
	style := lipgloss.NewStyle().Border(lipgloss.NormalBorder())
	if focused {
		return style.BorderForeground(lipgloss.Color("62"))
	}
	return style.BorderForeground(lipgloss.Color("240"))
}

func inputPaneStyle(focused bool) lipgloss.Style {
	style := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(0, 1)
	if focused {
		return style.BorderForeground(lipgloss.Color("62"))
	}
	return style.BorderForeground(lipgloss.Color("240"))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("62")).
			Padding(0, 1)
	mutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244"))
)
