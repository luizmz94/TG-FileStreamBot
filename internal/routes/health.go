package routes

import (
	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/bot"
	"EverythingSuckz/fsb/internal/utils"
	"context"
	"fmt"
	"html"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	defaultHealthProbeMessageID = 479688
	defaultHealthProbeTimeout   = 15 * time.Second
	defaultHealthConcurrency    = 4
)

// healthProbeRunning guards against overlapping probe runs, which would issue
// duplicate live API calls across every bot.
var healthProbeRunning int32

// LoadHealth registers the active bot health-check route.
// This is an on-demand probe: it actively fetches a known message from
// MEDIA_CHANNEL_ID with every bot and reports per-bot reachability. Unlike
// /status (which is passive and hides metadata-fetch failures), this confirms
// each bot can really access the channel right now.
//
// Registered via reflection on the main router and explicitly on the status
// server (see LoadStatusOnly).
func (e *allRoutes) LoadHealth(r *Route) {
	healthLog := e.log.Named("Health")
	defer healthLog.Info("Loaded bot health route")
	r.Engine.GET("/health/bots", getBotHealthRoute(healthLog))
}

type BotHealth struct {
	ID                  int    `json:"id"`
	Username            string `json:"username"`
	OK                  bool   `json:"ok"`
	LatencyMs           int64  `json:"latency_ms"`
	Error               string `json:"error,omitempty"`
	Detail              string `json:"detail,omitempty"`
	ConsecutiveFailures int32  `json:"consecutive_failures"`
	MetadataFailures    int64  `json:"metadata_failures"`
}

type BotHealthResponse struct {
	Channel        int64       `json:"channel_id"`
	ProbeMessageID int         `json:"probe_message_id"`
	TotalBots      int         `json:"total_bots"`
	HealthyBots    int         `json:"healthy_bots"`
	UnhealthyBots  int         `json:"unhealthy_bots"`
	DurationMs     int64       `json:"duration_ms"`
	Bots           []BotHealth `json:"bots"`
	Timestamp      time.Time   `json:"timestamp"`
}

func getBotHealthRoute(logger *zap.Logger) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if config.ValueOf.MediaChannelID == 0 {
			ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": "MEDIA_CHANNEL_ID not configured"})
			return
		}
		if bot.Workers == nil || len(bot.Workers.Bots) == 0 {
			ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": "no workers available"})
			return
		}

		// Reject overlapping runs so a refresh storm can't hammer the bots.
		if !atomic.CompareAndSwapInt32(&healthProbeRunning, 0, 1) {
			ctx.JSON(http.StatusTooManyRequests, gin.H{"error": "a health probe is already running"})
			return
		}
		defer atomic.StoreInt32(&healthProbeRunning, 0)

		msgID := defaultHealthProbeMessageID
		if q := ctx.Query("msg"); q != "" {
			if parsed, err := strconv.Atoi(q); err == nil && parsed > 0 {
				msgID = parsed
			}
		}
		timeout := defaultHealthProbeTimeout
		if q := ctx.Query("timeout"); q != "" {
			if parsed, err := strconv.Atoi(q); err == nil && parsed > 0 {
				timeout = time.Duration(parsed) * time.Second
			}
		}
		concurrency := defaultHealthConcurrency
		if q := ctx.Query("concurrency"); q != "" {
			if parsed, err := strconv.Atoi(q); err == nil && parsed > 0 {
				concurrency = parsed
			}
		}

		logger.Info("Running bot health probe",
			zap.Int("probeMessageID", msgID),
			zap.Int64("channelID", config.ValueOf.MediaChannelID),
			zap.Int("bots", len(bot.Workers.Bots)))

		start := time.Now()
		results := probeAllBots(logger, msgID, timeout, concurrency)
		response := BotHealthResponse{
			Channel:        config.ValueOf.MediaChannelID,
			ProbeMessageID: msgID,
			TotalBots:      len(results),
			DurationMs:     time.Since(start).Milliseconds(),
			Bots:           results,
			Timestamp:      time.Now(),
		}
		for _, b := range results {
			if b.OK {
				response.HealthyBots++
			} else {
				response.UnhealthyBots++
			}
		}

		if ctx.Query("format") == "html" || ctx.GetHeader("Accept") == "text/html" {
			ctx.Data(http.StatusOK, "text/html; charset=utf-8", []byte(generateHealthHTML(response)))
			return
		}
		ctx.JSON(http.StatusOK, response)
	}
}

// probeAllBots actively probes every worker against the media channel and
// returns one result per bot, ordered by worker ID. It also updates each
// worker's health counters so a recovered bot is re-promoted and a dead one is
// deprioritized in load balancing.
func probeAllBots(logger *zap.Logger, msgID int, timeout time.Duration, concurrency int) []BotHealth {
	workers := bot.Workers.Bots
	results := make([]BotHealth, len(workers))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, w := range workers {
		wg.Add(1)
		go func(i int, w *bot.Worker) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			start := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			file, err := utils.ProbeFileFromChannel(ctx, w.Client, config.ValueOf.MediaChannelID, msgID)
			recordFetchHealth(w, err)

			res := BotHealth{
				ID:                  w.ID,
				Username:            w.Self.Username,
				LatencyMs:           time.Since(start).Milliseconds(),
				ConsecutiveFailures: w.GetConsecutiveFailures(),
				MetadataFailures:    w.GetMetadataFailures(),
			}
			if err != nil {
				res.OK = false
				res.Error = err.Error()
			} else {
				res.OK = true
				res.Detail = fmt.Sprintf("%s (%d bytes)", file.FileName, file.FileSize)
			}
			results[i] = res
		}(i, w)
	}
	wg.Wait()

	sort.SliceStable(results, func(i, j int) bool { return results[i].ID < results[j].ID })
	return results
}

func generateHealthHTML(r BotHealthResponse) string {
	rows := ""
	for _, b := range r.Bots {
		status := "🟢 OK"
		rowStyle := "background:#f0fff4;"
		detail := html.EscapeString(b.Detail)
		if !b.OK {
			status = "🔴 FAIL"
			rowStyle = "background:#fed7d7;"
			detail = html.EscapeString(b.Error)
		}
		rows += fmt.Sprintf(
			"<tr style=\"%s\"><td>#%d</td><td>@%s</td><td>%s</td><td>%dms</td><td>%d</td><td>%d</td><td>%s</td></tr>",
			rowStyle, b.ID, html.EscapeString(b.Username), status, b.LatencyMs,
			b.ConsecutiveFailures, b.MetadataFailures, detail)
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>Bot Health Probe</title>
<style>
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;padding:24px;background:#f7fafc;color:#2d3748;}
h1{margin:0 0 4px;}
.sub{color:#718096;font-size:13px;margin-bottom:16px;}
.summary{display:flex;gap:16px;margin-bottom:20px;}
.card{background:#fff;border-radius:8px;padding:14px 18px;box-shadow:0 2px 6px rgba(0,0,0,.08);}
.card .v{font-size:26px;font-weight:700;}
.bad .v{color:#e53e3e;}.good .v{color:#2f855a;}
table{width:100%%;border-collapse:collapse;background:#fff;border-radius:8px;overflow:hidden;box-shadow:0 2px 6px rgba(0,0,0,.08);}
th,td{padding:10px 14px;text-align:left;border-bottom:1px solid #edf2f7;font-size:14px;}
th{background:#edf2f7;text-transform:uppercase;font-size:12px;letter-spacing:.5px;}
a.btn{display:inline-block;margin-bottom:16px;padding:8px 14px;background:#4299e1;color:#fff;border-radius:6px;text-decoration:none;font-size:14px;}
</style></head><body>
<h1>🩺 Bot Health Probe</h1>
<div class="sub">Active probe of channel %d, message %d — %s · took %dms</div>
<a class="btn" href="?format=html&msg=%d">↻ Re-run probe</a>
<div class="summary">
<div class="card"><div>Total</div><div class="v">%d</div></div>
<div class="card good"><div>Healthy</div><div class="v">%d</div></div>
<div class="card bad"><div>Unhealthy</div><div class="v">%d</div></div>
</div>
<table>
<thead><tr><th>ID</th><th>Bot</th><th>Result</th><th>Latency</th><th>Consec. Fails</th><th>Total Meta Fails</th><th>Detail</th></tr></thead>
<tbody>%s</tbody>
</table>
</body></html>`,
		r.Channel, r.ProbeMessageID, r.Timestamp.Format("2006-01-02 15:04:05"), r.DurationMs,
		r.ProbeMessageID, r.TotalBots, r.HealthyBots, r.UnhealthyBots, rows)
}
