// See LICENSE for licensing information

package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/bubbles/key"
	"github.com/richardartoul/swarmd/pkg/agent"
)

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
	m.usage = result.Usage
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

func (m *agentTUIModel) refreshTranscript(followTail bool) {
	offset := m.transcript.YOffset
	m.transcript.SetContent(m.renderTranscript())
	if followTail {
		m.transcript.GotoBottom()
		return
	}
	maxOffset := max(0, m.transcript.TotalLineCount()-m.transcript.Height)
	m.transcript.SetYOffset(min(offset, maxOffset))
}
