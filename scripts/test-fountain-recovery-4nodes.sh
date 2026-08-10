#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

GETH_BIN="${GETH_BIN:-/tmp/fountain-geth}"
EPOCH_LENGTH="${EPOCH_LENGTH:-4}"
MINIMUM_ROWS="${MINIMUM_ROWS:-4}"
KEY_COUNT="${KEY_COUNT:-8}"
ROUNDS="${ROUNDS:-2}"
HOT_PERCENT="${HOT_PERCENT:-30}"
RUN_DIR="${RUN_DIR:-data/fountain-run}"

rpc() {
	local port="$1"
	local payload="$2"
	curl --noproxy '*' -s --max-time 30 -H 'Content-Type: application/json' --data "$payload" "http://127.0.0.1:${port}"
}

stop_nodes() {
	RUN_DIR="$RUN_DIR" scripts/stop-partitioned-shadow-4nodes.sh >/dev/null 2>&1 || true
}
trap stop_nodes EXIT

GETH_BIN="$GETH_BIN" RUN_DIR="$RUN_DIR" EPOCH_LENGTH="$EPOCH_LENGTH" MINIMUM_ROWS="$MINIMUM_ROWS" \
	scripts/start-fountain-shadow-4nodes.sh

sleep 4
ROUNDS="$ROUNDS" KEY_COUNT="$KEY_COUNT" HOT_PERCENT="$HOT_PERCENT" WAIT_BLOCKS=1 EPOCH_LENGTH="$EPOCH_LENGTH" \
	scripts/send-fountain-shadow-rounds.sh

rpc 8545 '{"jsonrpc":"2.0","method":"miner_stop","params":[],"id":1}' >/dev/null
sleep 5

min_height=-1
for idx in 1 2 3 4; do
	port="$((8544 + idx))"
	hex_height="$(rpc "$port" '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | sed -n 's/.*"result":"\([^"]*\)".*/\1/p')"
	[ -n "$hex_height" ] || { echo "node${idx}: missing block height" >&2; exit 1; }
	height="$((16#${hex_height#0x}))"
	if [ "$min_height" -lt 0 ] || [ "$height" -lt "$min_height" ]; then
		min_height="$height"
	fi
	echo "node${idx} height=${height}"
done

boundary="$(((min_height / EPOCH_LENGTH) * EPOCH_LENGTH))"
if [ "$boundary" -lt 1 ]; then
	echo "no completed epoch boundary" >&2
	exit 1
fi
boundary_hex="$(printf '0x%x' "$boundary")"
block="$(rpc 8545 "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockByNumber\",\"params\":[\"${boundary_hex}\",false],\"id\":1}")"
root="$(printf '%s' "$block" | sed -n 's/.*"stateRoot":"\([^"]*\)".*/\1/p')"
[ -n "$root" ] || { echo "failed to resolve state root at ${boundary_hex}: ${block}" >&2; exit 1; }
echo "recovery boundary=${boundary} root=${root}"

peers_json="$(rpc 8545 '{"jsonrpc":"2.0","method":"admin_peers","params":[],"id":1}')"
mapfile -t peer_ids < <(printf '%s' "$peers_json" | grep -o '"id":"[^"]*"' | sed 's/"id":"//;s/"$//' | sort -u)
if [ "${#peer_ids[@]}" -eq 0 ]; then
	echo "node1 has no peers: ${peers_json}" >&2
	exit 1
fi
echo "node1 recovery peers=${#peer_ids[@]}"

success=false
for index in $(seq 1 "$KEY_COUNT"); do
	address="$(printf '0x100000000000000000000000%016x' "$index")"
	key_response="$(rpc 8545 "{\"jsonrpc\":\"2.0\",\"method\":\"web3_sha3\",\"params\":[\"${address}\"],\"id\":1}")"
	key="$(printf '%s' "$key_response" | sed -n 's/.*"result":"\([^"]*\)".*/\1/p')"
	[ -n "$key" ] || continue
	for peer_id in "${peer_ids[@]}"; do
		payload="{\"jsonrpc\":\"2.0\",\"method\":\"debug_requestFountainMPTPathLocal\",\"params\":[\"${peer_id}\",\"${root}\",\"${key}\"],\"id\":${index}}"
		response="$(rpc 8545 "$payload")"
		echo "attempt address=${address} key=${key} peer=${peer_id:0:16} response=${response}"
		if printf '%s' "$response" | grep -q '"ipaValidated":true'; then
			success=true
			break 2
		fi
	done
done

if [ "$success" != true ]; then
	echo "no remote fountain recovery request succeeded" >&2
	for idx in 1 2 3 4; do
		echo "node${idx} recent mptproof log:"
		grep -E 'MPT proof|fountain|Fountain' "${RUN_DIR}/node${idx}.log" | tail -30 || true
	done
	exit 1
fi

echo "REAL_FOUNTAIN_RECOVERY_OK"
