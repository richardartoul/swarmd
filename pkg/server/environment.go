package server

import (
	"context"
	"fmt"
	"sync"
)

type Environment struct {
	Runtime   *RuntimeManager
	Scheduler *Scheduler
}

func (e Environment) Run(ctx context.Context) error {
	if e.Runtime == nil {
		return fmt.Errorf("server environment requires a runtime manager")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		errCh <- e.Runtime.Run(ctx)
	}()
	if e.Scheduler != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- e.Scheduler.Run(ctx)
		}()
	}

	err := <-errCh
	cancel()
	wg.Wait()
	return err
}
