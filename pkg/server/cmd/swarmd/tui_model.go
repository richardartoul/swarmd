package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

type tuiPaneFocus int

const (
	tuiPaneList tuiPaneFocus = iota
	tuiPaneDetail
)

func (p tuiPaneFocus) label() string {
	switch p {
	case tuiPaneList:
		return "list"
	case tuiPaneDetail:
		return "detail"
	default:
		return "unknown"
	}
}

type tuiKeyMap struct {
	nextSection key.Binding
	prevSection key.Binding
	focusList   key.Binding
	focusDetail key.Binding
	selectItem  key.Binding
	back        key.Binding
	refresh     key.Binding
	toggleAuto  key.Binding
	trigger     key.Binding
	openAgent   key.Binding
	openMailbox key.Binding
	openRuns    key.Binding
	openSched   key.Binding
	openThread  key.Binding
	openSteps   key.Binding
	help        key.Binding
	quit        key.Binding
}

func newTUIKeyMap() tuiKeyMap {
	return tuiKeyMap{
		nextSection: key.NewBinding(key.WithKeys("tab", "]"), key.WithHelp("tab ]", "next section")),
		prevSection: key.NewBinding(key.WithKeys("shift+tab", "["), key.WithHelp("shift+tab [", "prev section")),
		focusList:   key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("left h", "focus list")),
		focusDetail: key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("right l", "focus detail")),
		selectItem:  key.NewBinding(key.WithKeys("enter", "o"), key.WithHelp("enter o", "open")),
		back:        key.NewBinding(key.WithKeys("esc", "backspace", "b"), key.WithHelp("esc b", "back")),
		refresh:     key.NewBinding(key.WithKeys("ctrl+r", "f5"), key.WithHelp("ctrl+r", "refresh")),
		toggleAuto:  key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "auto refresh")),
		trigger:     key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "trigger")),
		openAgent:   key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "agent")),
		openMailbox: key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "mailbox")),
		openRuns:    key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "runs")),
		openSched:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "schedules")),
		openThread:  key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "thread")),
		openSteps:   key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "steps")),
		help:        key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "toggle help")),
		quit:        key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

func (k tuiKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.nextSection, k.selectItem, k.trigger, k.refresh, k.toggleAuto, k.back, k.quit}
}

func (k tuiKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.nextSection, k.prevSection, k.selectItem, k.back},
		{k.focusList, k.focusDetail, k.trigger, k.refresh, k.toggleAuto},
		{k.openAgent, k.openMailbox, k.openRuns, k.openSched, k.openThread, k.openSteps},
		{k.help, k.quit},
	}
}

type tuiModel struct {
	ctx            context.Context
	store          *cpstore.Store
	dbPath         string
	sections       []tuiSection
	currentSection int
	stack          []tuiPage
	list           list.Model
	detail         viewport.Model
	detailRaw      string
	help           help.Model
	keys           tuiKeyMap
	focus          tuiPaneFocus
	width          int
	height         int
	showHelp       bool
	status         string
	autoRefresh    bool
	autoInterval   time.Duration
	autoRefreshSeq int
	pageLoading    bool
	pageLoadSeq    int
	pageLoadReason string
	detailLoading  bool
	detailLoadSeq  int
	detailCache    map[string]string
	currentDetail  string
	lastRefreshAt  time.Time
	triggering     bool
	triggerInput   textinput.Model
	triggerTarget  tuiTriggerTarget
}

type tuiPageApplyOptions struct {
	preserveFilter       bool
	preserveSelectionKey string
	preserveDetailScroll bool
}

func newTUIModel(ctx context.Context, store *cpstore.Store, dbPath string) (tuiModel, error) {
	delegate := list.NewDefaultDelegate()
	items := make([]list.Item, 0)
	left := list.New(items, delegate, 0, 0)
	left.Title = "Namespaces"
	left.SetShowHelp(false)
	left.SetShowStatusBar(false)
	left.SetShowPagination(true)
	left.SetFilteringEnabled(true)
	left.DisableQuitKeybindings()

	detail := viewport.New(0, 0)
	detail.MouseWheelEnabled = true

	model := tuiModel{
		ctx:          ctx,
		store:        store,
		dbPath:       dbPath,
		sections:     append([]tuiSection(nil), allTUISections...),
		list:         left,
		detail:       detail,
		help:         help.New(),
		keys:         newTUIKeyMap(),
		focus:        tuiPaneList,
		autoRefresh:  true,
		autoInterval: defaultTUIAutoRefreshInterval,
		detailCache:  make(map[string]string),
		triggerInput: newTUITriggerInput(),
	}
	root, err := loadTUIPage(ctx, store, tuiSectionNamespaces.rootPage())
	if err != nil {
		return tuiModel{}, err
	}
	model.stack = []tuiPage{root}
	model.currentSection = 0
	model.applyCurrentPage(root)
	model.lastRefreshAt = time.Now()
	model.syncDetailNow()
	return model, nil
}

func (m tuiModel) Init() tea.Cmd {
	return m.nextAutoRefreshCmd()
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		return m, nil
	case tuiAutoRefreshTickMsg:
		return m.handleAutoRefreshTick(msg)
	case tuiPageLoadedMsg:
		return m.handlePageLoaded(msg)
	case tuiDetailLoadedMsg:
		return m.handleDetailLoaded(msg)
	case tea.MouseMsg:
		if m.triggering {
			return m, nil
		}
		return m, m.updateMouse(msg)
	case tea.KeyMsg:
		if key.Matches(msg, m.keys.quit) {
			return m, tea.Quit
		}
		if key.Matches(msg, m.keys.help) {
			m.showHelp = !m.showHelp
			m.resize()
			return m, nil
		}
		if m.triggering {
			cmd, err := m.handleTriggerKey(msg)
			if err != nil {
				m.status = err.Error()
			}
			return m, cmd
		}

		if m.list.FilterState() != list.Filtering {
			if ok, cmd, err := m.handleGlobalKey(msg); ok {
				if err != nil {
					m.status = err.Error()
				}
				return m, cmd
			}
		}

		if m.focus == tuiPaneDetail {
			m.updateDetail(msg)
			return m, nil
		}

		return m, m.updateList(msg)
	default:
		if m.triggering {
			var cmd tea.Cmd
			m.triggerInput, cmd = m.triggerInput.Update(msg)
			return m, cmd
		}
		if m.focus == tuiPaneDetail {
			return m, m.updateDetailMsg(msg)
		}
		return m, m.updateList(msg)
	}
}

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

func (m *tuiModel) handleGlobalKey(msg tea.KeyMsg) (bool, tea.Cmd, error) {
	switch {
	case directSectionKey(msg.String()):
		cmd, err := m.switchSection(directSectionIndex(msg.String()))
		return true, cmd, err
	case key.Matches(msg, m.keys.nextSection):
		cmd, err := m.switchSectionByOffset(1)
		return true, cmd, err
	case key.Matches(msg, m.keys.prevSection):
		cmd, err := m.switchSectionByOffset(-1)
		return true, cmd, err
	case key.Matches(msg, m.keys.refresh):
		return true, m.requestPageReload("manual"), nil
	case key.Matches(msg, m.keys.toggleAuto):
		m.autoRefresh = !m.autoRefresh
		if m.autoRefresh {
			m.status = fmt.Sprintf("Auto refresh enabled (%s)", m.autoInterval)
			return true, m.nextAutoRefreshCmd(), nil
		}
		m.status = "Auto refresh paused"
		return true, nil, nil
	case key.Matches(msg, m.keys.trigger):
		cmd, ok := m.startTriggerMode()
		return ok, cmd, nil
	case key.Matches(msg, m.keys.focusList):
		m.focus = tuiPaneList
		m.status = ""
		return true, nil, nil
	case key.Matches(msg, m.keys.focusDetail):
		m.focus = tuiPaneDetail
		m.status = ""
		return true, nil, nil
	case key.Matches(msg, m.keys.back) && m.list.FilterState() == list.Unfiltered:
		if len(m.stack) > 1 {
			cmd, err := m.popPage()
			return true, cmd, err
		}
		if m.focus == tuiPaneDetail {
			m.focus = tuiPaneList
			m.status = ""
			return true, nil, nil
		}
	case m.focus == tuiPaneList && key.Matches(msg, m.keys.selectItem):
		cmd, err := m.openAction(tuiActionEnter)
		return true, cmd, err
	case m.focus == tuiPaneList && key.Matches(msg, m.keys.openAgent):
		cmd, err := m.openAction(tuiActionAgent)
		return true, cmd, err
	case m.focus == tuiPaneList && key.Matches(msg, m.keys.openMailbox):
		cmd, err := m.openAction(tuiActionMailbox)
		return true, cmd, err
	case m.focus == tuiPaneList && key.Matches(msg, m.keys.openRuns):
		cmd, err := m.openAction(tuiActionRuns)
		return true, cmd, err
	case m.focus == tuiPaneList && key.Matches(msg, m.keys.openSched):
		cmd, err := m.openAction(tuiActionSchedules)
		return true, cmd, err
	case m.focus == tuiPaneList && key.Matches(msg, m.keys.openThread):
		cmd, err := m.openAction(tuiActionThread)
		return true, cmd, err
	case m.focus == tuiPaneList && key.Matches(msg, m.keys.openSteps):
		cmd, err := m.openAction(tuiActionSteps)
		return true, cmd, err
	case m.focus == tuiPaneDetail && msg.String() == "/":
		m.focus = tuiPaneList
		m.status = ""
		return false, nil, nil
	}
	return false, nil, nil
}

func (m *tuiModel) updateDetail(msg tea.KeyMsg) {
	switch msg.String() {
	case "up", "k":
		m.detail.LineUp(1)
	case "down", "j":
		m.detail.LineDown(1)
	case "pgup", "ctrl+u":
		m.detail.HalfPageUp()
	case "pgdown", "ctrl+d", " ":
		m.detail.HalfPageDown()
	case "home", "g":
		m.detail.GotoTop()
	case "end", "G":
		m.detail.GotoBottom()
	default:
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		_ = cmd
	}
}

func (m *tuiModel) updateMouse(msg tea.MouseMsg) tea.Cmd {
	if pane, ok := m.paneAt(msg.X, msg.Y); ok {
		if msg.Action != tea.MouseActionMotion && m.focus != pane {
			m.focus = pane
			m.status = ""
		}
		if pane == tuiPaneDetail {
			return m.updateDetailMsg(msg)
		}
		return m.updateList(msg)
	}
	if m.focus == tuiPaneDetail {
		return m.updateDetailMsg(msg)
	}
	return m.updateList(msg)
}

func (m *tuiModel) switchSectionByOffset(delta int) (tea.Cmd, error) {
	next := (m.currentSection + delta + len(m.sections)) % len(m.sections)
	return m.switchSection(next)
}

func (m *tuiModel) switchSection(index int) (tea.Cmd, error) {
	if index < 0 || index >= len(m.sections) {
		return nil, fmt.Errorf("invalid section index %d", index)
	}
	page, err := loadTUIPage(m.ctx, m.store, m.sections[index].rootPage())
	if err != nil {
		return nil, err
	}
	m.cancelPageLoad()
	m.cancelDetailLoad()
	m.resetDetailCache()
	m.stack = []tuiPage{page}
	m.currentSection = index
	m.focus = tuiPaneList
	m.status = ""
	m.lastRefreshAt = time.Now()
	return m.applyCurrentPage(page), nil
}

func (m *tuiModel) requestPageReload(reason string) tea.Cmd {
	page := m.currentPage()
	if page == nil || m.pageLoading {
		return nil
	}
	m.pageLoading = true
	m.pageLoadSeq++
	m.pageLoadReason = reason
	if reason == "manual" {
		m.status = "Refreshing current view..."
	}
	return loadTUIPageCmd(m.ctx, m.store, *page, m.pageLoadSeq)
}

func (m *tuiModel) openAction(action tuiNavigationAction) (tea.Cmd, error) {
	page := m.currentPage()
	if page == nil {
		return nil, nil
	}
	item, ok := m.selectedItem()
	if !ok {
		return nil, nil
	}
	next, ok := nextTUIPage(*page, item, action)
	if !ok {
		return nil, nil
	}
	page.cursor = m.list.Index()
	loaded, err := loadTUIPage(m.ctx, m.store, next)
	if err != nil {
		return nil, err
	}
	m.cancelPageLoad()
	m.cancelDetailLoad()
	m.resetDetailCache()
	m.stack[len(m.stack)-1] = *page
	m.stack = append(m.stack, loaded)
	m.focus = tuiPaneList
	m.status = ""
	m.lastRefreshAt = time.Now()
	return m.applyCurrentPage(loaded), nil
}

func (m *tuiModel) popPage() (tea.Cmd, error) {
	if len(m.stack) <= 1 {
		return nil, nil
	}
	prev := m.stack[len(m.stack)-2]
	loaded, err := loadTUIPage(m.ctx, m.store, prev)
	if err != nil {
		return nil, err
	}
	m.cancelPageLoad()
	m.cancelDetailLoad()
	m.resetDetailCache()
	m.stack = m.stack[:len(m.stack)-1]
	page := loaded
	m.focus = tuiPaneList
	m.status = ""
	m.lastRefreshAt = time.Now()
	return m.applyCurrentPage(page), nil
}

func (m *tuiModel) applyCurrentPage(page tuiPage) tea.Cmd {
	return m.applyCurrentPageWithOptions(page, tuiPageApplyOptions{})
}

func (m *tuiModel) applyCurrentPageWithOptions(page tuiPage, opts tuiPageApplyOptions) tea.Cmd {
	filterText := ""
	if opts.preserveFilter {
		filterText = strings.TrimSpace(m.list.FilterValue())
	}
	page.cursor = page.clampCursor()
	if len(m.stack) == 0 {
		m.stack = []tuiPage{page}
	} else {
		m.stack[len(m.stack)-1] = page
	}
	m.currentSection = sectionIndex(page.section)
	_ = m.list.SetItems(page.items)
	if opts.preserveFilter && filterText != "" {
		m.list.SetFilterText(filterText)
	} else {
		m.list.ResetFilter()
	}
	m.selectVisibleItem(page, opts.preserveSelectionKey)
	m.updateListTitle(page)
	return m.syncDetail(opts.preserveDetailScroll)
}

func (m *tuiModel) captureCursor() {
	page := m.currentPage()
	if page == nil {
		return
	}
	page.cursor = m.list.Index()
}

func (m *tuiModel) syncDetailNow() {
	page := m.currentPage()
	if page == nil {
		m.currentDetail = ""
		m.detailLoading = false
		m.setDetailContent("No page selected.", false)
		return
	}
	item, ok := m.selectedItem()
	if !ok {
		m.currentDetail = ""
		m.detailLoading = false
		m.setDetailContent(page.title+"\n\n"+page.empty, false)
		return
	}
	key := m.itemKey(item)
	detail, err := renderTUIDetail(m.ctx, m.store, *page, item)
	if err != nil {
		m.currentDetail = key
		m.detailLoading = false
		m.setDetailContent(page.title+"\n\nError:\n"+err.Error(), false)
		return
	}
	m.currentDetail = key
	m.detailLoading = false
	m.detailCache[key] = detail
	m.setDetailContent(detail, false)
}

func (m *tuiModel) syncDetail(preserveScroll bool) tea.Cmd {
	page := m.currentPage()
	if page == nil {
		m.currentDetail = ""
		m.detailLoading = false
		m.setDetailContent("No page selected.", false)
		return nil
	}
	item, ok := m.selectedItem()
	if !ok {
		m.currentDetail = ""
		m.detailLoading = false
		m.setDetailContent(page.title+"\n\n"+page.empty, false)
		return nil
	}
	key := m.itemKey(item)
	if detail, ok := m.detailCache[key]; ok {
		m.currentDetail = key
		m.detailLoading = false
		m.setDetailContent(detail, preserveScroll)
		return nil
	}
	preserveScroll = preserveScroll && m.currentDetail == key && strings.TrimSpace(m.detailRaw) != ""
	m.currentDetail = key
	m.detailLoading = true
	m.detailLoadSeq++
	if !preserveScroll {
		m.setDetailContent(m.loadingDetailText(*page, item), false)
	}
	return loadTUIDetailCmd(m.ctx, m.store, *page, item, m.detailLoadSeq, key, preserveScroll)
}

func (m *tuiModel) setDetailContent(detail string, preserveScroll bool) {
	m.detailRaw = detail
	m.refreshDetailContent()
	if !preserveScroll {
		m.detail.GotoTop()
	}
}

func (m *tuiModel) currentPage() *tuiPage {
	if len(m.stack) == 0 {
		return nil
	}
	return &m.stack[len(m.stack)-1]
}

func (m tuiModel) selectedItem() (tuiItem, bool) {
	selected := m.list.SelectedItem()
	if selected == nil {
		return tuiItem{}, false
	}
	item, ok := selected.(tuiItem)
	return item, ok
}

func (m *tuiModel) updateList(msg tea.Msg) tea.Cmd {
	prevIndex := m.list.Index()
	prevFilter := m.list.FilterValue()
	prevFilterState := m.list.FilterState()
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	if prevIndex != m.list.Index() || prevFilter != m.list.FilterValue() || prevFilterState != m.list.FilterState() {
		m.status = ""
		m.captureCursor()
		m.updateListTitleForCurrentPage()
		return tea.Batch(cmd, m.syncDetail(false))
	}
	return cmd
}

func (m *tuiModel) updateDetailMsg(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	m.detail, cmd = m.detail.Update(msg)
	return cmd
}

func (m *tuiModel) resize() {
	listWidth, detailWidth, bodyHeight := m.paneDimensions()
	m.list.SetSize(maxInt(20, listWidth-2), maxInt(4, bodyHeight-2))
	m.detail.Width = maxInt(20, detailWidth-2)
	m.detail.Height = maxInt(4, bodyHeight-2)
	m.triggerInput.Width = maxInt(20, m.width-10)
	m.refreshDetailContent()
}

func (m tuiModel) paneDimensions() (listWidth, detailWidth, bodyHeight int) {
	listWidth = maxInt(32, m.width/3)
	if listWidth > m.width-40 {
		listWidth = maxInt(28, m.width/2)
	}
	detailWidth = maxInt(32, m.width-listWidth)
	bodyHeight = maxInt(8, m.height-lipgloss.Height(m.headerView())-lipgloss.Height(m.footerView()))
	return listWidth, detailWidth, bodyHeight
}

func (m tuiModel) paneAt(x, y int) (tuiPaneFocus, bool) {
	if m.width <= 0 || m.height <= 0 || x < 0 || y < 0 {
		return tuiPaneList, false
	}
	listWidth, detailWidth, bodyHeight := m.paneDimensions()
	headerHeight := lipgloss.Height(m.headerView())
	if y < headerHeight || y >= headerHeight+bodyHeight {
		return tuiPaneList, false
	}
	if x < listWidth {
		return tuiPaneList, true
	}
	if x < listWidth+detailWidth {
		return tuiPaneDetail, true
	}
	return tuiPaneList, false
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

func (m *tuiModel) refreshDetailContent() {
	m.detail.SetContent(wrapTUIDetail(m.detailRaw, m.detail.Width))
}

func (m *tuiModel) nextAutoRefreshCmd() tea.Cmd {
	if !m.autoRefresh || m.autoInterval <= 0 {
		return nil
	}
	m.autoRefreshSeq++
	seq := m.autoRefreshSeq
	interval := m.autoInterval
	return tea.Tick(interval, func(at time.Time) tea.Msg {
		return tuiAutoRefreshTickMsg{sequence: seq, at: at}
	})
}

func (m tuiModel) handleAutoRefreshTick(msg tuiAutoRefreshTickMsg) (tea.Model, tea.Cmd) {
	if msg.sequence != m.autoRefreshSeq {
		return m, nil
	}
	next := m.nextAutoRefreshCmd()
	if !m.autoRefresh || m.triggering || m.pageLoading || m.list.FilterState() == list.Filtering {
		return m, next
	}
	return m, tea.Batch(next, m.requestPageReload("auto"))
}

func (m tuiModel) handlePageLoaded(msg tuiPageLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.requestID != m.pageLoadSeq {
		return m, nil
	}
	m.pageLoading = false
	reason := m.pageLoadReason
	m.pageLoadReason = ""
	if msg.err != nil {
		m.status = "Refresh failed: " + msg.err.Error()
		return m, nil
	}
	selectionKey := m.currentSelectionKey()
	preserveScroll := selectionKey != "" && selectionKey == m.currentDetail
	m.resetDetailCache()
	if preserveScroll {
		m.currentDetail = selectionKey
	}
	m.lastRefreshAt = msg.loadedAt
	cmd := m.applyCurrentPageWithOptions(msg.page, tuiPageApplyOptions{
		preserveFilter:       true,
		preserveSelectionKey: selectionKey,
		preserveDetailScroll: preserveScroll,
	})
	if reason == "manual" {
		m.status = fmt.Sprintf("Refreshed %s at %s", msg.page.title, msg.loadedAt.Format("15:04:05"))
	}
	return m, cmd
}

func (m tuiModel) handleDetailLoaded(msg tuiDetailLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.requestID != m.detailLoadSeq || msg.itemKey != m.currentDetail {
		return m, nil
	}
	m.detailLoading = false
	if msg.err != nil {
		m.status = "Detail load failed: " + msg.err.Error()
		m.setDetailContent("Error loading details:\n"+msg.err.Error(), msg.preserveScroll)
		return m, nil
	}
	m.detailCache[msg.itemKey] = msg.detail
	m.setDetailContent(msg.detail, msg.preserveScroll)
	return m, nil
}

func (m *tuiModel) selectVisibleItem(page tuiPage, selectionKey string) {
	visible := m.list.VisibleItems()
	if len(visible) == 0 {
		m.list.Select(0)
		return
	}
	index := page.cursor
	if selectionKey != "" {
		for idx, item := range visible {
			tuiItem, ok := item.(tuiItem)
			if ok && m.itemKey(tuiItem) == selectionKey {
				index = idx
				break
			}
		}
	}
	if index < 0 {
		index = 0
	}
	if index >= len(visible) {
		index = len(visible) - 1
	}
	m.list.Select(index)
}

func (m *tuiModel) updateListTitleForCurrentPage() {
	page := m.currentPage()
	if page == nil {
		m.list.Title = "List"
		return
	}
	m.updateListTitle(*page)
}

func (m *tuiModel) updateListTitle(page tuiPage) {
	total := len(page.items)
	if m.list.FilterState() == list.Filtering || m.list.IsFiltered() {
		m.list.Title = fmt.Sprintf("%s (%d/%d)", page.title, len(m.list.VisibleItems()), total)
		return
	}
	m.list.Title = fmt.Sprintf("%s (%d)", page.title, total)
}

func (m *tuiModel) cancelPageLoad() {
	if m.pageLoading {
		m.pageLoadSeq++
	}
	m.pageLoading = false
	m.pageLoadReason = ""
}

func (m *tuiModel) cancelDetailLoad() {
	if m.detailLoading {
		m.detailLoadSeq++
	}
	m.detailLoading = false
}

func (m *tuiModel) resetDetailCache() {
	m.detailCache = make(map[string]string)
	m.currentDetail = ""
}

func (m tuiModel) itemKey(item tuiItem) string {
	return strings.Join([]string{
		string(item.kind),
		item.namespaceID,
		item.agentID,
		item.messageID,
		item.runID,
		item.threadID,
		item.title,
	}, "|")
}

func (m tuiModel) currentSelectionKey() string {
	item, ok := m.selectedItem()
	if !ok {
		return ""
	}
	return m.itemKey(item)
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

func sectionIndex(section tuiSection) int {
	for idx, candidate := range allTUISections {
		if candidate == section {
			return idx
		}
	}
	return 0
}

func directSectionKey(raw string) bool {
	return raw == "1" || raw == "2" || raw == "3" || raw == "4" || raw == "5"
}

func directSectionIndex(raw string) int {
	switch raw {
	case "1":
		return 0
	case "2":
		return 1
	case "3":
		return 2
	case "4":
		return 3
	case "5":
		return 4
	default:
		return 0
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
