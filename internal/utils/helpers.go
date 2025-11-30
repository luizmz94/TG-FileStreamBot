package utils

import (
	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/cache"
	"EverythingSuckz/fsb/internal/types"
	"context"
	"errors"
	"fmt"
	"math/rand"

	"github.com/celestix/gotgproto"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/storage"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

// https://stackoverflow.com/a/70802740/15807350
func Contains[T comparable](s []T, e T) bool {
	for _, v := range s {
		if v == e {
			return true
		}
	}
	return false
}

func GetTGMessage(ctx context.Context, client *gotgproto.Client, messageID int) (*tg.Message, error) {
	inputMessageID := tg.InputMessageClass(&tg.InputMessageID{ID: messageID})
	channel, err := GetLogChannelPeer(ctx, client.API(), client.PeerStorage)
	if err != nil {
		return nil, err
	}
	messageRequest := tg.ChannelsGetMessagesRequest{Channel: channel, ID: []tg.InputMessageClass{inputMessageID}}
	res, err := client.API().ChannelsGetMessages(ctx, &messageRequest)
	if err != nil {
		return nil, err
	}
	messages := res.(*tg.MessagesChannelMessages)
	message := messages.Messages[0]
	if _, ok := message.(*tg.Message); ok {
		return message.(*tg.Message), nil
	} else {
		return nil, fmt.Errorf("this file was deleted")
	}
}

func FileFromMedia(media tg.MessageMediaClass) (*types.File, error) {
	switch media := media.(type) {
	case *tg.MessageMediaDocument:
		document, ok := media.Document.AsNotEmpty()
		if !ok {
			return nil, fmt.Errorf("unexpected type %T", media)
		}
		var fileName string
		for _, attribute := range document.Attributes {
			if name, ok := attribute.(*tg.DocumentAttributeFilename); ok {
				fileName = name.FileName
				break
			}
		}
		return &types.File{
			Location: document.AsInputDocumentFileLocation(),
			FileSize: document.Size,
			FileName: fileName,
			MimeType: document.MimeType,
			ID:       document.ID,
		}, nil
	case *tg.MessageMediaPhoto:
		photo, ok := media.Photo.AsNotEmpty()
		if !ok {
			return nil, fmt.Errorf("unexpected type %T", media)
		}
		sizes := photo.Sizes
		if len(sizes) == 0 {
			return nil, errors.New("photo has no sizes")
		}
		photoSize := sizes[len(sizes)-1]
		size, ok := photoSize.AsNotEmpty()
		if !ok {
			return nil, errors.New("photo size is empty")
		}
		location := new(tg.InputPhotoFileLocation)
		location.ID = photo.GetID()
		location.AccessHash = photo.GetAccessHash()
		location.FileReference = photo.GetFileReference()
		location.ThumbSize = size.GetType()
		return &types.File{
			Location: location,
			FileSize: 0, // caller should judge if this is a photo or not
			FileName: fmt.Sprintf("photo_%d.jpg", photo.GetID()),
			MimeType: "image/jpeg",
			ID:       photo.GetID(),
		}, nil
	}
	return nil, fmt.Errorf("unexpected type %T", media)
}

func FileFromMessage(ctx context.Context, client *gotgproto.Client, messageID int) (*types.File, error) {
	key := fmt.Sprintf("file:%d:%d", messageID, client.Self.ID)
	log := Logger.Named("GetMessageMedia")
	var cachedMedia types.File
	err := cache.GetCache().Get(key, &cachedMedia)
	if err == nil {
		log.Debug("Using cached media message properties", zap.Int("messageID", messageID), zap.Int64("clientID", client.Self.ID))
		return &cachedMedia, nil
	}
	log.Debug("Fetching file properties from message ID", zap.Int("messageID", messageID), zap.Int64("clientID", client.Self.ID))
	message, err := GetTGMessage(ctx, client, messageID)
	if err != nil {
		return nil, err
	}
	file, err := FileFromMedia(message.Media)
	if err != nil {
		return nil, err
	}
	err = cache.GetCache().Set(
		key,
		file,
		3600,
	)
	if err != nil {
		log.Warn("Failed to cache file metadata (continuing without cache)", zap.Error(err))
	}
	return file, nil
}

// FileFromMessageAndChannel fetches a file from a specific channel and message ID
// This function is designed for direct streaming without using the internal hash/DB system
// It retrieves the message from the specified channel and extracts the file information
//
// Caching strategy:
// - File metadata: cached for 1 hour (3600s) to avoid repeated message fetches
// - Channel info: cached in PeerStorage for session lifetime (no expiry)
// - On cache hit, no Telegram API calls are made
func FileFromMessageAndChannel(ctx context.Context, client *gotgproto.Client, channelID int64, messageID int) (*types.File, error) {
	key := fmt.Sprintf("file:%d:%d:%d", channelID, messageID, client.Self.ID)
	log := Logger.Named("GetMessageMediaFromChannel")

	// Debug: log cache key and attempt
	log.Info("Attempting cache lookup",
		zap.String("cacheKey", key),
		zap.Int64("channelID", channelID),
		zap.Int("messageID", messageID),
		zap.Int64("clientID", client.Self.ID))

	var cachedMedia types.File
	err := cache.GetCache().Get(key, &cachedMedia)
	if err == nil {
		log.Info("✅ CACHE HIT - Using cached file metadata (no API call)",
			zap.String("cacheKey", key),
			zap.String("fileName", cachedMedia.FileName))
		return &cachedMedia, nil
	}
	log.Info("❌ CACHE MISS - fetching from Telegram API",
		zap.String("cacheKey", key),
		zap.Error(err))

	inputMessageID := tg.InputMessageClass(&tg.InputMessageID{ID: messageID})
	channel, err := GetChannelPeer(ctx, client.API(), client.PeerStorage, channelID)
	if err != nil {
		return nil, fmt.Errorf("failed to get channel peer: %w", err)
	}

	messageRequest := tg.ChannelsGetMessagesRequest{Channel: channel, ID: []tg.InputMessageClass{inputMessageID}}
	res, err := client.API().ChannelsGetMessages(ctx, &messageRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to get message from channel: %w", err)
	}

	messages := res.(*tg.MessagesChannelMessages)
	if len(messages.Messages) == 0 {
		return nil, fmt.Errorf("message not found in channel")
	}

	message, ok := messages.Messages[0].(*tg.Message)
	if !ok {
		return nil, fmt.Errorf("message was deleted or is not accessible")
	}

	file, err := FileFromMedia(message.Media)
	if err != nil {
		return nil, fmt.Errorf("failed to extract file from message: %w", err)
	}

	// Cache the file metadata for 1 hour
	log.Info("💾 Storing file in cache",
		zap.String("cacheKey", key),
		zap.String("fileName", file.FileName),
		zap.Int("ttl", 3600))
	err = cache.GetCache().Set(
		key,
		file,
		3600,
	)
	if err != nil {
		// Log warning but don't fail the request if caching fails
		log.Warn("Failed to cache file metadata (continuing without cache)", zap.Error(err))
	}
	return file, nil
}

func GetLogChannelPeer(ctx context.Context, api *tg.Client, peerStorage *storage.PeerStorage) (*tg.InputChannel, error) {
	return GetChannelPeer(ctx, api, peerStorage, config.ValueOf.LogChannelID)
}

// GetChannelPeer gets an InputChannel for any given channel ID
// This is a generic version of GetLogChannelPeer that works with any channel
// Uses PeerStorage as an in-memory cache to avoid repeated API calls
func GetChannelPeer(ctx context.Context, api *tg.Client, peerStorage *storage.PeerStorage, channelID int64) (*tg.InputChannel, error) {
	// Check PeerStorage first (acts as in-memory cache)
	// Once a channel is accessed, it stays in PeerStorage for the session lifetime
	cachedInputPeer := peerStorage.GetInputPeerById(channelID)

	switch peer := cachedInputPeer.(type) {
	case *tg.InputPeerEmpty:
		// Not cached, need to fetch from Telegram API
		break
	case *tg.InputPeerChannel:
		// Cache hit! Return without making API call
		return &tg.InputChannel{
			ChannelID:  peer.ChannelID,
			AccessHash: peer.AccessHash,
		}, nil
	default:
		return nil, errors.New("unexpected type of input peer")
	}

	// Cache miss - fetch from Telegram API
	inputChannel := &tg.InputChannel{
		ChannelID: channelID,
	}
	channels, err := api.ChannelsGetChannels(ctx, []tg.InputChannelClass{inputChannel})
	if err != nil {
		return nil, err
	}
	if len(channels.GetChats()) == 0 {
		return nil, errors.New("no channels found")
	}
	channel, ok := channels.GetChats()[0].(*tg.Channel)
	if !ok {
		return nil, errors.New("type assertion to *tg.Channel failed")
	}

	// Add to PeerStorage cache for future requests (persists for session lifetime)
	peerStorage.AddPeer(channel.GetID(), channel.AccessHash, storage.TypeChannel, "")
	return channel.AsInput(), nil
}

func ForwardMessages(ctx *ext.Context, fromChatId, toChatId int64, messageID int) (*tg.Updates, error) {
	fromPeer := ctx.PeerStorage.GetInputPeerById(fromChatId)
	if fromPeer.Zero() {
		return nil, fmt.Errorf("fromChatId: %d is not a valid peer", fromChatId)
	}
	toPeer, err := GetLogChannelPeer(ctx, ctx.Raw, ctx.PeerStorage)
	if err != nil {
		return nil, err
	}
	update, err := ctx.Raw.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{
		RandomID: []int64{rand.Int63()},
		FromPeer: fromPeer,
		ID:       []int{messageID},
		ToPeer:   &tg.InputPeerChannel{ChannelID: toPeer.ChannelID, AccessHash: toPeer.AccessHash},
	})
	if err != nil {
		return nil, err
	}
	return update.(*tg.Updates), nil
}
