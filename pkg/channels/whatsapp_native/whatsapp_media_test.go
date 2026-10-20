//go:build whatsapp_native

package whatsapp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestInboundMediaOfAndText(t *testing.T) {
	cases := []struct {
		name    string
		msg     *waE2E.Message
		kind    string
		text    string
		isMedia bool
	}{
		{"text", &waE2E.Message{Conversation: proto.String("hi")}, "", "hi", false},
		{"extended", &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("yo")}}, "", "yo", false},
		{"image caption", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("cap")}}, "image", "cap", true},
		{"sticker", &waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}, "sticker", "", true},
		{"voice note", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: proto.Bool(true)}}, "voice", "", true},
		{"audio file", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{}}, "audio", "", true},
		{"video caption", &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Caption: proto.String("v")}}, "video", "v", true},
		{"document", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("a.pdf"), Caption: proto.String("d")}}, "file", "d", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			im := inboundMediaOf(tc.msg)
			if (im != nil) != tc.isMedia || (im != nil && im.kind != tc.kind) {
				t.Fatalf("media kind = %+v, want %q", im, tc.kind)
			}
			if got := messageText(tc.msg); got != tc.text {
				t.Fatalf("text = %q, want %q", got, tc.text)
			}
		})
	}
}

func TestAnnotation(t *testing.T) {
	im := &inboundMedia{kind: "voice"}
	if got := im.annotation(""); got != "[voice]" {
		t.Fatal(got)
	}
	if got := im.annotation("too large, not downloaded"); got != "[voice: too large, not downloaded]" {
		t.Fatal(got)
	}
	doc := &inboundMedia{kind: "file", filename: "a.pdf"}
	if got := doc.annotation("download failed"); got != "[file: a.pdf, download failed]" {
		t.Fatal(got)
	}
}

func TestTooLarge(t *testing.T) {
	if tooLarge(10, 10) || !tooLarge(11, 10) || tooLarge(1<<40, 0) {
		t.Fatal("tooLarge boundaries wrong")
	}
}

func TestMediaFilenameExt(t *testing.T) {
	im := &inboundMedia{kind: "voice", mime: "audio/ogg; codecs=opus"}
	if got := im.mediaFilename("X"); got != "voice-X.ogg" {
		t.Fatal(got)
	}
	if got := (&inboundMedia{kind: "file", filename: "r.docx"}).mediaFilename("X"); got != "r.docx" {
		t.Fatal(got)
	}
}

func TestOutboundKind(t *testing.T) {
	for _, tc := range []struct{ part, ct, want string }{
		{"image", "", "image"}, {"audio", "", "audio"}, {"video", "video/mp4", "document"},
		{"file", "application/pdf", "document"}, {"", "image/png", "image"}, {"", "audio/mpeg", "audio"}, {"", "", "document"},
	} {
		if got := outboundKind(tc.part, tc.ct); got != tc.want {
			t.Errorf("outboundKind(%q,%q)=%q want %q", tc.part, tc.ct, got, tc.want)
		}
	}
}

func TestOutboundMime(t *testing.T) {
	if outboundMime("x/y", "a.png", nil) != "x/y" {
		t.Fatal("hint ignored")
	}
	if got := outboundMime("", "a.png", nil); got != "image/png" {
		t.Fatal(got)
	}
	if got := outboundMime("", "noext", []byte("%PDF-1.4")); got != "application/pdf" {
		t.Fatal(got)
	}
}

func TestBuildOutboundMessage(t *testing.T) {
	up := whatsmeow.UploadResponse{URL: "u", DirectPath: "p", FileLength: 3}
	if m := buildOutboundMessage("image", up, "image/png", "a.png", "hello"); m.GetImageMessage().GetCaption() != "hello" {
		t.Fatal("image caption")
	}
	m := buildOutboundMessage("audio", up, "audio/ogg", "voice-1.ogg", "")
	if !m.GetAudioMessage().GetPTT() || m.GetAudioMessage().GetMimetype() != "audio/ogg; codecs=opus" {
		t.Fatal("voice note not PTT")
	}
	if buildOutboundMessage("audio", up, "audio/mpeg", "song.mp3", "").GetAudioMessage().GetPTT() {
		t.Fatal("mp3 must not be PTT")
	}
	d := buildOutboundMessage("document", up, "application/pdf", "r.pdf", "c").GetDocumentMessage()
	if d.GetFileName() != "r.pdf" || d.GetMimetype() != "application/pdf" || d.GetCaption() != "c" {
		t.Fatal("document fields")
	}
}

func TestContextInfoOfAudioSticker(t *testing.T) {
	ci := &waE2E.ContextInfo{Participant: proto.String("x")}
	if contextInfoOf(&waE2E.Message{AudioMessage: &waE2E.AudioMessage{ContextInfo: ci}}) != ci ||
		contextInfoOf(&waE2E.Message{StickerMessage: &waE2E.StickerMessage{ContextInfo: ci}}) != ci {
		t.Fatal("audio/sticker context info not found")
	}
}

func TestRunMediaTaskBoundsConcurrency(t *testing.T) {
	c := &WhatsAppNativeChannel{mediaSem: make(chan struct{}, maxConcurrentMedia)}
	c.runCtx, c.runCancel = context.WithCancel(context.Background())
	defer c.runCancel()
	var cur, peak atomic.Int32
	for i := 0; i < 6; i++ {
		c.runMediaTask(func() {
			n := cur.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			cur.Add(-1)
		})
	}
	c.wg.Wait()
	if peak.Load() != maxConcurrentMedia {
		t.Fatalf("peak concurrency = %d, want %d", peak.Load(), maxConcurrentMedia)
	}
}

func TestRunMediaTaskSkippedWhenStopping(t *testing.T) {
	c := &WhatsAppNativeChannel{mediaSem: make(chan struct{}, 1)}
	c.stopping.Store(true)
	ran := false
	c.runMediaTask(func() { ran = true })
	c.wg.Wait()
	if ran {
		t.Fatal("task ran while stopping")
	}
}
