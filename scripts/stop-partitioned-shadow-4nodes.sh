#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

RUN_DIR="${RUN_DIR:-data/run}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-20}"

pid_running() {
	local pid="$1"
	[ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}

pid_matches_node() {
	local pid="$1"
	local idx="$2"
	local cmdline

	cmdline="$(ps -p "$pid" -o args= 2>/dev/null || true)"
	[ -n "$cmdline" ] || return 1
	case "$cmdline" in
		*"--datadir data/node${idx}"*|*"--datadir"*"data/node${idx}"*)
			return 0
			;;
	esac
	return 1
}

stop_pid() {
	local pid="$1"
	local label="$2"

	if ! pid_running "$pid"; then
		echo "${label}: pid ${pid} is not running"
		return 0
	fi

	echo "${label}: sending TERM to pid ${pid}"
	kill -TERM "$pid" 2>/dev/null || true

	for _ in $(seq 1 "$TIMEOUT_SECONDS"); do
		if ! pid_running "$pid"; then
			echo "${label}: stopped"
			return 0
		fi
		sleep 1
	done

	if pid_running "$pid"; then
		echo "${label}: still running after ${TIMEOUT_SECONDS}s; sending KILL"
		kill -KILL "$pid" 2>/dev/null || true
	fi
}

find_node_pids() {
	local idx="$1"
	local datadir="data/node${idx}"

	pgrep -f "go run ./cmd/geth .*--datadir ${datadir}" 2>/dev/null || true
	pgrep -f "/exe/geth .*--datadir ${datadir}" 2>/dev/null || true
}

for idx in 1 2 3 4; do
	pid_file="${RUN_DIR}/node${idx}.pid"
	seen=""

	if [ -f "$pid_file" ]; then
		pid="$(tr -d '[:space:]' <"$pid_file")"
		if pid_running "$pid" && pid_matches_node "$pid" "$idx"; then
			stop_pid "$pid" "node${idx}"
			seen="${seen} ${pid}"
		else
			echo "node${idx}: ignoring stale pid file ${pid_file}"
		fi
	fi

	for pid in $(find_node_pids "$idx"); do
		case " $seen " in
			*" ${pid} "*) continue ;;
		esac
		stop_pid "$pid" "node${idx}"
	done

	rm -f "$pid_file"
done

echo "Remaining matching geth processes:"
pgrep -af 'go run ./cmd/geth|/exe/geth' || true
