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
bidirectional pair of clients; v2 has no connections or channels.

Chain definitions belong in a config file. The link inventory is dynamic state
and will move to SQLite next; keeping the two separate lets operators
version-control config without treating the database as configuration.

## Requirements

- Go 1.24+
- The chain binary for each side (`karsad`, `gaiad`, ...).
- A funded relayer key on both chains, in a keyring the binary can read.
- A JSON config file.

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
```

With the default config path in place, `--config` can be omitted.

`--state <file>` overrides the local link state path. The default is
`~/.ibc-ferry/links.json`, or `IBC_FERRY_STATE` when set.

Running without a command keeps the legacy one-shot behavior:

```bash
ibc-ferry --config config.example.json
```

## Local state

Links are stored as a versioned JSON document:

```json
{
  "version": 1,
  "links": [
    {
      "id": "gaia-test-1:07-tendermint-0<->karsa_262144-1:07-tendermint-0",
      "chain_a": "gaia-test-1",
      "client_a": "07-tendermint-0",
      "chain_b": "karsa_262144-1",
      "client_b": "07-tendermint-0",
      "status": "active",
      "created_at": "2026-10-31T15:00:00Z"
    }
  ]
}
```

Writes are atomic: the state is written to a temporary file, synced, and
renamed. The storage layer is deliberately separate from the model so a
SQLite backend can replace it later without changing the CLI.

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

- **No timeout relay yet.** The v2 receipt query used by this CLI path does not
  return the non-membership proof required by `MsgTimeout`. Acks are supported.
- **No connect/pair/doctor yet.** The link commands are read-only for now; the
  link model and state store are in place for them.
- **Client updates are only for acknowledgements.** A fresh destination client
  is still created per one-shot run; use a new client ID for the next run.
- **No retry loop or batching.** One packet per invocation.
- **Experimental.** Not production relayer software.

## Tested

- Karsa <-> Karsa (two local nodes).
- Karsa -> Gaia v29 and Gaia v29 -> Karsa, native ICS20 v2 transfer.
- Receive, client update, and acknowledgement relay in both directions.

Harnesses live in `examples/`. They expect the Karsa checkout at
`../karsa` by default; override with `KARSA_ROOT=/path/to/karsa`.

## Status

Experimental. Interfaces and the config format may change.
