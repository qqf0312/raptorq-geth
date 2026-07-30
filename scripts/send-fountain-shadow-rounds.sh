#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

ROUNDS="${ROUNDS:-3}"
KEY_COUNT="${KEY_COUNT:-12}"
HOT_PERCENT="${HOT_PERCENT:-30}"
WAIT_BLOCKS="${WAIT_BLOCKS:-1}"
EPOCH_LENGTH="${EPOCH_LENGTH:-4}"
RPC_URL="${RPC_URL:-http://127.0.0.1:8545}"

if [ "$HOT_PERCENT" -lt 0 ] || [ "$HOT_PERCENT" -gt 100 ]; then
	echo "HOT_PERCENT must be between 0 and 100" >&2
	exit 1
fi

hot_count="$(((KEY_COUNT * HOT_PERCENT + 99) / 100))"
if [ "$hot_count" -gt "$KEY_COUNT" ]; then
	hot_count="$KEY_COUNT"
fi
cold_count="$((KEY_COUNT - hot_count))"

rpc() {
	local payload="$1"
	curl --noproxy '*' -s --max-time 10 -H 'Content-Type: application/json' --data "$payload" "$RPC_URL"
}

block_number() {
	local value
	value="$(rpc '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | sed -n 's/.*"result":"\([^"]*\)".*/\1/p')"
	[ -n "$value" ] || return 1
	printf '%d' "$((16#${value#0x}))"
}

wait_for_blocks() {
	local start="$1"
	local target="$((start + WAIT_BLOCKS))"
	local current
	while true; do
		current="$(block_number)" || {
			sleep 1
			continue
		}
		[ "$current" -ge "$target" ] && return
		sleep 1
	done
}

wait_for_height() {
	local target="$1"
	local current
	while true; do
		current="$(block_number)" || {
			sleep 1
			continue
		}
		[ "$current" -ge "$target" ] && return
		sleep 1
	done
}

for round in $(seq 1 "$ROUNDS"); do
	start="$(block_number)"
	cold_start="$((hot_count + (round - 1) * cold_count + 1))"
	echo "round=${round} startBlock=${start} keys=${KEY_COUNT} hot=${hot_count} cold=${cold_count}"
	if [ "$hot_count" -gt 0 ]; then
		START_INDEX=1 VALUE="$(printf '0x%x' "$round")" \
			./scripts/send-partitioned-shadow-txs.sh "$hot_count" sequential
	fi
	if [ "$cold_count" -gt 0 ]; then
		START_INDEX="$cold_start" VALUE="$(printf '0x%x' "$round")" \
			./scripts/send-partitioned-shadow-txs.sh "$cold_count" sequential
	fi
	wait_for_blocks "$start"
	echo "round=${round} minedBlock=$(block_number)"
done

echo "Waiting for the next epoch boundary"
start="$(block_number)"
target="$((((start + EPOCH_LENGTH - 1) / EPOCH_LENGTH) * EPOCH_LENGTH))"
wait_for_height "$target"
echo "finalBlock=$(block_number)"
