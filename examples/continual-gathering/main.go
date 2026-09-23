// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// Package main demonstrates continual gathering using a transport network change detector.
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

	"github.com/pion/ice/v5"
	"github.com/pion/transport/v5/netchange"
)

func main() {
	mode := flag.String("mode", "continually", "Gathering mode: 'once' or 'continually'")
	interval := flag.Duration("interval", 2*time.Second, "Network refresh fallback interval")
	flag.Parse()
	if *mode != "once" && *mode != "continually" {
		log.Fatal("Invalid mode: use 'once' or 'continually'")
	}
	if *interval <= 0 {
		log.Fatal("Interval must be positive")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *mode, *interval); err != nil {
		log.Printf("Continual gathering failed: %v", err)
	}
}

func run(ctx context.Context, mode string, interval time.Duration) error { //nolint:cyclop
	detector, err := netchange.NewDetector(
		netchange.WithPlatformTimeout(interval),
		netchange.WithPollInterval(interval),
	)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := detector.Close(); closeErr != nil {
			log.Printf("Failed to close detector: %v", closeErr)
		}
	}()

	agent, err := ice.NewAgent(ice.WithNet(detector)) //nolint:contextcheck // NewAgent has no context option.
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := agent.Close(); closeErr != nil {
			log.Printf("Failed to close agent: %v", closeErr)
		}
	}()

	gathered := make(chan struct{}, 1)
	if err = agent.OnCandidate(func(candidate ice.Candidate) {
		if candidate == nil {
			fmt.Println("=== Gathering pass completed ===")
			gathered <- struct{}{}

			return
		}
		fmt.Printf("[%s] Candidate: %s\n", time.Now().Format("15:04:05"), candidate)
	}); err != nil {
		return err
	}

	fmt.Println("Monitoring network interfaces. Press Ctrl+C to exit.")
	for {
		// The first Check returns the initial interfaces immediately. Later calls
		// block until a change and refresh the network snapshot used by the agent.
		changes, checkErr := detector.Check(ctx)
		if checkErr != nil {
			if ctx.Err() != nil {
				return nil
			}

			return checkErr
		}
		fmt.Printf("Network changes: %v\n", changes)
		if err = agent.Gather( //nolint:contextcheck // Gather uses the agent's lifetime context.
			ice.WithNetworkTypes([]ice.NetworkType{ice.NetworkTypeUDP4, ice.NetworkTypeUDP6}),
			ice.WithCandidateTypes([]ice.CandidateType{ice.CandidateTypeHost}),
		); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-gathered:
		}
		if mode == "once" {
			return nil
		}
	}
}
