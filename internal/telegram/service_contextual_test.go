package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

func TestContextualReplyToPreservesForumRoot(t *testing.T) {
	reply, ok := contextualReplyTo(core.MessageSendContext{ReplyToID: 900, TopicID: 100}).(*tg.InputReplyToMessage)
	if !ok || reply.ReplyToMsgID != 900 || reply.TopMsgID != 100 {
		t.Fatalf("reply=%T %+v, want reply=900 top=100", reply, reply)
	}
}

func TestContextualReplyToFallsBackToTopicRoot(t *testing.T) {
	reply, ok := contextualReplyTo(core.MessageSendContext{TopicID: 100}).(*tg.InputReplyToMessage)
	if !ok || reply.ReplyToMsgID != 100 || reply.TopMsgID != 0 {
		t.Fatalf("reply=%T %+v, want reply=100 top=0", reply, reply)
	}
	if got := contextualReplyTo(core.MessageSendContext{}); got != nil {
		t.Fatalf("empty context reply=%T %+v, want nil", got, got)
	}
}

func TestContextualUploadedMediaRetainsMediaSemantics(t *testing.T) {
	file := &tg.InputFile{ID: 1, Parts: 1, Name: "asset", MD5Checksum: ""}

	if _, ok := contextualUploadedMedia("photo", file).(*tg.InputMediaUploadedPhoto); !ok {
		t.Fatalf("photo media=%T", contextualUploadedMedia("photo", file))
	}
	sticker, ok := contextualUploadedMedia("sticker", file).(*tg.InputMediaUploadedDocument)
	if !ok || sticker.MimeType != "image/webp" || len(sticker.Attributes) != 1 {
		t.Fatalf("sticker media=%T %+v", sticker, sticker)
	}
	video, ok := contextualUploadedMedia("video", file).(*tg.InputMediaUploadedDocument)
	if !ok || video.MimeType != "video/mp4" || len(video.Attributes) != 1 {
		t.Fatalf("video media=%T %+v", video, video)
	}
	doc, ok := contextualUploadedMedia("file", file).(*tg.InputMediaUploadedDocument)
	if !ok || !doc.ForceFile {
		t.Fatalf("document media=%T %+v", doc, doc)
	}
}
