package routes

import (
	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/bot"
	"EverythingSuckz/fsb/internal/utils"
	"fmt"
	"io"
	"net/http"
	"strconv"

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

		logger.Info("Direct stream request", 
			zap.Int("messageID", messageID), 
			zap.Int64("channelID", config.ValueOf.MediaChannelID))

		// Get a worker to handle the request
		worker := bot.GetNextWorker()

		// Fetch file from the configured media channel
		file, err := utils.FileFromMessageAndChannel(ctx, worker.Client, config.ValueOf.MediaChannelID, messageID)
		if err != nil {
			logger.Error("Failed to get file from channel", 
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
			res, err := worker.Client.API().UploadGetFile(ctx, &tg.UploadGetFileRequest{
				Location: file.Location,
				Offset:   0,
				Limit:    1024 * 1024,
			})
			if err != nil {
				logger.Error("Failed to get photo file", zap.Error(err))
				ctx.JSON(http.StatusInternalServerError, gin.H{
					"error": "failed to get photo file",
				})
				return
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
			logger.Info("Content-Range", 
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
			lr, _ := utils.NewTelegramReader(ctx, worker.Client, file.Location, start, end, contentLength)
			if _, err := io.CopyN(w, lr, contentLength); err != nil {
				logger.Error("Error while copying stream", zap.Error(err))
			}
		}

		logger.Info("Direct stream completed successfully", 
			zap.Int("messageID", messageID), 
			zap.String("filename", file.FileName))
	}
}
