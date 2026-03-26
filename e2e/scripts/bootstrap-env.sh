#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

wait_for_bootstrap_file() {
  file_path="$1"
  timeout_seconds="${2:-120}"

  start="$(date +%s)"
  while [ ! -f "$file_path" ]; do
    now="$(date +%s)"
    if [ $((now - start)) -ge "$timeout_seconds" ]; then
      echo "Timed out waiting for bootstrap file: $file_path" >&2
      return 1
    fi
    sleep 2
  done
}

load_bootstrap_env() {
  file_path="$1"
  timeout_seconds="${2:-120}"

  wait_for_bootstrap_file "$file_path" "$timeout_seconds"

  set -a
  # shellcheck disable=SC1090
  . "$file_path"
  set +a
}