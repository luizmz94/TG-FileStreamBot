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
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gotd/td/tg"
	range_parser "github.com/quantumsheep/range-parser"
	"go.uber.org/zap"
)

// LoadDirect registers the direct streaming route
// This route allows streaming files directly from a configured media channel
// using only the message ID, without requiring hash validation or internal database
func (e *allRoutes) LoadDirect(r *Route) {
	directLog := e.log.Named("DirectStream")
	defer directLog.Info("Loaded direct stream route")
	r.Engine.GET("/direct/:messageID", getDirectStreamRoute(directLog))
}

// fetchFileWithRetry attempts to fetch file with timeout and automatic retry using different workers
// Returns file metadata or error after all retry attempts exhausted
func fetchFileWithRetry(bgCtx context.Context, logger *zap.Logger, worker *bot.Worker, messageID int, channelID int64) (*types.File, error) {
	// Create a context with 5 second timeout for the initial attempt
	ctx, cancel := context.WithTimeout(bgCtx, 5*time.Second)
	defer cancel()

	// Channel to receive the result
	type result struct {
		file *types.File
		err  error
	}
	resultChan := make(chan result, 1)

	// Attempt to fetch file in a goroutine
	go func() {
		file, err := utils.FileFromMessageAndChannel(bgCtx, worker.Client, channelID, messageID)
		resultChan <- result{file: file, err: err}
	}()

	// Wait for result or timeout
	select {
	case res := <-resultChan:
		if res.err == nil {
			logger.Debug("File fetched successfully",
				zap.Int("workerID", worker.ID),
				zap.Duration("duration", time.Since(time.Now())))
			return res.file, nil
		}
		// If error is not timeout related, return immediately
		if res.err.Error() == "message not found in channel" ||
			res.err.Error() == "message was deleted or is not accessible" {
			return nil, res.err
		}
		logger.Warn("Worker failed to fetch file, will retry with another worker",
			zap.Int("workerID", worker.ID),
			zap.Error(res.err))

	case <-ctx.Done():
		logger.Warn("Worker timeout (5s), retrying with another worker",
			zap.Int("workerID", worker.ID))
	}

	// First worker failed or timed out, try with a different worker
	excludeWorkers := []int{worker.ID}
	maxRetries := 3

	for retry := 0; retry < maxRetries; retry++ {
		fallbackWorker := bot.GetNextWorkerExcluding(excludeWorkers)
		if fallbackWorker == nil {
			logger.Error("No fallback workers available")
			return nil, fmt.Errorf("all workers exhausted")
		}

		logger.Info("Retrying with fallback worker",
			zap.Int("retry", retry+1),
			zap.Int("fallbackWorkerID", fallbackWorker.ID),
			zap.String("fallbackWorkerUsername", fallbackWorker.Self.Username))

		// Track the fallback worker's request
		retryStartTime := time.Now()
		fallbackWorker.StartRequest()

		// Create new timeout context for retry
		retryCtx, retryCancel := context.WithTimeout(bgCtx, 5*time.Second)

		retryResultChan := make(chan result, 1)
		go func() {
			file, err := utils.FileFromMessageAndChannel(bgCtx, fallbackWorker.Client, channelID, messageID)
			retryResultChan <- result{file: file, err: err}
		}()

		select {
		case res := <-retryResultChan:
			retryCancel()
			fallbackWorker.EndRequest(retryStartTime, res.err != nil)

			if res.err == nil {
				logger.Info("File fetched successfully with fallback worker",
					zap.Int("fallbackWorkerID", fallbackWorker.ID),
					zap.Int("retry", retry+1))
				return res.file, nil
			}

			// If it's a "not found" error, no point in retrying
			if res.err.Error() == "message not found in channel" ||
				res.err.Error() == "message was deleted or is not accessible" {
				return nil, res.err
			}

			logger.Warn("Fallback worker also failed",
				zap.Int("fallbackWorkerID", fallbackWorker.ID),
				zap.Error(res.err))
			excludeWorkers = append(excludeWorkers, fallbackWorker.ID)

		case <-retryCtx.Done():
			retryCancel()
			fallbackWorker.EndRequest(retryStartTime, true)
			logger.Warn("Fallback worker also timed out",
				zap.Int("fallbackWorkerID", fallbackWorker.ID))
			excludeWorkers = append(excludeWorkers, fallbackWorker.ID)
		}
	}

	return nil, fmt.Errorf("failed to fetch file after %d retries", maxRetries)
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

		logger.Debug("Direct stream request",
			zap.Int("messageID", messageID),
			zap.Int64("channelID", config.ValueOf.MediaChannelID))

		// Get a worker to handle the request with intelligent load balancing
		worker := bot.GetNextWorker()
		if worker == nil {
			logger.Error("No workers available")
			ctx.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "no workers available",
			})
			return
		}

		// Track this request
		requestStartTime := time.Now()
		worker.StartRequest()
		defer func() {
			// Check if request failed based on HTTP status
			failed := w.Status() >= 400
			worker.EndRequest(requestStartTime, failed)
		}()

		logger.Debug("Using worker for request",
			zap.Int("workerID", worker.ID),
			zap.String("workerUsername", worker.Self.Username),
			zap.Int32("activeRequests", worker.GetActiveRequests()))

		// Create a background context for Telegram API calls that won't be cancelled
		// when the HTTP client disconnects. This prevents "context canceled" errors
		// during file streaming.
		bgCtx := context.Background()

		// Fetch file with timeout and retry logic
		file, err := fetchFileWithRetry(bgCtx, logger, worker, messageID, config.ValueOf.MediaChannelID)
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

		// Handle photos (which have FileSize 0)
		if file.FileSize == 0 {
			res, err := worker.Client.API().UploadGetFile(bgCtx, &tg.UploadGetFileRequest{
				Location: file.Location,
				Offset:   0,
				Limit:    1024 * 1024,
			})
			if err != nil {
				// Check for FILE_REFERENCE_EXPIRED and retry for photos
				if strings.Contains(err.Error(), "FILE_REFERENCE_EXPIRED") {
					logger.Warn("FILE_REFERENCE_EXPIRED for photo, refetching metadata",
						zap.Int("messageID", messageID))

					freshFile, refetchErr := utils.RefetchFileFromMessageAndChannel(bgCtx, worker.Client, config.ValueOf.MediaChannelID, messageID)
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
					res, err = worker.Client.API().UploadGetFile(bgCtx, &tg.UploadGetFileRequest{
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
			lr, err := utils.NewTelegramReader(bgCtx, worker.Client, file.Location, start, end, contentLength)
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
					freshFile, refetchErr := utils.RefetchFileFromMessageAndChannel(bgCtx, worker.Client, config.ValueOf.MediaChannelID, messageID)
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
					lr2, err2 := utils.NewTelegramReader(bgCtx, worker.Client, freshFile.Location, start, end, contentLength)
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
