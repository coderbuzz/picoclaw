package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/media"
)

func TestInboxMedia_CopiesIntoWorkspace(t *testing.T) {
	store := media.NewFileMediaStore()
	src := filepath.Join(t.TempDir(), "wa-1.pdf")
	if err := os.WriteFile(src, []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(src, media.MediaMeta{Filename: "a.pdf", ContentType: "application/pdf"}, "chat")
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()

	refs := inboxMedia(store, ws, "inbox:x", []string{ref, "media://missing"})
	if len(refs) != 2 || refs[0] == ref || refs[1] != "media://missing" {
		t.Fatalf("refs = %v", refs)
	}
	path, meta, err := store.ResolveWithMeta(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(ws, "inbox", "wa-1.pdf"); path != want {
		t.Fatalf("path = %s, want %s", path, want)
	}
	if meta.ContentType != "application/pdf" {
		t.Fatalf("meta lost: %+v", meta)
	}
	if b, _ := os.ReadFile(path); string(b) != "%PDF" {
		t.Fatalf("content = %q", b)
	}
	// Same file in a later turn reuses the existing copy.
	if again := inboxMedia(store, ws, "inbox:x", []string{ref}); again[0] == ref {
		t.Fatalf("second call kept original ref")
	}
}
