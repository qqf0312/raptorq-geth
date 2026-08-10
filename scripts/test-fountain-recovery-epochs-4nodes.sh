#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

GETH_BIN="${GETH_BIN:-/tmp/fountain-geth}"
EPOCH_LENGTH="${EPOCH_LENGTH:-8}"
EPOCH_COUNT="${EPOCH_COUNT:-4}"
TXS_PER_BLOCK="${TXS_PER_BLOCK:-100}"
MINIMUM_ROWS="${MINIMUM_ROWS:-4}"
RUN_DIR="${RUN_DIR:-data/fountain-run}"
SEND_LOG="${RUN_DIR}/epoch-recovery-transactions.log"
RESULT_LOG="${RUN_DIR}/epoch-recovery-results.log"

rpc() {
	local port="$1"
	local payload="$2"
	curl --noproxy '*' -s --max-time 60 -H 'Content-Type: application/json' --data "$payload" "http://127.0.0.1:${port}"
}

block_number() {
	local response value
	response="$(rpc 8545 '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}')"
	value="$(printf '%s' "$response" | sed -n 's/.*"result":"\([^"]*\)".*/\1/p')"
	[ -n "$value" ] || return 1
	printf '%d\n' "$((16#${value#0x}))"
}

wait_for_height() {
	local port="$1"
	local target="$2"
	for _ in $(seq 1 2400); do
		local response value height
		response="$(rpc "$port" '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}')"
		value="$(printf '%s' "$response" | sed -n 's/.*"result":"\([^"]*\)".*/\1/p')"
		if [ -n "$value" ]; then
			height="$((16#${value#0x}))"
			[ "$height" -ge "$target" ] && return 0
		fi
		sleep 0.05
	done
	echo "timeout waiting for port ${port} at height ${target}" >&2
	return 1
}

stop_nodes() {
	RUN_DIR="$RUN_DIR" scripts/stop-partitioned-shadow-4nodes.sh >/dev/null 2>&1 || true
}
trap stop_nodes EXIT

GETH_BIN="$GETH_BIN" RUN_DIR="$RUN_DIR" GENESIS=data/genesis-fountain-bandwidth-test.json \
	EPOCH_LENGTH="$EPOCH_LENGTH" MINIMUM_ROWS="$MINIMUM_ROWS" AUTO_MINE=0 \
	MINER_GAS_LIMIT=2100000 TXPOOL_QUEUE_LIMIT=4096 \
	scripts/start-fountain-shadow-4nodes.sh
: >"$SEND_LOG"
: >"$RESULT_LOG"

peers_json="$(rpc 8545 '{"jsonrpc":"2.0","method":"admin_peers","params":[],"id":1}')"
mapfile -t peer_ids < <(printf '%s' "$peers_json" | grep -o '"id":"[^"]*"' | sed 's/"id":"//;s/"$//' | sort -u)
if [ "${#peer_ids[@]}" -eq 0 ]; then
	echo "node1 has no peers: ${peers_json}" >&2
	exit 1
fi

declare -a enodes
for idx in 1 2 3 4; do
	info="$(rpc "$((8544 + idx))" '{"jsonrpc":"2.0","method":"admin_nodeInfo","params":[],"id":1}')"
	enodes[$idx]="$(printf '%s' "$info" | sed -n 's/.*"enode":"\([^"]*\)".*/\1/p')"
done
for from in 1 2 3 4; do
	for to in 1 2 3 4; do
		[ "$from" -eq "$to" ] && continue
		rpc "$((8544 + from))" "{\"jsonrpc\":\"2.0\",\"method\":\"admin_addPeer\",\"params\":[\"${enodes[$to]}\"],\"id\":1}" >/dev/null
	done
done
sleep 2

recover_epoch() {
	local boundary="$1"
	local boundary_hex root block success response
	boundary_hex="$(printf '0x%x' "$boundary")"
	block="$(rpc 8545 "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockByNumber\",\"params\":[\"${boundary_hex}\",false],\"id\":1}")"
	root="$(printf '%s' "$block" | sed -n 's/.*"stateRoot":"\([^"]*\)".*/\1/p')"
	[ -n "$root" ] || { echo "missing state root at ${boundary}" >&2; return 1; }
	success=false
	for index in $(seq 1 "$TXS_PER_BLOCK"); do
		local address key_response key peer_id payload
		address="$(printf '0x100000000000000000000000%016x' "$index")"
		key_response="$(rpc 8545 "{\"jsonrpc\":\"2.0\",\"method\":\"web3_sha3\",\"params\":[\"${address}\"],\"id\":1}")"
		key="$(printf '%s' "$key_response" | sed -n 's/.*"result":"\([^"]*\)".*/\1/p')"
		[ -n "$key" ] || continue
		for peer_id in "${peer_ids[@]}"; do
			payload="{\"jsonrpc\":\"2.0\",\"method\":\"debug_requestFountainMPTPathLocal\",\"params\":[\"${peer_id}\",\"${root}\",\"${key}\"],\"id\":${index}}"
			response="$(rpc 8545 "$payload")"
			if printf '%s' "$response" | grep -q '"ipaValidated":true'; then
				echo "EPOCH_RECOVERY epoch=$((boundary / EPOCH_LENGTH)) height=${boundary} key=${key} peer=${peer_id:0:16} response=${response}" | tee -a "$RESULT_LOG"
				success=true
				break 2
			fi
		done
	done
	[ "$success" = true ] || { echo "recovery failed at epoch boundary ${boundary}" >&2; return 1; }
}

total_blocks="$((EPOCH_LENGTH * EPOCH_COUNT))"
echo "experiment epochs=${EPOCH_COUNT} epochLength=${EPOCH_LENGTH} blocks=${total_blocks} txsPerBlock=${TXS_PER_BLOCK} totalTxs=$((total_blocks * TXS_PER_BLOCK)) rows=${MINIMUM_ROWS}"

for data_block in $(seq 1 "$total_blocks"); do
	VALUE="$(printf '0x%x' "$data_block")" START_INDEX=1 \
		scripts/send-partitioned-shadow-txs.sh "$TXS_PER_BLOCK" sequential >>"$SEND_LOG"
	echo "queued dataBlock=${data_block}/${total_blocks} txs=${TXS_PER_BLOCK}"
done

status="$(rpc 8545 '{"jsonrpc":"2.0","method":"txpool_status","params":[],"id":1}')"
echo "txpool before mining: ${status}"
pending_hex="$(printf '%s' "$status" | sed -n 's/.*"pending":"\([^"]*\)".*/\1/p')"
pending="$((16#${pending_hex#0x}))"
if [ "$pending" -ne "$((total_blocks * TXS_PER_BLOCK))" ]; then
	echo "txpool has ${pending} executable transactions, expected $((total_blocks * TXS_PER_BLOCK))" >&2
	exit 1
fi
rpc 8545 '{"jsonrpc":"2.0","method":"miner_start","params":[],"id":1}' >/dev/null
(wait_for_height 8545 "$total_blocks" && rpc 8545 '{"jsonrpc":"2.0","method":"miner_stop","params":[],"id":1}' >/dev/null) &
stop_watcher_pid="$!"

for epoch in $(seq 1 "$EPOCH_COUNT"); do
	boundary="$((epoch * EPOCH_LENGTH))"
	wait_for_height 8545 "$boundary"
	for port in 8546 8547 8548; do
		wait_for_height "$port" "$boundary"
	done
	for height in $(seq "$(((epoch - 1) * EPOCH_LENGTH + 1))" "$boundary"); do
		height_hex="$(printf '0x%x' "$height")"
		count_response="$(rpc 8545 "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockTransactionCountByNumber\",\"params\":[\"${height_hex}\"],\"id\":1}")"
		count_hex="$(printf '%s' "$count_response" | sed -n 's/.*"result":"\([^"]*\)".*/\1/p')"
		count="$((16#${count_hex#0x}))"
		if [ "$count" -ne "$TXS_PER_BLOCK" ]; then
			echo "height ${height} has ${count} txs, expected ${TXS_PER_BLOCK}" >&2
			exit 1
		fi
	done
	echo "epoch=${epoch}/${EPOCH_COUNT} boundary=${boundary} verifiedBlocks=${EPOCH_LENGTH} txsPerBlock=${TXS_PER_BLOCK}"
	sleep 2
	recover_epoch "$boundary"
done

wait "$stop_watcher_pid"

echo "REAL_FOUNTAIN_FOUR_EPOCH_RECOVERY_OK"
