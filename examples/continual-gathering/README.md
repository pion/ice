# Continual Gathering Example

This example demonstrates the `ContinualGatheringPolicy` feature in Pion ICE, which allows agents to continuously discover network candidates throughout a connection's lifetime.

## Overview

Traditional ICE gathering (`GatherOnce`) collects candidates once at startup and stops. This can be problematic when:

- Users switch between WiFi and cellular networks
- Network interfaces are added/removed
- Moving between access points ("walk-out-the-door" problem)

With `GatherContinually`, the agent monitors for network changes and automatically discovers new candidates, enabling seamless connectivity transitions.

## Usage

```bash
# Traditional gathering (stops after initial collection)
go run main.go -mode once

# Continual gathering (monitors for network changes)
go run main.go -mode continually -interval 2s
```

## Testing

While running in continual mode, try:

- Connecting/disconnecting WiFi
- Enabling/disabling network adapters
- Switching between networks

New candidates will be discovered and reported automatically!

### Testing with Virtual Network Adapters (Linux)

You can easily test the continual gathering by creating/removing virtual network adapters:

```bash
# Create a virtual network adapter with an IP address
sudo ip link add veth0 type veth peer name veth1
sudo ip addr add 10.0.0.1/24 dev veth0
sudo ip link set veth0 up

# Wait a few seconds, then check the example output
# You should see new candidates for 10.0.0.1

# Remove the virtual adapter
sudo ip link delete veth0

# The removed interface will be detected and logged
```

Alternative using dummy interface:
```bash
# Create a dummy interface (simpler, no peer needed)
sudo ip link add dummy0 type dummy
sudo ip addr add 192.168.100.1/24 dev dummy0
sudo ip link set dummy0 up

# Remove it
sudo ip link delete dummy0
```

You can also change IP addresses on existing interfaces:
```bash
# Add a secondary IP to an existing interface
sudo ip addr add 172.16.0.1/24 dev eth0

# Remove it
sudo ip addr del 172.16.0.1/24 dev eth0
```
