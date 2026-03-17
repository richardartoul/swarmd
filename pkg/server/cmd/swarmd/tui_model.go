package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
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
		nextSection: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next section")),
		prevSection: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev section")),
		focusList:   key.NewBinding(key.WithKeys("left"), key.WithHelp("left", "focus list")),
		focusDetail: key.NewBinding(key.WithKeys("right"), key.WithHelp("right", "focus detail")),
		selectItem:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		back:        key.NewBinding(key.WithKeys("esc", "backspace"), key.WithHelp("esc", "back")),
		refresh:     key.NewBinding(key.WithKeys("ctrl+r"), key.WithHelp("ctrl+r", "refresh")),
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
	return []key.Binding{k.nextSection, k.selectItem, k.back, k.refresh, k.help, k.quit}
}

func (k tuiKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.nextSection, k.prevSection, k.selectItem, k.back},
		{k.focusList, k.focusDetail, k.openAgent, k.openMailbox, k.openRuns, k.openSched},
		{k.openThread, k.openSteps, k.refresh, k.help, k.quit},
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
		ctx:      ctx,
		store:    store,
		dbPath:   dbPath,
		sections: append([]tuiSection(nil), allTUISections...),
		list:     left,
		detail:   detail,
		help:     help.New(),
		keys:     newTUIKeyMap(),
		focus:    tuiPaneList,
	}
	root, err := loadTUIPage(ctx, store, tuiSectionNamespaces.rootPage())
	if err != nil {
		return tuiModel{}, err
	}
	model.stack = []tuiPage{root}
	model.currentSection = 0
	model.applyCurrentPage(root)
	return model, nil
}

func (m tuiModel) Init() tea.Cmd {
	return nil
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		return m, nil
	case tea.MouseMsg:
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

		if m.list.FilterState() != list.Filtering {
			if ok, err := m.handleGlobalKey(msg); ok {
				if err != nil {
					m.status = err.Error()
				}
				return m, nil
			}
		}

		if m.focus == tuiPaneDetail {
			m.updateDetail(msg)
			return m, nil
		}

		return m, m.updateList(msg)
	default:
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

func (m *tuiModel) handleGlobalKey(msg tea.KeyMsg) (bool, error) {
	switch {
	case directSectionKey(msg.String()):
		return true, m.switchSection(directSectionIndex(msg.String()))
	case key.Matches(msg, m.keys.nextSection):
		return true, m.switchSectionByOffset(1)
	case key.Matches(msg, m.keys.prevSection):
		return true, m.switchSectionByOffset(-1)
	case key.Matches(msg, m.keys.refresh):
		return true, m.reloadCurrentPage()
	case key.Matches(msg, m.keys.focusList):
		m.focus = tuiPaneList
		m.status = ""
		return true, nil
	case key.Matches(msg, m.keys.focusDetail):
		m.focus = tuiPaneDetail
		m.status = ""
		return true, nil
	case key.Matches(msg, m.keys.back) && m.list.FilterState() == list.Unfiltered:
		if len(m.stack) > 1 {
			m.popPage()
			return true, nil
		}
		if m.focus == tuiPaneDetail {
			m.focus = tuiPaneList
			m.status = ""
			return true, nil
		}
	case m.focus == tuiPaneList && key.Matches(msg, m.keys.selectItem):
		return true, m.openAction(tuiActionEnter)
	case m.focus == tuiPaneList && key.Matches(msg, m.keys.openAgent):
		return true, m.openAction(tuiActionAgent)
	case m.focus == tuiPaneList && key.Matches(msg, m.keys.openMailbox):
		return true, m.openAction(tuiActionMailbox)
	case m.focus == tuiPaneList && key.Matches(msg, m.keys.openRuns):
		return true, m.openAction(tuiActionRuns)
	case m.focus == tuiPaneList && key.Matches(msg, m.keys.openSched):
		return true, m.openAction(tuiActionSchedules)
	case m.focus == tuiPaneList && key.Matches(msg, m.keys.openThread):
		return true, m.openAction(tuiActionThread)
	case m.focus == tuiPaneList && key.Matches(msg, m.keys.openSteps):
		return true, m.openAction(tuiActionSteps)
	case m.focus == tuiPaneDetail && msg.String() == "/":
		m.focus = tuiPaneList
		m.status = ""
		return false, nil
	}
	return false, nil
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

func (m *tuiModel) switchSectionByOffset(delta int) error {
	next := (m.currentSection + delta + len(m.sections)) % len(m.sections)
	return m.switchSection(next)
}

func (m *tuiModel) switchSection(index int) error {
	if index < 0 || index >= len(m.sections) {
		return fmt.Errorf("invalid section index %d", index)
	}
	page, err := loadTUIPage(m.ctx, m.store, m.sections[index].rootPage())
	if err != nil {
		return err
	}
	m.stack = []tuiPage{page}
	m.currentSection = index
	m.focus = tuiPaneList
	m.status = ""
	m.applyCurrentPage(page)
	return nil
}

func (m *tuiModel) reloadCurrentPage() error {
	page := m.currentPage()
	if page == nil {
		return nil
	}
	page.cursor = m.list.Index()
	loaded, err := loadTUIPage(m.ctx, m.store, *page)
	if err != nil {
		return err
	}
	m.stack[len(m.stack)-1] = loaded
	m.status = ""
	m.applyCurrentPage(loaded)
	return nil
}

func (m *tuiModel) openAction(action tuiNavigationAction) error {
	page := m.currentPage()
	if page == nil {
		return nil
	}
	item, ok := m.selectedItem()
	if !ok {
		return nil
	}
	next, ok := nextTUIPage(*page, item, action)
	if !ok {
		return nil
	}
	page.cursor = m.list.Index()
	loaded, err := loadTUIPage(m.ctx, m.store, next)
	if err != nil {
		return err
	}
	m.stack[len(m.stack)-1] = *page
	m.stack = append(m.stack, loaded)
	m.focus = tuiPaneList
	m.status = ""
	m.applyCurrentPage(loaded)
	return nil
}

func (m *tuiModel) popPage() {
	if len(m.stack) <= 1 {
		return
	}
	m.stack = m.stack[:len(m.stack)-1]
	page := m.stack[len(m.stack)-1]
	m.focus = tuiPaneList
	m.status = ""
	m.applyCurrentPage(page)
}

func (m *tuiModel) applyCurrentPage(page tuiPage) {
	page.cursor = page.clampCursor()
	if len(m.stack) == 0 {
		m.stack = []tuiPage{page}
	} else {
		m.stack[len(m.stack)-1] = page
	}
	m.currentSection = sectionIndex(page.section)
	m.list.ResetFilter()
	m.list.Title = page.title
	_ = m.list.SetItems(page.items)
	if len(page.items) > 0 {
		m.list.Select(page.cursor)
	} else {
		m.list.Select(0)
	}
	m.syncDetail()
}

func (m *tuiModel) captureCursor() {
	page := m.currentPage()
	if page == nil {
		return
	}
	page.cursor = m.list.Index()
}

func (m *tuiModel) syncDetail() {
	page := m.currentPage()
	if page == nil {
		m.setDetailContent("No page selected.")
		return
	}
	item, ok := m.selectedItem()
	if !ok {
		m.setDetailContent(page.title + "\n\n" + page.empty)
		return
	}
	detail, err := renderTUIDetail(m.ctx, m.store, *page, item)
	if err != nil {
		m.setDetailContent(page.title + "\n\nError:\n" + err.Error())
		return
	}
	m.setDetailContent(detail)
}

func (m *tuiModel) setDetailContent(detail string) {
	m.detailRaw = detail
	m.refreshDetailContent()
	m.detail.GotoTop()
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
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	if prevIndex != m.list.Index() || prevFilter != m.list.FilterValue() {
		m.status = ""
		m.captureCursor()
		m.syncDetail()
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
	m.refreshDetailContent()
}

func (m tuiModel) paneDimensions() (listWidth, detailWidth, bodyHeight int) {
	listWidth = maxInt(32, m.width/3)
	if listWidth > m.width-40 {
		listWidth = maxInt(28, m.width/2)
	}
	detailWidth = maxInt(32, m.width-listWidth)
	bodyHeight = maxInt(10, m.height-6)
	if m.showHelp {
		bodyHeight = maxInt(8, m.height-9)
	}
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
	title := ""
	if page := m.currentPage(); page != nil {
		title = page.title
	}
	meta := fmt.Sprintf("focus: %s | db: %s", m.focus.label(), m.dbPath)
	return strings.Join([]string{
		lipgloss.JoinHorizontal(lipgloss.Top, tabs...),
		title,
		meta,
	}, "\n")
}

func (m tuiModel) footerView() string {
	m.help.Width = m.width
	m.help.ShowAll = m.showHelp
	status := m.statusLine()
	helpView := m.help.View(m.keys)
	if strings.TrimSpace(helpView) == "" {
		return status
	}
	return status + "\n" + helpView
}

func (m tuiModel) statusLine() string {
	if strings.TrimSpace(m.status) != "" {
		return m.status
	}
	status := fmt.Sprintf("focus=%s", m.focus.label())
	if page := m.currentPage(); page != nil {
		if item, ok := m.selectedItem(); ok {
			actions := availableTUIActions(*page, item)
			if len(actions) > 0 {
				status += " | links: " + strings.Join(actions, " ")
			}
		}
	}
	if m.list.FilterState() == list.Filtering {
		status += " | filtering list"
	} else if m.list.IsFiltered() {
		status += fmt.Sprintf(" | filter=%q", m.list.FilterValue())
	}
	return status
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
