package keystore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"

	"claude-custom/internal/config"
)

const service = "claude-custom"

var ErrNotFound = errors.New("secret not found")

func envName(provider string) string {
	var b strings.Builder
	b.WriteString("CLAUDE_CUSTOM_")
	for _, r := range strings.ToUpper(provider) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	b.WriteString("_KEY")
	return b.String()
}

func Get(provider string) (string, error) {
	if v := strings.TrimSpace(os.Getenv(envName(provider))); v != "" {
		return v, nil
	}
	if v, err := keyring.Get(service, provider); err == nil && v != "" {
		return v, nil
	}
	return getFile(provider)
}

func Set(provider, secret string) error {
	if strings.TrimSpace(secret) == "" {
		return errors.New("secret is empty")
	}
	if err := keyring.Set(service, provider, secret); err == nil {
		return nil
	}
	return setFile(provider, secret)
}

func Delete(provider string) error {
	keyringErr := keyring.Delete(service, provider)
	fileErr := deleteFile(provider)
	if keyringErr != nil && fileErr != nil {
		return ErrNotFound
	}
	return nil
}

func Has(provider string) bool {
	_, err := Get(provider)
	return err == nil
}

func Source(provider string) string {
	if strings.TrimSpace(os.Getenv(envName(provider))) != "" {
		return "environment"
	}
	if v, err := keyring.Get(service, provider); err == nil && v != "" {
		return "keyring"
	}
	if v, err := getFile(provider); err == nil && v != "" {
		_ = v
		return "file"
	}
	return "missing"
}

func getFile(provider string) (string, error) {
	store, err := readFile()
	if err != nil {
		return "", err
	}
	v, ok := store[provider]
	if !ok || v == "" {
		return "", ErrNotFound
	}
	return v, nil
}

func setFile(provider, secret string) error {
	store, err := readFile()
	if err != nil {
		return err
	}
	store[provider] = secret
	return writeFile(store)
}

func deleteFile(provider string) error {
	store, err := readFile()
	if err != nil {
		return err
	}
	if _, ok := store[provider]; !ok {
		return ErrNotFound
	}
	delete(store, provider)
	return writeFile(store)
}

func readFile() (map[string]string, error) {
	path, err := config.SecretsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	store := map[string]string{}
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return store, nil
}

func writeFile(store map[string]string) error {
	path, err := config.SecretsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	_ = os.Remove(path)
	return os.Rename(tmp, path)
}
