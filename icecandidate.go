package ice

import (
	"fmt"
	"strings"
	"strconv"
)

// ICECandidate represents a ice candidate
type ICECandidate struct {
	StatsID        string
	Foundation     string           		`json:"foundation"`
	Priority       uint32           		`json:"priority"`
	Address        string           		`json:"address"`
	Protocol       ICEProtocol      		`json:"protocol"`
	Port           uint16           		`json:"port"`
	Typ            ICECandidateType	 		`json:"type"`
	Component      uint16           		`json:"component"`
	RelatedAddress string           		`json:"relatedAddress"`
	RelatedPort    uint16           		`json:"relatedPort"`
	TCPType        string           		`json:"tcpType"`
	ExtensionAttributes []ICECandidateAttribute     `json:"extensionAttributes"`
}

// ICECandidateAttribute represents an ICE candidate extension attribute
type ICECandidateAttribute struct {
	Key   string
	Value string
}

// Conversion for package ice
func NewICECandidatesFromICE(iceCandidates []Candidate) ([]ICECandidate, error) {
	candidates := []ICECandidate{}

	for _, i := range iceCandidates {
		c, err := NewICECandidateFromICE(i)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}

	return candidates, nil
}

func NewICECandidateFromICE(i Candidate) (ICECandidate, error) {
	typ, err := ConvertTypeFromICE(i.Type())
	if err != nil {
		return ICECandidate{}, err
	}
	protocol, err := NewICEProtocol(i.NetworkType().NetworkShort())
	if err != nil {
		return ICECandidate{}, err
	}

	c := ICECandidate{
		StatsID:    i.ID(),
		Foundation: "foundation",
		Priority:   i.Priority(),
		Address:    i.Address(),
		Protocol:   protocol,
		Port:       uint16(i.Port()),
		Component:  i.Component(),
		Typ:        typ,
		TCPType:    i.TCPType().String(),
	}

	if i.RelatedAddress() != nil {
		c.RelatedAddress = i.RelatedAddress().Address
		c.RelatedPort = uint16(i.RelatedAddress().Port)
	}

	return c, nil
}

func (c ICECandidate) ToICE() (Candidate, error) {
	candidateID := c.StatsID
	switch c.Typ {
	case ICECandidateTypeHost:
		config := CandidateHostConfig{
			CandidateID: candidateID,
			Network:     c.Protocol.String(),
			Address:     c.Address,
			Port:        int(c.Port),
			Component:   c.Component,
			TCPType:     NewTCPType(c.TCPType),
		}
		return NewCandidateHost(&config)
	case ICECandidateTypeSrflx:
		config := CandidateServerReflexiveConfig{
			CandidateID: candidateID,
			Network:     c.Protocol.String(),
			Address:     c.Address,
			Port:        int(c.Port),
			Component:   c.Component,
			RelAddr:     c.RelatedAddress,
			RelPort:     int(c.RelatedPort),
		}
		return NewCandidateServerReflexive(&config)
	case ICECandidateTypePrflx:
		config := CandidatePeerReflexiveConfig{
			CandidateID: candidateID,
			Network:     c.Protocol.String(),
			Address:     c.Address,
			Port:        int(c.Port),
			Component:   c.Component,
			RelAddr:     c.RelatedAddress,
			RelPort:     int(c.RelatedPort),
		}
		return NewCandidatePeerReflexive(&config)
	case ICECandidateTypeRelay:
		config := CandidateRelayConfig{
			CandidateID: candidateID,
			Network:     c.Protocol.String(),
			Address:     c.Address,
			Port:        int(c.Port),
			Component:   c.Component,
			RelAddr:     c.RelatedAddress,
			RelPort:     int(c.RelatedPort),
		}
		return NewCandidateRelay(&config)
	default:
		return nil, fmt.Errorf("unknown candidate type: %s", c.Typ)
	}
}

func ConvertTypeFromICE(t CandidateType) (ICECandidateType, error) {
	switch t {
	case CandidateTypeHost:
		return ICECandidateTypeHost, nil
	case CandidateTypeServerReflexive:
		return ICECandidateTypeSrflx, nil
	case CandidateTypePeerReflexive:
		return ICECandidateTypePrflx, nil
	case CandidateTypeRelay:
		return ICECandidateTypeRelay, nil
	default:
		return ICECandidateType(t), fmt.Errorf("unknown ICE candidate type: %s", t)
	}
}

func (c ICECandidate) String() string {
	ic, err := c.ToICE()
	if err != nil {
		return fmt.Sprintf("%#v failed to convert to ICE: %s", c, err)
	}
	return ic.String()
}

// ToJSON returns an ICECandidateInit
// as indicated by the spec https://w3c.github.io/webrtc-pc/#dom-rtcicecandidate-tojson
func (c ICECandidate) ToJSON() ICECandidateInit {
	zeroVal := uint16(0)
	emptyStr := ""

	return ICECandidateInit{
		Candidate:     fmt.Sprintf("candidate:%s", c.Marshal()),
		SDPMid:        &emptyStr,
		SDPMLineIndex: &zeroVal,
	}
}

// Marshal returns the string representation of the ICECandidate
func (c ICECandidate) Marshal() string {
	val := fmt.Sprintf("%s %d %s %d %s %d typ %s",
		c.Foundation,
		c.Component,
		c.Protocol,
		c.Priority,
		c.Address,
		c.Port,
		c.Typ)

	if len(c.RelatedAddress) > 0 {
		val = fmt.Sprintf("%s raddr %s rport %d",
			val,
			c.RelatedAddress,
			c.RelatedPort)
	}

	for _, attr := range c.ExtensionAttributes {
		val = fmt.Sprintf("%s %s %s",
			val,
			attr.Key,
			attr.Value)
	}
	return val
}

// Unmarshal popuulates the ICECandidate from its string representation
func (c *ICECandidate) Unmarshal(raw string) error {
	split := strings.Fields(raw)
	if len(split) < 8 {
		return fmt.Errorf("attribute not long enough to be ICE candidate (%d)", len(split))
	}

	// Foundation
	c.Foundation = split[0]

	// Component
	component, err := strconv.ParseUint(split[1], 10, 16)
	if err != nil {
		return fmt.Errorf("could not parse component: %v", err)
	}
	c.Component = uint16(component)

	// Protocol
	c.Protocol, _ = NewICEProtocol(split[2])

	// Priority
	priority, err := strconv.ParseUint(split[3], 10, 32)
	if err != nil {
		return fmt.Errorf("could not parse priority: %v", err)
	}
	c.Priority = uint32(priority)

	// Address
	c.Address = split[4]

	// Port
	port, err := strconv.ParseUint(split[5], 10, 16)
	if err != nil {
		return fmt.Errorf("could not parse port: %v", err)
	}
	c.Port = uint16(port)

	c.Typ, _ = NewICECandidateType(split[7])

	if len(split) <= 8 {
		return nil
	}

	split = split[8:]

	if split[0] == "raddr" {
		if len(split) < 4 {
			return fmt.Errorf("could not parse related addresses: incorrect length")
		}

		// RelatedAddress
		c.RelatedAddress = split[1]

		// RelatedPort
		relatedPort, err := strconv.ParseUint(split[3], 10, 16)
		if err != nil {
			return fmt.Errorf("could not parse port: %v", err)
		}
		c.RelatedPort = uint16(relatedPort)

		if len(split) <= 4 {
			return nil
		}

		split = split[4:]
	}

	for i := 0; len(split) > i+1; i += 2 {
		c.ExtensionAttributes = append(c.ExtensionAttributes, ICECandidateAttribute{
			Key:   split[i],
			Value: split[i+1],
		})
	}

	return nil
}
