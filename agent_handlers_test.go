// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"testing"
	"time"

	"github.com/pion/transport/v2/test"
)

func TestConnectionStateNotifier(t *testing.T) {
	t.Run("TestManyUpdates", func(t *testing.T) {
		report := test.CheckRoutines(t)
		defer report()
		updates := make(chan struct{}, 1)
		c := &handlerNotifier{
			connectionStateFunc: func(_ ConnectionState) {
				updates <- struct{}{}
			},
			done: make(chan struct{}),
		}
		// Enqueue all updates upfront to ensure that it
		// doesn't block
		for i := 0; i < 10000; i++ {
			c.EnqueueConnectionState(ConnectionStateNew)
		}
		done := make(chan struct{})
		go func() {
			for i := 0; i < 10000; i++ {
				<-updates
			}
			select {
			case <-updates:
				t.Errorf("received more updates than expected")
			case <-time.After(1 * time.Second):
			}
			close(done)
		}()
		<-done
		c.Close()
	})
	t.Run("TestUpdateOrdering", func(t *testing.T) {
		report := test.CheckRoutines(t)
		defer report()
		updates := make(chan ConnectionState)
		c := &handlerNotifier{
			connectionStateFunc: func(cs ConnectionState) {
				updates <- cs
			},
			done: make(chan struct{}),
		}
		done := make(chan struct{})
		go func() {
			for i := 0; i < 10000; i++ {
				x := <-updates
				if x != ConnectionState(i) {
					t.Errorf("expected %d got %d", x, i)
				}
			}
			select {
			case <-updates:
				t.Errorf("received more updates than expected")
			case <-time.After(1 * time.Second):
			}
			close(done)
		}()
		for i := 0; i < 10000; i++ {
			c.EnqueueConnectionState(ConnectionState(i))
		}
		<-done
		c.Close()
	})
}
