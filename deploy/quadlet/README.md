# Podman Quadlet

Quadlet runs the relayer as a user systemd service. It is the Podman-native
alternative to Compose for a long-running single-host deployment.

The image is chain-agnostic: mount the chain binaries under
`~/ibc-ferry/bin` and the per-chain keyring homes under
`~/ibc-ferry/chains`.

## Layout

```text
~/ibc-ferry/
  config.toml
  bin/karsad
  bin/gaiad
  chains/karsa/
  chains/gaia/
  state/
```

The example config is `config.container.podman.toml` in the repository root.
It points binaries at `/opt/ferry/bin`, homes at `/var/lib/ferry/chains`, and
RPC at `host.containers.internal`.

## Install

```bash
mkdir -p ~/ibc-ferry/{bin,chains,state}
cp /path/to/karsad ~/ibc-ferry/bin/
cp /path/to/gaiad ~/ibc-ferry/bin/
cp config.container.podman.toml ~/ibc-ferry/config.toml

# Create dedicated relayer keys, not validator keys.
~/ibc-ferry/bin/karsad keys add ferry \
  --home ~/ibc-ferry/chains/karsa --keyring-backend file
~/ibc-ferry/bin/gaiad keys add ferry \
  --home ~/ibc-ferry/chains/gaia --keyring-backend file

mkdir -p ~/.config/containers/systemd
cp deploy/quadlet/ferry.container ~/.config/containers/systemd/

loginctl enable-linger "$USER"
systemctl --user daemon-reload
systemctl --user start ferry
systemctl --user status ferry
```

Logs:

```bash
journalctl --user -u ferry -f
```

Status and metrics:

```bash
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:8080/status
curl http://127.0.0.1:8080/metrics
```

## Rootless permissions

If the keyring is not readable by the container user:

```bash
podman unshare chown -R 10001:10001 ~/ibc-ferry/chains ~/ibc-ferry/state
```

`UserNS=keep-id:uid=10001,gid=10001` maps the container user to the host user,
so this is usually unnecessary for files created by the same user.

## Rootful Podman

Copy the unit to `/etc/containers/systemd/ferry.container`, change the
`%h/ibc-ferry` paths to `/srv/ibc-ferry`, then:

```bash
sudo systemctl daemon-reload
sudo systemctl start ferry
sudo systemctl status ferry
```

For rootful units use `WantedBy=multi-user.target`.

## Updates

The unit sets `AutoUpdate=registry` and uses `:latest`. To enable automatic
image updates:

```bash
systemctl --user enable --now podman-auto-update.timer
```

For production, pin a version tag instead of `latest` and update deliberately.

## Notes

- Do not mount validator homes. Use a dedicated relayer keyring.
- The image does not include `karsad`, `gaiad`, or any other chain binary.
- If `host.containers.internal` does not resolve on your Podman network, use
  `network_mode: host` equivalent (`Network=host`) and set RPC URLs to
  `127.0.0.1`.
