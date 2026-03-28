// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// nomadEventStreamMessage is a single NDJSON line from /v1/event/stream.
type nomadEventStreamMessage struct {
	Index  uint64       `json:"Index"`
	Events []nomadEvent `json:"Events"`
}

type nomadEvent struct {
	Topic   string          `json:"Topic"`
	Type    string          `json:"Type"`
	Key     string          `json:"Key"`
	Index   uint64          `json:"Index"`
	Payload json.RawMessage `json:"Payload"`
}

type nomadEventAllocPayload struct {
	Allocation struct {
		nomadAllocation
		JobID string `json:"JobID"`
	} `json:"Allocation"`
}

// allocEventResult is sent when the event stream detects a relevant allocation
// state change for the watched job/group.
type allocEventResult struct {
	AllocID      string
	ClientStatus string
	AllRunning   bool
}

// watchAllocEvents connects to Nomad's /v1/event/stream, filters allocation
// events for the given job/group, and sends results on the returned channel.
// The channel is closed on context cancellation, stream error, or disconnect.
// If the stream fails to connect, the channel closes immediately so the caller
// falls back to polling.
func (r *nomadRuntime) watchAllocEvents(ctx context.Context, jobID, group string) <-chan allocEventResult {
	ch := make(chan allocEventResult, 8)

	go func() {
		defer close(ch)

		logger := r.logger.With("job", jobID, "group", group, "component", "event-stream")

		endpoint := fmt.Sprintf("%s/v1/event/stream?topic=Allocation&namespace=default",
			strings.TrimRight(r.nomadAddr, "/"))

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			logger.WarnContext(ctx, "failed to create event stream request", "error", err)
			return
		}
		r.addNomadToken(req)

		// Long-lived streaming connection — no client-level timeout.
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			logger.WarnContext(ctx, "event stream connection failed", "error", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			msg, _ := io.ReadAll(resp.Body)
			logger.WarnContext(ctx, "event stream error status",
				"status", resp.StatusCode, "body", string(msg))
			return
		}

		logger.InfoContext(ctx, "connected to nomad event stream")
		r.parseAllocEventStream(ctx, resp.Body, jobID, group, ch, logger)
	}()

	return ch
}

func (r *nomadRuntime) parseAllocEventStream(
	ctx context.Context,
	body io.Reader,
	jobID, group string,
	ch chan<- allocEventResult,
	logger *slog.Logger,
) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}

		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}

		var msg nomadEventStreamMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			logger.WarnContext(ctx, "failed to decode event line", "error", err)
			continue
		}

		for _, evt := range msg.Events {
			if evt.Topic != "Allocation" {
				continue
			}

			var payload nomadEventAllocPayload
			if err := json.Unmarshal(evt.Payload, &payload); err != nil {
				continue
			}

			alloc := payload.Allocation
			if alloc.JobID != jobID || alloc.TaskGroup != group {
				continue
			}

			allRunning := alloc.ClientStatus == "running" && len(alloc.TaskStates) > 0
			if allRunning {
				for _, ts := range alloc.TaskStates {
					if ts.State != "running" {
						allRunning = false
						break
					}
				}
			}

			select {
			case ch <- allocEventResult{
				AllocID:      alloc.ID,
				ClientStatus: alloc.ClientStatus,
				AllRunning:   allRunning,
			}:
			case <-ctx.Done():
				return
			}
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		logger.WarnContext(ctx, "event stream read error", "error", err)
	}
}
