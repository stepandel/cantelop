package observer

import (
	"context"
	"sync"
)

// forEachBounded calls fn for every index in [0, n) with at most limit calls
// in flight. The first error cancels the context passed to the remaining
// calls, stops starting new ones, and is returned once every started call has
// finished. Callers write results into index-addressed slots, so output order
// does not depend on scheduling.
func forEachBounded(ctx context.Context, n, limit int, fn func(context.Context, int) error) error {
	if limit < 1 {
		limit = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wait     sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	slots := make(chan struct{}, limit)
	for i := 0; i < n; i++ {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			defer func() { <-slots }()
			if err := fn(ctx, i); err != nil {
				once.Do(func() {
					firstErr = err
					cancel()
				})
			}
		}()
	}
	wait.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}
