package groupopen

import "testing"

func TestStorePersists(t *testing.T) {
	dir := t.TempDir()
	s := For(dir)
	if s.IsOpen("a") {
		t.Fatal("default must be closed")
	}
	if err := s.Set("a", true); err != nil {
		t.Fatal(err)
	}
	// A fresh store on the same file (as after a restart) sees the state.
	fresh := &Store{path: s.path, open: map[string]bool{}}
	fresh.load()
	if !fresh.IsOpen("a") {
		t.Fatal("open state lost after reload")
	}
	_ = s.Set("a", false)
	fresh = &Store{path: s.path, open: map[string]bool{}}
	fresh.load()
	if fresh.IsOpen("a") {
		t.Fatal("close not persisted")
	}
}
