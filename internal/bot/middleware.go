package bot

import (
	"time"

	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/contrib/middleware/ratelimit"
	"github.com/gotd/td/telegram"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

func GetFloodMiddleware(log *zap.Logger) []telegram.Middleware {
	waiter := floodwait.NewSimpleWaiter().WithMaxRetries(10)
	// Allow higher throughput: 30 req/s sustained with bursts up to 15
	// Previous: 10 req/s with burst of 5 — too restrictive under concurrency
	ratelimiter := ratelimit.New(rate.Every(time.Millisecond*33), 15)
	return []telegram.Middleware{
		waiter,
		ratelimiter,
	}
}
