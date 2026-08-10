#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

GENESIS="${GENESIS:-data/genesis-fountain-test.json}"
NETWORK_ID="${NETWORK_ID:-12345}"
NODE_COUNT="${NODE_COUNT:-4}"
EPOCH_LENGTH="${EPOCH_LENGTH:-4}"
MINIMUM_ROWS="${MINIMUM_ROWS:-4}"
AUTO_MINE="${AUTO_MINE:-1}"
MINER_GAS_LIMIT="${MINER_GAS_LIMIT:-}"
TXPOOL_QUEUE_LIMIT="${TXPOOL_QUEUE_LIMIT:-}"
MINER_GAS_LIMIT="${MINER_GAS_LIMIT:-}"
TXPOOL_QUEUE_LIMIT="${TXPOOL_QUEUE_LIMIT:-}"
VERBOSITY="${VERBOSITY:-3}"
RUN_DIR="${RUN_DIR:-data/fountain-run}"
GOCACHE="${GOCACHE:-/tmp/go-build-cache}"
GETH_BIN="${GETH_BIN:-build/bin/geth}"

SIGNER="0x92424e4f86201c931ba53eb48619d93244c18e13"

mkdir -p "$RUN_DIR"

if [ ! -x "$GETH_BIN" ]; then
	echo "Building ${GETH_BIN}"
	env GOCACHE="$GOCACHE" go build -o "$GETH_BIN" ./cmd/geth
fi

rpc() {
	local port="$1"
	local payload="$2"
	curl --noproxy '*' -s --max-time 5 -H 'Content-Type: application/json' --data "$payload" "http://127.0.0.1:${port}"
}

rpc_alive() {
	local port="$1"
	rpc "$port" '{"jsonrpc":"2.0","method":"web3_clientVersion","params":[],"id":1}' | grep -q '"result"'
}

wait_rpc() {
	local port="$1"
	local name="$2"
	local pid="$3"
	local log_file="$4"
	for _ in $(seq 1 60); do
		if rpc_alive "$port"; then
			return 0
		fi
		if ! kill -0 "$pid" 2>/dev/null; then
			echo "ERROR: ${name} exited before RPC became ready" >&2
			tail -100 "$log_file" >&2
			return 1
		fi
		sleep 1
	done
	echo "ERROR: ${name} RPC timeout" >&2
	tail -100 "$log_file" >&2
	return 1
}

node_enode() {
	local port="$1"
	rpc "$port" '{"jsonrpc":"2.0","method":"admin_nodeInfo","params":[],"id":1}' |
		sed -n 's/.*"enode":"\([^"]*\)".*/\1/p'
}

add_peer() {
	local port="$1"
	local enode="$2"
	rpc "$port" "{\"jsonrpc\":\"2.0\",\"method\":\"admin_addPeer\",\"params\":[\"${enode}\"],\"id\":1}" >/dev/null
}

for idx in $(seq 1 "$NODE_COUNT"); do
	datadir="data/node${idx}"
	if [ ! -f "${datadir}/geth/chaindata/CURRENT" ]; then
		echo "Initializing ${datadir} from ${GENESIS}"
		"$GETH_BIN" --datadir "$datadir" init "$GENESIS" >/dev/null
	fi

	http_port="$((8544 + idx))"
	p2p_port="$((30302 + idx))"
	auth_port="$((8550 + idx))"
	node_index="$((idx - 1))"
	log_file="${RUN_DIR}/node${idx}.log"
	pid_file="${RUN_DIR}/node${idx}.pid"

	args=(
		--networkid "$NETWORK_ID"
		--datadir "$datadir"
		--port "$p2p_port"
		--http
		--http.addr 127.0.0.1
		--http.port "$http_port"
		--http.api admin,debug,eth,net,web3,personal,miner,txpool
		--authrpc.port "$auth_port"
		--nodiscover
		--fountainmptshadow
		--fountainmptshadow.epoch "$EPOCH_LENGTH"
		--fountainmptshadow.rows "$MINIMUM_ROWS"
		--fountainmptshadow.nodes "$NODE_COUNT"
		--fountainmptshadow.nodeindex "$node_index"
		--verbosity "$VERBOSITY"
	)
	if [ -n "$MINER_GAS_LIMIT" ]; then
		args+=(--miner.gaslimit "$MINER_GAS_LIMIT")
	fi
	if [ -n "$TXPOOL_QUEUE_LIMIT" ]; then
		args+=(
			--txpool.accountslots "$TXPOOL_QUEUE_LIMIT"
			--txpool.accountqueue "$TXPOOL_QUEUE_LIMIT"
			--txpool.globalslots "$TXPOOL_QUEUE_LIMIT"
			--txpool.globalqueue "$TXPOOL_QUEUE_LIMIT"
		)
	fi
	if [ -n "$MINER_GAS_LIMIT" ]; then
		args+=(--miner.gaslimit "$MINER_GAS_LIMIT")
	fi
	if [ -n "$TXPOOL_QUEUE_LIMIT" ]; then
		args+=(
			--txpool.accountslots "$TXPOOL_QUEUE_LIMIT"
			--txpool.accountqueue "$TXPOOL_QUEUE_LIMIT"
			--txpool.globalslots "$TXPOOL_QUEUE_LIMIT"
			--txpool.globalqueue "$TXPOOL_QUEUE_LIMIT"
		)
	fi
	if [ "$idx" -eq 1 ]; then
		args+=(
			--unlock "$SIGNER"
			--password data/node1/password.txt
			--allow-insecure-unlock
			--miner.etherbase "$SIGNER"
		)
		if [ "$AUTO_MINE" = "1" ]; then
			args+=(--mine)
		fi
	fi

	echo "Starting node${idx}: http=${http_port} p2p=${p2p_port} fountainNodeIndex=${node_index}"
	nohup "$GETH_BIN" "${args[@]}" >"$log_file" 2>&1 &
	pid="$!"
	echo "$pid" >"$pid_file"
	wait_rpc "$http_port" "node${idx}" "$pid" "$log_file"
done

declare -a ENODES
for idx in $(seq 1 "$NODE_COUNT"); do
	ENODES[$idx]="$(node_enode "$((8544 + idx))")"
done
for idx in $(seq 2 "$NODE_COUNT"); do
	add_peer 8545 "${ENODES[$idx]}"
	add_peer "$((8544 + idx))" "${ENODES[1]}"
done

echo "Fountain-only network ready"
for idx in $(seq 1 "$NODE_COUNT"); do
	port="$((8544 + idx))"
	block="$(rpc "$port" '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | sed -n 's/.*"result":"\([^"]*\)".*/\1/p')"
	peers="$(rpc "$port" '{"jsonrpc":"2.0","method":"net_peerCount","params":[],"id":1}' | sed -n 's/.*"result":"\([^"]*\)".*/\1/p')"
	echo "node${idx}: block=${block:-?} peers=${peers:-?} log=${RUN_DIR}/node${idx}.log data=data/node${idx}/geth/fountainmptdata"
done
