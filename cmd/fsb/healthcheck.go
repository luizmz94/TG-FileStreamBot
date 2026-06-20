package main

import (
	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/bot"
	"EverythingSuckz/fsb/internal/cache"
	"EverythingSuckz/fsb/internal/utils"
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/celestix/gotgproto"
	"github.com/celestix/gotgproto/sessionMaker"
	"github.com/glebarez/sqlite"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

var healthCheckCmd = &cobra.Command{
	Use:   "healthcheck",
	Short: "Probe every bot in fsb.env for live access to MEDIA_CHANNEL_ID.",
	Long: "Connects each bot token (MULTI_TOKEN* plus BOT_TOKEN) directly to Telegram with a\n" +
		"throwaway in-memory session and tries to fetch a known message from MEDIA_CHANNEL_ID.\n" +
		"This is an active probe — it reveals bots that lost channel access even though the\n" +
		"/status dashboard still shows them green (failed metadata fetches are never counted there).",
	DisableSuggestions: false,
	Run:                runHealthCheck,
}

func init() {
	healthCheckCmd.Flags().IntP("msg", "m", 0, "message ID inside MEDIA_CHANNEL_ID to probe (required)")
	healthCheckCmd.Flags().IntP("concurrency", "c", 3, "max simultaneous bot connections")
	healthCheckCmd.Flags().IntP("timeout", "t", 30, "per-bot timeout in seconds")
	healthCheckCmd.Flags().Bool("include-default", true, "also probe the main BOT_TOKEN bot")
	healthCheckCmd.Flags().String("session-dir", "", "if set, resume each bot from sessions/worker-N.session in this dir (faithful to prod's persisted PeerStorage) instead of a cold in-memory session")
	_ = healthCheckCmd.MarkFlagRequired("msg")
}

type probeResult struct {
	index     int
	token     string
	username  string
	ok        bool
	latency   time.Duration
	detail    string
	isDefault bool
}

// botIDFromToken returns the numeric bot id (the part before ":") for display
// when we can't resolve a username (e.g. the connection itself failed).
func botIDFromToken(token string) string {
	if i := strings.IndexByte(token, ':'); i > 0 {
		return token[:i]
	}
	if len(token) > 8 {
		return token[:8] + "..."
	}
	return token
}

func runHealthCheck(cmd *cobra.Command, args []string) {
	// Quiet logger: the probe prints its own report; we only want errors.
	utils.InitLogger(false, "error")
	log := utils.Logger
	config.Load(log, cmd)
	cache.InitCache(log)

	msgID, _ := cmd.Flags().GetInt("msg")
	concurrency, _ := cmd.Flags().GetInt("concurrency")
	timeoutSecs, _ := cmd.Flags().GetInt("timeout")
	includeDefault, _ := cmd.Flags().GetBool("include-default")
	sessionDir, _ := cmd.Flags().GetString("session-dir")

	if config.ValueOf.MediaChannelID == 0 {
		fmt.Println("ERROR: MEDIA_CHANNEL_ID is not set in fsb.env — nothing to probe against.")
		return
	}
	if msgID <= 0 {
		fmt.Println("ERROR: --msg must be a positive message ID present in MEDIA_CHANNEL_ID.")
		return
	}
	if concurrency < 1 {
		concurrency = 1
	}
	perBotTimeout := time.Duration(timeoutSecs) * time.Second

	// Build the list of tokens to probe: worker tokens first, then the default bot.
	type tokenEntry struct {
		token     string
		isDefault bool
	}
	var entries []tokenEntry
	for _, t := range config.ValueOf.MultiTokens {
		entries = append(entries, tokenEntry{token: t})
	}
	if includeDefault && config.ValueOf.BotToken != "" {
		entries = append(entries, tokenEntry{token: config.ValueOf.BotToken, isDefault: true})
	}

	if len(entries) == 0 {
		fmt.Println("ERROR: no bot tokens found in fsb.env (MULTI_TOKEN* / BOT_TOKEN).")
		return
	}

	fmt.Printf("Probing %d bot(s) against channel %d, message %d (timeout %ds, concurrency %d)...\n\n",
		len(entries), config.ValueOf.MediaChannelID, msgID, timeoutSecs, concurrency)

	results := make([]probeResult, len(entries))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, e := range entries {
		wg.Add(1)
		go func(i int, e tokenEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sessionPath := ""
			if sessionDir != "" && !e.isDefault {
				sessionPath = filepath.Join(sessionDir, fmt.Sprintf("worker-%d.session", i+1))
			}
			results[i] = probeBot(log, i, e.token, e.isDefault, msgID, perBotTimeout, sessionPath)
		}(i, e)
	}
	wg.Wait()

	printProbeReport(results, sessionDir != "")
}

// probeBot connects a single bot token and attempts one metadata fetch from the
// configured media channel. It always returns a result (never panics on a bad bot).
func probeBot(log *zap.Logger, index int, token string, isDefault bool, msgID int, timeout time.Duration, sessionPath string) probeResult {
	res := probeResult{index: index, token: token, isDefault: isDefault, username: botIDFromToken(token)}
	start := time.Now()

	var session sessionMaker.SessionConstructor = sessionMaker.SimpleSession() // in-memory, never touches sessions/
	if sessionPath != "" {
		session = sessionMaker.SqlSession(sqlite.Open(sessionPath))
	}

	// Bound the whole probe (connect + fetch) so a dead bot can't hang the run.
	type connResult struct {
		client *gotgproto.Client
		err    error
	}
	connCh := make(chan connResult, 1)
	go func() {
		client, err := gotgproto.NewClient(
			int(config.ValueOf.ApiID),
			config.ValueOf.ApiHash,
			gotgproto.ClientTypeBot(token),
			&gotgproto.ClientOpts{
				Session:          session,
				DisableCopyright: true,
				Middlewares:      bot.GetFloodMiddleware(log),
			},
		)
		connCh <- connResult{client: client, err: err}
	}()

	connTimer := time.NewTimer(timeout)
	defer connTimer.Stop()

	var client *gotgproto.Client
	select {
	case cr := <-connCh:
		if cr.err != nil {
			res.ok = false
			res.latency = time.Since(start)
			res.detail = "connect failed: " + cr.err.Error()
			return res
		}
		client = cr.client
	case <-connTimer.C:
		res.ok = false
		res.latency = time.Since(start)
		res.detail = fmt.Sprintf("connect timed out after %s", timeout)
		return res
	}
	defer client.Stop()

	if client.Self != nil && client.Self.Username != "" {
		res.username = "@" + client.Self.Username
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	file, err := utils.FileFromMessageAndChannel(ctx, client, config.ValueOf.MediaChannelID, msgID)
	res.latency = time.Since(start)
	if err != nil {
		res.ok = false
		res.detail = err.Error()
		return res
	}

	res.ok = true
	res.detail = fmt.Sprintf("%s (%s)", file.FileName, humanBytes(file.FileSize))
	return res
}

func printProbeReport(results []probeResult, usedSessionDir bool) {
	// Keep the original token order for a stable, predictable report.
	sort.SliceStable(results, func(i, j int) bool { return results[i].index < results[j].index })

	fmt.Printf("%-4s %-24s %-7s %-10s %s\n", "#", "BOT", "RESULT", "LATENCY", "DETAIL")
	fmt.Println(strings.Repeat("-", 90))

	var okCount int
	var failing []string
	var coldChannelInvalid int
	for n, r := range results {
		status := "OK"
		if !r.ok {
			status = "FAIL"
			if strings.Contains(r.detail, "CHANNEL_INVALID") {
				coldChannelInvalid++
			}
			label := r.username
			if r.isDefault {
				label += " (default)"
			}
			failing = append(failing, label)
		} else {
			okCount++
		}
		name := r.username
		if r.isDefault {
			name += " *"
		}
		fmt.Printf("%-4d %-24s %-7s %-10s %s\n",
			n+1, name, status, fmt.Sprintf("%dms", r.latency.Milliseconds()), r.detail)
	}

	fmt.Println(strings.Repeat("-", 90))
	fmt.Printf("Summary: %d OK, %d FAIL  (* = default BOT_TOKEN)\n", okCount, len(results)-okCount)
	if len(failing) > 0 {
		fmt.Printf("Failing bots: %s\n", strings.Join(failing, ", "))
	}
	if !usedSessionDir && coldChannelInvalid > 0 {
		fmt.Println()
		fmt.Printf("NOTE: %d bot(s) returned CHANNEL_INVALID on a COLD in-memory session.\n", coldChannelInvalid)
		fmt.Println("This usually means the bot just lacks the channel's cached access_hash, NOT that")
		fmt.Println("it is broken in production (where the access_hash is persisted in sessions/).")
		fmt.Println("Re-run with --session-dir sessions (against the live/copied session files) for a")
		fmt.Println("faithful health check.")
	}
}

func humanBytes(b int64) string {
	if b == 0 {
		return "photo/0 B"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGT"[exp])
}
