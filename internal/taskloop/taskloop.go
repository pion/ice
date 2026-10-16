// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// Package taskloop implements a task loop to run
// tasks sequentially in a separate Goroutine.
package taskloop

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// ErrClosed indicates that the loop has been stopped.
var ErrClosed = errors.New("the agent is closed")

type task struct {
	fn   func(context.Context)
	done chan struct{}
}

// Loop runs submitted task serially in a dedicated Goroutine.
type Loop struct {
	tasks chan task

	// State for closing
	done         chan struct{}
	taskLoopDone chan struct{}
	closeOnce    sync.Once

	// closed mirrors done and backs Closed(). It is set after done is closed,
	// so Closed() always implies Err() != nil.
	closed atomic.Bool
}

// New creates and starts a new task loop.
func New(onClose func()) *Loop {
	l := &Loop{
		tasks:        make(chan task),
		done:         make(chan struct{}),
		taskLoopDone: make(chan struct{}),
	}

	go l.runLoop(onClose)

	return l
}

// runLoop handles registered tasks and agent close.
func (l *Loop) runLoop(onClose func()) {
	defer func() {
		onClose()
		close(l.taskLoopDone)
	}()

	for {
		select {
		case <-l.done:
			return
		case t := <-l.tasks:
			t.fn(l)
			close(t.done)
		}
	}
}

// Close stops the loop after finishing the execution of the current task.
// Other pending tasks will not be executed.
func (l *Loop) Close() {
	l.CloseWithPreStop(nil)
}

// CloseWithPreStop stops the loop after finishing the execution of the current task.
// It calls preStop after the loop is marked closed and before waiting for the
// current task to return.
func (l *Loop) CloseWithPreStop(preStop func()) {
	l.closeOnce.Do(func() {
		close(l.done)
		l.closed.Store(true)

		if preStop != nil {
			preStop()
		}
	})
	<-l.taskLoopDone
}

// Closed reports whether the loop has been closed/stopped.
// It is an atomic load for hot paths, where Err costs a runtime call.
func (l *Loop) Closed() bool {
	return l.closed.Load()
}

// Run serially executes the submitted callback.
// Blocking tasks must be cancelable by context.
func (l *Loop) Run(ctx context.Context, t func(context.Context)) error {
	if l.Closed() {
		return ErrClosed
	}
	done := make(chan struct{})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.done:
		return ErrClosed
	case l.tasks <- task{t, done}:
		<-done

		return nil
	}
}

// The following methods implement context.Context for TaskLoop

// Done returns a channel that's closed when the task loop has been stopped.
func (l *Loop) Done() <-chan struct{} {
	return l.done
}

// Err returns nil if the task loop is still running.
// Otherwise it return errClosed if the loop has been closed/stopped.
func (l *Loop) Err() error {
	select {
	case <-l.done:
		return ErrClosed
	default:
		return nil
	}
}

// Deadline returns the no valid time as task loops have no deadline.
func (l *Loop) Deadline() (deadline time.Time, ok bool) {
	return time.Time{}, false
}

// Value is not supported for task loops.
func (l *Loop) Value(any) any {
	return nil
}
