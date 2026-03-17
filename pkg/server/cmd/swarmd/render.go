package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/richardartoul/swarmd/pkg/server"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

type namespaceInspectRow struct {
	Namespace     cpstore.Namespace
	AgentCount    int
	ScheduleCount int
	Mailbox       cpstore.MailboxSummary
}

type agentShowData struct {
	Agent     cpstore.RunnableAgent
	Schedules []cpstore.ScheduleRecord
}

type runShowData struct {
	Run     cpstore.RunRecord
	Message *cpstore.MailboxMessageRecord
	Steps   []cpstore.StepRecord
}

type threadShowData struct {
	NamespaceID string
	ThreadID    string
	Messages    []cpstore.MailboxThreadMessage
}

func renderConfigValidation(w io.Writer, configRoot string, summary server.AgentSpecSummary) {
	fmt.Fprintf(w, "config> valid config_root=%s namespaces=%d agents=%d schedules=%d\n", configRoot, summary.Namespaces, summary.Agents, summary.Schedules)
}

func renderSyncPlanOutput(w io.Writer, dbPath, configRoot string, plan server.SyncPlan) {
	fmt.Fprintf(w, "sync plan> sqlite=%s config_root=%s\n", dbPath, configRoot)
	fmt.Fprintf(
		w,
		"summary> namespaces(create=%d update=%d) agents(create=%d update=%d delete=%d) schedules(create=%d update=%d delete=%d)\n",
		plan.Summary.NamespacesCreated,
		plan.Summary.NamespacesUpdated,
		plan.Summary.AgentsCreated,
		plan.Summary.AgentsUpdated,
		plan.Summary.AgentsDeleted,
		plan.Summary.SchedulesCreated,
		plan.Summary.SchedulesUpdated,
		plan.Summary.SchedulesDeleted,
	)
	if !plan.HasChanges() {
		fmt.Fprintln(w, "changes> none")
		return
	}
	for _, change := range plan.NamespaceChanges {
		fmt.Fprintf(w, "namespace %s %s\n", change.Action, change.NamespaceID)
		for _, fieldChange := range change.Changes {
			renderFieldChange(w, fieldChange)
		}
	}
	for _, change := range plan.AgentChanges {
		fmt.Fprintf(w, "agent %s %s/%s", change.Action, change.NamespaceID, change.AgentID)
		if change.SourcePath != "" {
			fmt.Fprintf(w, " source=%s", change.SourcePath)
		}
		if change.ModelName != "" {
			fmt.Fprintf(w, " model=%s", change.ModelName)
		}
		if change.RootPath != "" {
			fmt.Fprintf(w, " root=%s", change.RootPath)
		}
		fmt.Fprintln(w)
		for _, fieldChange := range change.Changes {
			renderFieldChange(w, fieldChange)
		}
	}
	for _, change := range plan.ScheduleChanges {
		fmt.Fprintf(
			w,
			"schedule %s %s/%s/%s cron=%q timezone=%s enabled=%t",
			change.Action,
			change.NamespaceID,
			change.AgentID,
			change.ScheduleID,
			change.CronExpr,
			change.TimeZone,
			change.Enabled,
		)
		if change.SourcePath != "" {
			fmt.Fprintf(w, " source=%s", change.SourcePath)
		}
		fmt.Fprintln(w)
		for _, fieldChange := range change.Changes {
			renderFieldChange(w, fieldChange)
		}
	}
}

func renderNamespaceList(w io.Writer, rows []namespaceInspectRow) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "no namespaces found")
		return
	}
	tw := newTableWriter(w)
	fmt.Fprintln(tw, "NAMESPACE\tNAME\tAGENTS\tSCHEDULES\tQUEUED\tLEASED\tDEAD\tDONE")
	for _, row := range rows {
		fmt.Fprintf(
			tw,
			"%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\n",
			row.Namespace.ID,
			row.Namespace.Name,
			row.AgentCount,
			row.ScheduleCount,
			row.Mailbox.Queued,
			row.Mailbox.Leased,
			row.Mailbox.DeadLetter,
			row.Mailbox.Completed,
		)
	}
	_ = tw.Flush()
}

func renderAgentList(w io.Writer, agents []cpstore.RunnableAgent) {
	if len(agents) == 0 {
		fmt.Fprintln(w, "no agents found")
		return
	}
	tw := newTableWriter(w)
	fmt.Fprintln(tw, "NAMESPACE\tAGENT\tSTATE\tMODEL\tNET\tROOT")
	for _, agent := range agents {
		fmt.Fprintf(
			tw,
			"%s\t%s\t%s\t%s\t%t\t%s\n",
			agent.NamespaceID,
			agent.ID,
			agent.DesiredState,
			joinModel(agent.ModelProvider, agent.ModelName),
			agent.AllowNetwork,
			displayAgentRoot(agent),
		)
	}
	_ = tw.Flush()
}

func renderAgentShow(w io.Writer, data agentShowData) {
	agent := data.Agent
	fmt.Fprintln(w, "Agent")
	writeKeyValue(w, "namespace", agent.NamespaceID)
	writeKeyValue(w, "agent", agent.ID)
	writeKeyValue(w, "name", agent.Name)
	writeKeyValue(w, "role", string(agent.Role))
	writeKeyValue(w, "state", string(agent.DesiredState))
	writeKeyValue(w, "model", joinModel(agent.ModelProvider, agent.ModelName))
	writeKeyValue(w, "model_base_url", displayEmpty(agent.ModelBaseURL))
	writeKeyValue(w, "root", displayAgentRoot(agent))
	writeKeyValue(w, "network_enabled", fmt.Sprintf("%t", agent.AllowNetwork))
	writeKeyValue(w, "preserve_state", fmt.Sprintf("%t", agent.PreserveState))
	writeKeyValue(w, "max_steps", fmt.Sprintf("%d", agent.MaxSteps))
	writeKeyValue(w, "step_timeout", agent.StepTimeout.String())
	writeKeyValue(w, "max_output_bytes", fmt.Sprintf("%d", agent.MaxOutputBytes))
	writeKeyValue(w, "lease_duration", agent.LeaseDuration.String())
	writeKeyValue(w, "retry_delay", agent.RetryDelay.String())
	writeKeyValue(w, "max_attempts", fmt.Sprintf("%d", agent.MaxAttempts))
	writeKeyValue(w, "updated_at", formatTime(agent.UpdatedAt))
	writeBlock(w, "config", server.RenderEnvelope(agent.ConfigJSON))
	writeBlock(w, "system_prompt", agent.SystemPrompt)
	if len(data.Schedules) == 0 {
		writeBlock(w, "schedules", "")
		return
	}
	fmt.Fprintln(w, "schedules:")
	for _, schedule := range data.Schedules {
		fmt.Fprintf(
			w,
			"- %s cron=%q timezone=%s enabled=%t next=%s last=%s\n",
			schedule.ID,
			schedule.CronExpr,
			schedule.TimeZone,
			schedule.Enabled,
			formatTimePtr(schedule.NextFireAt),
			formatTimePtr(schedule.LastFireAt),
		)
	}
}

func renderMailboxList(w io.Writer, messages []cpstore.MailboxMessageRecord) {
	if len(messages) == 0 {
		fmt.Fprintln(w, "no mailbox messages found")
		return
	}
	tw := newTableWriter(w)
	fmt.Fprintln(tw, "NAMESPACE\tMESSAGE\tTHREAD\tAGENT\tSTATUS\tATTEMPTS\tKIND\tUPDATED\tERROR")
	for _, message := range messages {
		fmt.Fprintf(
			tw,
			"%s\t%s\t%s\t%s\t%s\t%d/%d\t%s\t%s\t%s\n",
			message.NamespaceID,
			message.ID,
			message.ThreadID,
			message.RecipientAgentID,
			message.Status,
			message.AttemptCount,
			message.MaxAttempts,
			message.Kind,
			formatTime(message.UpdatedAt),
			summarizeText(message.LastError, 48),
		)
	}
	_ = tw.Flush()
}

func renderStringList(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return "- " + strings.Join(values, "\n- ")
}

func displayAgentRoot(agent cpstore.RunnableAgent) string {
	return formatRootWithFilesystemKind(agent.RootPath, agent.ConfigJSON)
}

func formatRootWithFilesystemKind(rootPath, configJSON string) string {
	displayRoot := displayEmpty(rootPath)
	if kind, err := agentFilesystemKind(configJSON); err == nil && kind == "memory" {
		return displayRoot + " (memory)"
	}
	return displayRoot
}

func agentFilesystemKind(configJSON string) (string, error) {
	type config struct {
		Filesystem struct {
			Kind string `json:"kind,omitempty"`
		} `json:"filesystem,omitempty"`
	}
	var decoded config
	if err := cpstore.DecodeEnvelopeInto(configJSON, &decoded); err != nil {
		return "", err
	}
	kind := strings.TrimSpace(decoded.Filesystem.Kind)
	if kind == "" {
		return "disk", nil
	}
	return kind, nil
}

func renderMailboxShow(w io.Writer, message cpstore.MailboxMessageRecord) {
	fmt.Fprintln(w, "Mailbox Message")
	writeKeyValue(w, "namespace", message.NamespaceID)
	writeKeyValue(w, "message", message.ID)
	writeKeyValue(w, "thread", message.ThreadID)
	writeKeyValue(w, "from", displayParty(message.SenderAgentID))
	writeKeyValue(w, "to", message.RecipientAgentID)
	writeKeyValue(w, "kind", message.Kind)
	writeKeyValue(w, "status", string(message.Status))
	writeKeyValue(w, "attempts", fmt.Sprintf("%d/%d", message.AttemptCount, message.MaxAttempts))
	writeKeyValue(w, "run", displayEmpty(message.RunID))
	writeKeyValue(w, "available_at", formatTime(message.AvailableAt))
	writeKeyValue(w, "lease_owner", displayEmpty(message.LeaseOwner))
	writeKeyValue(w, "lease_expires_at", formatTimePtr(message.LeaseExpiresAt))
	writeKeyValue(w, "claimed_at", formatTimePtr(message.ClaimedAt))
	writeKeyValue(w, "completed_at", formatTimePtr(message.CompletedAt))
	writeKeyValue(w, "dead_letter_reason", displayEmpty(message.DeadLetterReason))
	writeKeyValue(w, "last_error", displayEmpty(message.LastError))
	writeBlock(w, "payload", server.RenderEnvelope(message.PayloadJSON))
	writeBlock(w, "metadata", server.RenderEnvelope(message.MetadataJSON))
}

func renderRunList(w io.Writer, runs []cpstore.RunRecord) {
	if len(runs) == 0 {
		fmt.Fprintln(w, "no runs found")
		return
	}
	tw := newTableWriter(w)
	fmt.Fprintln(tw, "NAMESPACE\tRUN\tAGENT\tSTATUS\tSTARTED\tDURATION\tMESSAGE\tERROR")
	for _, run := range runs {
		fmt.Fprintf(
			tw,
			"%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			run.NamespaceID,
			run.ID,
			run.AgentID,
			run.Status,
			formatTime(run.StartedAt),
			formatDuration(run.Duration),
			run.MessageID,
			summarizeText(run.Error, 56),
		)
	}
	_ = tw.Flush()
}

func renderRunShow(w io.Writer, data runShowData) {
	run := data.Run
	fmt.Fprintln(w, "Run")
	writeKeyValue(w, "namespace", run.NamespaceID)
	writeKeyValue(w, "run", run.ID)
	writeKeyValue(w, "agent", run.AgentID)
	writeKeyValue(w, "message", run.MessageID)
	writeKeyValue(w, "trigger", run.TriggerID)
	writeKeyValue(w, "status", run.Status)
	writeKeyValue(w, "started_at", formatTime(run.StartedAt))
	writeKeyValue(w, "finished_at", formatTimePtr(run.FinishedAt))
	writeKeyValue(w, "duration", formatDuration(run.Duration))
	writeKeyValue(w, "cwd", displayEmpty(run.CWD))
	writeKeyValue(w, "cached_tokens", fmt.Sprintf("%d", run.UsageCachedTokens))
	writeKeyValue(w, "error", displayEmpty(run.Error))
	writeBlock(w, "trigger_prompt", run.TriggerPrompt)
	writeBlock(w, "system_prompt", run.SystemPrompt)
	writeBlock(w, "value", server.RenderEnvelope(run.ValueJSON))
	if data.Message != nil {
		writeKeyValue(w, "message_status", string(data.Message.Status))
		writeBlock(w, "message_payload", server.RenderEnvelope(data.Message.PayloadJSON))
		writeBlock(w, "message_metadata", server.RenderEnvelope(data.Message.MetadataJSON))
	}
	if len(data.Steps) == 0 {
		fmt.Fprintln(w, "steps:\n  (none)")
		return
	}
	fmt.Fprintln(w, "steps:")
	for _, step := range data.Steps {
		fmt.Fprintf(w, "step %d\n", step.StepIndex)
		writeIndentedKeyValue(w, "type", stepTypeLabel(step))
		writeIndentedKeyValue(w, "status", step.Status)
		writeIndentedKeyValue(w, "duration", formatDuration(step.Duration))
		writeIndentedKeyValue(w, "exit_status", fmt.Sprintf("%d", step.ExitStatus))
		writeIndentedKeyValue(w, "started_at", formatTime(step.StartedAt))
		writeIndentedKeyValue(w, "finished_at", formatTime(step.FinishedAt))
		writeIndentedKeyValue(w, "cwd_before", displayEmpty(step.CWDBefore))
		writeIndentedKeyValue(w, "cwd_after", displayEmpty(step.CWDAfter))
		writeIndentedBlock(w, "thought", step.Thought)
		if hasStructuredStepAction(step) {
			writeIndentedKeyValue(w, "action_name", step.ActionName)
			writeIndentedKeyValue(w, "action_tool_kind", step.ActionToolKind)
			writeIndentedBlock(w, "action_input", step.ActionInput)
			writeIndentedBlock(w, "action_output", step.ActionOutput)
			writeIndentedKeyValue(w, "action_output_truncated", fmt.Sprintf("%t", step.ActionOutputTruncated))
		}
		writeIndentedBlock(w, "shell", step.Shell)
		writeIndentedBlock(w, "stdout", step.Stdout)
		writeIndentedBlock(w, "stderr", step.Stderr)
		writeIndentedKeyValue(w, "stdout_truncated", fmt.Sprintf("%t", step.StdoutTruncated))
		writeIndentedKeyValue(w, "stderr_truncated", fmt.Sprintf("%t", step.StderrTruncated))
		writeIndentedKeyValue(w, "error", displayEmpty(step.Error))
	}
}

func renderStepList(w io.Writer, steps []cpstore.StepRecord) {
	if len(steps) == 0 {
		fmt.Fprintln(w, "no steps found")
		return
	}
	tw := newTableWriter(w)
	fmt.Fprintln(tw, "STEP\tTYPE\tSTATUS\tDURATION\tEXIT\tACTION")
	for _, step := range steps {
		fmt.Fprintf(
			tw,
			"%d\t%s\t%s\t%s\t%d\t%s\n",
			step.StepIndex,
			stepTypeLabel(step),
			step.Status,
			formatDuration(step.Duration),
			step.ExitStatus,
			summarizeStepAction(step, 72),
		)
	}
	_ = tw.Flush()
}

func renderScheduleList(w io.Writer, schedules []cpstore.ScheduleRecord) {
	if len(schedules) == 0 {
		fmt.Fprintln(w, "no schedules found")
		return
	}
	tw := newTableWriter(w)
	fmt.Fprintln(tw, "NAMESPACE\tSCHEDULE\tAGENT\tENABLED\tCRON\tTIMEZONE\tNEXT\tLAST")
	for _, schedule := range schedules {
		fmt.Fprintf(
			tw,
			"%s\t%s\t%s\t%t\t%s\t%s\t%s\t%s\n",
			schedule.NamespaceID,
			schedule.ID,
			schedule.AgentID,
			schedule.Enabled,
			schedule.CronExpr,
			schedule.TimeZone,
			formatTimePtr(schedule.NextFireAt),
			formatTimePtr(schedule.LastFireAt),
		)
	}
	_ = tw.Flush()
}

func renderThreadShow(w io.Writer, data threadShowData) {
	fmt.Fprintln(w, "Thread")
	writeKeyValue(w, "namespace", data.NamespaceID)
	writeKeyValue(w, "thread", data.ThreadID)
	writeKeyValue(w, "messages", fmt.Sprintf("%d", len(data.Messages)))
	if len(data.Messages) == 0 {
		return
	}
	fmt.Fprintln(w, "history:")
	for _, message := range data.Messages {
		fmt.Fprintf(w, "message %s\n", message.ID)
		writeIndentedKeyValue(w, "from", displayParty(message.SenderAgentID))
		writeIndentedKeyValue(w, "to", message.RecipientAgentID)
		writeIndentedKeyValue(w, "kind", message.Kind)
		writeIndentedKeyValue(w, "status", string(message.Status))
		writeIndentedKeyValue(w, "created_at", formatTime(message.CreatedAt))
		writeIndentedKeyValue(w, "completed_at", formatTimePtr(message.CompletedAt))
		writeIndentedBlock(w, "payload", server.RenderEnvelope(message.PayloadJSON))
	}
}

func renderFieldChange(w io.Writer, change server.FieldChange) {
	if strings.Contains(change.Before, "\n") || strings.Contains(change.After, "\n") {
		fmt.Fprintf(w, "  %s:\n", change.Field)
		fmt.Fprintf(w, "    before:\n%s", indentBlock(displayEmpty(change.Before), "      "))
		if !strings.HasSuffix(change.Before, "\n") {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "    after:\n%s", indentBlock(displayEmpty(change.After), "      "))
		if !strings.HasSuffix(change.After, "\n") {
			fmt.Fprintln(w)
		}
		return
	}
	fmt.Fprintf(w, "  %s: %s -> %s\n", change.Field, displayEmpty(change.Before), displayEmpty(change.After))
}

func newTableWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}

func writeKeyValue(w io.Writer, key, value string) {
	fmt.Fprintf(w, "%s: %s\n", key, displayEmpty(value))
}

func writeIndentedKeyValue(w io.Writer, key, value string) {
	fmt.Fprintf(w, "  %s: %s\n", key, displayEmpty(value))
}

func writeBlock(w io.Writer, key, value string) {
	fmt.Fprintf(w, "%s:\n%s\n", key, indentBlock(displayEmpty(value), "  "))
}

func writeIndentedBlock(w io.Writer, key, value string) {
	fmt.Fprintf(w, "  %s:\n%s\n", key, indentBlock(displayEmpty(value), "    "))
}

func indentBlock(value, prefix string) string {
	lines := strings.Split(value, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

func summarizeText(value string, max int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" {
		return "-"
	}
	if max > 0 && len(value) > max {
		return value[:max-3] + "..."
	}
	return value
}

func summarizeStepAction(step cpstore.StepRecord, max int) string {
	if strings.TrimSpace(step.Shell) != "" {
		return summarizeText(step.Shell, max)
	}
	parts := make([]string, 0, 2)
	if name := strings.TrimSpace(step.ActionName); name != "" {
		parts = append(parts, name)
	}
	if input := strings.Join(strings.Fields(strings.TrimSpace(step.ActionInput)), " "); input != "" {
		parts = append(parts, input)
	}
	return summarizeText(strings.Join(parts, " "), max)
}

func stepTypeLabel(step cpstore.StepRecord) string {
	if value := strings.TrimSpace(step.StepType); value != "" {
		return value
	}
	switch {
	case hasStructuredStepAction(step):
		return "tool"
	case strings.TrimSpace(step.Shell) != "":
		return "shell"
	default:
		return "-"
	}
}

func hasStructuredStepAction(step cpstore.StepRecord) bool {
	if strings.TrimSpace(step.ActionInput) != "" ||
		strings.TrimSpace(step.ActionOutput) != "" ||
		strings.TrimSpace(step.ActionToolKind) != "" ||
		step.ActionOutputTruncated {
		return true
	}
	return strings.TrimSpace(step.Shell) == "" && strings.TrimSpace(step.ActionName) != ""
}

func displayEmpty(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func displayParty(agentID string) string {
	if strings.TrimSpace(agentID) == "" {
		return "external/system"
	}
	return agentID
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func formatTimePtr(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return formatTime(value.UTC())
}

func formatDuration(value time.Duration) string {
	if value == 0 {
		return "0s"
	}
	return value.String()
}

func joinModel(provider, name string) string {
	provider = strings.TrimSpace(provider)
	name = strings.TrimSpace(name)
	switch {
	case provider == "" && name == "":
		return "-"
	case provider == "":
		return name
	case name == "":
		return provider
	default:
		return provider + "/" + name
	}
}
