package connector

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type Credential struct {
	Endpoint string `json:"endpoint"`
	Token    string `json:"token"`
}

func DefaultCredentialPath() (string, error) {
	if configHome := os.Getenv("XDG_CONFIG_HOME"); configHome != "" {
		return filepath.Join(configHome, "mcpaste", "credential.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.New("resolve user config directory")
	}
	return filepath.Join(home, ".config", "mcpaste", "credential.json"), nil
}

func LoadCredential(path string) (Credential, error) {
	if path == "" {
		var err error
		path, err = DefaultCredentialPath()
		if err != nil {
			return Credential{}, err
		}
	}
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return Credential{}, ErrInvalidCredential
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxLocalCredentialBytes || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return Credential{}, ErrInvalidCredential
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxLocalCredentialBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) > maxLocalCredentialBytes {
		return Credential{}, ErrInvalidCredential
	}
	var credential Credential
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credential); err != nil {
		return Credential{}, ErrInvalidCredential
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Credential{}, ErrInvalidCredential
	}
	if _, err := validateLocalCredential(credential); err != nil {
		return Credential{}, ErrInvalidCredential
	}
	return credential, nil
}

func SaveCredential(path string, credential Credential) error {
	if path == "" {
		var err error
		path, err = DefaultCredentialPath()
		if err != nil {
			return err
		}
	}
	if _, err := validateLocalCredential(credential); err != nil {
		return ErrInvalidCredential
	}
	directory, name, err := openCredentialParent(path)
	if err != nil {
		return errors.New("create connector credential directory")
	}
	defer directory.Close()
	return writeCredentialAt(directory, name, credential)
}

func openCredentialParent(path string) (*os.File, string, error) {
	cleanPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, "", ErrInvalidCredential
	}
	name := filepath.Base(cleanPath)
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return nil, "", ErrInvalidCredential
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", ErrInvalidCredential
	}
	closeFD := true
	defer func() {
		if closeFD {
			_ = unix.Close(fd)
		}
	}()
	components := splitCredentialPath(filepath.Dir(cleanPath))
	symlinks := 0
	for len(components) != 0 {
		component := components[0]
		components = components[1:]
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil && credentialPathComponentIsSymlink(fd, component) {
			if symlinks >= 8 || !trustedCredentialSymlinkParent(fd) {
				return nil, "", ErrInvalidCredential
			}
			target, readErr := readCredentialSymlink(fd, component)
			if readErr != nil {
				return nil, "", ErrInvalidCredential
			}
			symlinks++
			if filepath.IsAbs(target) {
				if err := unix.Close(fd); err != nil {
					return nil, "", ErrInvalidCredential
				}
				fd, err = unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
				if err != nil {
					return nil, "", ErrInvalidCredential
				}
			}
			components = append(splitCredentialPath(target), components...)
			continue
		}
		if errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(fd, component, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				return nil, "", ErrInvalidCredential
			}
			next, openErr = unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openErr != nil {
			return nil, "", ErrInvalidCredential
		}
		if err := unix.Close(fd); err != nil {
			_ = unix.Close(next)
			return nil, "", ErrInvalidCredential
		}
		fd = next
	}
	if err := unix.Fchmod(fd, 0o700); err != nil {
		return nil, "", ErrInvalidCredential
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o077 != 0 {
		return nil, "", ErrInvalidCredential
	}
	directory := os.NewFile(uintptr(fd), filepath.Dir(cleanPath))
	if directory == nil {
		return nil, "", ErrInvalidCredential
	}
	closeFD = false
	return directory, name, nil
}

func writeCredentialAt(directory *os.File, name string, credential Credential) error {
	if directory == nil || name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return ErrInvalidCredential
	}
	data, err := json.Marshal(credential)
	if err != nil || len(data)+1 > maxLocalCredentialBytes {
		return errors.New("write connector credential")
	}
	data = append(data, '\n')
	directoryFD := int(directory.Fd())
	temporaryName, temporaryFD, err := createCredentialTemp(directoryFD)
	if err != nil {
		return errors.New("create connector credential temporary file")
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = unix.Unlinkat(directoryFD, temporaryName, 0)
		}
	}()
	temporary := os.NewFile(uintptr(temporaryFD), temporaryName)
	if temporary == nil {
		_ = unix.Close(temporaryFD)
		return errors.New("create connector credential temporary file")
	}
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return errors.New("secure connector credential temporary file")
	}
	if _, err := temporary.Write(data); err != nil {
		return errors.New("write connector credential")
	}
	if err := temporary.Sync(); err != nil {
		return errors.New("sync connector credential")
	}
	if err := temporary.Close(); err != nil {
		closed = true
		return errors.New("close connector credential")
	}
	closed = true
	if err := unix.Renameat(directoryFD, temporaryName, directoryFD, name); err != nil {
		return errors.New("replace connector credential")
	}
	removeTemporary = false
	if err := unix.Fsync(directoryFD); err != nil {
		return errors.New("sync connector credential directory")
	}
	return nil
}

func splitCredentialPath(path string) []string {
	trimmed := strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator))
	if trimmed == "" || trimmed == "." {
		return nil
	}
	return strings.Split(trimmed, string(filepath.Separator))
}

func credentialPathComponentIsSymlink(directoryFD int, name string) bool {
	var stat unix.Stat_t
	return unix.Fstatat(directoryFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW) == nil && stat.Mode&unix.S_IFMT == unix.S_IFLNK
}

func trustedCredentialSymlinkParent(directoryFD int) bool {
	var stat unix.Stat_t
	if unix.Fstat(directoryFD, &stat) != nil {
		return false
	}
	return stat.Uid != uint32(unix.Geteuid()) && stat.Mode&0o022 == 0
}

func readCredentialSymlink(directoryFD int, name string) (string, error) {
	buffer := make([]byte, 4<<10)
	length, err := unix.Readlinkat(directoryFD, name, buffer)
	if err != nil || length == 0 || length == len(buffer) {
		return "", ErrInvalidCredential
	}
	return string(buffer[:length]), nil
}

func createCredentialTemp(directoryFD int) (string, int, error) {
	for range 16 {
		var random [12]byte
		if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
			return "", -1, err
		}
		name := ".credential-" + hex.EncodeToString(random[:])
		fd, err := unix.Openat(directoryFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err == nil {
			return name, fd, nil
		}
		if !errors.Is(err, unix.EEXIST) {
			return "", -1, err
		}
	}
	return "", -1, ErrInvalidCredential
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
