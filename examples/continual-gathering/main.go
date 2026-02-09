// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// Package main demonstrates the ContinualGatheringPolicy feature
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/logging"
)

func main() { //nolint:cyclop
	var gatheringMode string
	var monitorInterval time.Duration

	flag.StringVar(&gatheringMode, "mode", "continually", "Gathering mode: 'once' or 'continually'")
	flag.DurationVar(&monitorInterval, "interval", 2*time.Second, "Network monitoring interval (for continual mode)")
	flag.Parse()

	// Determine gathering policy
	var policy ice.ContinualGatheringPolicy
	switch gatheringMode {
	case "once":
		policy = ice.GatherOnce
		fmt.Println("Using GatherOnce policy - gathering will complete after initial collection")
	case "continually":
		policy = ice.GatherContinually
		fmt.Printf("Using GatherContinually policy - monitoring for network changes every %v\n", monitorInterval)
	default:
		log.Fatalf("Invalid mode: %s. Use 'once' or 'continually'", gatheringMode)
	}

	// Create logger
	loggerFactory := logging.NewDefaultLoggerFactory()
	loggerFactory.DefaultLogLevel = logging.LogLevelDebug

	// Create ICE agent with the specified gathering policy using AgentOptions
	agent, err := ice.NewAgentWithOptions(
		ice.WithNetworkTypes([]ice.NetworkType{ice.NetworkTypeUDP4, ice.NetworkTypeUDP6}),
		ice.WithCandidateTypes([]ice.CandidateType{ice.CandidateTypeHost}),
		ice.WithContinualGatheringPolicy(policy),
		ice.WithNetworkMonitorInterval(monitorInterval),
	)
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}

	defer func() {
		if closeErr := agent.Close(); closeErr != nil {
			log.Printf("Failed to close agent: %v", closeErr)
		}
	}()

	// Track candidates
	candidateCount := 0
	candidateMap := make(map[string]ice.Candidate)

	// Set up candidate handler
	err = agent.OnCandidate(func(candidate ice.Candidate) {
		if candidate == nil {
			if policy == ice.GatherOnce {
				fmt.Println("\n=== Gathering completed (no more candidates) ===")
			}

			return
		}

		candidateCount++
		candidateID := candidate.String()

		if _, exists := candidateMap[candidateID]; !exists {
			candidateMap[candidateID] = candidate
			fmt.Printf("[%s] Candidate #%d: %s\n", time.Now().Format("15:04:05"), candidateCount, candidate)
		}
	})
	if err != nil {
		log.Fatalf("Failed to set candidate handler: %v", err) //nolint:gocritic
	}

	// Start gathering
	fmt.Println("\n=== Starting candidate gathering ===")
	err = agent.GatherCandidates()
	if err != nil {
		log.Fatalf("Failed to start gathering: %v", err)
	}

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Create a context for periodic status checks
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Periodically check and display gathering state
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				state, err := agent.GetGatheringState() //nolint:contextcheck
				if err != nil {
					log.Printf("Failed to get gathering state: %v", err)

					continue
				}

				localCandidates, err := agent.GetLocalCandidates() //nolint:contextcheck
				if err != nil {
					log.Printf("Failed to get local candidates: %v", err)

					continue
				}

				fmt.Printf("\n[%s] Status: GatheringState=%s, Candidates=%d\n",
					time.Now().Format("15:04:05"), state, len(localCandidates))

				if policy == ice.GatherContinually {
					fmt.Println("Tip: Try changing network interfaces (connect/disconnect WiFi, enable/disable network adapters)")
					fmt.Println("     New candidates will be discovered automatically!")
				}
			}
		}
	}()

	// Wait for interrupt signal
	fmt.Println("\nPress Ctrl+C to exit...")
	<-sigChan

	fmt.Println("\n=== Shutting down ===")
	cancel()

	// Display final statistics
	state, _ := agent.GetGatheringState()
	localCandidates, _ := agent.GetLocalCandidates()

	fmt.Printf("\nFinal Statistics:\n")
	fmt.Printf("  Gathering Policy: %s\n", policy)
	fmt.Printf("  Gathering State: %s\n", state)
	fmt.Printf("  Total Candidates Discovered: %d\n", candidateCount)
	fmt.Printf("  Unique Candidates: %d\n", len(candidateMap))
	fmt.Printf("  Current Active Candidates: %d\n", len(localCandidates))
}
