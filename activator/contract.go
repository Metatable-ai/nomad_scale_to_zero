// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"net/url"
	"strings"
)

type WorkloadRegistration struct {
	HostName        string `json:"host_name"`
	ServiceName     string `json:"service_name"`
	JobName         string `json:"job_name"`
	GroupName       string `json:"group_name,omitempty"`
	WorkloadClass   string `json:"workload_class"`
	WorkloadOrdinal int    `json:"workload_ordinal"`
	JobSpecKey      string `json:"job_spec_key"`
}

type RegistrySyncRequest struct {
	Workloads []WorkloadRegistration `json:"workloads"`
}

type RegistrySyncResponse struct {
	Status       string `json:"status"`
	SyncedCount  int    `json:"synced_count"`
	RemovedCount int    `json:"removed_count"`
}

type RegistryLookupResponse struct {
	Status   string                `json:"status"`
	HostName string                `json:"host_name"`
	Workload *WorkloadRegistration `json:"workload,omitempty"`
	Message  string                `json:"message,omitempty"`
}

type ActivateRequest struct {
	Host      string `json:"host"`
	Method    string `json:"method,omitempty"`
	Path      string `json:"path,omitempty"`
	Query     string `json:"query,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

type ActivateResponse struct {
	Status         string                `json:"status"`
	Mode           string                `json:"mode"`
	HostName       string                `json:"host_name"`
	RequestID      string                `json:"request_id,omitempty"`
	RequestTimeout string                `json:"request_timeout"`
	HoldRequest    bool                  `json:"hold_request"`
	Workload       *WorkloadRegistration `json:"workload,omitempty"`
	TargetURL      string                `json:"target_url,omitempty"`
	Message        string                `json:"message,omitempty"`
}

const defaultTaskGroupName = "main"

func normalizeHost(raw string) string {
	host := strings.ToLower(strings.TrimSpace(raw))
	if host == "" {
		return ""
	}
	if parsed, err := url.Parse("//" + host); err == nil && parsed.Hostname() != "" {
		host = parsed.Hostname()
	}
	return host
}

func normalizeWorkloadRegistration(record WorkloadRegistration) WorkloadRegistration {
	record.HostName = normalizeHost(record.HostName)
	record.ServiceName = strings.TrimSpace(record.ServiceName)
	record.JobName = strings.TrimSpace(record.JobName)
	record.GroupName = strings.TrimSpace(record.GroupName)
	record.WorkloadClass = strings.TrimSpace(record.WorkloadClass)
	record.JobSpecKey = strings.TrimSpace(record.JobSpecKey)
	if record.GroupName == "" {
		record.GroupName = defaultTaskGroupName
	}
	return record
}

func (r WorkloadRegistration) validate() error {
	switch {
	case r.HostName == "":
		return errors.New("host_name is required")
	case r.ServiceName == "":
		return errors.New("service_name is required")
	case r.JobName == "":
		return errors.New("job_name is required")
	case r.GroupName == "":
		return errors.New("group_name is required")
	case r.WorkloadClass == "":
		return errors.New("workload_class is required")
	case r.WorkloadOrdinal <= 0:
		return errors.New("workload_ordinal must be greater than zero")
	case r.JobSpecKey == "":
		return errors.New("job_spec_key is required")
	}
	return nil
}

func normalizeActivateRequest(req ActivateRequest) ActivateRequest {
	req.Host = normalizeHost(req.Host)
	req.Method = strings.TrimSpace(strings.ToUpper(req.Method))
	req.Path = strings.TrimSpace(req.Path)
	req.Query = strings.TrimSpace(req.Query)
	req.RequestID = strings.TrimSpace(req.RequestID)
	if req.Method == "" {
		req.Method = "GET"
	}
	if req.Path == "" {
		req.Path = "/"
	}
	return req
}

func (r ActivateRequest) validate() error {
	if r.Host == "" {
		return errors.New("host is required")
	}
	return nil
}
