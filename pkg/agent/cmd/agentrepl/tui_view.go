// See LICENSE for licensing information

package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

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
		fmt.Sprintf("tokens(in=%d cached=%d out=%d)", m.usage.InputTokens, m.usage.CachedTokens, m.usage.OutputTokens),
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

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("62")).
			Padding(0, 1)
	mutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244"))
)
