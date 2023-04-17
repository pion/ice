package main

import (
	"flag"
	"fmt"

	"gitlab.mty.wang/kepler/ice"
)

var (
	iceUrl        string
	icePort       int
	predictNumber int
)

func main() {
	flag.StringVar(&iceUrl, "i", "stun.mty.wang", "set ice server.")
	flag.IntVar(&icePort, "p", 3478, "set ice port.")
	flag.IntVar(&predictNumber, "n", 10, "set predict number.")
	flag.Parse()

	urls := []*ice.URL{}
	urls = append(urls, &ice.URL{
		Scheme: ice.SchemeTypeSTUN,
		Host:   iceUrl,
		Port:   icePort,
	})

	iceAgent, err := ice.NewAgent(&ice.AgentConfig{
		Urls:               urls,
		NetworkTypes:       []ice.NetworkType{ice.NetworkTypeUDP4},
		CandidateTypes:     []ice.CandidateType{ice.CandidateTypeServerReflexive},
		SrflxPredictNumber: predictNumber,
	})
	if err != nil {
		panic(err)
	}

	if err = iceAgent.OnCandidate(func(c ice.Candidate) {
		if c == nil {
			return
		}
		fmt.Println("Local Candidate gathered: ", c.String())
	}); err != nil {
		panic(err)
	}

	if err = iceAgent.GatherCandidates(); err != nil {
		panic(err)
	}

	select {}
}
