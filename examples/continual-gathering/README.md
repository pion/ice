# Continual Gathering Example

This example uses [`github.com/pion/transport/v5/netchange.Detector`](https://github.com/pion/transport/blob/main/netchange/detector.go) as the ICE
agent's network implementation through `ice.WithNet(detector)`.

The application calls `detector.Check(ctx)` in a loop. The first call reports the
initial interfaces immediately. Subsequent calls wait for interface or address
changes and refresh the detector's internal network. After each successful check,
the application calls `agent.Gather` to gather candidates.

## Usage

Run from the repository root:

```bash
go run ./examples/continual-gathering
go run ./examples/continual-gathering -mode continually -interval 2s
go run ./examples/continual-gathering -mode once
```

`-interval` sets the refresh timeout for native notifications and the polling
interval on platforms without native notifications. Native notifications can
trigger a check sooner. Checks keep waiting when a refresh finds no changes.
`-mode once` exits after the initial gathering pass. Ctrl+C stops continual mode.

## Testing

While running in continual mode, connect or disconnect a network adapter, or
add an IP address. The example prints the changes and gathers again.

On Linux, you can add a dummy interface in another terminal:

```bash
sudo ip link add dummy0 type dummy
sudo ip addr add 192.168.100.1/24 dev dummy0
sudo ip link set dummy0 up
```

After the new candidate appears, remove the interface:

```bash
sudo ip link delete dummy0
```
