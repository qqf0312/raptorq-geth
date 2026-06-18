#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

COUNT="${1:-10}"
TO_MODE="${2:-sequential}"
RPC_URL="${RPC_URL:-http://127.0.0.1:8545}"
FROM="${FROM:-0x92424e4f86201c931ba53eb48619d93244c18e13}"
START_INDEX="${START_INDEX:-1}"
VALUE="${VALUE:-0x1}"
GAS="${GAS:-0x5208}"
GAS_PRICE="${GAS_PRICE:-0x3b9aca00}"
SLEEP_BETWEEN_TXS="${SLEEP_BETWEEN_TXS:-0}"
RANDOM_SEED="${RANDOM_SEED:-partitioned-shadow}"

rpc() {
	local payload="$1"
	curl -s --max-time 10 -H 'Content-Type: application/json' --data "$payload" "$RPC_URL"
}

hex_result() {
	sed -n 's/.*"result":"\([^"]*\)".*/\1/p'
}

if ! [[ "$COUNT" =~ ^[0-9]+$ ]] || [ "$COUNT" -lt 1 ]; then
	echo "usage: $0 <positive-tx-count> [sequential|random]" >&2
	exit 1
fi
if [ "$TO_MODE" != "sequential" ] && [ "$TO_MODE" != "random" ]; then
	echo "usage: $0 <positive-tx-count> [sequential|random]" >&2
	exit 1
fi

to_address() {
	local idx="$1"
	if [ "$TO_MODE" = "random" ]; then
		printf '%s:%s' "$RANDOM_SEED" "$idx" | sha256sum | awk '{print "0x" substr($1, 1, 40)}'
	else
		printf '0x100000000000000000000000%016x' "$idx"
	fi
}

nonce_hex="$(rpc "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getTransactionCount\",\"params\":[\"${FROM}\",\"pending\"],\"id\":1}" | hex_result)"
if [ -z "$nonce_hex" ]; then
	echo "ERROR: failed to fetch pending nonce from ${RPC_URL}" >&2
	exit 1
fi
base_nonce="$((16#${nonce_hex#0x}))"

echo "Sending ${COUNT} txs from ${FROM} via ${RPC_URL}, base_nonce=${base_nonce}, to_mode=${TO_MODE}"

for offset in $(seq 0 "$((COUNT - 1))"); do
	idx="$((START_INDEX + offset))"
	nonce="$((base_nonce + offset))"
	nonce_hex="$(printf '0x%x' "$nonce")"
	to="$(to_address "$idx")"
	payload="{\"jsonrpc\":\"2.0\",\"method\":\"eth_sendTransaction\",\"params\":[{\"from\":\"${FROM}\",\"to\":\"${to}\",\"value\":\"${VALUE}\",\"gas\":\"${GAS}\",\"gasPrice\":\"${GAS_PRICE}\",\"nonce\":\"${nonce_hex}\"}],\"id\":${idx}}"
	response="$(rpc "$payload")"
	echo "tx ${offset}/${COUNT}: nonce=${nonce_hex} to=${to} response=${response}"
	if [ "$SLEEP_BETWEEN_TXS" != "0" ]; then
		sleep "$SLEEP_BETWEEN_TXS"
	fi
done

echo "Done. Check pending txs with:"
echo "curl -s -H 'Content-Type: application/json' --data '{\"jsonrpc\":\"2.0\",\"method\":\"txpool_status\",\"params\":[],\"id\":1}' ${RPC_URL}"
