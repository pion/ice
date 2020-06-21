package ice

import "time"

// GetCandidatePairsStats returns a list of candidate pair stats
func (a *Agent) GetCandidatePairsStats() []CandidatePairStats {
	resultChan := make(chan []CandidatePairStats, 1)
	err := a.run(func(agent *Agent) {
		result := make([]CandidatePairStats, 0, len(agent.checklist))
		for _, cp := range agent.checklist {
			stat := CandidatePairStats{
				Timestamp:         time.Now(),
				LocalCandidateID:  cp.local.ID(),
				RemoteCandidateID: cp.remote.ID(),
				State:             cp.state,
				Nominated:         cp.nominated,
				// PacketsSent uint32
				// PacketsReceived uint32
				// BytesSent uint64
				// BytesReceived uint64
				// LastPacketSentTimestamp time.Time
				// LastPacketReceivedTimestamp time.Time
				// FirstRequestTimestamp time.Time
				// LastRequestTimestamp time.Time
				// LastResponseTimestamp time.Time
				// TotalRoundTripTime float64
				// CurrentRoundTripTime float64
				// AvailableOutgoingBitrate float64
				// AvailableIncomingBitrate float64
				// CircuitBreakerTriggerCount uint32
				// RequestsReceived uint64
				// RequestsSent uint64
				// ResponsesReceived uint64
				// ResponsesSent uint64
				// RetransmissionsReceived uint64
				// RetransmissionsSent uint64
				// ConsentRequestsSent uint64
				// ConsentExpiredTimestamp time.Time
			}
			result = append(result, stat)
		}
		resultChan <- result
	})
	if err != nil {
		a.log.Errorf("error getting candidate pairs stats %v", err)
		return []CandidatePairStats{}
	}
	return <-resultChan
}

// GetLocalCandidatesStats returns a list of local candidates stats
func (a *Agent) GetLocalCandidatesStats() []CandidateStats {
	resultChan := make(chan []CandidateStats, 1)
	err := a.run(func(agent *Agent) {
		result := make([]CandidateStats, 0, len(agent.localCandidates))
		for networkType, localCandidates := range agent.localCandidates {
			for _, c := range localCandidates {
				stat := CandidateStats{
					Timestamp:     time.Now(),
					ID:            c.ID(),
					NetworkType:   networkType,
					IP:            c.Address(),
					Port:          c.Port(),
					CandidateType: c.Type(),
					Priority:      c.Priority(),
					// URL string
					RelayProtocol: "udp",
					// Deleted bool
				}
				result = append(result, stat)
			}
		}
		resultChan <- result
	})
	if err != nil {
		a.log.Errorf("error getting candidate pairs stats %v", err)
		return []CandidateStats{}
	}
	return <-resultChan
}

// GetRemoteCandidatesStats returns a list of remote candidates stats
func (a *Agent) GetRemoteCandidatesStats() []CandidateStats {
	resultChan := make(chan []CandidateStats, 1)
	err := a.run(func(agent *Agent) {
		result := make([]CandidateStats, 0, len(agent.remoteCandidates))
		for networkType, localCandidates := range agent.remoteCandidates {
			for _, c := range localCandidates {
				stat := CandidateStats{
					Timestamp:     time.Now(),
					ID:            c.ID(),
					NetworkType:   networkType,
					IP:            c.Address(),
					Port:          c.Port(),
					CandidateType: c.Type(),
					Priority:      c.Priority(),
					// URL string
					RelayProtocol: "udp",
				}
				result = append(result, stat)
			}
		}
		resultChan <- result
	})
	if err != nil {
		a.log.Errorf("error getting candidate pairs stats %v", err)
		return []CandidateStats{}
	}
	return <-resultChan
}
