// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// Package main provides a simple CLI that prints the host and srflx addresses
// produced by different address rewrite (1:1) rule configurations. It is designed to be run
// inside the accompanying docker-compose topology so that each scenario can
// demonstrate how multi-homed hosts, srflx pools, CIDR scoping, and TCP muxing
// interact with the new rules.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/logging"
)

const (
	defaultTimeout = 8 * time.Second
)

type scenario struct {
	Key             string
	Title           string
	Description     string
	RewriteRules    []ice.AddressRewriteRule
	NetworkTypes    []ice.NetworkType
	CandidateTypes  []ice.CandidateType
	TimeoutOverride time.Duration
}

func (s scenario) timeout() time.Duration {
	if s.TimeoutOverride > 0 {
		return s.TimeoutOverride
	}

	return defaultTimeout
}

func (s scenario) requiresTCPMux() bool {
	for _, nt := range s.NetworkTypes {
		if nt.IsTCP() {
			return true
		}
	}

	return false
}

func main() {
	log.SetFlags(0)

	var (
		scenarioKey string
		listOnly    bool
		timeout     time.Duration
	)

	flag.StringVar(&scenarioKey, "scenario", "all", "Scenario key to run (use -list to see options)")
	flag.BoolVar(&listOnly, "list", false, "List available scenarios")
	flag.DurationVar(&timeout, "timeout", 0, "Override gather timeout for each scenario")
	flag.Parse()

	scenarios := buildScenarios(timeout)

	if listOnly {
		fmt.Println("Available scenarios:")
		for _, sc := range scenarios {
			fmt.Printf("  %s\t%s\n", sc.Key, sc.Title)
		}

		return
	}

	fmt.Println("Address rewrite rule demonstration client")
	printInterfaceSnapshot()

	ctx := context.Background()

	if scenarioKey == "all" {
		for _, sc := range scenarios {
			if err := runScenario(ctx, sc); err != nil {
				log.Fatalf("scenario %s failed: %v", sc.Key, err)
			}
		}

		return
	}

	sc, ok := findScenario(scenarios, scenarioKey)
	if !ok {
		log.Fatalf("unknown scenario %q. Use -list to see valid keys.", scenarioKey)
	}

	if err := runScenario(ctx, sc); err != nil {
		log.Fatalf("scenario %s failed: %v", sc.Key, err)
	}
}

func findScenario(scenarios []scenario, key string) (scenario, bool) {
	for _, sc := range scenarios {
		if sc.Key == key {
			return sc, true
		}
	}

	return scenario{}, false
}

func buildScenarios(timeout time.Duration) []scenario {
	scenarios := []scenario{
		buildMultiNetworkScenario(),
		buildSrflxScenario(),
		buildScopedCatchAllScenario(),
		buildIfaceScopedScenario(),
	}

	if timeout > 0 {
		for i := range scenarios {
			scenarios[i].TimeoutOverride = timeout
		}
	}

	return scenarios
}

func buildMultiNetworkScenario() scenario {
	localBlue := envOrDefault("NAT_DEMO_BLUE_LOCAL", "10.10.0.20")
	publicBlue := envOrDefault("NAT_DEMO_BLUE_PUBLIC", "203.0.113.10")

	localGreen := envOrDefault("NAT_DEMO_GREEN_LOCAL", "10.20.0.20")
	publicGreen := envOrDefault("NAT_DEMO_GREEN_PUBLIC", "203.0.113.20")

	globalFallback := envOrDefault("NAT_DEMO_GLOBAL_HOST_FALLBACK", "198.51.100.200")
	dropLAN := envOrDefault("NAT_DEMO_DROP_LAN", "0") == "1"

	sc := scenario{
		Key:   "multi-net",
		Title: "Multiple host networks with UDP+TCP replacement",
		Description: "Maps each host interface to a deterministic public IP and enables TCP" +
			"muxing to prove the rules work for both UDP and ICE-TCP candidates.",
		NetworkTypes: []ice.NetworkType{
			ice.NetworkTypeUDP4,
			ice.NetworkTypeTCP4,
		},
		CandidateTypes: []ice.CandidateType{ice.CandidateTypeHost},
		RewriteRules: []ice.AddressRewriteRule{
			{
				External:        []string{publicBlue},
				Local:           localBlue,
				AsCandidateType: ice.CandidateTypeHost,
				Networks:        []ice.NetworkType{ice.NetworkTypeUDP4, ice.NetworkTypeTCP4},
			},
			{
				External:        []string{publicGreen},
				Local:           localGreen,
				AsCandidateType: ice.CandidateTypeHost,
				Networks:        []ice.NetworkType{ice.NetworkTypeUDP4, ice.NetworkTypeTCP4},
			},
			{
				External:        []string{globalFallback},
				AsCandidateType: ice.CandidateTypeHost,
			},
		},
	}

	if dropLAN {
		sc.RewriteRules = append(sc.RewriteRules, ice.AddressRewriteRule{
			External:        nil,
			Local:           localBlue,
			AsCandidateType: ice.CandidateTypeHost,
			Mode:            ice.AddressRewriteReplace,
		})
	}

	return sc
}

func buildIfaceScopedScenario() scenario {
	localBlue := envOrDefault("NAT_DEMO_BLUE_LOCAL", "10.10.0.20")
	publicBlue := envOrDefault("NAT_DEMO_BLUE_PUBLIC", "203.0.113.10")
	localGreen := envOrDefault("NAT_DEMO_GREEN_LOCAL", "10.20.0.20")
	publicGreen := envOrDefault("NAT_DEMO_GREEN_PUBLIC", "203.0.113.20")
	blueIface := envOrDefault("NAT_DEMO_BLUE_IFACE", "eth0")
	greenIface := envOrDefault("NAT_DEMO_GREEN_IFACE", "eth1")
	globalFallback := envOrDefault("NAT_DEMO_GLOBAL_HOST_FALLBACK", "198.51.100.200")

	return scenario{
		Key:   "iface",
		Title: "Interface-scoped host rewrite",
		Description: "Uses iface-scoped rules so only the intended NICs are rewritten." +
			"Others fall back to the global mapping.",
		NetworkTypes:   []ice.NetworkType{ice.NetworkTypeUDP4},
		CandidateTypes: []ice.CandidateType{ice.CandidateTypeHost},
		RewriteRules: []ice.AddressRewriteRule{
			{
				External:        []string{publicBlue},
				Local:           localBlue,
				Iface:           blueIface,
				AsCandidateType: ice.CandidateTypeHost,
			},
			{
				External:        []string{publicGreen},
				Local:           localGreen,
				Iface:           greenIface,
				AsCandidateType: ice.CandidateTypeHost,
			},
			{
				External:        []string{globalFallback},
				AsCandidateType: ice.CandidateTypeHost,
			},
		},
	}
}

func buildSrflxScenario() scenario {
	hostLocal := envOrDefault("NAT_DEMO_SERVICE_LOCAL", "10.30.0.20")
	hostPublic := envOrDefault("NAT_DEMO_SERVICE_HOST_PUBLIC", "203.0.113.30")
	srflxLocal := envOrDefault("NAT_DEMO_SRFLX_LOCAL", "0.0.0.0")
	srflxPrimary := envOrDefault("NAT_DEMO_SRFLX_PRIMARY", "198.51.100.50")
	srflxSecondary := envOrDefault("NAT_DEMO_SRFLX_SECONDARY", "198.51.100.60")

	return scenario{
		Key:   "srflx",
		Title: "Server-reflexive pool plus host override",
		Description: "Publishes a pair of pre-defined srflx addresses (one bound to 0.0.0.0 and one catch-all) " +
			"plus a host mapping for an \"edge\" interface, mirroring NAT64/CLAT style deployments",
		NetworkTypes: []ice.NetworkType{
			ice.NetworkTypeUDP4,
		},
		CandidateTypes: []ice.CandidateType{
			ice.CandidateTypeHost,
			ice.CandidateTypeServerReflexive,
		},
		RewriteRules: []ice.AddressRewriteRule{
			{
				External:        []string{srflxPrimary},
				Local:           srflxLocal,
				AsCandidateType: ice.CandidateTypeServerReflexive,
				Networks:        []ice.NetworkType{ice.NetworkTypeUDP4},
			},
			{
				External:        []string{srflxSecondary},
				AsCandidateType: ice.CandidateTypeServerReflexive,
				Networks:        []ice.NetworkType{ice.NetworkTypeUDP4},
			},
			{
				External:        []string{hostPublic},
				Local:           hostLocal,
				AsCandidateType: ice.CandidateTypeHost,
				Networks:        []ice.NetworkType{ice.NetworkTypeUDP4},
			},
		},
	}
}

func buildScopedCatchAllScenario() scenario {
	scopedLocal := envOrDefault("NAT_DEMO_SERVICE_LOCAL", "10.30.0.20")
	scopedPublic := envOrDefault("NAT_DEMO_SCOPED_PUBLIC", "203.0.113.40")
	scopedCIDR := envOrDefault("NAT_DEMO_SCOPED_CIDR", "10.30.0.0/24")
	catchAll := envOrDefault("NAT_DEMO_GLOBAL_HOST_FALLBACK", "198.51.100.200")

	return scenario{
		Key:   "scoped",
		Title: "CIDR scoped rule with catch-all fallback",
		Description: "Limits mapping to a specific CIDR while" +
			" keeping a catch-all public address for any traffic that lands on other interfaces, " +
			"Demonstrates the rule precedence and scope matching order.",
		NetworkTypes: []ice.NetworkType{
			ice.NetworkTypeUDP4,
		},
		CandidateTypes: []ice.CandidateType{ice.CandidateTypeHost},
		RewriteRules: []ice.AddressRewriteRule{
			{
				External:        []string{scopedPublic},
				Local:           scopedLocal,
				AsCandidateType: ice.CandidateTypeHost,
				CIDR:            scopedCIDR,
				Networks:        []ice.NetworkType{ice.NetworkTypeUDP4},
			},
			{
				External:        []string{catchAll},
				AsCandidateType: ice.CandidateTypeHost,
				CIDR:            scopedCIDR,
				Networks:        []ice.NetworkType{ice.NetworkTypeUDP4},
			},
		},
	}
}

func runScenario(ctx context.Context, sc scenario) error { //nolint:cyclop
	fmt.Printf("\n=~~++= %s (%s) =~~++=\n%s\n", sc.Title, sc.Key, sc.Description)
	printRules(sc.RewriteRules)

	var opts []ice.AgentOption

	if len(sc.RewriteRules) > 0 {
		opts = append(opts, ice.WithAddressRewriteRules(sc.RewriteRules...))
	}
	if len(sc.NetworkTypes) > 0 {
		opts = append(opts, ice.WithNetworkTypes(sc.NetworkTypes))
	}
	if len(sc.CandidateTypes) > 0 {
		opts = append(opts, ice.WithCandidateTypes(sc.CandidateTypes))
	}

	var tcpMux *ice.TCPMuxDefault
	if sc.requiresTCPMux() {
		listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4zero, Port: 0})
		if err != nil {
			return fmt.Errorf("scenario %s: tcp listen: %w", sc.Key, err)
		}
		tcpMux = ice.NewTCPMuxDefault(ice.TCPMuxParams{
			Listener: listener,
			Logger:   logging.NewDefaultLoggerFactory().NewLogger("nat-demo/tcp"),
		})
		opts = append(opts, ice.WithTCPMux(tcpMux))
		fmt.Printf("TCP mux listening on %s\n", listener.Addr())
		defer func() {
			if err := tcpMux.Close(); err != nil {
				log.Printf("failed to close TCP mux: %v", err)
			}
		}()
	}

	agent, err := ice.NewAgentWithOptions(opts...) //nolint:contextcheck
	if err != nil {
		return fmt.Errorf("scenario %s: create agent: %w", sc.Key, err)
	}
	defer func() {
		if closeErr := agent.Close(); closeErr != nil {
			log.Printf("failed to close agent: %v", closeErr)
		}
	}()

	done := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var gathered []string

	err = agent.OnCandidate(func(c ice.Candidate) {
		if c == nil {
			once.Do(func() {
				close(done)
			})

			return
		}

		line := formatCandidate(c)
		mu.Lock()
		gathered = append(gathered, line)
		mu.Unlock()
		fmt.Println("  " + line)
	})
	if err != nil {
		return fmt.Errorf("scenario %s: set candidate handler: %w", sc.Key, err)
	}

	if err := agent.GatherCandidates(); err != nil { //nolint:contextcheck
		return fmt.Errorf("scenario %s: gather: %w", sc.Key, err)
	}

	timeout := sc.timeout()
	select {
	case <-ctx.Done():
		return fmt.Errorf("scenario %s: context canceled: %w", sc.Key, ctx.Err())
	case <-done:
	case <-time.After(timeout):
		return fmt.Errorf("scenario %s: gather timed out after %s", sc.Key, timeout) //nolint:err113
	}

	mu.Lock()
	total := len(gathered)
	mu.Unlock()

	if total == 0 {
		fmt.Println("  (no candidates gathered)")
	}

	fmt.Printf("Scenario %s complete: %d candidates reported.\n", sc.Key, total)

	return nil
}

func formatCandidate(c ice.Candidate) string {
	network := c.NetworkType().String()
	if c.NetworkType().IsTCP() && c.TCPType() != ice.TCPTypeUnspecified {
		network = fmt.Sprintf("%s/%s", network, c.TCPType())
	}

	rel := "none"
	if relAddr := c.RelatedAddress(); relAddr != nil {
		rel = fmt.Sprintf("%s:%d", relAddr.Address, relAddr.Port)
	}

	return fmt.Sprintf("%s via %s -> %s:%d (rel=%s priority=%d)",
		c.Type(), network, c.Address(), c.Port(), rel, c.Priority())
}

func printRules(rules []ice.AddressRewriteRule) {
	if len(rules) == 0 {
		fmt.Println("No NAT rules configured.")

		return
	}

	fmt.Println("Active address rewrite rules:")
	for idx, rule := range rules {
		candidateType := rule.AsCandidateType
		if candidateType == ice.CandidateTypeUnspecified {
			candidateType = ice.CandidateTypeHost
		}

		scope := describeRuleScope(rule)

		fmt.Printf("  %d) %s => [%s]%s\n",
			idx+1,
			candidateType,
			strings.Join(rule.External, ", "),
			scope)
	}
}

func describeRuleScope(rule ice.AddressRewriteRule) string {
	parts := make([]string, 0, 4)

	if rule.Iface != "" {
		parts = append(parts, fmt.Sprintf("iface=%s", rule.Iface))
	}
	if rule.Local != "" {
		parts = append(parts, fmt.Sprintf("local=%s", rule.Local))
	}
	if rule.CIDR != "" {
		parts = append(parts, fmt.Sprintf("cidr=%s", rule.CIDR))
	}
	if len(rule.Networks) > 0 {
		parts = append(parts, fmt.Sprintf("networks=%s", formatNetworkList(rule.Networks)))
	}

	if len(parts) == 0 {
		return ""
	}

	return " | " + strings.Join(parts, " ")
}

func formatNetworkList(networks []ice.NetworkType) string {
	if len(networks) == 0 {
		return "all"
	}

	names := make([]string, len(networks))
	for i, nt := range networks {
		names[i] = nt.String()
	}
	sort.Strings(names)

	return strings.Join(names, ",")
}

func envOrDefault(key, fallback string) string {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}

	return fallback
}

func printInterfaceSnapshot() {
	fmt.Println("\nLocal interface snapshot:")
	ifaces, err := net.Interfaces()
	if err != nil {
		fmt.Printf("  unable to list interfaces: %v\n\n", err)

		return
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil || len(addrs) == 0 {
			continue
		}

		addrTexts := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			addrTexts = append(addrTexts, addr.String())
		}

		fmt.Printf("  %s: %s\n", iface.Name, strings.Join(addrTexts, ", "))
	}
	fmt.Println()
}
