package compose

import (
	"context"
	"time"
)

// Frame and chunk intervals of the streams the session produces.
const (
	VideoInterval = time.Second / 30
	AudioInterval = 20 * time.Millisecond
)

// maxCatchUp bounds the ticks run back to back after a stall; older missed
// ticks are skipped.
const maxCatchUp = 5

// Clock calls tick every interval until ctx ends, with the tick's due time
// rather than the moment it runs, so timestamped outputs get a regular rate.
// Ticks missed because a tick was slow are run at once (up to maxCatchUp),
// so consumers that assume a constant rate, like ffmpeg reading raw video at
// -r 30, stay in time (research R10).
func Clock(ctx context.Context, interval time.Duration, tick func(now time.Time)) {
	start := time.Now()
	t := time.NewTicker(interval)
	defer t.Stop()
	var done int64
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			due := int64(now.Sub(start) / interval)
			if due-done > maxCatchUp {
				done = due - maxCatchUp
			}
			for ; done < due; done++ {
				if ctx.Err() != nil {
					return
				}
				tick(start.Add(time.Duration(done+1) * interval))
			}
		}
	}
}
