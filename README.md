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
| `fee` | Optional fixed fee for JSON transactions (some chains reject zero fee) |
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

`--state <file>` overrides the SQLite state path. The default is
`~/.local/state/ibc-ferry/state.db` (or `$XDG_STATE_HOME/ibc-ferry/state.db`),
or `IBC_FERRY_STATE` when set.

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
| `scan_state` | Last scanned height per link |

Schema migrations run automatically on open. A `links.json` file from the
earlier prototype is imported on first open of the default state path; existing
rows are not overwritten.

The chain remains the source of truth. If the database is lost, packets can be
rebuilt by rescanning from a known height. The database exists so a restart
does not forget in-flight work.

## How it works

The tool builds the v2 messages as JSON and lets each chain's own binary sign
and broadcast them:

```text
MsgCreateClient
MsgSendPacket
MsgRecvPacket
```

This avoids compiling against one SDK/ibc-go version for both chains. It has
been tested across Karsa (ibc-go v11) and Gaia v29 (ibc-go v10).

## Limitations

- **Timeout is not wired to a command yet.** The non-membership proof,
  client update, and `MsgTimeout` path is implemented and live-tested, but the
  watcher that decides when to use it is still pending.
- **No automatic repair for orphans.** `doctor` reports orphaned or unlinked
  clients, but `pair` still needs explicit client IDs to repair them.
- **Client updates are driven by acks and timeouts.** The legacy one-shot mode
  still creates a fresh destination client per run; use a new client ID for
  the next run.
- **No retry loop or batching.** One packet per invocation.
- **Experimental.** Not production relayer software.

## Tested

- Karsa <-> Karsa (two local nodes).
- Karsa -> Gaia v29 and Gaia v29 -> Karsa, native ICS20 v2 transfer.
- Receive, client update, and acknowledgement relay in both directions.
- Idempotent `connect` and `pair` against a live Karsa v11 / Gaia v29 pair.
- `doctor` clean and broken-link detection against the same live pair.
- Short-timeout Karsa -> Gaia packet with a non-membership proof and a
  successful `MsgTimeout` relay.

Harnesses live in `examples/`. They expect the Karsa checkout at
`../karsa` by default; override with `KARSA_ROOT=/path/to/karsa`.

The live timeout test is opt-in:

```bash
FERRY_LIVE_CONFIG=... FERRY_LIVE_STATE=... \
FERRY_LIVE_SRC=karsa FERRY_LIVE_DST=gaia FERRY_LIVE_DENOM=akra \
go test -tags=live -run TestLiveTimeout -v
```

## Status

Experimental. Interfaces and the config format may change.
