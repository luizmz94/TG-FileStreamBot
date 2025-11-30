package routes

import (
	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/bot"
	"EverythingSuckz/fsb/internal/utils"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/celestix/gotgproto"
	"github.com/gin-gonic/gin"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

const (
	messageBufferSize = 20
	thumbCacheTTL     = 3600 // 1 hour
)

type ThumbnailFetcher struct {
	client        *gotgproto.Client
	logger        *zap.Logger
	messageBuffer *sync.Map
	bufferOrder   []int
	bufferMutex   sync.Mutex
	thumbDir      string
	entity        tg.InputChannelClass
	entityMutex   sync.Mutex
}

func NewThumbnailFetcher(client *gotgproto.Client, logger *zap.Logger, thumbDir string) *ThumbnailFetcher {
	// Create thumb directory if it doesn't exist
	if err := os.MkdirAll(thumbDir, 0755); err != nil {
		logger.Error("Failed to create thumb directory", zap.Error(err))
	}

	return &ThumbnailFetcher{
		client:        client,
		logger:        logger,
		messageBuffer: &sync.Map{},
		bufferOrder:   make([]int, 0, messageBufferSize),
		thumbDir:      thumbDir,
	}
}

func (tf *ThumbnailFetcher) resolveMessage(ctx context.Context, messageID int) (*tg.Message, error) {
	// Check buffer first
	if msg, ok := tf.messageBuffer.Load(messageID); ok {
		tf.logger.Debug("Message found in buffer", zap.Int("messageID", messageID))
		return msg.(*tg.Message), nil
	}

	// Get entity (channel peer)
	tf.entityMutex.Lock()
	if tf.entity == nil {
		// Use MEDIA_CHANNEL_ID
		channelID := config.ValueOf.MediaChannelID
		if channelID == 0 {
			tf.entityMutex.Unlock()
			return nil, fmt.Errorf("MEDIA_CHANNEL_ID not configured")
		}

		channel, err := utils.GetChannelPeer(ctx, tf.client.API(), tf.client.PeerStorage, channelID)
		if err != nil {
			tf.entityMutex.Unlock()
			return nil, fmt.Errorf("failed to get channel peer: %w", err)
		}
		tf.entity = channel
	}
	entity := tf.entity
	tf.entityMutex.Unlock()

	// Get message from channel
	inputMessageID := tg.InputMessageClass(&tg.InputMessageID{ID: messageID})
	messageRequest := tg.ChannelsGetMessagesRequest{Channel: entity, ID: []tg.InputMessageClass{inputMessageID}}
	res, err := tf.client.API().ChannelsGetMessages(ctx, &messageRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to get message from channel: %w", err)
	}

	messages := res.(*tg.MessagesChannelMessages)
	if len(messages.Messages) == 0 {
		return nil, fmt.Errorf("message not found in channel")
	}

	message, ok := messages.Messages[0].(*tg.Message)
	if !ok || message.Media == nil {
		return nil, fmt.Errorf("message was deleted or has no media")
	}

	// Add to buffer
	tf.addToBuffer(messageID, message)
	return message, nil
}

func (tf *ThumbnailFetcher) addToBuffer(messageID int, msg *tg.Message) {
	tf.bufferMutex.Lock()
	defer tf.bufferMutex.Unlock()

	// Add to buffer
	tf.messageBuffer.Store(messageID, msg)
	tf.bufferOrder = append(tf.bufferOrder, messageID)

	// Remove oldest if buffer is full
	if len(tf.bufferOrder) > messageBufferSize {
		oldest := tf.bufferOrder[0]
		tf.messageBuffer.Delete(oldest)
		tf.bufferOrder = tf.bufferOrder[1:]
	}
}

func (tf *ThumbnailFetcher) getThumbnail(ctx context.Context, messageID int) (string, error) {
	thumbFile := filepath.Join(tf.thumbDir, fmt.Sprintf("%d.jpg", messageID))

	// Check if thumbnail already exists
	if _, err := os.Stat(thumbFile); err == nil {
		tf.logger.Debug("Thumbnail file exists", zap.String("file", thumbFile))
		return thumbFile, nil
	}

	// Get message
	msg, err := tf.resolveMessage(ctx, messageID)
	if err != nil {
		return "", err
	}

	// Check if media is a video document
	media := msg.Media
	var document *tg.Document

	switch m := media.(type) {
	case *tg.MessageMediaDocument:
		doc, ok := m.Document.AsNotEmpty()
		if !ok {
			return "", fmt.Errorf("unsupported media type for thumbnail")
		}
		document = doc

		// Verify it's a video
		if document.MimeType == "" || (document.MimeType[:5] != "video" && document.MimeType[:5] != "image") {
			return "", fmt.Errorf("unsupported media type for thumbnail: %s", document.MimeType)
		}
	default:
		return "", fmt.Errorf("unsupported media type for thumbnail")
	}

	// Check if document has thumbs
	if len(document.Thumbs) == 0 {
		return "", fmt.Errorf("no thumbnail found in Telegram")
	}

	// Get the largest thumbnail (use the last one, which is usually the largest)
	largestThumb := document.Thumbs[len(document.Thumbs)-1]

	// Verify it's a valid thumbnail
	if _, ok := largestThumb.AsNotEmpty(); !ok {
		return "", fmt.Errorf("no valid thumbnail found")
	}

	// Get the type string from the thumbnail
	thumbSize, ok := largestThumb.AsNotEmpty()
	if !ok {
		return "", fmt.Errorf("failed to get thumbnail type")
	}

	// Download thumbnail
	location := &tg.InputDocumentFileLocation{
		ID:            document.ID,
		AccessHash:    document.AccessHash,
		FileReference: document.FileReference,
		ThumbSize:     thumbSize.GetType(),
	}

	// Create temp file
	tempFile := thumbFile + ".tmp"
	f, err := os.Create(tempFile)
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	defer f.Close()

	// Download using Telegram API
	offset := int64(0)
	limit := 1024 * 1024 // 1MB chunks

	for {
		res, err := tf.client.API().UploadGetFile(ctx, &tg.UploadGetFileRequest{
			Location: location,
			Offset:   offset,
			Limit:    limit,
		})
		if err != nil {
			os.Remove(tempFile)
			return "", fmt.Errorf("failed to download thumbnail: %w", err)
		}

		file, ok := res.(*tg.UploadFile)
		if !ok {
			os.Remove(tempFile)
			return "", fmt.Errorf("unexpected upload response type")
		}

		bytes := file.GetBytes()
		if len(bytes) == 0 {
			break
		}

		if _, err := f.Write(bytes); err != nil {
			os.Remove(tempFile)
			return "", fmt.Errorf("failed to write thumbnail: %w", err)
		}

		if len(bytes) < limit {
			break
		}

		offset += int64(len(bytes))
	}

	f.Close()

	// Rename temp file to final file
	if err := os.Rename(tempFile, thumbFile); err != nil {
		os.Remove(tempFile)
		return "", fmt.Errorf("failed to rename temp file: %w", err)
	}

	tf.logger.Info("✅ Thumbnail saved", zap.String("file", thumbFile))
	return thumbFile, nil
}

// Global thumbnail fetcher instance
var thumbnailFetcher *ThumbnailFetcher
var thumbnailFetcherOnce sync.Once

func getThumbnailFetcher(logger *zap.Logger) *ThumbnailFetcher {
	thumbnailFetcherOnce.Do(func() {
		// Use the default bot worker
		worker := bot.GetNextWorker()
		thumbDir := "./thumbnails"

		// Check if THUMB_DIR is configured in environment
		if envThumbDir := os.Getenv("THUMB_DIR"); envThumbDir != "" {
			thumbDir = envThumbDir
		}

		thumbnailFetcher = NewThumbnailFetcher(worker.Client, logger, thumbDir)
	})
	return thumbnailFetcher
}

func (e *allRoutes) LoadThumb(r *Route) {
	thumbLog := e.log.Named("Thumb")
	defer thumbLog.Info("Loaded thumbnail route")
	r.Engine.GET("/thumb/:messageID", getThumbnailRoute(thumbLog))
}

func getThumbnailRoute(logger *zap.Logger) gin.HandlerFunc {
	return func(ctx *gin.Context) {
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

		logger.Info("Thumbnail request",
			zap.Int("messageID", messageID),
			zap.Int64("channelID", config.ValueOf.MediaChannelID))

		// Get thumbnail
		fetcher := getThumbnailFetcher(logger)
		thumbFile, err := fetcher.getThumbnail(ctx, messageID)
		if err != nil {
			logger.Error("Failed to fetch thumbnail", zap.Int("messageID", messageID), zap.Error(err))

			// Return appropriate status code based on error
			if err.Error() == "media not found" || err.Error() == "message not found" {
				ctx.JSON(http.StatusNotFound, gin.H{
					"error": "media not found",
				})
			} else if err.Error() == "no thumbnail found in Telegram" {
				ctx.JSON(http.StatusNotFound, gin.H{
					"error": "no thumbnail found in Telegram",
				})
			} else {
				ctx.JSON(http.StatusInternalServerError, gin.H{
					"error": fmt.Sprintf("internal error: %v", err),
				})
			}
			return
		}

		// Serve the thumbnail file
		ctx.File(thumbFile)
	}
}
