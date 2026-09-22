package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackupRestoreIntegrityAndLock(t *testing.T) {
	src := t.TempDir()
	root := t.TempDir()
	snap := filepath.Join(root, "snapshot")
	restored := filepath.Join(root, "restored")
	if err := os.WriteFile(filepath.Join(src, "samples"), []byte("samples"), 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := Lock(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := Create(src, snap); err == nil {
		t.Fatal("backup accepted active source")
	}
	lock.Close()
	if err := Create(src, snap); err != nil {
		t.Fatal(err)
	}
	if err := Restore(snap, restored); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(restored, "samples"))
	if err != nil || string(b) != "samples" {
		t.Fatal("restore mismatch", err)
	}
	if err := Restore(snap, restored); err == nil {
		t.Fatal("overwrote existing directory")
	}
	os.WriteFile(filepath.Join(snap, "samples"), []byte("corrupt"), 0600)
	if err := Restore(snap, filepath.Join(root, "bad")); err == nil {
		t.Fatal("corrupt backup accepted")
	}
}
