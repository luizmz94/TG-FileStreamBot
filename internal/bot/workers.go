package bot

import (
	"EverythingSuckz/fsb/config"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/celestix/gotgproto"
	"github.com/celestix/gotgproto/sessionMaker"
	"github.com/glebarez/sqlite"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

type WorkerMetrics struct {
	ActiveRequests    int32     // Current active requests
	TotalRequests     int64     // Total requests handled
	FailedRequests    int64     // Total failed requests
	TotalResponseTime int64     // Total response time in milliseconds
	StartTime         time.Time // When the worker started
	LastRequestTime   time.Time // Last request timestamp
	Last5Times        []int64   // Last 5 response times in milliseconds
}

type Worker struct {
	ID           int
	Client       *gotgproto.Client
	Self         *tg.User
	log          *zap.Logger
	metrics      WorkerMetrics
	metricsMutex sync.RWMutex
	last5Times   []int64 // Circular buffer for last 5 response times
	last5Mutex   sync.Mutex
}

func (w *Worker) String() string {
	return fmt.Sprintf("{Worker (%d|@%s)}", w.ID, w.Self.Username)
}

// StartRequest increments the active request counter
func (w *Worker) StartRequest() {
	atomic.AddInt32(&w.metrics.ActiveRequests, 1)
	atomic.AddInt64(&w.metrics.TotalRequests, 1)
	w.metricsMutex.Lock()
	w.metrics.LastRequestTime = time.Now()
	w.metricsMutex.Unlock()
}

// EndRequest decrements the active request counter and records response time
func (w *Worker) EndRequest(startTime time.Time, failed bool) {
	atomic.AddInt32(&w.metrics.ActiveRequests, -1)

	duration := time.Since(startTime).Milliseconds()
	atomic.AddInt64(&w.metrics.TotalResponseTime, duration)

	// Store in last 5 times buffer
	w.last5Mutex.Lock()
	if w.last5Times == nil {
		w.last5Times = make([]int64, 0, 5)
	}
	if len(w.last5Times) >= 5 {
		// Remove oldest entry
		w.last5Times = w.last5Times[1:]
	}
	w.last5Times = append(w.last5Times, duration)
	w.last5Mutex.Unlock()

	if failed {
		atomic.AddInt64(&w.metrics.FailedRequests, 1)
	}
}

// GetActiveRequests returns the current number of active requests
func (w *Worker) GetActiveRequests() int32 {
	return atomic.LoadInt32(&w.metrics.ActiveRequests)
}

// GetMetrics returns a copy of the current metrics
func (w *Worker) GetMetrics() WorkerMetrics {
	w.metricsMutex.RLock()
	defer w.metricsMutex.RUnlock()

	return WorkerMetrics{
		ActiveRequests:    atomic.LoadInt32(&w.metrics.ActiveRequests),
		TotalRequests:     atomic.LoadInt64(&w.metrics.TotalRequests),
		FailedRequests:    atomic.LoadInt64(&w.metrics.FailedRequests),
		TotalResponseTime: atomic.LoadInt64(&w.metrics.TotalResponseTime),
		StartTime:         w.metrics.StartTime,
		LastRequestTime:   w.metrics.LastRequestTime,
	}
}

// GetAverageResponseTime returns average response time of last 5 requests in milliseconds
func (w *Worker) GetAverageResponseTime() float64 {
	w.last5Mutex.Lock()
	defer w.last5Mutex.Unlock()

	if len(w.last5Times) == 0 {
		return 0
	}

	var total int64
	for _, t := range w.last5Times {
		total += t
	}
	return float64(total) / float64(len(w.last5Times))
}

type BotWorkers struct {
	Bots     []*Worker
	starting int
	index    int
	mut      sync.Mutex
	log      *zap.Logger
}

var Workers *BotWorkers = &BotWorkers{
	log:  nil,
	Bots: make([]*Worker, 0),
}

func (w *BotWorkers) Init(log *zap.Logger) {
	w.log = log.Named("Workers")
}

func (w *BotWorkers) AddDefaultClient(client *gotgproto.Client, self *tg.User) {
	if w.Bots == nil {
		w.Bots = make([]*Worker, 0)
	}
	w.incStarting()
	worker := &Worker{
		Client: client,
		ID:     w.starting,
		Self:   self,
		log:    w.log,
	}
	worker.metrics.StartTime = time.Now()
	w.Bots = append(w.Bots, worker)
	w.log.Sugar().Infof("Default bot loaded as Worker #%d: @%s", w.starting, self.Username)
}

func (w *BotWorkers) incStarting() {
	w.mut.Lock()
	defer w.mut.Unlock()
	w.starting++
}

func (w *BotWorkers) Add(token string) (err error) {
	w.incStarting()
	var botID int = w.starting
	client, err := startWorker(w.log, token, botID)
	if err != nil {
		return err
	}
	// Extract bot ID from token for logging (first part before :)
	tokenPrefix := token[:10] + "..."
	w.log.Sugar().Infof("Worker #%d loaded: @%s (token: %s)", botID, client.Self.Username, tokenPrefix)
	worker := &Worker{
		Client: client,
		ID:     botID,
		Self:   client.Self,
		log:    w.log,
	}
	worker.metrics.StartTime = time.Now()
	w.Bots = append(w.Bots, worker)
	return nil
}

// GetNextWorker selects the best available worker using intelligent load balancing
// Priority: 1) Least active requests (immediate availability)
//  2. Least total requests (long-term distribution to avoid rate limits)
func GetNextWorker() *Worker {
	Workers.mut.Lock()
	defer Workers.mut.Unlock()

	if len(Workers.Bots) == 0 {
		Workers.log.Error("No workers available")
		return nil
	}

	// Calculate score for each worker (lower is better)
	// Score = (activeRequests * 1000) + (totalRequests / 10)
	// This gives priority to immediate availability while considering long-term usage
	var selectedWorker *Worker
	minScore := float64(999999999)

	for _, worker := range Workers.Bots {
		activeReqs := float64(worker.GetActiveRequests())
		totalReqs := float64(atomic.LoadInt64(&worker.metrics.TotalRequests))

		// Weight: Active requests are 10000x more important than total
		// This ensures free workers are always chosen first
		// But among free workers, distributes based on total usage
		score := (activeReqs * 10000) + totalReqs

		if score < minScore {
			minScore = score
			selectedWorker = worker
		}
	}

	Workers.log.Sugar().Debugf("Selected worker %d (active: %d, total: %d, score: %.0f)",
		selectedWorker.ID,
		selectedWorker.GetActiveRequests(),
		atomic.LoadInt64(&selectedWorker.metrics.TotalRequests),
		minScore)

	return selectedWorker
}

// GetNextWorkerExcluding selects the least loaded worker excluding the specified worker IDs
// This is useful for retry logic when a worker fails or times out
// Uses the same scoring algorithm as GetNextWorker
func GetNextWorkerExcluding(excludeIDs []int) *Worker {
	Workers.mut.Lock()
	defer Workers.mut.Unlock()

	if len(Workers.Bots) == 0 {
		Workers.log.Error("No workers available")
		return nil
	}

	var selectedWorker *Worker
	minScore := float64(999999999)

	for _, worker := range Workers.Bots {
		// Skip excluded workers
		excluded := false
		for _, excludeID := range excludeIDs {
			if worker.ID == excludeID {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}

		activeReqs := float64(worker.GetActiveRequests())
		totalReqs := float64(atomic.LoadInt64(&worker.metrics.TotalRequests))
		score := (activeReqs * 10000) + totalReqs

		if score < minScore {
			minScore = score
			selectedWorker = worker
		}
	}

	if selectedWorker != nil {
		Workers.log.Sugar().Debugf("Selected fallback worker %d (active: %d, total: %d, score: %.0f)",
			selectedWorker.ID,
			selectedWorker.GetActiveRequests(),
			atomic.LoadInt64(&selectedWorker.metrics.TotalRequests),
			minScore)
	}

	return selectedWorker
}

// GetDefaultWorker returns the default/main bot (first bot in the list)
// This should be used for operations that require channel access
func GetDefaultWorker() *Worker {
	Workers.mut.Lock()
	defer Workers.mut.Unlock()
	if len(Workers.Bots) == 0 {
		Workers.log.Error("No workers available")
		return nil
	}
	return Workers.Bots[0]
}

func StartWorkers(log *zap.Logger) (*BotWorkers, error) {
	Workers.Init(log)

	if len(config.ValueOf.MultiTokens) == 0 {
		Workers.log.Sugar().Info("No worker bot tokens provided, skipping worker initialization")
		return Workers, nil
	}
	Workers.log.Sugar().Info("Starting")
	if config.ValueOf.UseSessionFile {
		Workers.log.Sugar().Info("Using session file for workers")
		newpath := filepath.Join(".", "sessions")
		if err := os.MkdirAll(newpath, os.ModePerm); err != nil {
			Workers.log.Error("Failed to create sessions directory", zap.Error(err))
			return nil, err
		}
	}

	totalBots := len(config.ValueOf.MultiTokens)
	workerStartTimeout := time.Duration(config.ValueOf.WorkerStartTimeoutSeconds) * time.Second
	if workerStartTimeout <= 0 {
		workerStartTimeout = 120 * time.Second
	}

	const maxConcurrent = 3  // max simultaneous connections to Telegram
	const maxRetries = 3     // max retry attempts per worker
	const retryDelay = 5 * time.Second

	// Track which tokens failed so we can retry them
	type workerResult struct {
		index int
		err   error
	}

	startBatch := func(indices []int) []workerResult {
		var wg sync.WaitGroup
		results := make([]workerResult, len(indices))
		sem := make(chan struct{}, maxConcurrent)

		for j, idx := range indices {
			wg.Add(1)
			go func(j, idx int) {
				defer wg.Done()
				sem <- struct{}{}        // acquire semaphore
				defer func() { <-sem }() // release semaphore

				ctx, cancel := context.WithTimeout(context.Background(), workerStartTimeout)
				defer cancel()

				done := make(chan error, 1)
				go func() {
					done <- Workers.Add(config.ValueOf.MultiTokens[idx])
				}()

				select {
				case err := <-done:
					results[j] = workerResult{index: idx, err: err}
				case <-ctx.Done():
					results[j] = workerResult{index: idx, err: fmt.Errorf("timed out after %s", workerStartTimeout)}
				}
			}(j, idx)
		}
		wg.Wait()
		return results
	}

	// Initial attempt: all workers
	allIndices := make([]int, totalBots)
	for i := range allIndices {
		allIndices[i] = i
	}

	var successfulStarts int32
	failedIndices := allIndices

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			Workers.log.Sugar().Infof("Retrying %d failed workers (attempt %d/%d) after %s delay...",
				len(failedIndices), attempt, maxRetries, retryDelay)
			time.Sleep(retryDelay)
		}

		results := startBatch(failedIndices)

		var newFailed []int
		for _, r := range results {
			if r.err != nil {
				Workers.log.Error("Failed to start worker",
					zap.Int("index", r.index),
					zap.Int("attempt", attempt+1),
					zap.Error(r.err))
				newFailed = append(newFailed, r.index)
			} else {
				atomic.AddInt32(&successfulStarts, 1)
			}
		}

		failedIndices = newFailed
		if len(failedIndices) == 0 {
			break
		}
	}

	if len(failedIndices) > 0 {
		Workers.log.Sugar().Warnf("%d workers failed to start after %d retries: indices %v",
			len(failedIndices), maxRetries, failedIndices)
	}

	Workers.log.Sugar().Infof("Successfully started %d/%d bots", successfulStarts, totalBots)
	return Workers, nil
}

func startWorker(l *zap.Logger, botToken string, index int) (*gotgproto.Client, error) {
	log := l.Named("Worker").Sugar()
	log.Infof("Starting worker with index - %d", index)
	var sessionType sessionMaker.SessionConstructor
	if config.ValueOf.UseSessionFile {
		sessionType = sessionMaker.SqlSession(sqlite.Open(fmt.Sprintf("sessions/worker-%d.session", index)))
	} else {
		sessionType = sessionMaker.SimpleSession()
	}
	client, err := gotgproto.NewClient(
		int(config.ValueOf.ApiID),
		config.ValueOf.ApiHash,
		gotgproto.ClientTypeBot(botToken),
		&gotgproto.ClientOpts{
			Session:          sessionType,
			DisableCopyright: true,
			Middlewares:      GetFloodMiddleware(log.Desugar()),
		},
	)
	if err != nil {
		return nil, err
	}
	return client, nil
}
