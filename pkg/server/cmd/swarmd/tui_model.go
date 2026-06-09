package main

import (
	"context"
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

func (m *tuiModel) captureCursor() {
	page := m.currentPage()
	if page == nil {
		return
	}
	page.cursor = m.list.Index()
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

func (m *tuiModel) resize() {
	listWidth, detailWidth, bodyHeight := m.paneDimensions()
	m.list.SetSize(max(20, listWidth-2), max(4, bodyHeight-2))
	m.detail.Width = max(20, detailWidth-2)
	m.detail.Height = max(4, bodyHeight-2)
	m.triggerInput.Width = max(20, m.width-10)
	m.refreshDetailContent()
}

func (m tuiModel) paneDimensions() (listWidth, detailWidth, bodyHeight int) {
	listWidth = max(32, m.width/3)
	if listWidth > m.width-40 {
		listWidth = max(28, m.width/2)
	}
	detailWidth = max(32, m.width-listWidth)
	bodyHeight = max(8, m.height-lipgloss.Height(m.headerView())-lipgloss.Height(m.footerView()))
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
