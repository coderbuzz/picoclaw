//go:build whatsapp_native

package whatsapp

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
)

const (
	defaultMaxMediaBytes = 20 << 20
	mediaTransferTimeout = 90 * time.Second
	maxConcurrentMedia   = 2
)

// inboundMedia describes the downloadable attachment of an incoming message.
type inboundMedia struct {
	dl       whatsmeow.DownloadableMessage
	kind     string // image | sticker | voice | audio | video | file
	mime     string
	filename string
	caption  string
	size     uint64
}

// inboundMediaOf returns the attachment of m, or nil for plain-text messages.
func inboundMediaOf(m *waE2E.Message) *inboundMedia {
	switch {
	case m.GetImageMessage() != nil:
		x := m.GetImageMessage()
		return &inboundMedia{x, "image", x.GetMimetype(), "", x.GetCaption(), x.GetFileLength()}
	case m.GetStickerMessage() != nil:
		x := m.GetStickerMessage()
		return &inboundMedia{x, "sticker", x.GetMimetype(), "", "", x.GetFileLength()}
	case m.GetAudioMessage() != nil:
		x := m.GetAudioMessage()
		kind := "audio"
		if x.GetPTT() {
			kind = "voice"
		}
		return &inboundMedia{x, kind, x.GetMimetype(), "", "", x.GetFileLength()}
	case m.GetVideoMessage() != nil:
		x := m.GetVideoMessage()
		return &inboundMedia{x, "video", x.GetMimetype(), "", x.GetCaption(), x.GetFileLength()}
	case m.GetDocumentMessage() != nil:
		x := m.GetDocumentMessage()
		return &inboundMedia{x, "file", x.GetMimetype(), x.GetFileName(), x.GetCaption(), x.GetFileLength()}
	}
	return nil
}

// messageText returns the typed text, or the caption of an attachment.
func messageText(m *waE2E.Message) string {
	if t := m.GetConversation(); t != "" {
		return t
	}
	if t := m.GetExtendedTextMessage().GetText(); t != "" {
		return t
	}
	if im := inboundMediaOf(m); im != nil {
		return im.caption
	}
	return ""
}

func tooLarge(size uint64, limit int64) bool {
	return limit > 0 && size > uint64(limit)
}

// annotation is the placeholder appended to the text; "[voice]"/"[audio]" are what the agent's
// transcription step replaces with the transcript. note is "" when the file was stored.
func (im *inboundMedia) annotation(note string) string {
	label := im.kind
	if im.kind == "file" && im.filename != "" {
		label += ": " + im.filename
	}
	if note != "" {
		if label == im.kind {
			return "[" + label + ": " + note + "]"
		}
		return "[" + label + ", " + note + "]"
	}
	return "[" + label + "]"
}

// mediaFilename gives the stored file a name whose extension the agent can classify (audio, image).
func (im *inboundMedia) mediaFilename(id string) string {
	if im.filename != "" {
		return im.filename
	}
	return im.kind + "-" + id + extForMime(im.mime)
}

var mimeExt = map[string]string{
	"audio/ogg": ".ogg", "audio/mpeg": ".mp3", "audio/mp4": ".m4a", "audio/aac": ".aac", "audio/amr": ".amr",
	"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif",
	"video/mp4": ".mp4", "application/pdf": ".pdf",
}

func extForMime(m string) string {
	base, _, err := mime.ParseMediaType(m)
	if err != nil {
		base = strings.ToLower(strings.TrimSpace(m))
	}
	return mimeExt[base]
}

// outboundKind picks the WhatsApp message type for a media part: image, audio, or document.
func outboundKind(partType, contentType string) string {
	switch {
	case partType == "image" || (partType == "" && strings.HasPrefix(contentType, "image/")):
		return "image"
	case partType == "audio" || (partType == "" && strings.HasPrefix(contentType, "audio/")):
		return "audio"
	}
	return "document"
}

// outboundMime resolves the MIME type: explicit hint, then filename extension, then content sniffing.
func outboundMime(hint, filename string, data []byte) string {
	if hint != "" {
		return hint
	}
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename))); t != "" {
		return t
	}
	return http.DetectContentType(data)
}

// isVoiceNote mirrors Telegram: ogg/opus files named "voice*" are sent as push-to-talk bubbles.
func isVoiceNote(filename string) bool {
	fn := strings.ToLower(filename)
	return strings.Contains(fn, "voice") && (strings.HasSuffix(fn, ".ogg") || strings.HasSuffix(fn, ".oga"))
}

// buildOutboundMessage wraps an uploaded file in the message type chosen by outboundKind.
func buildOutboundMessage(kind string, up whatsmeow.UploadResponse, mimetype, filename, caption string) *waE2E.Message {
	url, path := proto.String(up.URL), proto.String(up.DirectPath)
	switch kind {
	case "image":
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			URL: url, DirectPath: path, MediaKey: up.MediaKey, Mimetype: proto.String(mimetype),
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			Caption: proto.String(caption),
		}}
	case "audio":
		ptt := isVoiceNote(filename)
		if ptt {
			mimetype = "audio/ogg; codecs=opus"
		}
		return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
			URL: url, DirectPath: path, MediaKey: up.MediaKey, Mimetype: proto.String(mimetype),
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			PTT: proto.Bool(ptt),
		}}
	}
	if filename == "" {
		filename = "file"
	}
	return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
		URL: url, DirectPath: path, MediaKey: up.MediaKey, Mimetype: proto.String(mimetype),
		FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		FileName: proto.String(filename), Title: proto.String(filename), Caption: proto.String(caption),
	}}
}

func waMediaType(kind string) whatsmeow.MediaType {
	switch kind {
	case "image":
		return whatsmeow.MediaImage
	case "audio":
		return whatsmeow.MediaAudio
	}
	return whatsmeow.MediaDocument
}

// runMediaTask runs fn in a goroutine tracked by c.wg (same stopping protocol as reconnect),
// at most maxConcurrentMedia at a time.
func (c *WhatsAppNativeChannel) runMediaTask(fn func()) {
	c.reconnectMu.Lock()
	if c.stopping.Load() {
		c.reconnectMu.Unlock()
		return
	}
	c.wg.Add(1)
	c.reconnectMu.Unlock()
	go func() {
		defer c.wg.Done()
		select {
		case c.mediaSem <- struct{}{}:
			defer func() { <-c.mediaSem }()
			fn()
		case <-c.runCtx.Done():
		}
	}()
}

func (c *WhatsAppNativeChannel) maxMediaBytes() int64 {
	if c.config.MaxMediaBytes > 0 {
		return c.config.MaxMediaBytes
	}
	return defaultMaxMediaBytes
}

// fetchInboundMedia downloads im into the media store and returns the ref plus a note for the
// annotation ("" on success). Errors are logged without message content or sender.
func (c *WhatsAppNativeChannel) fetchInboundMedia(client *whatsmeow.Client, im *inboundMedia, scope, id string) (string, string) {
	limit := c.maxMediaBytes()
	if tooLarge(im.size, limit) {
		return "", "too large, not downloaded"
	}
	ctx, cancel := context.WithTimeout(c.runCtx, mediaTransferTimeout)
	defer cancel()
	data, err := client.Download(ctx, im.dl)
	if err != nil {
		logger.WarnCF("whatsapp", "Media download failed", map[string]any{"kind": im.kind, "error": err.Error()})
		return "", "download failed"
	}
	if tooLarge(uint64(len(data)), limit) {
		return "", "too large, not downloaded"
	}
	store := c.GetMediaStore()
	if store == nil {
		return "", "download failed"
	}
	dir := media.TempDir()
	if err = os.MkdirAll(dir, 0o700); err != nil {
		logger.WarnCF("whatsapp", "Media dir failed", map[string]any{"error": err.Error()})
		return "", "download failed"
	}
	f, err := os.CreateTemp(dir, "wa-*"+filepath.Ext(im.mediaFilename(id)))
	if err == nil {
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		logger.WarnCF("whatsapp", "Media save failed", map[string]any{"error": err.Error()})
		return "", "download failed"
	}
	ref, err := store.Store(f.Name(), media.MediaMeta{
		Filename:      im.mediaFilename(id),
		ContentType:   im.mime,
		Source:        "whatsapp",
		CleanupPolicy: media.CleanupPolicyDeleteOnCleanup,
	}, scope)
	if err != nil {
		_ = os.Remove(f.Name())
		logger.WarnCF("whatsapp", "Media store failed", map[string]any{"error": err.Error()})
		return "", "download failed"
	}
	return ref, ""
}

// SendMedia implements channels.MediaSender.
func (c *WhatsAppNativeChannel) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil || !client.IsConnected() || client.Store.ID == nil {
		return nil, fmt.Errorf("whatsapp connection not established: %w", channels.ErrTemporary)
	}
	store := c.GetMediaStore()
	if store == nil {
		return nil, fmt.Errorf("no media store available: %w", channels.ErrSendFailed)
	}
	to, err := parseJID(msg.ChatID)
	if err != nil {
		return nil, fmt.Errorf("invalid chat id: %w", channels.ErrSendFailed)
	}

	var ids []string
	failed := false
	for _, part := range msg.Parts {
		path, meta, err := store.ResolveWithMeta(part.Ref)
		if err != nil {
			logger.ErrorCF("whatsapp", "Failed to resolve media ref", map[string]any{"ref": part.Ref, "error": err.Error()})
			continue
		}
		// Stat first so an oversized file never reaches RAM.
		if fi, serr := os.Stat(path); serr != nil || tooLarge(uint64(fi.Size()), c.maxMediaBytes()) {
			logger.ErrorCF("whatsapp", "Media file missing or over max_media_bytes, skipped", nil)
			failed = true
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			logger.ErrorCF("whatsapp", "Failed to read media file", map[string]any{"error": err.Error()})
			continue
		}
		filename := part.Filename
		if filename == "" {
			filename = meta.Filename
		}
		if filename == "" {
			filename = filepath.Base(path)
		}
		contentType := part.ContentType
		if contentType == "" {
			contentType = meta.ContentType
		}
		mimetype := outboundMime(contentType, filename, data)
		kind := outboundKind(part.Type, mimetype)

		upCtx, cancel := context.WithTimeout(ctx, mediaTransferTimeout)
		up, err := client.Upload(upCtx, data, waMediaType(kind))
		if err != nil {
			cancel()
			logger.ErrorCF("whatsapp", "Media upload failed", map[string]any{"kind": kind, "error": err.Error()})
			return ids, fmt.Errorf("whatsapp upload media: %w", channels.ErrTemporary)
		}
		resp, err := client.SendMessage(upCtx, to, buildOutboundMessage(kind, up, mimetype, filename, part.Caption))
		cancel()
		if err != nil {
			logger.ErrorCF("whatsapp", "Media send failed", map[string]any{"kind": kind, "error": err.Error()})
			return ids, fmt.Errorf("whatsapp send media: %w", channels.ErrTemporary)
		}
		ids = append(ids, resp.ID)
	}
	if failed {
		return ids, fmt.Errorf("whatsapp send media: part skipped: %w", channels.ErrSendFailed)
	}
	return ids, nil
}
