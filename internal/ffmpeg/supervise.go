package ffmpeg

import (
	"context"
	"log/slog"
	"time"
)

// Restart back-off bounds.
const (
	restartMin = 500 * time.Millisecond
	restartMax = 5 * time.Second
	// restartReset: a process that ran this long resets the back-off.
	restartReset = 10 * time.Second
)

// Supervise runs start's process again each time it exits, until ctx ends,
// waiting restartMin, doubling up to restartMax between quick failures.
// onExit, if set, is called with each exit's error. Supervise returns when
// ctx ends and the last process has stopped.
func Supervise(ctx context.Context, logger *slog.Logger, name string, start func(context.Context) (*Process, error), onExit func(error)) {
	delay := restartMin
	for ctx.Err() == nil {
		began := time.Now()
		p, err := start(ctx)
		if err == nil {
			<-p.Done()
			err = p.Err()
		}
		if ctx.Err() != nil {
			return
		}
		if onExit != nil {
			onExit(err)
		}
		if time.Since(began) > restartReset {
			delay = restartMin
		}
		logger.Debug("Restarting", slog.String("process", name), slog.Duration("in", delay), slog.Any("error", err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(2*delay, restartMax)
	}
}
