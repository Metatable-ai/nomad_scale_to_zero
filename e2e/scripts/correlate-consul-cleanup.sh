#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 4 ]; then
	echo "Usage: $0 SNAPSHOT_DIR [MANIFEST_FILE] [WORKLOAD_PREFIX] [IDLE_TIMEOUT_SECONDS]" >&2
	exit 1
fi

snapshot_dir="$1"
manifest_file="${2:-}"
workload_prefix="${3:-echo-s2z-}"
idle_timeout_seconds="${4:-0}"

python3 - "$snapshot_dir" "$manifest_file" "$workload_prefix" "$idle_timeout_seconds" <<'PY'
import json
import os
import re
import sys
from datetime import datetime, timezone

snapshot_dir, manifest_file, workload_prefix, idle_timeout_raw = sys.argv[1:5]
try:
    idle_timeout_seconds = int(idle_timeout_raw)
except ValueError:
    idle_timeout_seconds = 0

errors = []

RFC3339_RE = re.compile(
    r"^(?P<base>\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(?P<fraction>\d+))?(?P<tz>Z|[+-]\d{2}:\d{2})$"
)


def utc_now():
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def clean_text(value):
    if value in ("", None):
        return None
    return value


def parse_rfc3339(value):
    if not value or not isinstance(value, str):
        return None

    match = RFC3339_RE.match(value.strip())
    if not match:
        return None

    fraction = match.group("fraction") or ""
    tz = "+00:00" if match.group("tz") == "Z" else match.group("tz")
    if fraction:
        fraction = fraction[:6].ljust(6, "0")
        text = f"{match.group('base')}.{fraction}{tz}"
    else:
        text = f"{match.group('base')}{tz}"

    try:
        return datetime.fromisoformat(text)
    except ValueError:
        return None


def format_rfc3339(value):
    if value is None:
        return None
    return value.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


def ns_to_rfc3339(value):
    if not isinstance(value, (int, float)):
        return None

    try:
        return format_rfc3339(datetime.fromtimestamp(value / 1_000_000_000, tz=timezone.utc))
    except (OverflowError, OSError, ValueError):
        return None


def load_json(path, default, label):
    if not path or not os.path.isfile(path) or os.path.getsize(path) == 0:
        return default, False

    try:
        with open(path, "r", encoding="utf-8") as handle:
            return json.load(handle), True
    except Exception as exc:
        errors.append(f"{label}: {exc}")
        return default, False


def load_manifest(path):
    if not path or not os.path.isfile(path) or os.path.getsize(path) == 0:
        return [], False

    entries = []
    try:
        with open(path, "r", encoding="utf-8") as handle:
            for raw_line in handle:
                line = raw_line.strip()
                if not line:
                    continue

                parts = line.split("|")
                job_name = parts[0] if len(parts) > 0 else ""
                if not job_name:
                    continue

                workload_ordinal = None
                if len(parts) > 3 and parts[3]:
                    try:
                        workload_ordinal = int(parts[3])
                    except ValueError:
                        errors.append(f"manifest workload ordinal parse failed for {job_name}: {parts[3]}")

                entries.append(
                    {
                        "job_name": job_name,
                        "service_name": parts[1] if len(parts) > 1 and parts[1] else job_name,
                        "workload_class": parts[2] if len(parts) > 2 and parts[2] else None,
                        "workload_ordinal": workload_ordinal,
                        "job_spec_key": parts[4] if len(parts) > 4 and parts[4] else None,
                    }
                )
    except Exception as exc:
        errors.append(f"manifest: {exc}")
        return [], False

    return entries, True


def normalize_object(value, label):
    if isinstance(value, dict):
        return value
    if value in ({}, None):
        return {}
    errors.append(f"{label}: expected JSON object")
    return {}


def normalize_array(value, label):
    if isinstance(value, list):
        return value
    if value in ([], None):
        return []
    errors.append(f"{label}: expected JSON array")
    return []


def summarize_job_groups(job):
    summary = {
        "running": 0,
        "starting": 0,
        "queued": 0,
        "complete": 0,
        "failed": 0,
        "lost": 0,
        "unknown": 0,
    }

    job_summary = (job.get("JobSummary") or {}).get("Summary", {})
    for group in job_summary.values():
        summary["running"] += int(group.get("Running") or 0)
        summary["starting"] += int(group.get("Starting") or 0)
        summary["queued"] += int(group.get("Queued") or 0)
        summary["complete"] += int(group.get("Complete") or 0)
        summary["failed"] += int(group.get("Failed") or 0)
        summary["lost"] += int(group.get("Lost") or 0)
        summary["unknown"] += int(group.get("Unknown") or 0)

    return summary


def project_job(job):
    if not job:
        return None

    return {
        "job_id": job.get("ID") or job.get("Name"),
        "name": job.get("Name"),
        "status": job.get("Status"),
        "type": job.get("Type"),
        "stop": job.get("Stop"),
        "priority": job.get("Priority"),
        "submit_time_ns": job.get("SubmitTime"),
        "submit_time": ns_to_rfc3339(job.get("SubmitTime")),
        "modify_index": job.get("ModifyIndex"),
        "group_summary": summarize_job_groups(job),
        "group_summaries": (job.get("JobSummary") or {}).get("Summary", {}),
    }


def project_last_event(events):
    if not events:
        return None

    event = events[-1]
    return {
        "type": event.get("Type"),
        "display_message": clean_text(event.get("DisplayMessage")),
        "message": clean_text(event.get("Message")),
        "time_ns": event.get("Time"),
        "time": ns_to_rfc3339(event.get("Time")),
        "exit_code": event.get("ExitCode"),
        "signal": event.get("Signal"),
        "fails_task": event.get("FailsTask"),
    }


def project_task_states(task_states):
    projected = {}
    for task_name, task_state in (task_states or {}).items():
        projected[task_name] = {
            "state": task_state.get("State"),
            "failed": task_state.get("Failed"),
            "restarts": task_state.get("Restarts"),
            "started_at": task_state.get("StartedAt"),
            "finished_at": task_state.get("FinishedAt"),
            "last_restart": task_state.get("LastRestart"),
            "last_event": project_last_event(task_state.get("Events")),
        }
    return projected


def project_node(node):
    if not node:
        return None

    return {
        "node_id": node.get("ID"),
        "node_name": node.get("Name"),
        "status": node.get("Status"),
        "datacenter": node.get("Datacenter"),
        "drain": node.get("Drain"),
        "scheduling_eligibility": node.get("SchedulingEligibility"),
    }


def project_allocation(alloc, nodes_by_id):
    node = nodes_by_id.get(alloc.get("NodeID"))
    return {
        "allocation_id": alloc.get("ID"),
        "name": alloc.get("Name"),
        "desired_status": alloc.get("DesiredStatus"),
        "desired_description": clean_text(alloc.get("DesiredDescription")),
        "client_status": alloc.get("ClientStatus"),
        "client_description": clean_text(alloc.get("ClientDescription")),
        "node_id": alloc.get("NodeID"),
        "node_name": clean_text(alloc.get("NodeName")),
        "node": project_node(node),
        "create_time_ns": alloc.get("CreateTime"),
        "create_time": ns_to_rfc3339(alloc.get("CreateTime")),
        "modify_time_ns": alloc.get("ModifyTime"),
        "modify_time": ns_to_rfc3339(alloc.get("ModifyTime")),
        "task_states": project_task_states(alloc.get("TaskStates")),
    }


def project_check(check):
    return {
        "check_id": check.get("CheckID"),
        "name": check.get("Name"),
        "status": check.get("Status"),
        "node_name": check.get("Node"),
        "service_id": clean_text(check.get("ServiceID")),
        "output": clean_text(check.get("Output")),
        "type": clean_text(check.get("Type")),
        "interval": clean_text(check.get("Interval")),
        "timeout": clean_text(check.get("Timeout")),
        "notes": clean_text(check.get("Notes")),
        "service_tags": sorted(set(check.get("ServiceTags") or [])),
        "create_index": check.get("CreateIndex"),
        "modify_index": check.get("ModifyIndex"),
    }


def count_by(items, field):
    counts = {}
    for item in items:
        value = item.get(field) or "unknown"
        counts[value] = counts.get(value, 0) + 1
    return counts


def maybe_class_from_tags(tags):
    for tag in tags or []:
        if tag.startswith("e2e.workload.class="):
            workload_class = tag.split("=", 1)[1]
            if workload_class:
                return workload_class
    return None


def unique_tags(*tag_lists):
    tags = set()
    for tag_list in tag_lists:
        for tag in tag_list or []:
            if tag:
                tags.add(tag)
    return sorted(tags)


def diagnose(active_nomad, last_activity_present, last_activity_age_seconds):
    signals = []
    if active_nomad:
        signals.append("nomad-workload-active")
    else:
        signals.append("nomad-workload-inactive")

    if last_activity_present:
        if last_activity_age_seconds is None:
            signals.append("redis-activity-unparseable")
        elif idle_timeout_seconds > 0 and last_activity_age_seconds <= idle_timeout_seconds:
            signals.append("redis-activity-within-idle-timeout")
        elif idle_timeout_seconds > 0:
            signals.append("redis-activity-older-than-idle-timeout")
        else:
            signals.append("redis-activity-present")
    else:
        signals.append("redis-activity-missing")

    if active_nomad:
        if last_activity_present and last_activity_age_seconds is not None and idle_timeout_seconds > 0 and last_activity_age_seconds <= idle_timeout_seconds:
            signals.append("recent-activity-aligns-with-rewake")
            return "re-wake-likely", signals

        signals.append("nomad-still-active-past-idle-window")
        return "missed-scale-down-likely", signals

    signals.append("consul-stale-after-nomad-inactive")
    return "deregistration-race-likely", signals


metadata, metadata_present = load_json(os.path.join(snapshot_dir, "metadata.json"), {}, "metadata.json")
catalog_services, catalog_present = load_json(
    os.path.join(snapshot_dir, "consul", "catalog-services.json"),
    {},
    "consul/catalog-services.json",
)
health_checks, checks_present = load_json(
    os.path.join(snapshot_dir, "consul", "health-state-any.json"),
    [],
    "consul/health-state-any.json",
)
nomad_jobs, jobs_present = load_json(
    os.path.join(snapshot_dir, "nomad", "jobs.json"),
    [],
    "nomad/jobs.json",
)
nomad_allocations, allocations_present = load_json(
    os.path.join(snapshot_dir, "nomad", "allocations.json"),
    [],
    "nomad/allocations.json",
)
nomad_nodes, nodes_present = load_json(
    os.path.join(snapshot_dir, "nomad", "nodes.json"),
    [],
    "nomad/nodes.json",
)
redis_activity, redis_activity_present = load_json(
    os.path.join(snapshot_dir, "redis", "activity.json"),
    {"activities": []},
    "redis/activity.json",
)
manifest_entries, manifest_present = load_manifest(manifest_file)

metadata = normalize_object(metadata, "metadata.json")
catalog_services = normalize_object(catalog_services, "consul/catalog-services.json")
health_checks = normalize_array(health_checks, "consul/health-state-any.json")
nomad_jobs = normalize_array(nomad_jobs, "nomad/jobs.json")
nomad_allocations = normalize_array(nomad_allocations, "nomad/allocations.json")
nomad_nodes = normalize_array(nomad_nodes, "nomad/nodes.json")
redis_activity = normalize_object(redis_activity, "redis/activity.json")
redis_activity["activities"] = normalize_array(redis_activity.get("activities"), "redis/activity.json.activities")

snapshot_captured_at = metadata.get("captured_at") or redis_activity.get("captured_at")
snapshot_captured_dt = parse_rfc3339(snapshot_captured_at)

manifest_by_service = {entry["service_name"]: entry for entry in manifest_entries}
manifest_by_job = {entry["job_name"]: entry for entry in manifest_entries}
jobs_by_name = {
    (job.get("Name") or job.get("ID")): job
    for job in nomad_jobs
    if job.get("Name") or job.get("ID")
}
nodes_by_id = {
    node.get("ID"): node
    for node in nomad_nodes
    if node.get("ID")
}
activity_by_service = {
    record.get("service"): record
    for record in redis_activity.get("activities", [])
    if record.get("service")
}

checks_by_service = {}
for check in health_checks:
    service_name = check.get("ServiceName") or ""
    if not service_name.startswith(workload_prefix):
        continue
    checks_by_service.setdefault(service_name, []).append(check)

stale_services = sorted(
    set(
        [service for service in catalog_services.keys() if service.startswith(workload_prefix)]
        + list(checks_by_service.keys())
    )
)

details = []
cause_counts = {
    "re-wake-likely": 0,
    "missed-scale-down-likely": 0,
    "deregistration-race-likely": 0,
}
services_by_cause = {
    "re-wake-likely": [],
    "missed-scale-down-likely": [],
    "deregistration-race-likely": [],
}
active_nomad_services = []
inactive_nomad_services = []

for service_name in stale_services:
    manifest_entry = manifest_by_service.get(service_name) or manifest_by_job.get(service_name)
    job_name = (manifest_entry or {}).get("job_name") or service_name
    job = jobs_by_name.get(job_name)
    job_summary = summarize_job_groups(job or {})
    job_allocations = [alloc for alloc in nomad_allocations if alloc.get("JobID") == job_name]
    job_allocations.sort(key=lambda alloc: alloc.get("CreateTime") or 0, reverse=True)

    active_allocations = [
        alloc
        for alloc in job_allocations
        if (alloc.get("DesiredStatus") == "run" and alloc.get("ClientStatus") in {"pending", "running"})
    ]
    terminal_allocations = [
        alloc
        for alloc in job_allocations
        if alloc not in active_allocations
    ]

    active_nomad = bool(active_allocations) or any(
        job_summary.get(field, 0) > 0 for field in ("running", "starting", "queued")
    )

    service_checks = checks_by_service.get(service_name, [])
    projected_checks = [project_check(check) for check in service_checks]
    critical_consul_check_count = sum(
        1 for check in service_checks if (check.get("Status") or "").lower() == "critical"
    )

    catalog_tags = catalog_services.get(service_name) or []
    check_tags = []
    for check in service_checks:
        check_tags.extend(check.get("ServiceTags") or [])
    consul_tags = unique_tags(catalog_tags, check_tags)

    workload_class = (manifest_entry or {}).get("workload_class") or maybe_class_from_tags(consul_tags)
    activity_record = activity_by_service.get(service_name)
    last_activity = (activity_record or {}).get("last_activity")
    last_activity_dt = parse_rfc3339(last_activity)
    last_activity_age_seconds = None
    if last_activity_dt and snapshot_captured_dt:
        last_activity_age_seconds = max(0, int((snapshot_captured_dt - last_activity_dt).total_seconds()))

    likely_cause, diagnostic_signals = diagnose(
        active_nomad=active_nomad,
        last_activity_present=last_activity is not None,
        last_activity_age_seconds=last_activity_age_seconds,
    )

    if active_allocations:
        diagnostic_signals.append("nomad-active-allocations-present")
    if critical_consul_check_count > 0:
        diagnostic_signals.append("critical-consul-check-present")

    projected_active_allocations = [project_allocation(alloc, nodes_by_id) for alloc in active_allocations]
    projected_terminal_allocations = [project_allocation(alloc, nodes_by_id) for alloc in terminal_allocations]

    projected_nodes = []
    seen_node_ids = set()
    for alloc in job_allocations:
        node_id = alloc.get("NodeID")
        if not node_id or node_id in seen_node_ids:
            continue
        seen_node_ids.add(node_id)
        node = nodes_by_id.get(node_id)
        node_allocations = [item for item in job_allocations if item.get("NodeID") == node_id]
        node_active_allocations = [
            item
            for item in node_allocations
            if item.get("DesiredStatus") == "run" and item.get("ClientStatus") in {"pending", "running"}
        ]
        projected_nodes.append(
            {
                "node_id": node_id,
                "node_name": clean_text(alloc.get("NodeName")) or clean_text((node or {}).get("Name")),
                "status": (node or {}).get("Status"),
                "datacenter": (node or {}).get("Datacenter"),
                "drain": (node or {}).get("Drain"),
                "scheduling_eligibility": (node or {}).get("SchedulingEligibility"),
                "allocation_count": len(node_allocations),
                "active_allocation_count": len(node_active_allocations),
                "terminal_allocation_count": len(node_allocations) - len(node_active_allocations),
            }
        )

    projected_nodes.sort(key=lambda item: ((item.get("node_name") or ""), item.get("node_id") or ""))

    details.append(
        {
            "service_name": service_name,
            "job_name": job_name,
            "workload_class": workload_class,
            "workload_ordinal": (manifest_entry or {}).get("workload_ordinal"),
            "job_spec_key": (manifest_entry or {}).get("job_spec_key"),
            "manifest_present": manifest_entry is not None,
            "consul_service_present": service_name in catalog_services,
            "consul_tags": consul_tags,
            "redis_activity": {
                "key": clean_text((activity_record or {}).get("key")),
                "last_activity": last_activity,
                "age_seconds": last_activity_age_seconds,
                "present": last_activity is not None,
            },
            "nomad_job": project_job(job),
            "nomad_allocations": {
                "total_count": len(job_allocations),
                "active_count": len(active_allocations),
                "terminal_count": len(terminal_allocations),
                "client_status_counts": count_by(job_allocations, "ClientStatus"),
                "desired_status_counts": count_by(job_allocations, "DesiredStatus"),
                "active": projected_active_allocations,
                "terminal": projected_terminal_allocations,
            },
            "nomad_client_nodes": projected_nodes,
            "consul_checks": projected_checks,
            "critical_consul_check_count": critical_consul_check_count,
            "control_plane_state": "nomad-active" if active_nomad else "nomad-inactive",
            "likely_cause": likely_cause,
            "diagnostic_signals": sorted(set(diagnostic_signals)),
        }
    )

    cause_counts[likely_cause] += 1
    services_by_cause[likely_cause].append(service_name)
    if active_nomad:
        active_nomad_services.append(service_name)
    else:
        inactive_nomad_services.append(service_name)

for services in services_by_cause.values():
    services.sort()

active_nomad_services.sort()
inactive_nomad_services.sort()

result = {
    "generated_at": utc_now(),
    "snapshot_label": metadata.get("label"),
    "snapshot_captured_at": snapshot_captured_at,
    "idle_timeout_seconds": idle_timeout_seconds,
    "workload_prefix": workload_prefix,
    "sources": {
        "manifest_present": manifest_present,
        "catalog_services_present": catalog_present,
        "health_checks_present": checks_present,
        "nomad_jobs_present": jobs_present,
        "nomad_allocations_present": allocations_present,
        "nomad_nodes_present": nodes_present,
        "redis_activity_present": redis_activity_present,
    },
    "errors": errors,
    "stale_workload_detail_count": len(details),
    "diagnosis_summary": {
        "cause_counts": cause_counts,
        "active_nomad_service_count": len(active_nomad_services),
        "inactive_nomad_service_count": len(inactive_nomad_services),
        "active_nomad_services": active_nomad_services,
        "inactive_nomad_services": inactive_nomad_services,
        "services_by_cause": services_by_cause,
    },
    "stale_workload_details": details,
}

json.dump(result, sys.stdout, separators=(",", ":"))
PY
