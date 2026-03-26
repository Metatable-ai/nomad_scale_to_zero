// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package traefik_plugin

import (
	"bufio"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

func newPluginLogger(name string) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(os.Getenv("S2Z_LOG_LEVEL"))) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})).With("plugin", name)
	return logger
}

func readBootstrapToken(key string) string {
	bootstrapDir := strings.TrimSpace(os.Getenv("S2Z_BOOTSTRAP_DIR"))
	if bootstrapDir == "" {
		bootstrapDir = "/bootstrap"
	}

	for _, fileName := range []string{"nomad.env", "consul.env"} {
		value := readEnvFileValue(filepath.Join(bootstrapDir, fileName), key)
		if value != "" {
			return value
		}
	}

	return ""
}

func readEnvFileValue(filePath, key string) string {
	file, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer file.Close()

	prefix := key + "="
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}

	return ""
}
