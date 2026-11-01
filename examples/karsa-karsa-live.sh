#!/usr/bin/env bash
# Runs the opt-in live tests against two local Karsa nodes. It starts the nodes,
# pairs them with `ferry connect`, runs the live tests, and tears everything
# down. Nothing is left running afterwards.
set -euo pipefail

KARSA_ROOT="${KARSA_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../karsa" && pwd)}"
FERRY_SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FERRY="${FERRY_BIN:-/tmp/ibc-ferry-live}"
BIN="${KARSAD_BIN:-/tmp/karsad-live}"
A=/tmp/karsa-live-a
B=/tmp/karsa-live-b
KEYRING=test
STATE="${FERRY_LIVE_STATE:-/tmp/ferry-live-state.db}"
CFG=/tmp/ferry-live.toml
BATCH="${FERRY_LIVE_BATCH:-3}"
RUN="${FERRY_LIVE_RUN:-^TestLive(Timeout|Watcher|CallbackGas)$}"
PID_A=""
PID_B=""

cleanup() {
  [[ -n "$PID_A" ]] && kill "$PID_A" 2>/dev/null || true
  [[ -n "$PID_B" ]] && kill "$PID_B" 2>/dev/null || true
  [[ -n "$PID_A" ]] && wait "$PID_A" 2>/dev/null || true
  [[ -n "$PID_B" ]] && wait "$PID_B" 2>/dev/null || true
}
trap cleanup EXIT

setup_chain() {
  local home="$1" chain="$2" rpc="$3" p2p="$4" grpc="$5" jsonrpc="$6"
  rm -rf "$home"
  "$BIN" init ibc --chain-id "$chain" --home "$home" >/dev/null 2>&1
  "$BIN" keys add val --keyring-backend "$KEYRING" --algo eth_secp256k1 --home "$home" >/dev/null 2>&1
  "$BIN" keys add relayer --keyring-backend "$KEYRING" --algo eth_secp256k1 --home "$home" >/dev/null 2>&1
  local val relayer
  val="$("$BIN" keys show val -a --keyring-backend "$KEYRING" --home "$home")"
  relayer="$("$BIN" keys show relayer -a --keyring-backend "$KEYRING" --home "$home")"
  "$BIN" genesis add-genesis-account "$val" 100000000000000000000000000akra \
    --keyring-backend "$KEYRING" --home "$home" >/dev/null
  "$BIN" genesis add-genesis-account "$relayer" 1000000000000000000000000akra \
    --keyring-backend "$KEYRING" --home "$home" >/dev/null
  "$BIN" genesis gentx val 1000000000000000000000akra \
    --gas-prices 20000000000akra --chain-id "$chain" --keyring-backend "$KEYRING" --home "$home" >/dev/null
  "$BIN" genesis collect-gentxs --home "$home" >/dev/null
  sed -i.bak 's/^type = "flood"/type = "app"/' "$home/config/config.toml"
  python3 - "$home/config/genesis.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p))
d["consensus"]["params"]["block"]["max_bytes"] = "3000000"
d["consensus"]["params"]["block"]["max_gas"] = "100000000"
json.dump(d, open(p, "w"), separators=(",", ":"))
PY
  "$BIN" start --home "$home" --chain-id "$chain" --keyring-backend "$KEYRING" \
    --minimum-gas-prices=20000000000akra --evm.min-tip=20000000000 --json-rpc.enable=true \
    --json-rpc.address "127.0.0.1:${jsonrpc}" \
    --json-rpc.ws-address "127.0.0.1:$((jsonrpc + 1))" \
    --json-rpc.api eth,txpool,personal,net,debug,web3 \
    --rpc.laddr "tcp://127.0.0.1:$rpc" --p2p.laddr "tcp://127.0.0.1:$p2p" \
    --grpc.address "127.0.0.1:$grpc" >"$home/node.log" 2>&1 &
  echo $!
}

wait_blocks() {
  local rpc="$1"
  for _ in $(seq 1 90); do
    out="$(curl -s -m 2 "http://127.0.0.1:$rpc/status" || true)"
    if [[ "$out" == *'"latest_block_height"'* && "$out" != *'"latest_block_height":"0"'* ]]; then
      return 0
    fi
    sleep 1
  done
  return 1
}

( cd "$KARSA_ROOT" && go build -o "$BIN" ./cmd/karsad )
( cd "$FERRY_SRC" && go build -o "$FERRY" . )

echo "setting up chain a"
PID_A="$(setup_chain "$A" karsa_262144-1 26667 26666 9095 18555)"
echo "setting up chain b"
PID_B="$(setup_chain "$B" karsa_262144-2 26668 26669 9096 18557)"

wait_blocks 26667 || { tail -40 "$A/node.log"; exit 1; }
wait_blocks 26668 || { tail -40 "$B/node.log"; exit 1; }
echo "both chains producing blocks"

cat > "$CFG" <<TOML
[chains.a]
binary = "$BIN"
home = "$A"
node = "tcp://127.0.0.1:26667"
rpc = "http://127.0.0.1:26667"
chain_id = "karsa_262144-1"
keyring_backend = "$KEYRING"
from = "relayer"
gas_prices = "20000000000akra"


[chains.b]
binary = "$BIN"
home = "$B"
node = "tcp://127.0.0.1:26668"
rpc = "http://127.0.0.1:26668"
chain_id = "karsa_262144-2"
keyring_backend = "$KEYRING"
from = "relayer"
gas_prices = "20000000000akra"

TOML

rm -f "$STATE" "$STATE-shm" "$STATE-wal"
echo "linking the chains"
"$FERRY" --config "$CFG" --state "$STATE" connect a b

# Let the account sequence settle before the tests start signing.
sleep 12

echo "running live tests matching ${RUN}"
cd "$FERRY_SRC"
FERRY_LIVE_CONFIG="$CFG" FERRY_LIVE_STATE="$STATE" FERRY_LIVE_SRC=a FERRY_LIVE_DST=b \
  FERRY_LIVE_DENOM=akra FERRY_LIVE_BATCH="$BATCH" \
  go test -tags=live -run "$RUN" -v -count=1 -timeout 40m
