#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

GENESIS="${GENESIS:-data/genesis.json}"
NETWORK_ID="${NETWORK_ID:-12345}"
PARTITIONS="${PARTITIONS:-4}"
VERBOSITY="${VERBOSITY:-3}"
RUN_DIR="${RUN_DIR:-data/run}"
GOCACHE="${GOCACHE:-/tmp/go-build-cache}"

SIGNER="0x92424e4f86201c931ba53eb48619d93244c18e13"

mkdir -p "$RUN_DIR"

GO_BIN="${GO_BIN:-$(command -v go || true)}"
if [ -z "$GO_BIN" ] && [ -x /usr/local/go/bin/go ]; then
	GO_BIN=/usr/local/go/bin/go
fi

GETH_BIN="${GETH_BIN:-}"
if [ -z "$GETH_BIN" ] && [ -x build/bin/geth ]; then
	GETH_BIN=build/bin/geth
fi

if [ -z "$GETH_BIN" ] && [ -z "$GO_BIN" ]; then
	echo "ERROR: neither build/bin/geth nor go was found. Set GETH_BIN=/path/to/geth or GO_BIN=/path/to/go." >&2
	exit 1
fi

run_geth() {
	if [ -n "$GETH_BIN" ]; then
		"$GETH_BIN" "$@"
	else
		env GOCACHE="$GOCACHE" "$GO_BIN" run ./cmd/geth "$@"
	fi
}

rpc() {
	local port="$1"
	local payload="$2"
	curl -s --max-time 5 -H 'Content-Type: application/json' --data "$payload" "http://127.0.0.1:${port}"
}

rpc_alive() {
	local port="$1"
	rpc "$port" '{"jsonrpc":"2.0","method":"web3_clientVersion","params":[],"id":1}' | grep -q '"result"'
}

wait_rpc() {
	local port="$1"
	local name="$2"
	local pid="${3:-}"
	local log_file="${4:-}"
	for _ in $(seq 1 60); do
		if rpc_alive "$port"; then
			return 0
		fi
		if [ -n "$pid" ] && ! kill -0 "$pid" 2>/dev/null; then
			echo "ERROR: ${name} exited before RPC became ready on port ${port}" >&2
			if [ -n "$log_file" ] && [ -f "$log_file" ]; then
				echo "----- ${log_file} -----" >&2
				tail -80 "$log_file" >&2
			fi
			return 1
		fi
		sleep 1
	done
	echo "ERROR: ${name} RPC did not become ready on port ${port}" >&2
	if [ -n "$log_file" ] && [ -f "$log_file" ]; then
		echo "----- ${log_file} -----" >&2
		tail -80 "$log_file" >&2
	fi
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

init_node() {
	local datadir="$1"
	if [ ! -f "${datadir}/geth/chaindata/CURRENT" ]; then
		echo "Initializing ${datadir}"
		run_geth --datadir "$datadir" init "$GENESIS" >/dev/null
	fi
}

start_node() {
	local idx="$1"
	local datadir="data/node${idx}"
	local p2p_port="$((30302 + idx))"
	local http_port="$((8544 + idx))"
	local auth_port="$((8550 + idx))"
	local node_partition="$((idx - 1))"
	local log_file="${RUN_DIR}/node${idx}.log"
	local pid_file="${RUN_DIR}/node${idx}.pid"

	if rpc_alive "$http_port"; then
		echo "node${idx} already running on http://127.0.0.1:${http_port}"
		return 0
	fi

	init_node "$datadir"
	echo "Starting node${idx}: http=${http_port}, p2p=${p2p_port}, nodepartition=${node_partition}, coldtrieshadow=on"

	local args=(
		--networkid "$NETWORK_ID"
		--datadir "$datadir"
		--port "$p2p_port"
		--http
		--http.addr 127.0.0.1
		--http.port "$http_port"
		--http.api admin,eth,net,web3,personal,miner,txpool
		--authrpc.port "$auth_port"
		--nodiscover
		--partitionedmptshadow
		--partitionedmptshadow.partitions "$PARTITIONS"
		--partitionedmptshadow.nodepartition "$node_partition"
		--coldtrieshadow
		--verbosity "$VERBOSITY"
	)

	if [ "$idx" -eq 1 ]; then
		args+=(
			--unlock "$SIGNER"
			--password data/node1/password.txt
			--allow-insecure-unlock
			--mine
			--miner.etherbase "$SIGNER"
		)
	fi

	if [ -n "$GETH_BIN" ]; then
		nohup "$GETH_BIN" "${args[@]}" >"$log_file" 2>&1 &
	else
		nohup env GOCACHE="$GOCACHE" "$GO_BIN" run ./cmd/geth "${args[@]}" >"$log_file" 2>&1 &
	fi
	pid="$!"
	echo "$pid" >"$pid_file"
	wait_rpc "$http_port" "node${idx}" "$pid" "$log_file"
}

for idx in 1 2 3 4; do
	start_node "$idx"
done

echo "Collecting enodes"
ENODE1="$(node_enode 8545)"
ENODE2="$(node_enode 8546)"
ENODE3="$(node_enode 8547)"
ENODE4="$(node_enode 8548)"

echo "Connecting peers"
add_peer 8545 "$ENODE2"
add_peer 8545 "$ENODE3"
add_peer 8545 "$ENODE4"
add_peer 8546 "$ENODE1"
add_peer 8547 "$ENODE1"
add_peer 8548 "$ENODE1"
add_peer 8547 "$ENODE2"
add_peer 8548 "$ENODE2"

echo "Status"
for idx in 1 2 3 4; do
	port="$((8544 + idx))"
	block="$(rpc "$port" '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | sed -n 's/.*"result":"\([^"]*\)".*/\1/p')"
	peers="$(rpc "$port" '{"jsonrpc":"2.0","method":"net_peerCount","params":[],"id":1}' | sed -n 's/.*"result":"\([^"]*\)".*/\1/p')"
	echo "node${idx}: http://127.0.0.1:${port} block=${block:-?} peers=${peers:-?} log=${RUN_DIR}/node${idx}.log coldtriedata=data/node${idx}/geth/coldtriedata"
done
