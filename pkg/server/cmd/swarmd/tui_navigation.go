package main

import (
	"strings"

	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func nextTUIPage(page tuiPage, item tuiItem, action tuiNavigationAction) (tuiPage, bool) {
	switch action {
	case tuiActionEnter:
		switch page.kind {
		case tuiPageNamespaces:
			return newAgentsPage(tuiPageQuery{NamespaceID: item.namespaceID}), true
		case tuiPageAgents:
			return newRunsPage(tuiPageQuery{NamespaceID: item.namespaceID, AgentID: item.agentID}), true
		case tuiPageMailbox:
			if item.threadID != "" {
				return newThreadPage(tuiPageQuery{NamespaceID: item.namespaceID, ThreadID: item.threadID}), true
			}
			if item.runID != "" {
				return newRunsPage(tuiPageQuery{NamespaceID: item.namespaceID, RunID: item.runID}), true
			}
		case tuiPageRuns:
			return newStepsPage(tuiPageQuery{NamespaceID: item.namespaceID, RunID: item.runID}), true
		case tuiPageSchedules:
			return newAgentsPage(tuiPageQuery{NamespaceID: item.namespaceID, AgentID: item.agentID}), true
		}
	case tuiActionAgent:
		if agentID := linkedAgentID(item); agentID != "" {
			return newAgentsPage(tuiPageQuery{NamespaceID: item.namespaceID, AgentID: agentID}), true
		}
	case tuiActionMailbox:
		switch item.kind {
		case tuiItemAgent:
			return newMailboxPage(tuiPageQuery{NamespaceID: item.namespaceID, AgentID: item.agentID}), true
		case tuiItemRun, tuiItemStep:
			if item.messageID != "" {
				return newMailboxPage(tuiPageQuery{NamespaceID: item.namespaceID, MessageID: item.messageID}), true
			}
		}
	case tuiActionRuns:
		if agentID := linkedAgentID(item); agentID != "" {
			return newRunsPage(tuiPageQuery{NamespaceID: item.namespaceID, AgentID: agentID}), true
		}
	case tuiActionSchedules:
		switch item.kind {
		case tuiItemNamespace:
			return newSchedulesPage(tuiPageQuery{NamespaceID: item.namespaceID}), true
		default:
			if agentID := linkedAgentID(item); agentID != "" {
				return newSchedulesPage(tuiPageQuery{NamespaceID: item.namespaceID, AgentID: agentID}), true
			}
		}
	case tuiActionThread:
		if item.threadID != "" {
			return newThreadPage(tuiPageQuery{NamespaceID: item.namespaceID, ThreadID: item.threadID}), true
		}
	case tuiActionSteps:
		if item.runID != "" {
			return newStepsPage(tuiPageQuery{NamespaceID: item.namespaceID, RunID: item.runID}), true
		}
	}
	return tuiPage{}, false
}

func availableTUIActions(page tuiPage, item tuiItem) []string {
	type candidate struct {
		action tuiNavigationAction
		label  string
	}
	candidates := []candidate{
		{action: tuiActionEnter, label: "enter"},
		{action: tuiActionAgent, label: "a"},
		{action: tuiActionMailbox, label: "m"},
		{action: tuiActionRuns, label: "r"},
		{action: tuiActionSchedules, label: "s"},
		{action: tuiActionThread, label: "t"},
		{action: tuiActionSteps, label: "p"},
	}
	labels := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := nextTUIPage(page, item, candidate.action); ok {
			labels = append(labels, candidate.label)
		}
	}
	return labels
}

func linkedAgentID(item tuiItem) string {
	if strings.TrimSpace(item.agentID) != "" {
		return item.agentID
	}
	if message, ok := item.value.(cpstore.MailboxThreadMessage); ok {
		return preferredThreadAgent(message)
	}
	return ""
}

func preferredThreadAgent(message cpstore.MailboxThreadMessage) string {
	if strings.TrimSpace(message.RecipientAgentID) != "" {
		return message.RecipientAgentID
	}
	return strings.TrimSpace(message.SenderAgentID)
}
