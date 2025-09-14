// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"testing"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/require"
)

func TestUseCandidateAttr_AddTo(t *testing.T) {
	msg := stun.New()
	msg.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}
	require.False(t, UseCandidate().IsSet(msg))

	require.NoError(t, UseCandidate().AddTo(msg))
	msg.Encode()

	msg2 := &stun.Message{Raw: append([]byte{}, msg.Raw...)}
	require.NoError(t, msg2.Decode())
	require.True(t, UseCandidate().IsSet(msg2))
}
