package state

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	"claude-custom/internal/config"
)

type RunInfo struct {
	PID       int       `json:"pid"`
	Addr      string    `json:"addr"`
	Token     string    `json:"token"`
	Profile   string    `json:"profile"`
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	StartedAt time.Time `json:"started_at"`
}

func Write(info RunInfo) error {
	path, err := config.RunPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(info, "", "  ")
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

func Read() (*RunInfo, error) {
	path, err := config.RunPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var info RunInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func Remove() error {
	path, err := config.RunPath()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func Alive(info *RunInfo) bool {
	if info == nil || info.PID <= 0 {
		return false
	}
	return alive(info.PID)
}
