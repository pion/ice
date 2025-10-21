// SPDX-FileCopyrightText: 2025 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// Package main demonstrates automatic renomination with real network interfaces.
// Run two instances of this program - one controlling and one controlled - and use
// traffic control (tc) commands to simulate network changes and trigger automatic renomination.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/logging"
)

const (
	rttNotAvailable = "N/A"
)

//nolint:gochecknoglobals
var (
	isControlling                 bool
	iceAgent                      *ice.Agent
	remoteAuthChannel             chan string
	localHTTPPort, remoteHTTPPort int
	localHTTPAddr, remoteHTTPAddr string
	selectedLocalCandidateID      string
	selectedRemoteCandidateID     string
)

// getRTT returns the current RTT for the selected candidate pair.
func getRTT() string {
	if selectedLocalCandidateID == "" || selectedRemoteCandidateID == "" {
		return rttNotAvailable
	}

	stats := iceAgent.GetCandidatePairsStats()
	for _, stat := range stats {
		if stat.LocalCandidateID == selectedLocalCandidateID && stat.RemoteCandidateID == selectedRemoteCandidateID {
			if stat.CurrentRoundTripTime > 0 {
				return fmt.Sprintf("%.2fms", stat.CurrentRoundTripTime*1000)
			}

			return rttNotAvailable
		}
	}

	return rttNotAvailable
}

// HTTP Listener to get ICE Credentials from remote Peer.
func remoteAuth(_ http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		panic(err)
	}

	remoteAuthChannel <- r.PostForm["ufrag"][0]
	remoteAuthChannel <- r.PostForm["pwd"][0]
}

// HTTP Listener to get ICE Candidate from remote Peer.
func remoteCandidate(_ http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		panic(err)
	}

	c, err := ice.UnmarshalCandidate(r.PostForm["candidate"][0])
	if err != nil {
		panic(err)
	}

	if err := iceAgent.AddRemoteCandidate(c); err != nil { //nolint:contextcheck
		panic(err)
	}

	fmt.Printf("Added remote candidate: %s\n", c)
}

func main() { //nolint:cyclop,maintidx
	var (
		err  error
		conn *ice.Conn
	)
	remoteAuthChannel = make(chan string, 3)

	flag.BoolVar(&isControlling, "controlling", false, "is ICE Agent controlling")
	flag.Parse()

	if isControlling {
		// Controlling agent runs in ns1 namespace
		localHTTPPort = 9000
		remoteHTTPPort = 9001
		localHTTPAddr = "192.168.100.2"  // veth1 in ns1
		remoteHTTPAddr = "192.168.100.1" // veth0 in default namespace
	} else {
		// Controlled agent runs in default namespace
		localHTTPPort = 9001
		remoteHTTPPort = 9000
		localHTTPAddr = "192.168.100.1"  // veth0 in default namespace
		remoteHTTPAddr = "192.168.100.2" // veth1 in ns1
	}

	http.HandleFunc("/remoteAuth", remoteAuth)
	http.HandleFunc("/remoteCandidate", remoteCandidate)
	go func() {
		if err = http.ListenAndServe(fmt.Sprintf("%s:%d", localHTTPAddr, localHTTPPort), nil); err != nil { //nolint:gosec
			panic(err)
		}
	}()

	fmt.Println("=== Automatic Renomination Example ===")
	if isControlling {
		fmt.Println("Local Agent is CONTROLLING (with automatic renomination enabled)")
	} else {
		fmt.Println("Local Agent is CONTROLLED")
	}
	fmt.Println()
	fmt.Print("Press 'Enter' when both processes have started")
	if _, err = bufio.NewReader(os.Stdin).ReadBytes('\n'); err != nil {
		panic(err)
	}

	// Create the ICE agent with automatic renomination enabled on the controlling side
	// Use InterfaceFilter to constrain to veth interfaces for testing
	interfaceFilter := func(interfaceName string) bool {
		// Allow all veth interfaces (veth0, veth1, veth2, veth3)
		// This gives us multiple candidate pairs for automatic renomination
		return len(interfaceName) >= 4 && interfaceName[:4] == "veth"
	}

	// Create a logger factory with Debug level enabled
	loggerFactory := logging.NewDefaultLoggerFactory()
	loggerFactory.DefaultLogLevel = logging.LogLevelDebug

	if isControlling {
		renominationInterval := 3 * time.Second
		iceAgent, err = ice.NewAgentWithOptions(
			ice.WithNetworkTypes([]ice.NetworkType{ice.NetworkTypeUDP4, ice.NetworkTypeUDP6}),
			ice.WithInterfaceFilter(interfaceFilter),
			ice.WithLoggerFactory(loggerFactory),
			ice.WithRenomination(ice.DefaultNominationValueGenerator()),
			ice.WithAutomaticRenomination(renominationInterval),
		)
	} else {
		iceAgent, err = ice.NewAgentWithOptions(
			ice.WithNetworkTypes([]ice.NetworkType{ice.NetworkTypeUDP4, ice.NetworkTypeUDP6}),
			ice.WithInterfaceFilter(interfaceFilter),
			ice.WithLoggerFactory(loggerFactory),
		)
	}
	if err != nil {
		panic(err)
	}

	// When we have gathered a new ICE Candidate send it to the remote peer
	if err = iceAgent.OnCandidate(func(c ice.Candidate) {
		if c == nil {
			return
		}

		fmt.Printf("Local candidate: %s\n", c)

		_, err = http.PostForm(fmt.Sprintf("http://%s:%d/remoteCandidate", remoteHTTPAddr, remoteHTTPPort), //nolint
			url.Values{
				"candidate": {c.Marshal()},
			})
		if err != nil {
			panic(err)
		}
	}); err != nil {
		panic(err)
	}

	// When ICE Connection state has changed print to stdout
	if err = iceAgent.OnConnectionStateChange(func(c ice.ConnectionState) {
		fmt.Printf(">>> ICE Connection State: %s\n", c.String())
	}); err != nil {
		panic(err)
	}

	// When selected candidate pair changes, print it
	if err = iceAgent.OnSelectedCandidatePairChange(func(local, remote ice.Candidate) {
		// Track the selected candidate IDs for RTT lookup
		selectedLocalCandidateID = local.ID()
		selectedRemoteCandidateID = remote.ID()

		fmt.Println()
		fmt.Println(">>> SELECTED CANDIDATE PAIR CHANGED <<<")
		fmt.Printf("    Local:  %s (type: %s)\n", local, local.Type())
		fmt.Printf("    Remote: %s (type: %s)\n", remote, remote.Type())
		fmt.Println()
	}); err != nil {
		panic(err)
	}

	// Get the local auth details and send to remote peer
	localUfrag, localPwd, err := iceAgent.GetLocalUserCredentials()
	if err != nil {
		panic(err)
	}

	_, err = http.PostForm(fmt.Sprintf("http://%s:%d/remoteAuth", remoteHTTPAddr, remoteHTTPPort), //nolint
		url.Values{
			"ufrag": {localUfrag},
			"pwd":   {localPwd},
		})
	if err != nil {
		panic(err)
	}

	remoteUfrag := <-remoteAuthChannel
	remotePwd := <-remoteAuthChannel

	if err = iceAgent.GatherCandidates(); err != nil {
		panic(err)
	}

	fmt.Println("Gathering candidates...")
	time.Sleep(2 * time.Second) // Give time for candidate gathering

	// Start the ICE Agent. One side must be controlled, and the other must be controlling
	fmt.Println("Starting ICE connection...")
	if isControlling {
		conn, err = iceAgent.Dial(context.Background(), remoteUfrag, remotePwd)
	} else {
		conn, err = iceAgent.Accept(context.Background(), remoteUfrag, remotePwd)
	}
	if err != nil {
		panic(err)
	}

	fmt.Println()
	fmt.Println("=== CONNECTED ===")
	fmt.Println()

	if isControlling {
		fmt.Println("Automatic renomination is enabled on the controlling agent.")
		fmt.Println("To test it, you can use traffic control (tc) to change network conditions:")
		fmt.Println()
		fmt.Println("  # Add 100ms latency to veth0:")
		fmt.Println("  sudo tc qdisc add dev veth0 root netem delay 100ms")
		fmt.Println()
		fmt.Println("  # Remove the latency:")
		fmt.Println("  sudo tc qdisc del dev veth0 root")
		fmt.Println()
		fmt.Println("Watch for \"SELECTED CANDIDATE PAIR CHANGED\" messages above.")
		fmt.Println("The agent will automatically renominate to better paths when detected.")
		fmt.Println()

		// Print debug info about candidate pairs every 5 seconds
		go func() {
			for {
				time.Sleep(5 * time.Second)
				fmt.Println()
				fmt.Println("=== DEBUG: Candidate Pair States ===")
				stats := iceAgent.GetCandidatePairsStats()
				for _, stat := range stats {
					fmt.Printf("  %s <-> %s\n", stat.LocalCandidateID, stat.RemoteCandidateID)
					fmt.Printf("    State: %s, Nominated: %v\n", stat.State, stat.Nominated)
					if stat.CurrentRoundTripTime > 0 {
						fmt.Printf("    RTT: %.2fms\n", stat.CurrentRoundTripTime*1000)
					}
				}
				fmt.Println("===================================")
				fmt.Println()
			}
		}()
	}

	// Send a message every 5 seconds
	go func() {
		counter := 0
		for {
			time.Sleep(5 * time.Second)
			counter++

			role := "controlling"
			if !isControlling {
				role = "controlled"
			}
			msg := fmt.Sprintf("Message #%d from %s agent", counter, role)
			if _, err = conn.Write([]byte(msg)); err != nil {
				fmt.Printf("Write error: %v\n", err)

				return
			}

			fmt.Printf("Sent: %s [RTT: %s]\n", msg, getRTT())
		}
	}()

	// Receive messages in a loop from the remote peer
	buf := make([]byte, 1500)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			fmt.Printf("Read error: %v\n", err)

			return
		}

		fmt.Printf("Received: %s [RTT: %s]\n", string(buf[:n]), getRTT())
	}
}
