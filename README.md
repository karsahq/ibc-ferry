# ibc-ferry

A minimal **IBC v2** packet ferry for native Cosmos chains.

This is the Go implementation. The validated Python prototype remains on the
`prototype/python` branch as a reference.

The relayer builds IBC v2 messages as JSON and lets each chain's own binary sign
and broadcast them. That keeps one tool working across chains with different
ibc-go versions, such as Karsa (v11) and Gaia (v10).

## What it does

The legacy one-shot mode:

1. Creates a client on the source chain tracking the destination.
2. Adds the counterparty client ID.
3. Sends one `MsgSendPacket` with an ICS20 payload.
4. Queries the packet commitment proof at the source's latest height.
5. Creates the destination client at that exact proof height.
6. Adds the counterparty on the destination.
7. Submits `MsgRecvPacket` to the destination.
8. Queries the acknowledgement proof, updates the source client to that
   proof height, and submits `MsgAcknowledgement`.

Creating the destination client *after* the send, at the proof height, avoids
an `MsgUpdateClient` transaction.

The newer CLI also keeps a local inventory of links. An IBC v2 link is just a
bidirectional pair of clients; v2 has no connections or channels. `connect`
reuses existing clients when possible, creates missing ones, registers both
counterparties, verifies them, and persists the link.

Chain definitions belong in a config file. Runtime state belongs in SQLite;
keeping the two separate lets operators version-control config without treating
the database as configuration.

## Requirements

- Go 1.24+
- The chain binary for each side (`karsad`, `gaiad`, ...).
- A funded relayer key on both chains, in a keyring the binary can read.
- A TOML or JSON config file.

## Build

```bash
go build -o ibc-ferry .
```

## Container image

The published image contains only `ibc-ferry`. It deliberately does **not**
bundle `karsad`, `gaiad`, or any other chain binary. Mount the binaries and
keyring homes for the chains you want to relay; the same image works for any
Cosmos chain.

Image: `ghcr.io/karsahq/ibc-ferry` (tags: `main`, `sha-...`, version tags, and
`latest` tracking `main`). Pin a version tag for production. For a private
package, run `podman login ghcr.io` first.

Each push to `main` publishes `main`, `latest`, and `sha-<commit>`. One build
is a tagged multi-arch index plus four untagged child manifests (amd64, arm64,
provenance, SBOM), so the prune job in the image workflow keeps the newest 30
package versions, roughly six builds, and deletes older ones. Untagged versions
are never pruned on their own, because that would delete an arch image or
attestation that a kept index still references.

Podman:

```bash
mkdir -p bin chains/karsa chains/gaia
cp /path/to/karsad bin/
cp /path/to/gaiad bin/

# Create the relayer key inside the mounted home, with the chain binary you
# already placed in bin/. Use a dedicated relayer home, not a validator home.
bin/karsad keys add ferry --home chains/karsa --keyring-backend file
bin/gaiad keys add ferry --home chains/gaia --keyring-backend file

podman compose -f compose.podman.yaml pull
podman compose -f compose.podman.yaml up -d
```

Docker:

```bash
docker compose -f compose.yaml up -d
```

The compose files default to `config.container.podman.toml` and
`config.container.docker.toml`. Override paths with `FERRY_CONFIG`,
`FERRY_BIN_DIR`, and `FERRY_CHAINS_DIR`.

Path layout inside the container:

| Path | Purpose |
|---|---|
| `/opt/ferry/bin` | Mounted chain binaries |
| `/var/lib/ferry/chains` | Mounted per-chain homes and keyrings |
| `/var/lib/ibc-ferry` | SQLite state volume |
| `/var/lib/ibc-ferry/state.db` | SQLite state file set by the image `CMD` |
| `/etc/ibc-ferry/config.toml` | Mounted config |

The image sets `--state /var/lib/ibc-ferry/state.db`, so links and packet
progress live in the mounted state volume instead of `$HOME/.local` and survive
container restarts and image updates.

Rootless Podman maps the container user to a subuid unless `userns_mode:
keep-id` is used. If the keyring is not readable, chown the dedicated chains
directory:

```bash
podman unshare chown -R 10001:10001 chains
```

If `host.containers.internal` is not enough for your Podman network, use
`network_mode: host` and change the config RPC URLs to `127.0.0.1`.

This setup is verified with rootless Podman 6.1 (`podman compose` through
`podman-compose` 1.6) and with the Quadlet unit from `deploy/quadlet`.

The image is built and pushed by `.github/workflows/image.yml` on `main` and
version tags. The workflow uses multi-arch `linux/amd64` and `linux/arm64` and
GHCR via `GITHUB_TOKEN`.

For single-host production with Podman, use the Quadlet service in
`deploy/quadlet/ferry.container` and the setup notes in
`deploy/quadlet/README.md`. It runs the same image under systemd with
`Restart=always`, journald logs, and optional registry auto-update.

## Config

TOML is the primary format. `config.example.toml` shows a multi-chain config;
`config.example.json` and `config.chains.example.json` remain supported for
existing JSON setups and legacy one-shot transfers.

The loader picks the format from the filename extension (`.toml` or `.json`)
and falls back to content sniffing. `--config` overrides the default path
`~/.config/ibc-ferry/config.toml`; `IBC_FERRY_CONFIG` can override it too.

| Field | Meaning |
|---|---|
| `chains` | Named chain entries for CLI commands |
| `src` / `dst` | Legacy one-shot chain entries |
| `binary` | Path to the chain CLI |
| `home` | Chain home directory |
| `node` | CometBFT RPC, e.g. `tcp://127.0.0.1:26657` |
| `rpc` | CometBFT HTTP RPC, e.g. `http://127.0.0.1:26657` |
| `chain_id` | Cosmos chain ID |
| `keyring_backend` | `test`, `file`, ... |
| `from` | Relayer key name |
| `gas_prices` | Used by CLI transactions such as `add-counterparty` |
| `fee` | Fee floor for JSON transactions; raised to `gas_limit x gas_prices` when a batch needs more |
| `src_client` / `dst_client` | Legacy one-shot client IDs |
| `ack` | Legacy one-shot; defaults to `true`. Set `false` for receive-only runs |
| `denom` / `amount` / `receiver` | Legacy one-shot ICS20 transfer fields |

## Commands

Global flags go before the command:

```bash
ibc-ferry --config config.toml chains
ibc-ferry --config config.toml links [--chain <name-or-chain-id>]
ibc-ferry --config config.toml link <link-id>
ibc-ferry --config config.toml connect <chain-a> <chain-b>
ibc-ferry --config config.toml pair <chain-a> <client-a> <chain-b> <client-b>
ibc-ferry --config config.toml doctor [--strict]
ibc-ferry --config config.toml status [--json]
ibc-ferry --config config.toml prune [--older-than 168h]
ibc-ferry --config config.toml relay --link <link-id> --once
ibc-ferry --config config.toml relay --link <link-id>
ibc-ferry --config config.toml start [--http-addr 127.0.0.1:8080]
```

With the default config path in place, `--config` can be omitted.

`connect` is idempotent: it prefers a complete existing pair, then an
unpaired client that tracks the other chain, and only creates a client when
none is usable. `pair` verifies explicit client IDs, fills in missing
counterparties, and persists the link. `pair` cannot overwrite a counterparty
that is already registered to a different client.

The chain requires the same relayer key that created a client to register its
counterparty. If a client was created by another key, use that key or create a
new client instead.

`doctor` checks configured chains, relayer balances, stored links, client
status, trust-period remaining, orphan/unlinked clients, and packet counts from
SQLite. Errors make it exit non-zero; `--strict` also fails on warnings.

`relay --once` runs one scan-and-relay cycle; without `--once` it keeps
running for the selected link. `start` runs the loop for every stored link and
serves status/metrics on `127.0.0.1:8080` by default. Each cycle scans
`send_packet` and `write_acknowledgement` events, stores new packets in SQLite,
then decides between recv, ack, and timeout. The defaults are a 5s interval,
one block of finality, a 1000-block lookback on first run, 7-day retention
for terminal packets, a 1-hour client refresh, and a batch size of 10. Use
`--interval`, `--finality`, `--lookback`, `--retention`, `--client-refresh`,
and `--batch-size` to change them; `--retention 0` disables pruning,
`--client-refresh 0` disables idle client updates, and `--batch-size 1`
disables batching. Failed packets get exponential backoff.

Relay actions are grouped by destination chain, client, action, and proof
height. One client update and one transaction are used per group. With
`--finality >= 1`, recv proofs use one common source height per cycle, so a
burst of packets can be received in one transaction. `--finality 0` falls back
to per-packet proofs. If a batch fails, the watcher retries its messages
individually so one bad packet does not block the rest.

When the acknowledgement bytes are missing from local state, for example when
another relayer received the packet, the watcher searches the destination
chain's transaction index for the `write_acknowledgement` event and recovers
the original ack before relaying it.

Client refresh matters for links with no traffic: an IBC client expires if it
is never updated during its trusting period. The watcher refreshes both clients
periodically, not only when a packet needs relaying.

`status` reads the local SQLite state and prints a table; `--json` emits the
same data for scripts. `prune` removes old `acked` and `timed_out` packets
manually; the watcher applies the same retention automatically.

`--state <file>` overrides the SQLite state path. The default is
`~/.local/state/ibc-ferry/state.db` (or `$XDG_STATE_HOME/ibc-ferry/state.db`),
or `IBC_FERRY_STATE` when set.

Karsa enables EIP-1559 and a nonzero minimum gas price. Use
`gas_prices = "20000000000akra"` for CLI transactions and a `fee`
(100000000000000000 akra in the examples) for JSON transactions.

A packet whose memo requests an EVM callback makes the chain run that callback
on the relay transaction's gas meter. The relayer therefore adds the memo's
`gas_limit` to that message's gas limit: `dest_callback` on receive, and
`src_callback` on acknowledgement or timeout. The reservation is capped at
1,000,000 per payload, which is the cap Karsa configures. Without it the
callback would run with whatever gas the relay transaction has left and fail
with out-of-gas.

Measured on the Karsa keepers: a plain ICS20 v2 receive costs about 166,000 gas,
and the same receive with a destination callback that approves the voucher and
pulls it costs about 407,000. A callback may spend up to the limit in its memo,
so the relayer reserves the full limit rather than the gas one contract happens
to use.

Chains check a fee against the transaction gas limit, not against the gas that
is actually used. The relayer therefore treats `fee` as a floor and raises it
to `gas_limit x gas_prices` whenever that is larger, which matters for batches:
ten received packets share one proof height and one transaction of roughly 8.3M
gas, above what the single-message fee covers. Leave `fee` out entirely and the
amount is derived from `gas_prices` alone.

Running without a command keeps the legacy one-shot behavior:

```bash
ibc-ferry --config config.example.json
```

## Local state

Runtime state lives in SQLite with WAL mode and `synchronous=FULL`. The initial
schema contains:

| Table | Purpose |
|---|---|
| `links` | Client pairs and their status |
| `packets` | Packet work queue, tx hashes, proof heights and retry state |
| `scan_state` | Last scanned height per link and chain |

Schema migrations run automatically on open. A `links.json` file from the
earlier prototype is imported on first open of the default state path; existing
rows are not overwritten.

The chain remains the source of truth. If the database is lost, packets can be
rebuilt by rescanning from a known height. The database exists so a restart
does not forget in-flight work.

## Observability

`ferry start` exposes an HTTP server on the configured address:

| Endpoint | Purpose |
|---|---|
| `/healthz` | 200 when the last relay cycle was recent and successful |
| `/status` | JSON links, packet counts, pending packets, and scan heights |
| `/metrics` | Prometheus text metrics |

The metrics include `ferry_links_total`, `ferry_packets`,
`ferry_pending_packets`, `ferry_scan_height`, `ferry_relay_cycles_total`,
`ferry_relay_actions_total`, `ferry_pruned_packets_total`,
`ferry_relay_last_cycle_timestamp_seconds`, and `ferry_relay_last_cycle_error`. Bind to loopback unless you put an
authenticated proxy in front; there is no built-in authentication.

## How it works

The tool builds the v2 messages as JSON and lets each chain's own binary sign
and broadcast them:

```text
MsgCreateClient
MsgSendPacket
MsgRecvPacket
MsgAcknowledgement
MsgTimeout
```

This avoids compiling against one SDK/ibc-go version for both chains. It has
been tested across Karsa (ibc-go v11) and Gaia v29 (ibc-go v10).

## Limitations

- **No automatic repair for orphans.** `doctor` reports orphaned or unlinked
  clients, but `pair` still needs explicit client IDs to repair them.
- **Batching is per proof height.** With `--finality 0`, or when a proof at
  the common height is unavailable, packets may use separate transactions.
- **Ack recovery needs the transaction indexer.** If the destination node runs
  with the CometBFT indexer disabled, ack bytes cannot be recovered from
  `write_acknowledgement` events outside the scan window.
- **Status API has no authentication.** Keep it on loopback or place it behind
  an authenticated proxy.
- **A successful EVM callback is not relayed end to end.** A callback packet is
  relayed with the reserved gas, but its target has no code in the harness, so
  the chain answers with an error acknowledgement. Exercising a successful
  callback also needs an ERC20 token pair, a callback contract, and the isolated
  receiver address that the receiving chain derives.
- **Client updates are driven by recv, ack, and timeout.** The legacy one-shot
  mode still creates a fresh destination client per run; use a new client ID
  for the next run.
- **Experimental.** Not production relayer software.

## Tested

- Karsa <-> Karsa (two local nodes).
- Karsa -> Gaia v29 and Gaia v29 -> Karsa, native ICS20 v2 transfer.
- Receive, client update, and acknowledgement relay in both directions.
- Idempotent `connect` and `pair` against a live Karsa v11 / Gaia v29 pair.
- `doctor` clean and broken-link detection against the same live pair.
- Short-timeout packets with a non-membership proof and a successful
  `MsgTimeout` relay in both directions.
- Ten packets sent in one transaction, received in one transaction, and
  acknowledged in two, with the relayer raising the fee to cover the batch gas.
- A packet whose memo asks for a 1,000,000 gas destination callback relayed end
  to end, with the receive transaction carrying the reserved 2,100,000 gas limit
  (300,000 batch overhead, 800,000 per message, 1,000,000 callback).
- Watcher ingestion, recv, ack, and timeout in both directions against the
  live Karsa v11 / Gaia v29 pair.
- Idle client refresh against the same live pair.
- A three-packet burst received in one transaction and acknowledged in one
  transaction.
- Acknowledgement bytes recovered from the transaction index after a packet
  was received by another relayer.
- Status, metrics, and health handlers through unit tests.
- Gas reservation for callback packets and fee derivation for single and
  batched transactions through unit tests.
- Terminal packet retention and manual pruning through unit tests.

Harnesses live in `examples/`. They expect the Karsa checkout at
`../karsa` by default; override with `KARSA_ROOT=/path/to/karsa`.

The live timeout and watcher tests are opt-in:

```bash
FERRY_LIVE_CONFIG=... FERRY_LIVE_STATE=... \
FERRY_LIVE_SRC=karsa FERRY_LIVE_DST=gaia FERRY_LIVE_DENOM=akra \
go test -tags=live -run 'TestLive(Timeout|Watcher|CallbackGas)' -v
```

`TestLiveWatcherBatchPackets` sends three packets unless `FERRY_LIVE_BATCH`
sets a different count. Send a batch of ten to exercise the fee path: the send,
the receive, and the acknowledgements then exceed the single-message fee.

## Status

Experimental. Interfaces and the config format may change.
