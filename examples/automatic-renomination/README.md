# Automatic Renomination Example

This example demonstrates the ICE automatic renomination feature using real network interfaces. Automatic renomination allows the controlling ICE agent to automatically switch between candidate pairs when a better connection path becomes available.

## What is Automatic Renomination?

Automatic renomination is a feature where the controlling ICE agent continuously monitors candidate pairs and automatically switches to better pairs when they become available. This is particularly useful for:

- **Adapting to network changes**: When network conditions improve or degrade
- **Optimizing for latency**: Automatically switching to lower-latency paths
- **Quality of service**: Maintaining the best possible connection quality
- **Interface failover**: Switching to alternate interfaces when primary path fails

## How It Works

The automatic renomination feature evaluates candidate pairs based on:

1. **Candidate types**: Direct connections (host-to-host) are preferred over relay connections
2. **Round-trip time (RTT)**: Lower latency paths are preferred
3. **Connection stability**: Pairs that have recently received responses are favored

When a candidate pair is found that is significantly better than the current selection (>10ms RTT improvement or direct vs relay), the agent automatically renominates to use the better pair.

## Quick Start Tutorial

This step-by-step tutorial walks you through setting up virtual network interfaces and testing automatic renomination.

**Important:** This example uses **network namespaces with two veth pairs** to properly isolate network traffic so that `tc` (traffic control) rules can affect latency. The controlled agent runs in the default namespace, and the controlling agent runs in a separate namespace (ns1). They communicate via two veth pairs, giving us multiple candidate pairs for automatic renomination.

### Step 1: Create Network Namespace with Two veth Pairs

Create a network namespace and two veth pairs to connect them:

```bash
# Create namespace
sudo ip netns add ns1

# Create FIRST veth pair (veth0 <-> veth1)
sudo ip link add veth0 type veth peer name veth1
sudo ip link set veth1 netns ns1
sudo ip addr add 192.168.100.1/24 dev veth0
sudo ip link set veth0 up
sudo ip netns exec ns1 ip addr add 192.168.100.2/24 dev veth1
sudo ip netns exec ns1 ip link set veth1 up

# Create SECOND veth pair (veth2 <-> veth3)
sudo ip link add veth2 type veth peer name veth3
sudo ip link set veth3 netns ns1
sudo ip addr add 192.168.101.1/24 dev veth2
sudo ip link set veth2 up
sudo ip netns exec ns1 ip addr add 192.168.101.2/24 dev veth3
sudo ip netns exec ns1 ip link set veth3 up

# Bring up loopback in ns1
sudo ip netns exec ns1 ip link set lo up
```

**Verify connectivity on both pairs:**
```bash
# Ping via first veth pair
ping -c 2 192.168.100.2

# Ping via second veth pair
ping -c 2 192.168.101.2
```

You should see successful pings on both with low latency (~0.05ms).

**Verify that tc rules work:**
```bash
# Add 100ms latency to veth0 (first pair)
sudo tc qdisc add dev veth0 root netem delay 100ms

# Test ping via first pair - should now show ~100ms latency
ping -c 2 192.168.100.2

# Test ping via second pair - should still be fast
ping -c 2 192.168.101.2

# Remove tc rule for now
sudo tc qdisc del dev veth0 root
```

After adding the tc rule to veth0, pings to 192.168.100.2 should show ~100ms latency, while pings to 192.168.101.2 remain fast. This proves tc is working and we have independent paths!

### Step 2: Start the Controlled Agent (Default Namespace)

Open a terminal and start the controlled (non-controlling) agent in the **default namespace**:

```bash
cd examples/automatic-renomination
go run main.go
```

**Expected output:**
```
=== Automatic Renomination Example ===
Local Agent is CONTROLLED

Press 'Enter' when both processes have started
```

Don't press Enter yet - wait for Step 3.

### Step 3: Start the Controlling Agent (ns1 Namespace)

Open a second terminal and start the controlling agent in the **ns1 namespace**:

```bash
cd examples/automatic-renomination
sudo ip netns exec ns1 go run main.go -controlling
```

**Expected output:**
```
=== Automatic Renomination Example ===
Local Agent is CONTROLLING (with automatic renomination enabled)

Press 'Enter' when both processes have started
```

### Step 4: Connect the Agents

Press Enter in **both terminals**. You should see candidate gathering and connection establishment:

**Expected output (both terminals):**
```
Gathering candidates...
Local candidate: candidate:... 192.168.100.x ... typ host
Local candidate: candidate:... 192.168.101.x ... typ host
Added remote candidate: candidate:... 192.168.100.x ... typ host
Added remote candidate: candidate:... 192.168.101.x ... typ host
Starting ICE connection...
>>> ICE Connection State: Checking

>>> SELECTED CANDIDATE PAIR CHANGED <<<
    Local:  candidate:... 192.168.10x.x ... typ host (type: host)
    Remote: candidate:... 192.168.10x.x ... typ host (type: host)

>>> ICE Connection State: Connected

=== CONNECTED ===
```

You should see **2 local candidates** and **2 remote candidates**, giving you **4 candidate pairs** total.

**Controlling agent will also show:**
```
Automatic renomination is enabled on the controlling agent.
To test it, you can use traffic control (tc) to change network conditions:

  # Add 100ms latency to eth0:
  sudo tc qdisc add dev eth0 root netem delay 100ms

  # Remove the latency:
  sudo tc qdisc del dev eth0 root

Watch for "SELECTED CANDIDATE PAIR CHANGED" messages above.
The agent will automatically renominate to better paths when detected.
```

You should also see periodic messages being exchanged with RTT information:
```
Sent: Message #1 from controlling agent [RTT: 0.35ms]
Received: Message #1 from controlled agent [RTT: 0.35ms]
```

### Step 5: Add Latency to Trigger Renomination

In a third terminal, add latency to veth0 (the first veth pair):

```bash
# Add 100ms latency to veth0
sudo tc qdisc add dev veth0 root netem delay 100ms

# Check that the rule was applied
sudo tc qdisc show dev veth0
```

**Expected output:**
```
qdisc netem 8001: root refcnt 2 limit 1000 delay 100ms
```

### Step 6: Observe Automatic Renomination

Watch the **controlling agent's terminal**. Look at the debug output showing candidate pair states:

```
=== DEBUG: Candidate Pair States ===
  candidate:... <-> candidate:...
    State: succeeded, Nominated: true
    RTT: 100.xx ms   <-- This pair now has high latency!
  candidate:... <-> candidate:...
    State: succeeded, Nominated: false
    RTT: 0.3x ms     <-- This pair is still fast!
===================================
```

Within a few seconds (based on the renomination interval of 3 seconds), once the RTT difference exceeds 10ms, you should see:

**Expected output:**
```
>>> SELECTED CANDIDATE PAIR CHANGED <<<
    Local:  candidate:... 192.168.101.x ... typ host (type: host)
    Remote: candidate:... 192.168.101.x ... typ host (type: host)
```

This shows the agent automatically switched from the slow path (192.168.100.x) to the fast path (192.168.101.x)!

**What to look for:**
- RTT on the nominated pair increases from ~0.3ms to ~100ms after adding tc rule
- The RTT displayed in sent/received messages will also increase to ~100ms
- After 3-6 seconds, renomination triggers
- New selected pair uses the 192.168.101.x addresses (veth2/veth3)
- RTT in both the debug output and sent/received messages drops back to ~0.3ms

### Step 7: Remove Latency and Observe Switch Back

Remove the latency from veth0:

```bash
sudo tc qdisc del dev veth0 root
```

Wait a few seconds and watch for another renomination event as the agent switches back to the now-improved path.

### Step 8: Clean Up

When you're done testing, clean up the namespace and both veth pairs:

```bash
# Stop both agents with Ctrl+C first

# Remove any traffic control rules
sudo tc qdisc del dev veth0 root 2>/dev/null || true
sudo tc qdisc del dev veth2 root 2>/dev/null || true

# Remove namespace (this automatically removes veth1 and veth3)
sudo ip netns del ns1 2>/dev/null || true

# Remove veth0 and veth2 from default namespace
sudo ip link delete veth0 2>/dev/null || true
sudo ip link delete veth2 2>/dev/null || true

# Verify cleanup
ip link show | grep veth  # Should show no output
ip netns list | grep ns1  # Should show no output
```

## Running the Example

This example requires running two processes - one controlling and one controlled.

### Terminal 1 (Controlling Agent)

```bash
go run main.go -controlling
```

### Terminal 2 (Controlled Agent)

```bash
go run main.go
```

Press Enter in both terminals once both processes are running to start the ICE connection.

## Testing Automatic Renomination

Once connected, you'll see messages like:

```
=== CONNECTED ===

Automatic renomination is enabled on the controlling agent.
To test it, you can use traffic control (tc) to change network conditions:

  # Add 100ms latency to eth0:
  sudo tc qdisc add dev eth0 root netem delay 100ms

  # Remove the latency:
  sudo tc qdisc del dev eth0 root

Watch for "SELECTED CANDIDATE PAIR CHANGED" messages above.
```

### Using Traffic Control (tc) to Trigger Renomination

Traffic control (`tc`) is a Linux tool for simulating network conditions. Here are some useful commands:

#### Add latency to an interface

```bash
# Add 100ms delay
sudo tc qdisc add dev eth0 root netem delay 100ms

# Add variable latency (50ms ± 10ms)
sudo tc qdisc add dev eth0 root netem delay 50ms 10ms
```

#### Simulate packet loss

```bash
# Add 5% packet loss
sudo tc qdisc add dev eth0 root netem loss 5%
```

#### Limit bandwidth

```bash
# Limit to 1mbit
sudo tc qdisc add dev eth0 root tbf rate 1mbit burst 32kbit latency 400ms
```

#### Remove all rules

```bash
# Remove all tc rules from interface
sudo tc qdisc del dev eth0 root
```

#### Check current rules

```bash
# View current tc configuration
sudo tc qdisc show dev eth0
```

### Expected Behavior

When you change network conditions with `tc`, watch the controlling agent's output:

1. Initial connection will select the best available path
2. When you add latency/loss, the RTT increases
3. If the RTT difference exceeds 10ms, automatic renomination may trigger
4. You'll see a "SELECTED CANDIDATE PAIR CHANGED" message
5. The connection continues using the new pair

**Note**: Renomination only occurs if a significantly better pair is available. Simply degrading one path won't trigger renomination unless there's an alternate path that's measurably better.

## Understanding the Output

### Connection State Changes

```
>>> ICE Connection State: Checking
>>> ICE Connection State: Connected
```

These show the overall ICE connection state progression.

### Candidate Discovery

```
Local candidate: candidate:1 1 udp 2130706431 192.168.1.100 54321 typ host
Added remote candidate: candidate:2 1 udp 2130706431 192.168.1.101 54322 typ host
```

These show discovered local and remote ICE candidates.

### Pair Selection

```
>>> SELECTED CANDIDATE PAIR CHANGED <<<
    Local:  candidate:1 1 udp 2130706431 192.168.1.100 54321 typ host (type: host)
    Remote: candidate:2 1 udp 2130706431 192.168.1.101 54322 typ host (type: host)
```

This indicates automatic renomination occurred and shows the new selected pair.

### Message RTT Display

```
Sent: Message #1 from controlling agent [RTT: 0.35ms]
Received: Message #1 from controlled agent [RTT: 0.35ms]
```

Each sent and received message displays the current Round-Trip Time (RTT) of the selected candidate pair. This RTT value:
- Shows the current latency of the connection path
- Updates in real-time as network conditions change
- Helps verify that automatic renomination is working (RTT should improve after switching to a better path)
- May show "N/A" briefly during initial connection or if RTT hasn't been measured yet

## Testing Scenarios

### Scenario 1: Interface Latency Change

1. Start both agents
2. Wait for connection
3. Add latency to one interface: `sudo tc qdisc add dev eth0 root netem delay 100ms`
4. If multiple interfaces exist with different RTTs, automatic renomination should occur
5. Remove latency: `sudo tc qdisc del dev eth0 root`

### Scenario 2: Multiple Network Interfaces

If your machine has multiple network interfaces (e.g., WiFi and Ethernet):

1. Start agents connected via one interface
2. Degrade that interface with `tc`
3. The agent should automatically switch to the other interface if it provides better quality

### Scenario 3: Connection Recovery

1. Start with one interface having high latency
2. Once connected, remove the latency
3. The agent should detect the improved path and switch back

## Configuration Options

You can modify the example to customize automatic renomination:

### Renomination Interval

```go
renominationInterval := 5 * time.Second  // How often to check (default: 3s)
iceAgent, err = ice.NewAgentWithOptions(
    ice.WithNetworkTypes([]ice.NetworkType{ice.NetworkTypeUDP4, ice.NetworkTypeUDP6}),
    ice.WithInterfaceFilter(interfaceFilter),
    ice.WithRenomination(ice.DefaultNominationValueGenerator()),
    ice.WithAutomaticRenomination(renominationInterval),
)
```

### Interface Filter

The example uses an `InterfaceFilter` to constrain the ICE agent to only use veth interfaces:

```go
interfaceFilter := func(interfaceName string) bool {
    // Allow all veth interfaces (veth0, veth1, veth2, veth3)
    // This gives us multiple candidate pairs for automatic renomination
    return len(interfaceName) >= 4 && interfaceName[:4] == "veth"
}
```

To use your real network interfaces instead:

```go
// Option 1: Use all interfaces (no InterfaceFilter)
iceAgent, err = ice.NewAgentWithOptions(
    ice.WithNetworkTypes([]ice.NetworkType{ice.NetworkTypeUDP4, ice.NetworkTypeUDP6}),
    ice.WithRenomination(ice.DefaultNominationValueGenerator()),
    ice.WithAutomaticRenomination(renominationInterval),
)

// Option 2: Filter to specific real interfaces (e.g., eth0 and wlan0)
interfaceFilter := func(interfaceName string) bool {
    return interfaceName == "eth0" || interfaceName == "wlan0"
}

// Option 3: Use only IPv4 interfaces starting with "eth"
interfaceFilter := func(interfaceName string) bool {
    return strings.HasPrefix(interfaceName, "eth")
}
```

**Note:** When using real network interfaces without network namespaces, you'll need to run the two processes on different machines to properly test tc rules, as local traffic on the same machine may bypass tc.

## Troubleshooting

### ICE agent not using veth interfaces

If you see candidates on your real interfaces (like eth0, eth2, wlan0) instead of veth0/veth1:

- **Check the InterfaceFilter**: Make sure the code has the `InterfaceFilter` configured to only allow veth0 and veth1
- **Verify veth interfaces exist**: Run `ip link show veth0` (and `sudo ip netns exec ns1 ip link show veth1`) to confirm they're created
- **Verify interfaces are UP**: Run `ip link show veth0` and check for `UP` in the output
- **Check IP addresses**: Run `ip addr show veth0` and `sudo ip netns exec ns1 ip addr show veth1` to confirm the 192.168.100.x addresses are assigned

### No candidates found / Connection fails

If the agents fail to connect after adding the InterfaceFilter:

- **Create dummy interfaces first**: The dummy interfaces must be created before starting the agents
- **Both agents need the filter**: Both controlling and controlled agents must have the same InterfaceFilter
- **Check for errors**: Look for errors during candidate gathering that might indicate interface issues

### No renomination occurring

- **Only one candidate pair available**: Automatic renomination needs alternate paths to switch between. If only one candidate pair exists, renomination won't occur.
- **Insufficient quality difference**: The new path must be significantly better (>10ms RTT improvement or better candidate type) to trigger renomination.
- **Not enough time elapsed**: The renomination interval (default 3s) must pass before evaluation occurs.
- **Wrong interface**: Make sure you're adding latency to the interface that's actually being used. Check the "SELECTED CANDIDATE PAIR CHANGED" message to see which interface/IP is in use.

### Permission denied with tc commands

All `tc` commands require root privileges. Use `sudo` before each command.

### Interface name not found

Use `ip link` to list available network interfaces on your system. Replace `eth0` with your actual interface name (e.g., `enp0s3`, `wlan0`, `wlp3s0`).

## Cleanup

After testing, it's important to clean up any virtual interfaces and traffic control rules you created.

### Remove Traffic Control Rules

If you added any tc rules to interfaces, remove them:

```bash
# Remove tc rules from veth interfaces
sudo tc qdisc del dev veth0 root 2>/dev/null || true
sudo tc qdisc del dev veth2 root 2>/dev/null || true

# List all interfaces with tc rules
tc qdisc show

# Remove tc rules from any interface shown above
# sudo tc qdisc del dev <interface-name> root
```

### Remove Network Namespace and veth Pairs

If you created the namespace and veth pairs for testing, remove them:

```bash
# Remove namespace (automatically removes veth1 and veth3)
sudo ip netns del ns1 2>/dev/null || true

# Remove veth0 and veth2 from default namespace
sudo ip link delete veth0 2>/dev/null || true
sudo ip link delete veth2 2>/dev/null || true

# Verify removal
ip link show | grep veth     # Should show no output
ip netns list | grep ns1     # Should show no output
```

### Verify Clean State

Check that everything is cleaned up:

```bash
# Check for any remaining tc rules
tc qdisc show

# Check for veth interfaces
ip link show | grep veth

# Check for namespaces
ip netns list

# All commands should show no veth interfaces or ns1 namespace if cleanup was successful
```

## Additional Resources

- [ICE RFC 8445](https://datatracker.ietf.org/doc/html/rfc8445) - Interactive Connectivity Establishment (ICE) Protocol
- [draft-thatcher-ice-renomination](https://datatracker.ietf.org/doc/html/draft-thatcher-ice-renomination-01) - ICE Renomination Specification
- [Linux tc man page](https://man7.org/linux/man-pages/man8/tc.8.html) - Traffic Control documentation
- [netem documentation](https://wiki.linuxfoundation.org/networking/netem) - Network Emulation guide
- [Linux network namespaces](https://man7.org/linux/man-pages/man8/ip-netns.8.html) - Network namespace documentation
- [veth pairs](https://man7.org/linux/man-pages/man4/veth.4.html) - Virtual ethernet pair documentation
