package ice

import (
	"fmt"

	"github.com/pion/sdp/v2"
)

// ICECandidate represents a ice candidate
type ICECandidate struct {
	StatsID        string
	Foundation     string           `json:"foundation"`
	Priority       uint32           `json:"priority"`
	Address        string           `json:"address"`
	Protocol       ICEProtocol      `json:"protocol"`
	Port           uint16           `json:"port"`
	Typ            ICECandidateType `json:"type"`
	Component      uint16           `json:"component"`
	RelatedAddress string           `json:"relatedAddress"`
	RelatedPort    uint16           `json:"relatedPort"`
	TCPType        string           `json:"tcpType"`
}

// Conversion for package ice
func NewICECandidatesFromICE(iceCandidates []Candidate) ([]ICECandidate, error) {
	candidates := []ICECandidate{}

	for _, i := range iceCandidates {
		c, err := newICECandidateFromICE(i)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}

	return candidates, nil
}

func newICECandidateFromICE(i Candidate) (ICECandidate, error) {
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

func iceCandidateToSDP(c ICECandidate) sdp.ICECandidate {
	var extensions []sdp.ICECandidateAttribute

	if c.Protocol == ICEProtocolTCP && c.TCPType != "" {
		extensions = append(extensions, sdp.ICECandidateAttribute{
			Key:   "tcptype",
			Value: c.TCPType,
		})
	}

	return sdp.ICECandidate{
		Foundation:          c.Foundation,
		Priority:            c.Priority,
		Address:             c.Address,
		Protocol:            c.Protocol.String(),
		Port:                c.Port,
		Component:           c.Component,
		Typ:                 c.Typ.String(),
		RelatedAddress:      c.RelatedAddress,
		RelatedPort:         c.RelatedPort,
		ExtensionAttributes: extensions,
	}
}

func newICECandidateFromSDP(c sdp.ICECandidate) (ICECandidate, error) {
	typ, err := NewICECandidateType(c.Typ)
	if err != nil {
		return ICECandidate{}, err
	}
	protocol, err := NewICEProtocol(c.Protocol)
	if err != nil {
		return ICECandidate{}, err
	}

	var tcpType string
	if protocol == ICEProtocolTCP {
		for _, attr := range c.ExtensionAttributes {
			if attr.Key == "tcptype" {
				tcpType = attr.Value
				break
			}
		}
	}

	return ICECandidate{
		Foundation:     c.Foundation,
		Priority:       c.Priority,
		Address:        c.Address,
		Protocol:       protocol,
		Port:           c.Port,
		Component:      c.Component,
		Typ:            typ,
		RelatedAddress: c.RelatedAddress,
		RelatedPort:    c.RelatedPort,
		TCPType:        tcpType,
	}, nil
}

// ToJSON returns an ICECandidateInit
// as indicated by the spec https://w3c.github.io/webrtc-pc/#dom-rtcicecandidate-tojson
func (c ICECandidate) ToJSON() ICECandidateInit {
	zeroVal := uint16(0)
	emptyStr := ""

	return ICECandidateInit{
		Candidate:     fmt.Sprintf("candidate:%s", iceCandidateToSDP(c).Marshal()),
		SDPMid:        &emptyStr,
		SDPMLineIndex: &zeroVal,
	}
}
