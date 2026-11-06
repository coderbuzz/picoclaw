package agent

import (
	"io"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
)

// inboxMedia links (or copies) the turn's media into <workspace>/inbox and returns
// refs to those copies. Sandboxed agents only see their workspace, so the shared
// media dir (files of every chat) must stay hidden while this chat's files stay
// readable. A ref that cannot be moved is kept as is.
func inboxMedia(store media.MediaStore, workspace, scope string, refs []string) []string {
	if store == nil || len(refs) == 0 {
		return refs
	}
	dir := filepath.Join(workspace, "inbox")
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		newRef, err := inboxOne(store, dir, scope, ref)
		if err != nil {
			logger.WarnCF("agent", "Inbox media copy failed", map[string]any{"ref": ref, "error": err.Error()})
			newRef = ref
		}
		out = append(out, newRef)
	}
	return out
}

func inboxOne(store media.MediaStore, dir, scope, ref string) (string, error) {
	src, meta, err := store.ResolveWithMeta(ref)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, filepath.Base(src))
	if _, err := os.Stat(dst); os.IsNotExist(err) {
		if os.Link(src, dst) != nil {
			if err := copyFile(src, dst); err != nil {
				return "", err
			}
		}
	}
	// The copy is ours: the media cleaner removes it after max age.
	meta.CleanupPolicy = media.CleanupPolicyDeleteOnCleanup
	return store.Store(dst, meta, scope)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}
