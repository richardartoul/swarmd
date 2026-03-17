package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"

	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func loadTUIPage(ctx context.Context, store *cpstore.Store, page tuiPage) (tuiPage, error) {
	var (
		items []list.Item
		err   error
	)
	switch page.kind {
	case tuiPageNamespaces:
		items, err = loadNamespaceItems(ctx, store)
	case tuiPageAgents:
		items, err = loadAgentItems(ctx, store, page.query)
	case tuiPageMailbox:
		items, err = loadMailboxItems(ctx, store, page.query)
	case tuiPageRuns:
		items, err = loadRunItems(ctx, store, page.query)
	case tuiPageSchedules:
		items, err = loadScheduleItems(ctx, store, page.query)
	case tuiPageThread:
		items, err = loadThreadItems(ctx, store, page.query)
	case tuiPageSteps:
		items, err = loadStepItems(ctx, store, page.query)
	default:
		err = fmt.Errorf("unsupported TUI page %q", page.kind)
	}
	if err != nil {
		return tuiPage{}, err
	}
	page.items = items
	page.cursor = page.clampCursor()
	return page, nil
}

func loadNamespaceItems(ctx context.Context, store *cpstore.Store) ([]list.Item, error) {
	namespaces, err := store.ListNamespaces(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]list.Item, 0, len(namespaces))
	for _, ns := range namespaces {
		snapshot, err := store.SnapshotNamespace(ctx, ns.ID)
		if err != nil {
			return nil, err
		}
		row := namespaceInspectRow{
			Namespace:     snapshot.Namespace,
			AgentCount:    len(snapshot.Agents),
			ScheduleCount: len(snapshot.Schedules),
			Mailbox:       snapshot.Mailbox,
		}
		desc := fmt.Sprintf(
			"%s | agents=%d schedules=%d queued=%d leased=%d done=%d dead=%d",
			displayEmpty(snapshot.Namespace.Name),
			row.AgentCount,
			row.ScheduleCount,
			row.Mailbox.Queued,
			row.Mailbox.Leased,
			row.Mailbox.Completed,
			row.Mailbox.DeadLetter,
		)
		items = append(items, tuiItem{
			kind:        tuiItemNamespace,
			title:       snapshot.Namespace.ID,
			desc:        desc,
			filter:      strings.Join([]string{snapshot.Namespace.ID, snapshot.Namespace.Name, desc}, " "),
			namespaceID: snapshot.Namespace.ID,
			value:       row,
		})
	}
	return items, nil
}

func loadAgentItems(ctx context.Context, store *cpstore.Store, query tuiPageQuery) ([]list.Item, error) {
	var agents []cpstore.RunnableAgent
	if strings.TrimSpace(query.AgentID) != "" {
		if strings.TrimSpace(query.NamespaceID) == "" {
			return nil, fmt.Errorf("agent view requires namespace id when selecting a single agent")
		}
		agent, err := store.GetAgent(ctx, query.NamespaceID, query.AgentID)
		if err != nil {
			return nil, err
		}
		agents = []cpstore.RunnableAgent{agent}
	} else {
		var err error
		agents, err = store.ListAgents(ctx, cpstore.ListAgentsParams{NamespaceID: query.NamespaceID})
		if err != nil {
			return nil, err
		}
	}
	items := make([]list.Item, 0, len(agents))
	for _, agent := range agents {
		desc := fmt.Sprintf(
			"state=%s model=%s net=%t root=%s",
			agent.DesiredState,
			joinModel(agent.ModelProvider, agent.ModelName),
			agent.AllowNetwork,
			formatRootWithFilesystemKind(agent.RootPath, agent.ConfigJSON),
		)
		items = append(items, tuiItem{
			kind:        tuiItemAgent,
			title:       fmt.Sprintf("%s/%s", agent.NamespaceID, agent.ID),
			desc:        desc,
			filter:      strings.Join([]string{agent.NamespaceID, agent.ID, agent.Name, agent.SystemPrompt, desc}, " "),
			namespaceID: agent.NamespaceID,
			agentID:     agent.ID,
			value:       agent,
		})
	}
	return items, nil
}

func loadMailboxItems(ctx context.Context, store *cpstore.Store, query tuiPageQuery) ([]list.Item, error) {
	var messages []cpstore.MailboxMessageRecord
	if strings.TrimSpace(query.MessageID) != "" {
		if strings.TrimSpace(query.NamespaceID) == "" {
			return nil, fmt.Errorf("message view requires namespace id when selecting a single message")
		}
		message, err := store.GetMailboxMessage(ctx, query.NamespaceID, query.MessageID)
		if err != nil {
			return nil, err
		}
		messages = []cpstore.MailboxMessageRecord{message}
	} else {
		var err error
		messages, err = store.ListMailboxMessages(ctx, cpstore.ListMailboxMessagesParams{
			NamespaceID: query.NamespaceID,
			AgentID:     query.AgentID,
			Limit:       200,
		})
		if err != nil {
			return nil, err
		}
	}
	items := make([]list.Item, 0, len(messages))
	for _, message := range messages {
		desc := fmt.Sprintf(
			"%s | %s -> %s | %s | %s | attempts=%d/%d",
			message.NamespaceID,
			displayParty(message.SenderAgentID),
			message.RecipientAgentID,
			message.Status,
			summarizeText(message.Kind, 32),
			message.AttemptCount,
			message.MaxAttempts,
		)
		items = append(items, tuiItem{
			kind:        tuiItemMailbox,
			title:       message.ID,
			desc:        desc,
			filter:      strings.Join([]string{message.NamespaceID, message.ID, message.ThreadID, message.RecipientAgentID, message.Kind, message.LastError, desc}, " "),
			namespaceID: message.NamespaceID,
			agentID:     message.RecipientAgentID,
			messageID:   message.ID,
			runID:       message.RunID,
			threadID:    message.ThreadID,
			value:       message,
		})
	}
	return items, nil
}

func loadRunItems(ctx context.Context, store *cpstore.Store, query tuiPageQuery) ([]list.Item, error) {
	var runs []cpstore.RunRecord
	if strings.TrimSpace(query.RunID) != "" {
		if strings.TrimSpace(query.NamespaceID) == "" {
			return nil, fmt.Errorf("run view requires namespace id when selecting a single run")
		}
		run, err := store.GetRun(ctx, query.NamespaceID, query.RunID)
		if err != nil {
			return nil, err
		}
		runs = []cpstore.RunRecord{run}
	} else {
		var err error
		runs, err = store.ListRuns(ctx, cpstore.ListRunsParams{
			NamespaceID: query.NamespaceID,
			AgentID:     query.AgentID,
			Limit:       200,
		})
		if err != nil {
			return nil, err
		}
	}
	items := make([]list.Item, 0, len(runs))
	for _, run := range runs {
		desc := fmt.Sprintf(
			"%s | %s | %s | duration=%s | message=%s",
			run.NamespaceID,
			run.AgentID,
			run.Status,
			formatDuration(run.Duration),
			displayEmpty(run.MessageID),
		)
		items = append(items, tuiItem{
			kind:        tuiItemRun,
			title:       run.ID,
			desc:        desc,
			filter:      strings.Join([]string{run.NamespaceID, run.ID, run.AgentID, run.TriggerID, run.Error, desc}, " "),
			namespaceID: run.NamespaceID,
			agentID:     run.AgentID,
			messageID:   run.MessageID,
			runID:       run.ID,
			value:       run,
		})
	}
	return items, nil
}

func loadScheduleItems(ctx context.Context, store *cpstore.Store, query tuiPageQuery) ([]list.Item, error) {
	schedules, err := loadSchedules(ctx, store, query.NamespaceID, query.AgentID)
	if err != nil {
		return nil, err
	}
	items := make([]list.Item, 0, len(schedules))
	for _, schedule := range schedules {
		desc := fmt.Sprintf(
			"%s | enabled=%t | cron=%s | next=%s",
			schedule.AgentID,
			schedule.Enabled,
			schedule.CronExpr,
			formatTimePtr(schedule.NextFireAt),
		)
		items = append(items, tuiItem{
			kind:        tuiItemSchedule,
			title:       fmt.Sprintf("%s/%s", schedule.NamespaceID, schedule.ID),
			desc:        desc,
			filter:      strings.Join([]string{schedule.NamespaceID, schedule.ID, schedule.AgentID, schedule.CronExpr, schedule.TimeZone, desc}, " "),
			namespaceID: schedule.NamespaceID,
			agentID:     schedule.AgentID,
			value:       schedule,
		})
	}
	return items, nil
}

func loadThreadItems(ctx context.Context, store *cpstore.Store, query tuiPageQuery) ([]list.Item, error) {
	messages, err := store.ListThreadMessages(ctx, query.NamespaceID, query.ThreadID, 200)
	if err != nil {
		return nil, err
	}
	items := make([]list.Item, 0, len(messages))
	for _, message := range messages {
		desc := fmt.Sprintf(
			"%s -> %s | %s | %s | created=%s",
			displayParty(message.SenderAgentID),
			message.RecipientAgentID,
			message.Kind,
			message.Status,
			formatTime(message.CreatedAt),
		)
		items = append(items, tuiItem{
			kind:        tuiItemThreadMessage,
			title:       message.ID,
			desc:        desc,
			filter:      strings.Join([]string{message.ID, message.ThreadID, message.SenderAgentID, message.RecipientAgentID, message.Kind, desc}, " "),
			namespaceID: query.NamespaceID,
			agentID:     preferredThreadAgent(message),
			messageID:   message.ID,
			threadID:    message.ThreadID,
			value:       message,
		})
	}
	return items, nil
}

func loadStepItems(ctx context.Context, store *cpstore.Store, query tuiPageQuery) ([]list.Item, error) {
	steps, err := store.ListStepsByRun(ctx, query.NamespaceID, query.RunID)
	if err != nil {
		return nil, err
	}
	items := make([]list.Item, 0, len(steps))
	for _, step := range steps {
		desc := fmt.Sprintf(
			"%s | %s | duration=%s | exit=%d | %s",
			stepTypeLabel(step),
			step.Status,
			formatDuration(step.Duration),
			step.ExitStatus,
			summarizeStepAction(step, 48),
		)
		items = append(items, tuiItem{
			kind:        tuiItemStep,
			title:       fmt.Sprintf("step %d", step.StepIndex),
			desc:        desc,
			filter:      strings.Join([]string{step.RunID, step.MessageID, step.AgentID, step.StepType, step.Thought, step.Shell, step.ActionName, step.ActionInput, desc}, " "),
			namespaceID: step.NamespaceID,
			agentID:     step.AgentID,
			messageID:   step.MessageID,
			runID:       step.RunID,
			value:       step,
		})
	}
	return items, nil
}
