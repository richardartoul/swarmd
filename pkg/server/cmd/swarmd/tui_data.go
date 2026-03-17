package main

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
)

type tuiSection int

const (
	tuiSectionNamespaces tuiSection = iota
	tuiSectionAgents
	tuiSectionMailbox
	tuiSectionRuns
	tuiSectionSchedules
)

var allTUISections = []tuiSection{
	tuiSectionNamespaces,
	tuiSectionAgents,
	tuiSectionMailbox,
	tuiSectionRuns,
	tuiSectionSchedules,
}

func (s tuiSection) label() string {
	switch s {
	case tuiSectionNamespaces:
		return "Namespaces"
	case tuiSectionAgents:
		return "Agents"
	case tuiSectionMailbox:
		return "Mailbox"
	case tuiSectionRuns:
		return "Runs"
	case tuiSectionSchedules:
		return "Schedules"
	default:
		return "Unknown"
	}
}

func (s tuiSection) rootPage() tuiPage {
	switch s {
	case tuiSectionNamespaces:
		return newNamespacesPage()
	case tuiSectionAgents:
		return newAgentsPage(tuiPageQuery{})
	case tuiSectionMailbox:
		return newMailboxPage(tuiPageQuery{})
	case tuiSectionRuns:
		return newRunsPage(tuiPageQuery{})
	case tuiSectionSchedules:
		return newSchedulesPage(tuiPageQuery{})
	default:
		return newNamespacesPage()
	}
}

type tuiPageKind string

const (
	tuiPageNamespaces tuiPageKind = "namespaces"
	tuiPageAgents     tuiPageKind = "agents"
	tuiPageMailbox    tuiPageKind = "mailbox"
	tuiPageRuns       tuiPageKind = "runs"
	tuiPageSchedules  tuiPageKind = "schedules"
	tuiPageThread     tuiPageKind = "thread"
	tuiPageSteps      tuiPageKind = "steps"
)

type tuiNavigationAction string

const (
	tuiActionEnter     tuiNavigationAction = "enter"
	tuiActionAgent     tuiNavigationAction = "agent"
	tuiActionMailbox   tuiNavigationAction = "mailbox"
	tuiActionRuns      tuiNavigationAction = "runs"
	tuiActionSchedules tuiNavigationAction = "schedules"
	tuiActionThread    tuiNavigationAction = "thread"
	tuiActionSteps     tuiNavigationAction = "steps"
)

type tuiPageQuery struct {
	NamespaceID string
	AgentID     string
	MessageID   string
	RunID       string
	ThreadID    string
}

type tuiPage struct {
	kind    tuiPageKind
	section tuiSection
	title   string
	empty   string
	query   tuiPageQuery
	items   []list.Item
	cursor  int
}

func (p tuiPage) clampCursor() int {
	if len(p.items) == 0 {
		return 0
	}
	if p.cursor < 0 {
		return 0
	}
	if p.cursor >= len(p.items) {
		return len(p.items) - 1
	}
	return p.cursor
}

type tuiItemKind string

const (
	tuiItemNamespace     tuiItemKind = "namespace"
	tuiItemAgent         tuiItemKind = "agent"
	tuiItemMailbox       tuiItemKind = "mailbox"
	tuiItemRun           tuiItemKind = "run"
	tuiItemSchedule      tuiItemKind = "schedule"
	tuiItemThreadMessage tuiItemKind = "thread_message"
	tuiItemStep          tuiItemKind = "step"
)

type tuiItem struct {
	kind        tuiItemKind
	title       string
	desc        string
	filter      string
	namespaceID string
	agentID     string
	messageID   string
	runID       string
	threadID    string
	value       any
}

func (i tuiItem) Title() string       { return i.title }
func (i tuiItem) Description() string { return i.desc }
func (i tuiItem) FilterValue() string { return i.filter }

func newNamespacesPage() tuiPage {
	return tuiPage{
		kind:    tuiPageNamespaces,
		section: tuiSectionNamespaces,
		title:   "Namespaces",
		empty:   "No namespaces found.",
	}
}

func newAgentsPage(query tuiPageQuery) tuiPage {
	title := "Agents"
	switch {
	case query.NamespaceID != "" && query.AgentID != "":
		title = fmt.Sprintf("Agent %s/%s", query.NamespaceID, query.AgentID)
	case query.NamespaceID != "":
		title = fmt.Sprintf("Agents / %s", query.NamespaceID)
	}
	return tuiPage{
		kind:    tuiPageAgents,
		section: tuiSectionAgents,
		title:   title,
		empty:   "No agents found.",
		query:   query,
	}
}

func newMailboxPage(query tuiPageQuery) tuiPage {
	title := "Mailbox"
	switch {
	case query.NamespaceID != "" && query.MessageID != "":
		title = fmt.Sprintf("Message %s/%s", query.NamespaceID, query.MessageID)
	case query.NamespaceID != "" && query.AgentID != "":
		title = fmt.Sprintf("Mailbox / %s / %s", query.NamespaceID, query.AgentID)
	case query.NamespaceID != "":
		title = fmt.Sprintf("Mailbox / %s", query.NamespaceID)
	}
	return tuiPage{
		kind:    tuiPageMailbox,
		section: tuiSectionMailbox,
		title:   title,
		empty:   "No mailbox messages found.",
		query:   query,
	}
}

func newRunsPage(query tuiPageQuery) tuiPage {
	title := "Runs"
	switch {
	case query.NamespaceID != "" && query.RunID != "":
		title = fmt.Sprintf("Run %s/%s", query.NamespaceID, query.RunID)
	case query.NamespaceID != "" && query.AgentID != "":
		title = fmt.Sprintf("Runs / %s / %s", query.NamespaceID, query.AgentID)
	case query.NamespaceID != "":
		title = fmt.Sprintf("Runs / %s", query.NamespaceID)
	}
	return tuiPage{
		kind:    tuiPageRuns,
		section: tuiSectionRuns,
		title:   title,
		empty:   "No runs found.",
		query:   query,
	}
}

func newSchedulesPage(query tuiPageQuery) tuiPage {
	title := "Schedules"
	switch {
	case query.NamespaceID != "" && query.AgentID != "":
		title = fmt.Sprintf("Schedules / %s / %s", query.NamespaceID, query.AgentID)
	case query.NamespaceID != "":
		title = fmt.Sprintf("Schedules / %s", query.NamespaceID)
	}
	return tuiPage{
		kind:    tuiPageSchedules,
		section: tuiSectionSchedules,
		title:   title,
		empty:   "No schedules found.",
		query:   query,
	}
}

func newThreadPage(query tuiPageQuery) tuiPage {
	return tuiPage{
		kind:    tuiPageThread,
		section: tuiSectionMailbox,
		title:   fmt.Sprintf("Thread %s/%s", query.NamespaceID, query.ThreadID),
		empty:   "No thread messages found.",
		query:   query,
	}
}

func newStepsPage(query tuiPageQuery) tuiPage {
	return tuiPage{
		kind:    tuiPageSteps,
		section: tuiSectionRuns,
		title:   fmt.Sprintf("Steps %s/%s", query.NamespaceID, query.RunID),
		empty:   "No steps found.",
		query:   query,
	}
}
