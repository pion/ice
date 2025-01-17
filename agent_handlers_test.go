// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"testing"
	"time"

	"github.com/pion/transport/v3/test"
)

func TestConnectionStateNotifier(t *testing.T) {
	t.Run("TestManyUpdates", func(t *testing.T) {
		defer test.CheckRoutines(t)()

		updates := make(chan struct{}, 1)
		notifier := &handlerNotifier{
			connectionStateFunc: func(_ ConnectionState) {
				updates <- struct{}{}
			},
			done: make(chan struct{}),
		}
		// Enqueue all updates upfront to ensure that it
		// doesn't block
		for i := 0; i < 10000; i++ {
			notifier.EnqueueConnectionState(ConnectionStateNew)
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
		notifier.Close(true)
	})
	t.Run("TestUpdateOrdering", func(t *testing.T) {
		defer test.CheckRoutines(t)()
		updates := make(chan ConnectionState)
		notifer := &handlerNotifier{
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
			notifer.EnqueueConnectionState(ConnectionState(i))
		}
		<-done
		notifer.Close(true)
	})
}
