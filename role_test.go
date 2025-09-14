// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnmarshalText_Success(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Role
	}{
		{"controlling", "controlling", Controlling},
		{"controlled", "controlled", Controlled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r Role
			err := r.UnmarshalText([]byte(tt.in))
			require.NoError(t, err)
			require.Equal(t, tt.want, r)
		})
	}
}

func TestUnmarshalText_UnknownKeepsValueAndErrors(t *testing.T) {
	r := Controlled
	err := r.UnmarshalText([]byte("neither"))
	require.ErrorIs(t, err, errUnknownRole)
	require.Equal(t, Controlled, r, "role should remain unchanged on error")
}

func TestMarshalText(t *testing.T) {
	tests := []struct {
		name string
		in   Role
		want string
	}{
		{"controlling", Controlling, "controlling"},
		{"controlled", Controlled, "controlled"},
		{"unknown", Role(99), "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := tt.in.MarshalText()
			require.NoError(t, err)
			require.Equal(t, tt.want, string(b))
		})
	}
}

func TestString(t *testing.T) {
	require.Equal(t, "controlling", Controlling.String())
	require.Equal(t, "controlled", Controlled.String())
	require.Equal(t, "unknown", Role(255).String())
}
