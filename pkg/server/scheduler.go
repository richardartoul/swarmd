package server

import (
	"context"
	"fmt"
	"time"

	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

type Scheduler struct {
	Store        *cpstore.Store
	PollInterval time.Duration
	BatchSize    int
}

func (s Scheduler) Run(ctx context.Context) error {
	if s.Store == nil {
		return fmt.Errorf("server scheduler requires a store")
	}
	pollInterval := s.PollInterval
	if pollInterval <= 0 {
		pollInterval = time.Second
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	if _, err := s.FireOnce(ctx); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := s.FireOnce(ctx); err != nil {
				return err
			}
		}
	}
}

func (s Scheduler) FireOnce(ctx context.Context) ([]cpstore.MailboxMessageRecord, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("server scheduler requires a store")
	}
	batchSize := s.BatchSize
	if batchSize <= 0 {
		batchSize = 32
	}
	return s.Store.FireDueSchedules(ctx, time.Now().UTC(), batchSize)
}
