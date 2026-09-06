package voice

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/media"
)

// Resolver resolves queries or Telegram messages into playable Source objects.
type Resolver struct {
	mediaSvc *media.Service
	dlReg    *download.Registry
}

// NewResolver creates a new Resolver instance.
func NewResolver(mediaSvc *media.Service, dlReg *download.Registry) *Resolver {
	return &Resolver{
		mediaSvc: mediaSvc,
		dlReg:    dlReg,
	}
}

// ResolveInput resolves user argument strings and/or replied messages into a Source.
func (r *Resolver) ResolveInput(ctx context.Context, cmdCtx *core.Context, rawQuery string) (*Source, error) {
	// 1. Check if user replied to an audio or video message
	if cmdCtx != nil {
		reply, err := cmdCtx.GetReply()
		if err == nil && reply != nil && reply.Media != nil {
			if src, ok := r.resolveCoreMedia(reply, cmdCtx.ChatID()); ok {
				src.RequesterID = cmdCtx.SenderID()
				if rawQuery != "" {
					src.Title = rawQuery // user override
				}
				return src, nil
			}
		}
	}

	query := strings.TrimSpace(rawQuery)
	if query == "" {
		return nil, fmt.Errorf("no media or query provided (reply to audio/video or pass a link/title)")
	}

	// 2. Check if query is an HTTP/HTTPS URL
	if strings.HasPrefix(query, "http://") || strings.HasPrefix(query, "https://") {
		return r.resolveURL(ctx, query, cmdCtx)
	}

	// 3. Search or title query
	src := &Source{
		ID:        fmt.Sprintf("search-%d", time.Now().UnixNano()),
		Title:     query,
		Artist:    "Unknown",
		Duration:  0,
		SourceURL: query,
		Type:      SourceAudio,
	}
	if cmdCtx != nil {
		src.ChatID = cmdCtx.ChatID()
		src.RequesterID = cmdCtx.SenderID()
	}

	return src, nil
}

func (r *Resolver) resolveURL(ctx context.Context, rawURL string, cmdCtx *core.Context) (*Source, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	filename := path.Base(parsed.Path)
	if filename == "" || filename == "." || filename == "/" {
		filename = "Online Stream"
	}

	srcType := SourceAudio
	lowerPath := strings.ToLower(parsed.Path)
	if strings.HasSuffix(lowerPath, ".mp4") || strings.HasSuffix(lowerPath, ".mkv") || strings.HasSuffix(lowerPath, ".webm") {
		srcType = SourceVideo
	}

	src := &Source{
		ID:        rawURL,
		Title:     filename,
		Artist:    parsed.Host,
		Duration:  0,
		SourceURL: rawURL,
		Type:      srcType,
	}

	if cmdCtx != nil {
		src.ChatID = cmdCtx.ChatID()
		src.RequesterID = cmdCtx.SenderID()
	}

	return src, nil
}

func (r *Resolver) resolveCoreMedia(msg *core.Message, chatID int64) (*Source, bool) {
	if msg == nil || msg.Media == nil {
		return nil, false
	}

	m := msg.Media
	srcType := SourceAudio
	if m.Type == "video" {
		srcType = SourceVideo
	} else if m.Type != "audio" && m.Type != "voice" && m.Type != "document" {
		return nil, false
	}

	title := m.FileName
	if title == "" {
		if m.Type == "voice" {
			title = "Voice Message"
		} else if srcType == SourceVideo {
			title = "Video Clip"
		} else {
			title = "Audio Track"
		}
	}

	return &Source{
		ID:          fmt.Sprintf("msg-%d", msg.ID),
		Title:       title,
		Artist:      "Telegram",
		Duration:    time.Duration(m.Duration) * time.Second,
		SourceURL:   fmt.Sprintf("tg://msg/%d", msg.ID),
		Type:        srcType,
		ChatID:      chatID,
		RequesterID: msg.SenderID,
	}, true
}
