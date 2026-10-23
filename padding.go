// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import "github.com/pion/stun/v4"

// The PADDING attribute header, and the MESSAGE-INTEGRITY (HMAC-SHA1) and FINGERPRINT (CRC-32)
// attributes that follow it, each with its 4-byte attribute header.
const paddingOverhead = 4 + (4 + 20) + (4 + 4)

// paddingAttr pads a STUN message to at least the given size with a PADDING attribute
// (RFC 5780 Section 7.6). It leaves room for MESSAGE-INTEGRITY and FINGERPRINT, which must be
// added right after it.
type paddingAttr int

// AddTo adds PADDING attribute to message.
func (p paddingAttr) AddTo(m *stun.Message) error {
	// Add rounds the value up to a multiple of 4 bytes, so the message may end up slightly larger.
	m.Add(stun.AttrPadding, make([]byte, max(0, int(p)-len(m.Raw)-paddingOverhead)))

	return nil
}
