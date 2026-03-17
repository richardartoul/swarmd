package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/richardartoul/swarmd/pkg/server"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func renderTUIDetail(ctx context.Context, store *cpstore.Store, page tuiPage, selected tuiItem) (string, error) {
	switch page.kind {
	case tuiPageNamespaces:
		snapshot, err := store.SnapshotNamespace(ctx, selected.namespaceID)
		if err != nil {
			return "", err
		}
		return renderNamespaceSnapshotDetail(snapshot), nil
	case tuiPageAgents:
		agent, err := store.GetAgent(ctx, selected.namespaceID, selected.agentID)
		if err != nil {
			return "", err
		}
		schedules, err := loadSchedules(ctx, store, selected.namespaceID, selected.agentID)
		if err != nil {
			return "", err
		}
		return renderText(func(w io.Writer) {
			renderAgentShow(w, agentShowData{
				Agent:     agent,
				Schedules: schedules,
			})
		}), nil
	case tuiPageMailbox:
		message, err := store.GetMailboxMessage(ctx, selected.namespaceID, selected.messageID)
		if err != nil {
			return "", err
		}
		return renderText(func(w io.Writer) {
			renderMailboxShow(w, message)
		}), nil
	case tuiPageRuns:
		run, err := store.GetRun(ctx, selected.namespaceID, selected.runID)
		if err != nil {
			return "", err
		}
		steps, err := store.ListStepsByRun(ctx, selected.namespaceID, selected.runID)
		if err != nil {
			return "", err
		}
		var message *cpstore.MailboxMessageRecord
		if strings.TrimSpace(run.MessageID) != "" {
			record, err := store.GetMailboxMessage(ctx, run.NamespaceID, run.MessageID)
			if err == nil {
				message = &record
			} else if !errors.Is(err, cpstore.ErrNotFound) {
				return "", err
			}
		}
		return renderText(func(w io.Writer) {
			renderRunShow(w, runShowData{
				Run:     run,
				Message: message,
				Steps:   steps,
			})
		}), nil
	case tuiPageSchedules:
		schedule, ok := selected.value.(cpstore.ScheduleRecord)
		if !ok {
			return "", fmt.Errorf("unexpected schedule item type %T", selected.value)
		}
		return renderScheduleDetail(schedule), nil
	case tuiPageThread:
		messages, err := store.ListThreadMessages(ctx, page.query.NamespaceID, page.query.ThreadID, 200)
		if err != nil {
			return "", err
		}
		return renderThreadHistoryDetail(page.query.NamespaceID, page.query.ThreadID, messages, selected), nil
	case tuiPageSteps:
		step, ok := selected.value.(cpstore.StepRecord)
		if !ok {
			return "", fmt.Errorf("unexpected step item type %T", selected.value)
		}
		return renderStepDetail(step), nil
	default:
		return "", fmt.Errorf("unsupported detail page %q", page.kind)
	}
}

func renderNamespaceSnapshotDetail(snapshot cpstore.NamespaceSnapshot) string {
	var out bytes.Buffer
	fmt.Fprintln(&out, "Namespace")
	writeKeyValue(&out, "namespace", snapshot.Namespace.ID)
	writeKeyValue(&out, "name", snapshot.Namespace.Name)
	writeKeyValue(&out, "created_at", formatTime(snapshot.Namespace.CreatedAt))
	writeKeyValue(&out, "updated_at", formatTime(snapshot.Namespace.UpdatedAt))
	writeKeyValue(&out, "agents", fmt.Sprintf("%d", len(snapshot.Agents)))
	writeKeyValue(&out, "schedules", fmt.Sprintf("%d", len(snapshot.Schedules)))
	writeKeyValue(&out, "queued", fmt.Sprintf("%d", snapshot.Mailbox.Queued))
	writeKeyValue(&out, "leased", fmt.Sprintf("%d", snapshot.Mailbox.Leased))
	writeKeyValue(&out, "completed", fmt.Sprintf("%d", snapshot.Mailbox.Completed))
	writeKeyValue(&out, "dead_letter", fmt.Sprintf("%d", snapshot.Mailbox.DeadLetter))
	writeBlock(&out, "limits", prettyJSON(snapshot.Namespace.LimitsJSON))
	if len(snapshot.Agents) > 0 {
		fmt.Fprintln(&out)
		fmt.Fprintln(&out, "agents:")
		renderAgentList(&out, snapshot.Agents)
	}
	if len(snapshot.Schedules) > 0 {
		fmt.Fprintln(&out)
		fmt.Fprintln(&out, "schedules:")
		renderScheduleList(&out, snapshot.Schedules)
	}
	return strings.TrimRight(out.String(), "\n")
}

func renderScheduleDetail(schedule cpstore.ScheduleRecord) string {
	var out bytes.Buffer
	fmt.Fprintln(&out, "Schedule")
	writeKeyValue(&out, "namespace", schedule.NamespaceID)
	writeKeyValue(&out, "schedule", schedule.ID)
	writeKeyValue(&out, "agent", schedule.AgentID)
	writeKeyValue(&out, "enabled", fmt.Sprintf("%t", schedule.Enabled))
	writeKeyValue(&out, "cron", schedule.CronExpr)
	writeKeyValue(&out, "timezone", schedule.TimeZone)
	writeKeyValue(&out, "next_fire_at", formatTimePtr(schedule.NextFireAt))
	writeKeyValue(&out, "last_fire_at", formatTimePtr(schedule.LastFireAt))
	writeKeyValue(&out, "created_at", formatTime(schedule.CreatedAt))
	writeKeyValue(&out, "updated_at", formatTime(schedule.UpdatedAt))
	writeBlock(&out, "payload", server.RenderEnvelope(schedule.PayloadJSON))
	return strings.TrimRight(out.String(), "\n")
}

func renderThreadHistoryDetail(namespaceID, threadID string, messages []cpstore.MailboxThreadMessage, selected tuiItem) string {
	var out bytes.Buffer
	renderThreadShow(&out, threadShowData{
		NamespaceID: namespaceID,
		ThreadID:    threadID,
		Messages:    messages,
	})
	if message, ok := selected.value.(cpstore.MailboxThreadMessage); ok {
		fmt.Fprintln(&out)
		fmt.Fprintf(&out, "selected_message: %s\n", message.ID)
		writeKeyValue(&out, "from", displayParty(message.SenderAgentID))
		writeKeyValue(&out, "to", message.RecipientAgentID)
		writeKeyValue(&out, "kind", message.Kind)
		writeKeyValue(&out, "status", string(message.Status))
		writeKeyValue(&out, "created_at", formatTime(message.CreatedAt))
		writeKeyValue(&out, "completed_at", formatTimePtr(message.CompletedAt))
		writeBlock(&out, "payload", server.RenderEnvelope(message.PayloadJSON))
	}
	return strings.TrimRight(out.String(), "\n")
}

func renderStepDetail(step cpstore.StepRecord) string {
	var out bytes.Buffer
	fmt.Fprintln(&out, "Step")
	writeKeyValue(&out, "namespace", step.NamespaceID)
	writeKeyValue(&out, "run", step.RunID)
	writeKeyValue(&out, "message", step.MessageID)
	writeKeyValue(&out, "agent", step.AgentID)
	writeKeyValue(&out, "step_index", fmt.Sprintf("%d", step.StepIndex))
	writeKeyValue(&out, "type", stepTypeLabel(step))
	writeKeyValue(&out, "status", step.Status)
	writeKeyValue(&out, "duration", formatDuration(step.Duration))
	writeKeyValue(&out, "exit_status", fmt.Sprintf("%d", step.ExitStatus))
	writeKeyValue(&out, "started_at", formatTime(step.StartedAt))
	writeKeyValue(&out, "finished_at", formatTime(step.FinishedAt))
	writeKeyValue(&out, "cwd_before", step.CWDBefore)
	writeKeyValue(&out, "cwd_after", step.CWDAfter)
	writeBlock(&out, "thought", step.Thought)
	if hasStructuredStepAction(step) {
		writeKeyValue(&out, "action_name", step.ActionName)
		writeKeyValue(&out, "action_tool_kind", step.ActionToolKind)
		writeBlock(&out, "action_input", step.ActionInput)
		writeBlock(&out, "action_output", step.ActionOutput)
		writeKeyValue(&out, "action_output_truncated", fmt.Sprintf("%t", step.ActionOutputTruncated))
	}
	writeBlock(&out, "shell", step.Shell)
	writeBlock(&out, "stdout", step.Stdout)
	writeBlock(&out, "stderr", step.Stderr)
	writeKeyValue(&out, "stdout_truncated", fmt.Sprintf("%t", step.StdoutTruncated))
	writeKeyValue(&out, "stderr_truncated", fmt.Sprintf("%t", step.StderrTruncated))
	writeKeyValue(&out, "error", step.Error)
	return strings.TrimRight(out.String(), "\n")
}

func renderText(render func(io.Writer)) string {
	var out bytes.Buffer
	render(&out)
	return strings.TrimRight(out.String(), "\n")
}

func prettyJSON(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return value
	}
	formatted, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		return value
	}
	return string(formatted)
}
