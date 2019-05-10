package ice

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/pion/stun"
)

func assertInboundUsername(m *stun.Message, expectedUsername string) error {
	usernameAttr := &stun.Username{}
	usernameRawAttr, usernameFound := m.GetOneAttribute(stun.AttrUsername)

	if !usernameFound {
		return fmt.Errorf("inbound packet missing Username")
	} else if err := usernameAttr.Unpack(m, usernameRawAttr); err != nil {
		return err
	}

	if usernameAttr.Username != expectedUsername {
		return fmt.Errorf("username mismatch expected(%x) actual(%x)", expectedUsername, usernameAttr.Username)
	}

	return nil
}

func assertInboundMessageIntegrity(m *stun.Message, key []byte) error {
	messageIntegrityAttr := &stun.MessageIntegrity{}
	messageIntegrityRawAttr, messageIntegrityAttrFound := m.GetOneAttribute(stun.AttrMessageIntegrity)

	if !messageIntegrityAttrFound {
		return fmt.Errorf("inbound packet missing MessageIntegrity")
	} else if err := messageIntegrityAttr.Unpack(m, messageIntegrityRawAttr); err != nil {
		return err
	}

	tailLength := messageIntegrityRawAttr.Length + stunAttrHeaderLength
	rawCopy := make([]byte, len(m.Raw))
	copy(rawCopy, m.Raw)

	// If we have a fingerprint we need to exclude it from the MessageIntegrity computation
	if rawFingerprint, hasFingerprint := m.GetOneAttribute(stun.AttrFingerprint); hasFingerprint {
		fingerprintLength := rawFingerprint.Length + stunAttrHeaderLength
		tailLength += fingerprintLength

		// Rewrite the packet header to be new length (excluding values we don't care about)
		currLength := binary.BigEndian.Uint16(rawCopy[2:4])
		binary.BigEndian.PutUint16(rawCopy[2:], currLength-fingerprintLength)
	}

	lengthToHash := len(rawCopy) - int(tailLength)
	if lengthToHash < 1 {
		return fmt.Errorf("unable to assert MessageIntegrity, length calculation failed (%d)", lengthToHash)
	}

	computedMessageIntegrity, err := stun.MessageIntegrityCalculateHMAC(key, rawCopy[:lengthToHash])
	if err != nil {
		return err
	} else if !bytes.Equal(computedMessageIntegrity, messageIntegrityRawAttr.Value) {
		return fmt.Errorf("messageIntegrity mismatch expected(%x) actual(%x)", computedMessageIntegrity, messageIntegrityRawAttr.Value)
	}

	return nil
}
