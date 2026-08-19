package connector

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCredentialFileIsMode0600AndAtomicallyReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "mcpaste", "credential.json")
	first := Credential{Endpoint: "http://127.0.0.1:38421", Token: "example-token-not-real"}
	if err := SaveCredential(path, first); err != nil {
		t.Fatalf("SaveCredential() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credential: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %o, want 600", info.Mode().Perm())
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat credential directory: %v", err)
	}
	if directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("credential directory mode = %o, want 700", directoryInfo.Mode().Perm())
	}
	second := Credential{Endpoint: "http://[::1]:38421", Token: "example-token-replacement-not-real"}
	if err := SaveCredential(path, second); err != nil {
		t.Fatalf("replacement SaveCredential() error = %v", err)
	}
	loaded, err := LoadCredential(path)
	if err != nil {
		t.Fatalf("LoadCredential() error = %v", err)
	}
	if loaded != second {
		t.Fatalf("loaded credential = %#v, want %#v", loaded, second)
	}
}

func TestSaveCredentialRejectsSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "credential-parent")
	if err := os.Symlink(target, parent); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "credential.json")
	err := SaveCredential(path, Credential{Endpoint: "http://127.0.0.1:38421", Token: "parent-symlink-token"})
	if err == nil {
		t.Fatal("SaveCredential() followed a symlinked parent")
	}
	if _, err := os.Lstat(filepath.Join(target, "credential.json")); !os.IsNotExist(err) {
		t.Fatalf("symlink target credential exists/error = %v", err)
	}
}

func TestCredentialWriteRemainsAnchoredWhenParentPathIsSwapped(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "credential-parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "credential.json")
	directory, name, err := openCredentialParent(path)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	anchored := filepath.Join(root, "anchored-parent")
	attacker := filepath.Join(root, "attacker")
	if err := os.Mkdir(attacker, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(parent, anchored); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(attacker, parent); err != nil {
		t.Fatal(err)
	}
	credential := Credential{Endpoint: "http://127.0.0.1:38421", Token: "swap-race-token"}
	if err := writeCredentialAt(directory, name, credential); err != nil {
		t.Fatalf("writeCredentialAt() error = %v", err)
	}
	loaded, err := LoadCredential(filepath.Join(anchored, "credential.json"))
	if err != nil || loaded != credential {
		t.Fatalf("anchored credential/error = %#v/%v", loaded, err)
	}
	entries, err := os.ReadDir(attacker)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("attacker directory entries = %v, want none", entries)
	}
}

func TestLoadCredentialRejectsSymlinksPermissionsAndNonRegularFiles(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "credential.json")
	credential := Credential{Endpoint: "http://127.0.0.1:38421", Token: "example-token-not-real"}
	if err := SaveCredential(path, credential); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredential(path); err == nil {
		t.Fatal("LoadCredential() accepted group/other-readable file")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(directory, "credential-link.json")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredential(symlink); err == nil {
		t.Fatal("LoadCredential() followed a symlink")
	}
	if _, err := LoadCredential(directory); err == nil {
		t.Fatal("LoadCredential() accepted a directory")
	}
}

func TestLoadCredentialRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := LoadCredential(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("LoadCredential() accepted a FIFO")
		}
	case <-time.After(300 * time.Millisecond):
		writer, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_ = writer.Close()
		}
		t.Fatal("LoadCredential() blocked on a FIFO")
	}
}

func TestLoadCredentialIsBoundedAndStrict(t *testing.T) {
	directory := t.TempDir()
	tests := []struct {
		name string
		body string
	}{
		{name: "oversized", body: `{"endpoint":"http://127.0.0.1:38421","token":"` + strings.Repeat("x", 20<<10) + `"}`},
		{name: "unknown field", body: `{"endpoint":"http://127.0.0.1:38421","token":"token","extra":true}`},
		{name: "trailing JSON", body: `{"endpoint":"http://127.0.0.1:38421","token":"token"}{}`},
		{name: "remote endpoint", body: `{"endpoint":"https://example.invalid","token":"token"}`},
		{name: "token whitespace", body: `{"endpoint":"http://127.0.0.1:38421","token":"two words"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(directory, strings.ReplaceAll(test.name, " ", "-")+".json")
			if err := os.WriteFile(path, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadCredential(path); err == nil {
				t.Fatal("LoadCredential() accepted invalid credential")
			}
		})
	}
}

func TestCredentialErrorsDoNotEchoTokenOrPath(t *testing.T) {
	secret := "example-secret-token-not-real"
	path := filepath.Join(t.TempDir(), "secret-path.json")
	for _, err := range []error{
		SaveCredential(path, Credential{Endpoint: "https://example.invalid", Token: secret}),
		func() error {
			_, err := LoadCredential(path)
			return err
		}(),
	} {
		if err == nil {
			t.Fatal("credential operation unexpectedly succeeded")
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), path) {
			t.Fatalf("credential error leaked secret/path: %v", err)
		}
	}
}
