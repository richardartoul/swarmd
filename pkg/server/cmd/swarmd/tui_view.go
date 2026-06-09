package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
)

func (m tuiModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading swarmd TUI..."
	}
	listWidth, detailWidth, bodyHeight := m.paneDimensions()
	header := m.headerView()
	footer := m.footerView()
	left := paneStyle(m.focus == tuiPaneList).Width(listWidth).Height(bodyHeight).Render(m.list.View())
	right := paneStyle(m.focus == tuiPaneDetail).Width(detailWidth).Height(bodyHeight).Render(m.detail.View())
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m tuiModel) headerView() string {
	tabStyle := lipgloss.NewStyle().Padding(0, 1)
	activeTab := tabStyle.Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62"))
	inactiveTab := tabStyle.Foreground(lipgloss.Color("252")).Background(lipgloss.Color("238"))
	tabs := make([]string, 0, len(m.sections))
	for idx, section := range m.sections {
		label := fmt.Sprintf("%d %s", idx+1, section.label())
		if idx == m.currentSection {
			tabs = append(tabs, activeTab.Render(label))
			continue
		}
		tabs = append(tabs, inactiveTab.Render(label))
	}
	meta := []string{
		fmt.Sprintf("focus: %s", m.focus.label()),
		"auto: " + m.autoRefreshSummary(),
	}
	if !m.lastRefreshAt.IsZero() {
		meta = append(meta, "updated: "+m.lastRefreshAt.Format("15:04:05"))
	}
	if loading := m.loadingSummary(); loading != "" {
		meta = append(meta, loading)
	}
	meta = append(meta, "db: "+m.dbPath)
	return strings.Join([]string{
		lipgloss.JoinHorizontal(lipgloss.Top, tabs...),
		m.breadcrumbView(),
		strings.Join(meta, " | "),
	}, "\n")
}

func (m tuiModel) footerView() string {
	m.help.Width = m.width
	m.help.ShowAll = m.showHelp
	lines := []string{m.statusLine()}
	if actions := strings.TrimSpace(m.footerActionView()); actions != "" {
		lines = append(lines, actions)
	}
	helpView := m.help.View(m.keys)
	if strings.TrimSpace(helpView) != "" {
		lines = append(lines, helpView)
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) statusLine() string {
	if strings.TrimSpace(m.status) != "" {
		return m.status
	}
	status := []string{fmt.Sprintf("focus=%s", m.focus.label())}
	if visible := len(m.list.VisibleItems()); visible > 0 {
		status = append(status, fmt.Sprintf("item=%d/%d", m.list.Index()+1, visible))
	}
	if total := len(m.list.Items()); total > 0 && total != len(m.list.VisibleItems()) {
		status = append(status, fmt.Sprintf("total=%d", total))
	}
	if page := m.currentPage(); page != nil {
		if item, ok := m.selectedItem(); ok {
			actions := availableTUIActions(*page, item)
			if len(actions) > 0 {
				status = append(status, "shortcuts: "+strings.Join(actions, " "))
			}
		}
	}
	if m.pageLoading {
		status = append(status, "refreshing view")
	} else if m.detailLoading {
		status = append(status, "loading details")
	}
	if m.list.FilterState() == list.Filtering {
		status = append(status, "filtering list")
	} else if m.list.IsFiltered() {
		status = append(status, fmt.Sprintf("filter=%q", m.list.FilterValue()))
	}
	return strings.Join(status, " | ")
}

func paneStyle(focused bool) lipgloss.Style {
	style := lipgloss.NewStyle().Border(lipgloss.NormalBorder())
	if focused {
		return style.BorderForeground(lipgloss.Color("62"))
	}
	return style.BorderForeground(lipgloss.Color("240"))
}

func (m tuiModel) loadingDetailText(page tuiPage, item tuiItem) string {
	lines := []string{
		page.title,
		"",
		fmt.Sprintf("Loading details for %s...", item.title),
	}
	if m.pageLoading {
		lines = append(lines, "", "Refreshing view in the background...")
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) breadcrumbView() string {
	if len(m.stack) == 0 {
		return "path: no page selected"
	}
	parts := make([]string, 0, len(m.stack))
	for _, page := range m.stack {
		parts = append(parts, page.title)
	}
	return "path: " + strings.Join(parts, " > ")
}

func (m tuiModel) autoRefreshSummary() string {
	if !m.autoRefresh {
		return "off"
	}
	return "on/" + m.autoInterval.String()
}

func (m tuiModel) loadingSummary() string {
	parts := make([]string, 0, 2)
	if m.pageLoading {
		parts = append(parts, "page")
	}
	if m.detailLoading {
		parts = append(parts, "detail")
	}
	if len(parts) == 0 {
		return ""
	}
	return "loading: " + strings.Join(parts, "+")
}

func wrapTUIDetail(detail string, width int) string {
	if width <= 0 || strings.TrimSpace(detail) == "" {
		return detail
	}
	return strings.TrimRight(lipgloss.NewStyle().Width(width).Render(detail), "\n")
}
