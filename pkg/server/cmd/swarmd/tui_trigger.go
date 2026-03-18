package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func newTUITriggerInput() textinput.Model {
	input := textinput.New()
	input.Prompt = ""
	input.Placeholder = "Type a prompt to enqueue"
	return input
}

func (m *tuiModel) startTriggerMode() (tea.Cmd, bool) {
	page := m.currentPage()
	if page == nil {
		return nil, false
	}
	item, ok := m.selectedItem()
	if !ok {
		return nil, false
	}
	target, ok := triggerTarget(*page, item)
	if !ok {
		return nil, false
	}
	m.triggering = true
	m.triggerTarget = target
	m.triggerInput.SetValue("")
	m.triggerInput.Placeholder = fmt.Sprintf("Type a prompt for %s", target.Label)
	cmd := m.triggerInput.Focus()
	m.status = ""
	m.resize()
	return cmd, true
}

func (m *tuiModel) exitTriggerMode() {
	m.triggering = false
	m.triggerTarget = tuiTriggerTarget{}
	m.triggerInput.SetValue("")
	m.triggerInput.Blur()
	m.resize()
}

func (m *tuiModel) handleTriggerKey(msg tea.KeyMsg) (tea.Cmd, error) {
	switch msg.String() {
	case "esc":
		m.status = ""
		m.exitTriggerMode()
		return nil, nil
	case "ctrl+u":
		m.status = ""
		m.triggerInput.SetValue("")
		return nil, nil
	case "enter":
		return nil, m.submitTriggerPrompt()
	default:
		m.status = ""
		var cmd tea.Cmd
		m.triggerInput, cmd = m.triggerInput.Update(msg)
		return cmd, nil
	}
}

func (m *tuiModel) submitTriggerPrompt() error {
	if !m.triggering {
		return nil
	}
	prompt := m.triggerInput.Value()
	agentRecord, err := m.store.GetAgent(m.ctx, m.triggerTarget.NamespaceID, m.triggerTarget.AgentID)
	if err != nil {
		return fmt.Errorf("load agent %s: %w", m.triggerTarget.Label, err)
	}
	params := cpstore.CreateMailboxMessageParams{
		NamespaceID:      m.triggerTarget.NamespaceID,
		RecipientAgentID: m.triggerTarget.AgentID,
		Kind:             "user.prompt",
		Payload:          map[string]any{"text": prompt},
		Metadata:         map[string]any{"source": "tui.manual_trigger"},
	}
	if agentRecord.MaxAttempts > 0 {
		params.MaxAttempts = agentRecord.MaxAttempts
	}
	targetLabel := m.triggerTarget.Label
	record, err := m.store.EnqueueMessage(m.ctx, params)
	if err != nil {
		return fmt.Errorf("enqueue manual trigger for %s: %w", targetLabel, err)
	}
	m.exitTriggerMode()
	if err := m.reloadCurrentPage(); err != nil {
		return err
	}
	m.status = fmt.Sprintf("queued prompt for %s (message=%s thread=%s)", targetLabel, record.ID, record.ThreadID)
	return nil
}

func (m tuiModel) footerActionView() string {
	if m.triggering {
		return strings.Join([]string{
			fmt.Sprintf("%s: %s", m.triggerTarget.ActionLabel, m.triggerTarget.Label),
			"Prompt: " + m.triggerInput.View(),
			strings.Join([]string{
				renderFooterButton("enter", "Submit"),
				renderFooterButton("ctrl+u", "Clear"),
				renderFooterButton("esc", "Cancel"),
			}, " "),
		}, "\n")
	}
	page := m.currentPage()
	if page == nil {
		return ""
	}
	item, ok := m.selectedItem()
	if !ok {
		return ""
	}
	target, ok := triggerTarget(*page, item)
	if !ok {
		return ""
	}
	return renderFooterButton("x", target.ActionLabel)
}

func renderFooterButton(keyLabel, label string) string {
	return fmt.Sprintf("[ %s %s ]", keyLabel, label)
}
