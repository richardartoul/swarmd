package main

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

const defaultTUIAutoRefreshInterval = 3 * time.Second

type tuiAutoRefreshTickMsg struct {
	sequence int
	at       time.Time
}

type tuiPageLoadedMsg struct {
	requestID int
	page      tuiPage
	loadedAt  time.Time
	err       error
}

type tuiDetailLoadedMsg struct {
	requestID      int
	itemKey        string
	detail         string
	preserveScroll bool
	err            error
}

func loadTUIPageCmd(ctx context.Context, store *cpstore.Store, page tuiPage, requestID int) tea.Cmd {
	return func() tea.Msg {
		loaded, err := loadTUIPage(ctx, store, page)
		return tuiPageLoadedMsg{
			requestID: requestID,
			page:      loaded,
			loadedAt:  time.Now(),
			err:       err,
		}
	}
}

func loadTUIDetailCmd(
	ctx context.Context,
	store *cpstore.Store,
	page tuiPage,
	item tuiItem,
	requestID int,
	itemKey string,
	preserveScroll bool,
) tea.Cmd {
	return func() tea.Msg {
		detail, err := renderTUIDetail(ctx, store, page, item)
		return tuiDetailLoadedMsg{
			requestID:      requestID,
			itemKey:        itemKey,
			detail:         detail,
			preserveScroll: preserveScroll,
			err:            err,
		}
	}
}
