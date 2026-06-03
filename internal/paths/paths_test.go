package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathInsideRootAllowed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "lib")
	if err := os.MkdirAll(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "a", "1.png")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsWithinRoots(target, []string{root}) {
		t.Errorf("expected %q to be within %q", target, root)
	}
}

func TestPathOutsideRootRejected(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "lib")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(tmp, "secret.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if IsWithinRoots(outside, []string{root}) {
		t.Errorf("expected %q to be rejected against %q", outside, root)
	}
}

func TestTraversalRejected(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "lib")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	sneaky := filepath.Join(root, "..", "secret.txt")
	if IsWithinRoots(sneaky, []string{root}) {
		t.Errorf("expected traversal %q to be rejected", sneaky)
	}
}
