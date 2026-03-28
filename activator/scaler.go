// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultIdleTimeout        = 5 * time.Minute
	defaultScaleDownInterval  = 30 * time.Second
	defaultMinScaleDownAge    = 1 * time.Minute
	leaderLockTTL             = 45 * time.Second
	leaderRenewInterval       = 15 * time.Second
	scaleDownOpTimeout        = 10 * time.Second
)

// ScaleDownController periodically scans registered workloads and scales
// idle ones to zero. Only one replica runs scale-down via leader election.
type ScaleDownController struct {
	logger           *slog.Logger
	store            stateStore
	nomadAddr        string
	nomadToken       string
	client           *http.Client
	idleTimeout      time.Duration
	scanInterval     time.Duration
	minScaleDownAge  time.Duration
	ownerID          string
}

// ScaleDownConfig holds configuration for the scale-down controller.
type ScaleDownConfig struct {
	IdleTimeout     time.Duration
	ScanInterval    time.Duration
	MinScaleDownAge time.Duration
}

func newScaleDownController(logger *slog.Logger, store stateStore, nomadAddr, nomadToken string) *ScaleDownController {
	hostname, _ := os.Hostname()
	ownerID := fmt.Sprintf("scaler-%s-%d", hostname, time.Now().UnixNano())

	idleTimeout := envOrDefaultDuration("ACTIVATOR_IDLE_TIMEOUT", defaultIdleTimeout)
	scanInterval := envOrDefaultDuration("ACTIVATOR_SCALE_DOWN_INTERVAL", defaultScaleDownInterval)
	minScaleDownAge := envOrDefaultDuration("ACTIVATOR_MIN_SCALE_DOWN_AGE", defaultMinScaleDownAge)

	return &ScaleDownController{
		logger:          logger.With("component", "scale-down"),
		store:           store,
		nomadAddr:       nomadAddr,
		nomadToken:      nomadToken,
		client:          &http.Client{Timeout: scaleDownOpTimeout},
		idleTimeout:     idleTimeout,
		scanInterval:    scanInterval,
		minScaleDownAge: minScaleDownAge,
		ownerID:         ownerID,
	}
}

// Run starts the scale-down controller loop. Blocks until ctx is cancelled.
func (c *ScaleDownController) Run(ctx context.Context) {
	c.logger.Info("scale-down controller starting",
		"idle_timeout", c.idleTimeout.String(),
		"scan_interval", c.scanInterval.String(),
		"min_scale_down_age", c.minScaleDownAge.String(),
		"owner", c.ownerID,
	)

	ticker := time.NewTicker(c.scanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.releaseLeader()
			c.logger.Info("scale-down controller stopped")
			return
		case <-ticker.C:
			c.tick(ctx)
		}
	}
}

func (c *ScaleDownController) tick(ctx context.Context) {
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	isLeader, err := c.tryBecomeLeader(lockCtx)
	if err != nil {
		c.logger.Warn("leader election failed", "error", err)
		return
	}
	if !isLeader {
		return
	}

	c.scanAndScaleDown(ctx)
}

func (c *ScaleDownController) tryBecomeLeader(ctx context.Context) (bool, error) {
	acquired, err := c.store.AcquireLeaderLock(ctx, c.ownerID, leaderLockTTL)
	if err != nil {
		return false, err
	}
	if acquired {
		return true, nil
	}

	// We might already be the leader — try to renew.
	renewed, err := c.store.RenewLeaderLock(ctx, c.ownerID, leaderLockTTL)
	if err != nil {
		return false, err
	}
	return renewed, nil
}

func (c *ScaleDownController) releaseLeader() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.store.ReleaseLeaderLock(ctx, c.ownerID); err != nil {
		c.logger.Warn("release leader lock failed", "error", err)
	}
}

func (c *ScaleDownController) scanAndScaleDown(ctx context.Context) {
	hosts, err := c.store.ListRegisteredHosts(ctx)
	if err != nil {
		c.logger.Warn("list registered hosts failed", "error", err)
		return
	}

	now := time.Now().UTC()
	scaled := 0

	for _, host := range hosts {
		if ctx.Err() != nil {
			return
		}

		workload, ok, err := c.store.LookupWorkload(ctx, host)
		if err != nil || !ok {
			continue
		}

		shouldScale, reason := c.shouldScaleDown(ctx, workload, now)
		if !shouldScale {
			continue
		}

		logger := c.logger.With("job", workload.JobName, "group", workload.GroupName, "service", workload.ServiceName, "reason", reason)

		if err := c.scaleToZero(ctx, workload); err != nil {
			logger.Warn("scale-to-zero failed", "error", err)
			continue
		}

		// Clear cached ready endpoint so next request triggers fresh activation.
		if clearErr := c.store.ClearReadyEndpoint(ctx, host); clearErr != nil {
			logger.Warn("clear ready endpoint after scale-down failed", "error", clearErr)
		}
		if clearErr := c.store.ClearActivationState(ctx, host); clearErr != nil {
			logger.Warn("clear activation state after scale-down failed", "error", clearErr)
		}

		logger.Info("scaled to zero")
		scaled++
	}

	if scaled > 0 {
		c.logger.Info("scale-down sweep complete", "hosts_scanned", len(hosts), "scaled_down", scaled)
	}
}

func (c *ScaleDownController) shouldScaleDown(ctx context.Context, workload WorkloadRegistration, now time.Time) (bool, string) {
	// Skip if there's an active activation in progress.
	state, ok, err := c.store.GetActivationState(ctx, workload.HostName)
	if err == nil && ok && state.Status == activationStatusPending {
		return false, ""
	}

	// Check current job count — skip if already at 0.
	count, err := c.getJobGroupCount(ctx, workload.JobName, workload.GroupName)
	if err != nil || count == 0 {
		return false, ""
	}

	// Check last activity timestamp.
	lastActivity, ok, err := c.store.GetActivity(ctx, workload.ServiceName)
	if err != nil {
		c.logger.Warn("get activity failed", "service", workload.ServiceName, "error", err)
		return false, ""
	}
	if !ok {
		// No activity recorded — initialize to now to avoid immediate scale-down.
		_ = c.store.SetActivity(ctx, workload.ServiceName, now)
		return false, ""
	}

	idleFor := now.Sub(lastActivity)
	if idleFor < c.idleTimeout {
		return false, ""
	}

	return true, fmt.Sprintf("idle for %s (threshold %s)", idleFor.Round(time.Second), c.idleTimeout)
}

func (c *ScaleDownController) getJobGroupCount(ctx context.Context, jobID, groupName string) (int, error) {
	endpoint := fmt.Sprintf("%s/v1/job/%s", c.nomadAddr, url.PathEscape(jobID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	if c.nomadToken != "" {
		req.Header.Set("X-Nomad-Token", c.nomadToken)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return 0, nil
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("nomad job %s: status %d", jobID, resp.StatusCode)
	}

	var job struct {
		Status     string `json:"Status"`
		TaskGroups []struct {
			Name  string `json:"Name"`
			Count int    `json:"Count"`
		} `json:"TaskGroups"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return 0, fmt.Errorf("decode job %s: %w", jobID, err)
	}

	for _, g := range job.TaskGroups {
		if g.Name == groupName {
			return g.Count, nil
		}
	}
	return 0, nil
}

func (c *ScaleDownController) scaleToZero(ctx context.Context, workload WorkloadRegistration) error {
	endpoint := fmt.Sprintf("%s/v1/job/%s/scale", c.nomadAddr, url.PathEscape(workload.JobName))
	payload := fmt.Sprintf(`{"Count":0,"Target":{"Group":"%s"},"Message":"activator scale-down: idle timeout exceeded"}`, workload.GroupName)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.nomadToken != "" {
		req.Header.Set("X-Nomad-Token", c.nomadToken)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("scale %s to zero: %w", workload.JobName, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("scale %s to zero: status %d", workload.JobName, resp.StatusCode)
	}
	return nil
}
