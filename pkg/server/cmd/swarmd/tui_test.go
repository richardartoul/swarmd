package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

func TestRunTUIRejectsNonTTY(t *testing.T) {
	t.Parallel()

	fixture := seedCommandRunFixture(t)

	var stdout bytes.Buffer
	err := run(context.Background(), []string{"tui", "-db", fixture.dbPath}, commandIO{
		stdout: &stdout,
		stderr: &stdout,
	})
	if err == nil {
		t.Fatal("run(tui) error = nil, want non-interactive terminal failure")
	}
	if !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("run(tui) error = %v, want interactive terminal message", err)
	}
}

func TestLoadTUIPageMailboxSingleMessage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := seedCommandRunFixture(t)
	store, err := cpstore.OpenReadOnly(ctx, fixture.dbPath)
	if err != nil {
		t.Fatalf("cpstore.OpenReadOnly() error = %v", err)
	}
	defer store.Close()

	page, err := loadTUIPage(ctx, store, newMailboxPage(tuiPageQuery{
		NamespaceID: fixture.namespaceID,
		MessageID:   fixture.messageID,
	}))
	if err != nil {
		t.Fatalf("loadTUIPage(mailbox) error = %v", err)
	}
	if len(page.items) != 1 {
		t.Fatalf("len(page.items) = %d, want 1", len(page.items))
	}
	item, ok := page.items[0].(tuiItem)
	if !ok {
		t.Fatalf("page.items[0] type = %T, want tuiItem", page.items[0])
	}
	if item.messageID != fixture.messageID {
		t.Fatalf("item.messageID = %q, want %q", item.messageID, fixture.messageID)
	}
	if item.threadID == "" {
		t.Fatal("item.threadID was empty")
	}
}

func TestLoadTUIPageNamespacesEmptyDatabase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "empty.db")
	store := openCommandTestStore(t, dbPath)
	defer store.Close()

	page, err := loadTUIPage(ctx, store, newNamespacesPage())
	if err != nil {
		t.Fatalf("loadTUIPage(namespaces) error = %v", err)
	}
	if len(page.items) != 0 {
		t.Fatalf("len(page.items) = %d, want 0", len(page.items))
	}
}

func TestLoadTUIPageSchedulesAcrossNamespaces(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := seedTUIScheduleFixture(t)
	store, err := cpstore.OpenReadOnly(ctx, dbPath)
	if err != nil {
		t.Fatalf("cpstore.OpenReadOnly() error = %v", err)
	}
	defer store.Close()

	page, err := loadTUIPage(ctx, store, newSchedulesPage(tuiPageQuery{}))
	if err != nil {
		t.Fatalf("loadTUIPage(schedules) error = %v", err)
	}
	if len(page.items) != 2 {
		t.Fatalf("len(page.items) = %d, want 2", len(page.items))
	}
	first, ok := page.items[0].(tuiItem)
	if !ok {
		t.Fatalf("page.items[0] type = %T, want tuiItem", page.items[0])
	}
	second, ok := page.items[1].(tuiItem)
	if !ok {
		t.Fatalf("page.items[1] type = %T, want tuiItem", page.items[1])
	}
	if first.title != "namespace-a/sched-a" {
		t.Fatalf("first.title = %q, want namespace-a/sched-a", first.title)
	}
	if second.title != "namespace-b/sched-b" {
		t.Fatalf("second.title = %q, want namespace-b/sched-b", second.title)
	}
}

func TestNextTUIPageFollowsMailboxAndRunLinks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := seedCommandRunFixture(t)
	store, err := cpstore.OpenReadOnly(ctx, fixture.dbPath)
	if err != nil {
		t.Fatalf("cpstore.OpenReadOnly() error = %v", err)
	}
	defer store.Close()

	mailboxPage, err := loadTUIPage(ctx, store, newMailboxPage(tuiPageQuery{
		NamespaceID: fixture.namespaceID,
		MessageID:   fixture.messageID,
	}))
	if err != nil {
		t.Fatalf("loadTUIPage(mailbox) error = %v", err)
	}
	message := mailboxPage.items[0].(tuiItem)

	threadPage, ok := nextTUIPage(mailboxPage, message, tuiActionThread)
	if !ok {
		t.Fatal("nextTUIPage(mailbox, thread) = !ok, want thread page")
	}
	if threadPage.kind != tuiPageThread {
		t.Fatalf("threadPage.kind = %q, want %q", threadPage.kind, tuiPageThread)
	}

	runPage, err := loadTUIPage(ctx, store, newRunsPage(tuiPageQuery{
		NamespaceID: fixture.namespaceID,
		RunID:       fixture.runID,
	}))
	if err != nil {
		t.Fatalf("loadTUIPage(runs) error = %v", err)
	}
	run := runPage.items[0].(tuiItem)
	stepsPage, ok := nextTUIPage(runPage, run, tuiActionEnter)
	if !ok {
		t.Fatal("nextTUIPage(run, enter) = !ok, want steps page")
	}
	if stepsPage.kind != tuiPageSteps {
		t.Fatalf("stepsPage.kind = %q, want %q", stepsPage.kind, tuiPageSteps)
	}
}

func TestRenderScheduleDetailIncludesDecodedPayload(t *testing.T) {
	t.Parallel()

	schedule := seedSingleScheduleRecord(t)
	output := renderScheduleDetail(schedule)
	if !strings.Contains(output, "\"source\": \"cron\"") {
		t.Fatalf("renderScheduleDetail() output = %q, want decoded payload source", output)
	}
	if !strings.Contains(output, "Schedule") {
		t.Fatalf("renderScheduleDetail() output = %q, want Schedule header", output)
	}
}

func TestRenderStepDetailIncludesToolActionFields(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.March, 17, 12, 0, 0, 0, time.UTC)
	step := cpstore.StepRecord{
		NamespaceID:           "namespace-demo",
		RunID:                 "run-demo",
		MessageID:             "message-demo",
		AgentID:               "agent-demo",
		StepIndex:             2,
		StepType:              "tool",
		Thought:               "inspect the config file",
		ActionName:            "read_file",
		ActionToolKind:        "function",
		ActionInput:           `{"file_path":"config.yaml"}`,
		ActionOutput:          "1|name: demo\n2|enabled: true\n",
		ActionOutputTruncated: true,
		StartedAt:             startedAt,
		FinishedAt:            startedAt.Add(10 * time.Millisecond),
		Duration:              10 * time.Millisecond,
		Status:                "ok",
	}

	output := renderStepDetail(step)
	for _, want := range []string{
		"type: tool",
		"action_name: read_file",
		"action_tool_kind: function",
		"action_input:",
		`{"file_path":"config.yaml"}`,
		"action_output:",
		"1|name: demo",
		"action_output_truncated: true",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("renderStepDetail() output = %q, want %q", output, want)
		}
	}
}

func TestRenderStepListFallsBackToToolActionWhenShellEmpty(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderStepList(&out, []cpstore.StepRecord{{
		StepIndex:   1,
		StepType:    "tool",
		Status:      "ok",
		Duration:    25 * time.Millisecond,
		ActionName:  "read_file",
		ActionInput: `{"file_path":"config.yaml"}`,
	}})

	output := out.String()
	for _, want := range []string{
		"TYPE",
		"ACTION",
		"tool",
		"read_file",
		"config.yaml",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("renderStepList() output = %q, want %q", output, want)
		}
	}
}

func TestWrapTUIDetailConstrainsRenderedLineWidth(t *testing.T) {
	t.Parallel()

	raw := strings.Join([]string{
		"stderr:",
		"  coreutils: mkdir /Users/richie/Documents/sh/data/agents/default/example-api-bug-hunter",
		`  printf '%s\n' "$LIMITED_SUBPAGES" | while IFS= read -r path; do`,
	}, "\n")

	const width = 40
	got := wrapTUIDetail(raw, width)

	for _, line := range strings.Split(got, "\n") {
		if actual := lipgloss.Width(line); actual > width {
			t.Fatalf("line width = %d, want <= %d for %q", actual, width, line)
		}
	}
	if !strings.Contains(got, "\n") {
		t.Fatalf("wrapTUIDetail() output = %q, want wrapped multiline output", got)
	}
}

func TestTUIWheelScrollsDetailPaneUnderMouse(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := seedCommandRunFixture(t)
	store, err := cpstore.OpenReadOnly(ctx, fixture.dbPath)
	if err != nil {
		t.Fatalf("cpstore.OpenReadOnly() error = %v", err)
	}
	defer store.Close()

	model, err := newTUIModel(ctx, store, fixture.dbPath)
	if err != nil {
		t.Fatalf("newTUIModel() error = %v", err)
	}
	model.width = 120
	model.height = 30
	model.resize()
	model.focus = tuiPaneList
	model.setDetailContent(strings.TrimSpace(strings.Repeat("line\n", 64)))

	listWidth, _, _ := model.paneDimensions()
	mouseY := lipgloss.Height(model.headerView()) + 1
	mouseX := listWidth + 1

	updatedModel, _ := model.Update(tea.MouseMsg{
		X:      mouseX,
		Y:      mouseY,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelDown,
	})
	got, ok := updatedModel.(tuiModel)
	if !ok {
		t.Fatalf("Update() model type = %T, want tuiModel", updatedModel)
	}
	if got.focus != tuiPaneDetail {
		t.Fatalf("focus = %v, want %v", got.focus, tuiPaneDetail)
	}
	if got.detail.YOffset <= 0 {
		t.Fatalf("detail.YOffset = %d, want > 0 after wheel scroll", got.detail.YOffset)
	}
}

func seedTUIScheduleFixture(t *testing.T) string {
	t.Helper()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "tui-schedules.db")
	store := openCommandTestStore(t, dbPath)
	defer store.Close()

	firstNamespace, err := store.CreateNamespace(ctx, cpstore.CreateNamespaceParams{
		ID:   "namespace-a",
		Name: "Namespace A",
	})
	if err != nil {
		t.Fatalf("CreateNamespace(namespace-a) error = %v", err)
	}
	secondNamespace, err := store.CreateNamespace(ctx, cpstore.CreateNamespaceParams{
		ID:   "namespace-b",
		Name: "Namespace B",
	})
	if err != nil {
		t.Fatalf("CreateNamespace(namespace-b) error = %v", err)
	}

	for _, ns := range []cpstore.Namespace{firstNamespace, secondNamespace} {
		if _, err := store.CreateAgent(ctx, cpstore.CreateAgentParams{
			NamespaceID:  ns.ID,
			AgentID:      "worker",
			Name:         "worker",
			Role:         cpstore.AgentRoleWorker,
			DesiredState: cpstore.AgentDesiredStateRunning,
			RootPath:     filepath.Join(t.TempDir(), ns.ID, "worker"),
			ModelName:    "test-model",
			SystemPrompt: "You are a test worker.",
			MaxAttempts:  3,
		}); err != nil {
			t.Fatalf("CreateAgent(%s) error = %v", ns.ID, err)
		}
	}

	if _, err := store.CreateSchedule(ctx, cpstore.CreateScheduleParams{
		NamespaceID: firstNamespace.ID,
		ScheduleID:  "sched-a",
		AgentID:     "worker",
		CronExpr:    "* * * * *",
		TimeZone:    "UTC",
		Payload:     map[string]any{"source": "cron"},
		Enabled:     true,
	}); err != nil {
		t.Fatalf("CreateSchedule(sched-a) error = %v", err)
	}
	if _, err := store.CreateSchedule(ctx, cpstore.CreateScheduleParams{
		NamespaceID: secondNamespace.ID,
		ScheduleID:  "sched-b",
		AgentID:     "worker",
		CronExpr:    "*/5 * * * *",
		TimeZone:    "UTC",
		Payload:     map[string]any{"source": "cron"},
		Enabled:     true,
	}); err != nil {
		t.Fatalf("CreateSchedule(sched-b) error = %v", err)
	}

	return dbPath
}

func seedSingleScheduleRecord(t *testing.T) cpstore.ScheduleRecord {
	t.Helper()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "single-schedule.db")
	store := openCommandTestStore(t, dbPath)
	defer store.Close()

	ns, err := store.CreateNamespace(ctx, cpstore.CreateNamespaceParams{
		ID:   "namespace-schedule",
		Name: "namespace-schedule",
	})
	if err != nil {
		t.Fatalf("CreateNamespace() error = %v", err)
	}
	if _, err := store.CreateAgent(ctx, cpstore.CreateAgentParams{
		NamespaceID:  ns.ID,
		AgentID:      "worker",
		Name:         "worker",
		Role:         cpstore.AgentRoleWorker,
		DesiredState: cpstore.AgentDesiredStateRunning,
		RootPath:     filepath.Join(t.TempDir(), "worker"),
		ModelName:    "test-model",
		SystemPrompt: "You are a test worker.",
		MaxAttempts:  3,
	}); err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}
	record, err := store.CreateSchedule(ctx, cpstore.CreateScheduleParams{
		NamespaceID: ns.ID,
		ScheduleID:  "sched-1",
		AgentID:     "worker",
		CronExpr:    "* * * * *",
		TimeZone:    "UTC",
		Payload:     map[string]any{"source": "cron"},
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("CreateSchedule() error = %v", err)
	}
	return record
}
