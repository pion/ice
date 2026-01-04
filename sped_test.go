// SPDX-FileCopyrightText: 2025 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"encoding/binary"
	"testing"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/require"
)

func TestDtlsInStunAttribute_GetFrom(t *testing.T) {
	m := new(stun.Message)
	var dtlsInStun DtlsInStunAttribute
	require.ErrorIs(t, stun.ErrAttributeNotFound, dtlsInStun.GetFrom(m))

	expectedValue := []byte{0x01, 0x02, 0x03, 0x04}
	m.Add(stun.AttrDtlsInStun, expectedValue)

	var dtlsInStun1 DtlsInStunAttribute
	require.NoError(t, dtlsInStun1.GetFrom(m))
	require.Equal(t, expectedValue, []byte(dtlsInStun1))
}

func TestDtlsInStunAttribute_AddTo(t *testing.T) {
	m := new(stun.Message)
	dtlsInStun := DtlsInStunAttribute([]byte{0x05, 0x06, 0x07, 0x08})
	require.NoError(t, dtlsInStun.AddTo(m))

	v, err := m.Get(stun.AttrDtlsInStun)
	require.NoError(t, err)
	require.Equal(t, []byte{0x05, 0x06, 0x07, 0x08}, v)
}

func TestDtlsInStunAckAttribute_GetFrom(t *testing.T) {
	m := new(stun.Message)
	var dtlsInStunAck DtlsInStunAckAttribute
	require.ErrorIs(t, stun.ErrAttributeNotFound, dtlsInStunAck.GetFrom(m))

	// Test with valid data
	expectedValue := []uint32{0x01020304, 0x05060708}
	byteValue := make([]byte, 8)
	binary.BigEndian.PutUint32(byteValue[0:4], expectedValue[0])
	binary.BigEndian.PutUint32(byteValue[4:8], expectedValue[1])
	m.Add(stun.AttrDtlsInStunAck, byteValue)

	var dtlsInStunAck1 DtlsInStunAckAttribute
	require.NoError(t, dtlsInStunAck1.GetFrom(m))
	require.Equal(t, expectedValue, []uint32(dtlsInStunAck1))

	// Test with invalid size (not multiple of 4)
	m2 := new(stun.Message)
	m2.Add(stun.AttrDtlsInStunAck, []byte{0x01, 0x02, 0x03})
	var dtlsInStunAck2 DtlsInStunAckAttribute
	require.ErrorIs(t, stun.ErrAttributeSizeInvalid, dtlsInStunAck2.GetFrom(m2))
	require.Empty(t, dtlsInStunAck2)

	// Test with invalid size (greater than ackSize)
	m3 := new(stun.Message)
	m3.Add(stun.AttrDtlsInStunAck, make([]byte, ackSizeBytes+4))
	var dtlsInStunAck3 DtlsInStunAckAttribute
	require.ErrorIs(t, stun.ErrAttributeSizeInvalid, dtlsInStunAck3.GetFrom(m3))
	require.Empty(t, dtlsInStunAck3)
}

func TestDtlsInStunAckAttribute_AddTo(t *testing.T) {
	m := new(stun.Message)
	dtlsInStunAck := DtlsInStunAckAttribute([]uint32{0x090a0b0c, 0x0d0e0f10})
	require.NoError(t, dtlsInStunAck.AddTo(m))

	v, err := m.Get(stun.AttrDtlsInStunAck)
	require.NoError(t, err)

	expectedByteValue := make([]byte, 8)
	binary.BigEndian.PutUint32(expectedByteValue[0:4], 0x090a0b0c)
	binary.BigEndian.PutUint32(expectedByteValue[4:8], 0x0d0e0f10)
	require.Equal(t, expectedByteValue, v)

	// Test with more than 4 elements (should not add to message)
	m2 := new(stun.Message)
	dtlsInStunAck2 := DtlsInStunAckAttribute([]uint32{1, 2, 3, 4, 5})
	require.ErrorIs(t, stun.ErrAttributeSizeInvalid, dtlsInStunAck2.AddTo(m2))
	_, err = m2.Get(stun.AttrDtlsInStunAck)
	require.ErrorIs(t, err, stun.ErrAttributeNotFound)
}
