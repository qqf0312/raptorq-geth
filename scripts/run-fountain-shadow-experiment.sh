#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

RUN_DIR="${RUN_DIR:-data/fountain-run}"
GETH_BIN="${GETH_BIN:-/tmp/fountain-geth}"
GOCACHE="${GOCACHE:-/tmp/go-build-cache}"
EPOCH_LENGTH="${EPOCH_LENGTH:-4}"
FINAL_SETTLE_SECONDS="${FINAL_SETTLE_SECONDS:-4}"
STOP_MINING_AFTER_WORKLOAD="${STOP_MINING_AFTER_WORKLOAD:-false}"

stop_nodes() {
	RUN_DIR="$RUN_DIR" scripts/stop-partitioned-shadow-4nodes.sh
}
trap stop_nodes EXIT

GETH_BIN="$GETH_BIN" RUN_DIR="$RUN_DIR" EPOCH_LENGTH="$EPOCH_LENGTH" MINIMUM_ROWS="${MINIMUM_ROWS:-4}" \
	scripts/start-fountain-shadow-4nodes.sh

sleep 4
ROUNDS="${ROUNDS:-3}" KEY_COUNT="${KEY_COUNT:-12}" HOT_PERCENT="${HOT_PERCENT:-30}" \
	WAIT_BLOCKS="${WAIT_BLOCKS:-1}" EPOCH_LENGTH="$EPOCH_LENGTH" scripts/send-fountain-shadow-rounds.sh
if [[ "$STOP_MINING_AFTER_WORKLOAD" == "true" ]]; then
	curl --noproxy '*' -s --max-time 5 -H 'Content-Type: application/json' \
		--data '{"jsonrpc":"2.0","method":"miner_stop","params":[],"id":1}' \
		"http://127.0.0.1:8545" >/dev/null
fi
sleep "$FINAL_SETTLE_SECONDS"

echo "Final live node status"
for idx in 1 2 3 4; do
	port="$((8544 + idx))"
	block="$(curl --noproxy '*' -s --max-time 5 -H 'Content-Type: application/json' \
		--data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
		"http://127.0.0.1:${port}" |
		sed -n 's/.*"result":"\([^"]*\)".*/\1/p')"
	peers="$(curl --noproxy '*' -s --max-time 5 -H 'Content-Type: application/json' \
		--data '{"jsonrpc":"2.0","method":"net_peerCount","params":[],"id":1}' \
		"http://127.0.0.1:${port}" |
		sed -n 's/.*"result":"\([^"]*\)".*/\1/p')"
	echo "node${idx} block=${block:-?} peers=${peers:-?}"
done

echo "Epoch summaries"
for idx in 1 2 3 4; do
	echo "node${idx}:"
	grep 'Fountain MPT shadow epoch finalized' "${RUN_DIR}/node${idx}.log" | tail -8 || true
done

stop_nodes
trap - EXIT

echo "Persisted proof audit"
env GOCACHE="$GOCACHE" go run ./cmd/fountainmptstats -nodes 4
