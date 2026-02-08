package routes

import (
	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/bot"
	"EverythingSuckz/fsb/internal/types"
	"EverythingSuckz/fsb/internal/utils"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gotd/td/tg"
	range_parser "github.com/quantumsheep/range-parser"
	"go.uber.org/zap"
)

// RequestLog tracks information about each request to the /direct endpoint
type RequestLog struct {
	Timestamp  time.Time `json:"timestamp"`
	MessageID  int       `json:"message_id"`
	WorkerID   int       `json:"worker_id"`
	WorkerName string    `json:"worker_name"`
	ClientIP   string    `json:"client_ip"`
	RangeStart int64     `json:"range_start"`
	RangeEnd   int64     `json:"range_end"`
	ChunkSize  int64     `json:"chunk_size"`
	BytesSent  int64     `json:"bytes_sent"`
	FileSize   int64     `json:"file_size"`
	StatusCode int       `json:"status_code"`
	Duration   int64     `json:"duration_ms"`
	UserAgent  string    `json:"user_agent"`
	Referer    string    `json:"referer"`
}

// Global request log storage (circular buffer for last 300 requests)
var (
	requestLogs     = make([]RequestLog, 0, 300)
	requestLogMutex sync.RWMutex
)

// AddRequestLog adds a new request log entry, maintaining only the last 300
func AddRequestLog(log RequestLog) {
	requestLogMutex.Lock()
	defer requestLogMutex.Unlock()

	if len(requestLogs) >= 300 {
		// Remove oldest entry
		requestLogs = requestLogs[1:]
	}
	requestLogs = append(requestLogs, log)
}

// GetRequestLogs returns a copy of all request logs
func GetRequestLogs() []RequestLog {
	requestLogMutex.RLock()
	defer requestLogMutex.RUnlock()

	// Return a copy to avoid concurrent access issues
	logs := make([]RequestLog, len(requestLogs))
	copy(logs, requestLogs)
	return logs
}

// LoadDirect registers the direct streaming route
// This route allows streaming files directly from a configured media channel
// using only the message ID, without requiring hash validation or internal database
func (e *allRoutes) LoadDirect(r *Route) {
	directLog := e.log.Named("DirectStream")
	defer directLog.Info("Loaded direct stream route")
	r.Engine.GET("/direct/:messageID", getDirectStreamRoute(directLog))
}

// fetchFileWithRetry attempts to fetch file with timeout and automatic retry using different workers.
// It returns both the file metadata and the worker that produced it so we can stream using
// the same bot account (file_reference is tied to the bot).
func fetchFileWithRetry(
	bgCtx context.Context,
	logger *zap.Logger,
	worker *bot.Worker,
	messageID int,
	channelID int64,
	exclude []int,
) (*types.File, *bot.Worker, error) {
	// keep track of which workers have been tried to avoid immediate reuse
	excludeWorkers := append([]int{}, exclude...)
	excludeWorkers = append(excludeWorkers, worker.ID)

	type result struct {
		file *types.File
		err  error
		w    *bot.Worker
	}

	tryOnce := func(ctx context.Context, w *bot.Worker) result {
		// bound each attempt to 5s to avoid hanging on a single bot
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		file, err := utils.FileFromMessageAndChannel(ctx, w.Client, channelID, messageID)
		return result{file: file, err: err, w: w}
	}

	res := tryOnce(bgCtx, worker)
	if res.err == nil {
		logger.Debug("File fetched successfully",
			zap.Int("workerID", worker.ID))
		return res.file, worker, nil
	}

	// Hard errors should stop immediately
	if res.err != nil && (res.err.Error() == "message not found in channel" ||
		res.err.Error() == "message was deleted or is not accessible") {
		return nil, nil, res.err
	}

	logger.Warn("Worker failed to fetch file, will retry with another worker",
		zap.Int("workerID", worker.ID),
		zap.Error(res.err))

	maxRetries := 3
	for retry := 0; retry < maxRetries; retry++ {
		fallbackWorker := bot.GetNextWorkerExcluding(excludeWorkers)
		if fallbackWorker == nil {
			logger.Error("No fallback workers available")
			return nil, nil, fmt.Errorf("all workers exhausted")
		}
		excludeWorkers = append(excludeWorkers, fallbackWorker.ID)

		logger.Info("Retrying with fallback worker",
			zap.Int("retry", retry+1),
			zap.Int("fallbackWorkerID", fallbackWorker.ID),
			zap.String("fallbackWorkerUsername", fallbackWorker.Self.Username))

		res = tryOnce(bgCtx, fallbackWorker)
		if res.err == nil {
			logger.Info("File fetched successfully with fallback worker",
				zap.Int("fallbackWorkerID", fallbackWorker.ID),
				zap.Int("retry", retry+1))
			return res.file, fallbackWorker, nil
		}

		if res.err.Error() == "message not found in channel" ||
			res.err.Error() == "message was deleted or is not accessible" {
			return nil, nil, res.err
		}

		logger.Warn("Fallback worker also failed",
			zap.Int("fallbackWorkerID", fallbackWorker.ID),
			zap.Error(res.err))
	}

	return nil, nil, fmt.Errorf("failed to fetch file after %d retries", maxRetries)
}

// fetchFileWithRace spins two workers concurrently and returns the first successful response.
// If only one worker is provided it behaves like a single attempt with retry logic.
func fetchFileWithRace(
	bgCtx context.Context,
	logger *zap.Logger,
	workers []*bot.Worker,
	messageID int,
	channelID int64,
) (*types.File, *bot.Worker, error) {
	if len(workers) == 0 {
		return nil, nil, fmt.Errorf("no workers provided")
	}

	// If there's only one worker, fall back to normal retry logic
	if len(workers) == 1 {
		return fetchFileWithRetry(bgCtx, logger, workers[0], messageID, channelID, nil)
	}

	type result struct {
		file   *types.File
		worker *bot.Worker
		err    error
	}

	ctx, cancel := context.WithCancel(bgCtx)
	defer cancel()

	results := make(chan result, len(workers))

	for _, w := range workers {
		worker := w
		go func() {
			attemptCtx, attemptCancel := context.WithTimeout(ctx, 5*time.Second)
			defer attemptCancel()

			file, err := utils.FileFromMessageAndChannel(attemptCtx, worker.Client, channelID, messageID)
			// Use buffered channel to avoid goroutine leak if caller returns early
			results <- result{file: file, worker: worker, err: err}
		}()
	}

	var firstErr error
	for i := 0; i < len(workers); i++ {
		select {
		case res := <-results:
			if res.err == nil && res.file != nil {
				// cancel other attempts; they will exit because of context cancellation
				cancel()
				logger.Info("Race winner",
					zap.Int("workerID", res.worker.ID),
					zap.String("workerUsername", res.worker.Self.Username))
				return res.file, res.worker, nil
			}
			if firstErr == nil {
				firstErr = res.err
			}
		case <-ctx.Done():
			// context cancelled because another worker already succeeded
			return nil, nil, ctx.Err()
		}
	}

	if firstErr == nil {
		firstErr = fmt.Errorf("failed to fetch file with any worker")
	}
	return nil, nil, firstErr
}

func getDirectStreamRoute(logger *zap.Logger) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		w := ctx.Writer
		r := ctx.Request

		// Check if MEDIA_CHANNEL_ID is configured
		if config.ValueOf.MediaChannelID == 0 {
			logger.Error("MEDIA_CHANNEL_ID not configured")
			ctx.JSON(http.StatusInternalServerError, gin.H{
				"error": "MEDIA_CHANNEL_ID not configured",
			})
			return
		}

		// Parse and validate message ID
		messageIDParam := ctx.Param("messageID")
		messageID, err := strconv.Atoi(messageIDParam)
		if err != nil {
			logger.Warn("Invalid message ID", zap.String("messageID", messageIDParam), zap.Error(err))
			ctx.JSON(http.StatusBadRequest, gin.H{
				"error": "invalid message ID",
			})
			return
		}

		// Validate HMAC signature if STREAM_SECRET is configured
		if config.ValueOf.StreamSecret != "" {
			signature := ctx.Query("sig")
			expiration := ctx.Query("exp")

			if err := utils.ValidateHMACSignature(config.ValueOf.StreamSecret, messageID, signature, expiration); err != nil {
				logger.Warn("HMAC validation failed",
					zap.Int("messageID", messageID),
					zap.String("clientIP", ctx.ClientIP()),
					zap.Error(err))
				ctx.JSON(http.StatusUnauthorized, gin.H{
					"error": "unauthorized: invalid or expired signature",
				})
				return
			}
		}

		logger.Debug("Direct stream request",
			zap.Int("messageID", messageID),
			zap.Int64("channelID", config.ValueOf.MediaChannelID))

		// Choose up to two workers and race them; fallback to others if needed
		primaryWorker := bot.GetNextWorker()
		if primaryWorker == nil {
			logger.Error("No workers available")
			ctx.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "no workers available",
			})
			return
		}
		workerPool := []*bot.Worker{primaryWorker}
		seenWorkerIDs := []int{primaryWorker.ID}

		if secondary := bot.GetNextWorkerExcluding(seenWorkerIDs); secondary != nil {
			workerPool = append(workerPool, secondary)
			seenWorkerIDs = append(seenWorkerIDs, secondary.ID)
		}

		// Track this request
		requestStartTime := time.Now()

		// Initialize request log (worker info is filled after winner is known)
		reqLog := RequestLog{
			Timestamp: requestStartTime,
			MessageID: messageID,
			ClientIP:  ctx.ClientIP(),
			UserAgent: ctx.GetHeader("User-Agent"),
			Referer:   ctx.GetHeader("Referer"),
		}

		// Create a background context for Telegram API calls that won't be cancelled
		// when the HTTP client disconnects. This prevents "context canceled" errors
		// during file streaming.
		bgCtx := context.Background()

		// Race two bots (when available) and fall back to remaining pool if both fail
		file, selectedWorker, err := fetchFileWithRace(bgCtx, logger, workerPool, messageID, config.ValueOf.MediaChannelID)
		if err != nil {
			fallbackWorker := bot.GetNextWorkerExcluding(seenWorkerIDs)
			if fallbackWorker != nil {
				seenWorkerIDs = append(seenWorkerIDs, fallbackWorker.ID)
				file, selectedWorker, err = fetchFileWithRetry(bgCtx, logger, fallbackWorker, messageID, config.ValueOf.MediaChannelID, seenWorkerIDs)
			}
		}

		if err != nil {
			logger.Error("Failed to get file from channel after retries",
				zap.Int("messageID", messageID),
				zap.Int64("channelID", config.ValueOf.MediaChannelID),
				zap.Error(err))

			// Check if it's a "not found" type of error
			if err.Error() == "message not found in channel" || err.Error() == "message was deleted or is not accessible" {
				ctx.JSON(http.StatusNotFound, gin.H{
					"error": "message not found or has no media",
				})
				return
			}

			// Other errors are likely Telegram API issues
			ctx.JSON(http.StatusBadGateway, gin.H{
				"error": "failed to fetch file from Telegram",
			})
			return
		}

		// Safety check: ensure we have a worker for streaming
		if selectedWorker == nil {
			logger.Error("No worker selected after fetch")
			ctx.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "no workers available",
			})
			return
		}

		// Now that we know the winning worker, mark the request as active
		selectedWorker.StartRequest()
		reqLog.WorkerID = selectedWorker.ID
		reqLog.WorkerName = selectedWorker.Self.Username

		defer func() {
			// Check if request failed based on HTTP status
			failed := w.Status() >= 400
			selectedWorker.EndRequest(requestStartTime, failed)

			// Complete request log with actual bytes sent
			// Usa o Size() nativo do gin.ResponseWriter que conta bytes escritos
			reqLog.StatusCode = w.Status()
			reqLog.Duration = time.Since(requestStartTime).Milliseconds()
			reqLog.BytesSent = int64(w.Size())
			AddRequestLog(reqLog)
		}()

		logger.Debug("Using worker for request",
			zap.Int("workerID", selectedWorker.ID),
			zap.String("workerUsername", selectedWorker.Self.Username),
			zap.Int32("activeRequests", selectedWorker.GetActiveRequests()))

		// Handle photos (which have FileSize 0)
		if file.FileSize == 0 {
			res, err := selectedWorker.Client.API().UploadGetFile(bgCtx, &tg.UploadGetFileRequest{
				Location: file.Location,
				Offset:   0,
				Limit:    1024 * 1024,
			})
			if err != nil {
				// Check for FILE_REFERENCE_EXPIRED and retry for photos
				if strings.Contains(err.Error(), "FILE_REFERENCE_EXPIRED") {
					logger.Warn("FILE_REFERENCE_EXPIRED for photo, refetching metadata",
						zap.Int("messageID", messageID))

					freshFile, refetchErr := utils.RefetchFileFromMessageAndChannel(bgCtx, selectedWorker.Client, config.ValueOf.MediaChannelID, messageID)
					if refetchErr != nil {
						logger.Error("Failed to refetch photo after FILE_REFERENCE_EXPIRED",
							zap.Int("messageID", messageID),
							zap.Error(refetchErr))
						ctx.JSON(http.StatusInternalServerError, gin.H{
							"error": "photo file reference expired and refetch failed",
						})
						return
					}

					// Retry with fresh file_reference
					res, err = selectedWorker.Client.API().UploadGetFile(bgCtx, &tg.UploadGetFileRequest{
						Location: freshFile.Location,
						Offset:   0,
						Limit:    1024 * 1024,
					})
					if err != nil {
						logger.Error("Failed to get photo file after refetch", zap.Error(err))
						ctx.JSON(http.StatusInternalServerError, gin.H{
							"error": "failed to get photo file after refetch",
						})
						return
					}
				} else {
					logger.Error("Failed to get photo file", zap.Error(err))
					ctx.JSON(http.StatusInternalServerError, gin.H{
						"error": "failed to get photo file",
					})
					return
				}
			}
			result, ok := res.(*tg.UploadFile)
			if !ok {
				logger.Error("Unexpected response type for photo")
				ctx.JSON(http.StatusInternalServerError, gin.H{
					"error": "unexpected response",
				})
				return
			}
			fileBytes := result.GetBytes()
			ctx.Header("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", file.FileName))
			if r.Method != "HEAD" {
				ctx.Data(http.StatusOK, file.MimeType, fileBytes)
			}
			return
		}

		// Handle range requests for video/document streaming
		ctx.Header("Accept-Ranges", "bytes")
		var start, end int64
		rangeHeader := r.Header.Get("Range")

		if rangeHeader == "" {
			start = 0
			end = file.FileSize - 1
			w.WriteHeader(http.StatusOK)
		} else {
			ranges, err := range_parser.Parse(file.FileSize, r.Header.Get("Range"))
			if err != nil {
				logger.Warn("Failed to parse range header", zap.Error(err))
				ctx.JSON(http.StatusBadRequest, gin.H{
					"error": "invalid range header",
				})
				return
			}
			start = ranges[0].Start
			end = ranges[0].End
			ctx.Header("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, file.FileSize))
			logger.Debug("Content-Range",
				zap.Int64("start", start),
				zap.Int64("end", end),
				zap.Int64("fileSize", file.FileSize))
			w.WriteHeader(http.StatusPartialContent)
		}

		// Update request log with file and range info
		reqLog.FileSize = file.FileSize
		reqLog.RangeStart = start
		reqLog.RangeEnd = end
		reqLog.ChunkSize = end - start + 1

		contentLength := end - start + 1
		mimeType := file.MimeType

		if mimeType == "" {
			mimeType = "application/octet-stream"
		}

		ctx.Header("Content-Type", mimeType)
		ctx.Header("Content-Length", strconv.FormatInt(contentLength, 10))

		disposition := "inline"

		// Allow forced download via query parameter
		if ctx.Query("d") == "true" {
			disposition = "attachment"
		}

		ctx.Header("Content-Disposition", fmt.Sprintf("%s; filename=\"%s\"", disposition, file.FileName))

		// Stream the file content
		if r.Method != "HEAD" {
			lr, err := utils.NewTelegramReader(bgCtx, selectedWorker.Client, file.Location, start, end, contentLength)
			if err != nil {
				logger.Error("Failed to create Telegram reader",
					zap.Int("messageID", messageID),
					zap.Error(err))
				return
			}

			bytesWritten, err := io.CopyN(w, lr, contentLength)
			if err != nil {
				// Check if the error is due to client disconnection
				if ctx.Request.Context().Err() != nil {
					logger.Warn("Client disconnected during stream",
						zap.Int("messageID", messageID),
						zap.Int64("bytesWritten", bytesWritten),
						zap.Int64("expectedBytes", contentLength),
						zap.Error(ctx.Request.Context().Err()))
					return
				}

				// Check if it's FILE_REFERENCE_EXPIRED error and retry once
				if err.Error() != "" && strings.Contains(err.Error(), "FILE_REFERENCE_EXPIRED") {
					logger.Warn("FILE_REFERENCE_EXPIRED detected, refetching file metadata and retrying",
						zap.Int("messageID", messageID))

					// Refetch file metadata with fresh file_reference
					freshFile, refetchErr := utils.RefetchFileFromMessageAndChannel(bgCtx, selectedWorker.Client, config.ValueOf.MediaChannelID, messageID)
					if refetchErr != nil {
						logger.Error("Failed to refetch file after FILE_REFERENCE_EXPIRED",
							zap.Int("messageID", messageID),
							zap.Error(refetchErr))
						ctx.JSON(http.StatusInternalServerError, gin.H{
							"error": "file reference expired and refetch failed",
						})
						return
					}

					// Retry streaming with fresh file_reference
					lr2, err2 := utils.NewTelegramReader(bgCtx, selectedWorker.Client, freshFile.Location, start, end, contentLength)
					if err2 != nil {
						logger.Error("Failed to create Telegram reader after refetch",
							zap.Int("messageID", messageID),
							zap.Error(err2))
						return
					}

					bytesWritten2, err2 := io.CopyN(w, lr2, contentLength)
					if err2 != nil {
						logger.Error("Error while copying stream after refetch",
							zap.Int("messageID", messageID),
							zap.Int64("bytesWritten", bytesWritten2),
							zap.Int64("expectedBytes", contentLength),
							zap.Error(err2))
						return
					}

					logger.Debug("Direct stream completed successfully after refetch",
						zap.Int("messageID", messageID),
						zap.String("filename", freshFile.FileName),
						zap.Int64("bytesStreamed", bytesWritten2))
					return
				}

				logger.Error("Error while copying stream",
					zap.Int("messageID", messageID),
					zap.Int64("bytesWritten", bytesWritten),
					zap.Int64("expectedBytes", contentLength),
					zap.Error(err))
				return
			}

			logger.Debug("Direct stream completed successfully",
				zap.Int("messageID", messageID),
				zap.String("filename", file.FileName),
				zap.Int64("bytesStreamed", bytesWritten))
		}
	}
}
