// Package parallel runs jobs on a pool of workers.
package parallel

import (
	"context"
	"sync"
)

// Run runs fn for 0..n-1 on a pool of workers and stops at the first
// error.
func Run(ctx context.Context, n, workers int, progress func(done, total int), fn func(ctx context.Context, i int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan int)
	var (
		mu       sync.Mutex
		done     int
		firstErr error
		wg       sync.WaitGroup
	)
	finish := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			if firstErr == nil {
				firstErr = err
				cancel()
			}
			return
		}
		done++
		if progress != nil {
			progress(done, n)
		}
	}

	for w := 0; w < max(workers, 1); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				finish(fn(ctx, i))
			}
		}()
	}

feed:
	for i := 0; i < n; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}
