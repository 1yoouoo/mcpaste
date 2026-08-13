package connector

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	file, err := os.Open(path)
	if err != nil {
		return Credential{}, errors.New("read connector credential")
	}
	defer file.Close()
	var credential Credential
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credential); err != nil || credential.Endpoint == "" || credential.Token == "" {
		return Credential{}, errors.New("invalid connector credential")
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
	if credential.Endpoint == "" || credential.Token == "" {
		return errors.New("invalid connector credential")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return errors.New("create connector credential directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return errors.New("secure connector credential directory")
	}
	temporary, err := os.CreateTemp(directory, ".credential-*")
	if err != nil {
		return errors.New("create connector credential temporary file")
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return errors.New("secure connector credential temporary file")
	}
	encoder := json.NewEncoder(temporary)
	if err := encoder.Encode(credential); err != nil {
		_ = temporary.Close()
		return errors.New("write connector credential")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return errors.New("sync connector credential")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("close connector credential")
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return errors.New("replace connector credential")
	}
	if err := syncDirectory(directory); err != nil {
		return errors.New("sync connector credential directory")
	}
	removeTemporary = false
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
