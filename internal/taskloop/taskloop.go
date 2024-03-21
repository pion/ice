// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// Package taskloop implements a task loop to run
// tasks sequentially in a separate Goroutine.
package taskloop

import (
	"context"
	"errors"
	"fmt"
	"time"

	atomicx "github.com/pion/ice/v3/internal/atomic"
)

// ErrClosed indicates that the loop has been stopped
var ErrClosed = errors.New("task loop has been stopped")

type task struct {
	fn   func(context.Context)
	done chan struct{}
}

// Loop runs submitted task serially in a dedicated Goroutine
type Loop struct {
	tasks        chan task
	onClose      func()
	done         chan struct{}
	taskLoopDone chan struct{}
	err          atomicx.Error
}

// NewLoop creates and starts a new task loop
func NewLoop(onClose func()) *Loop {
	l := &Loop{
		tasks:        make(chan task),
		done:         make(chan struct{}),
		taskLoopDone: make(chan struct{}),
		onClose:      onClose,
	}

	go l.runLoop()

	return l
}

// Close stops the loop after finishing the execution of the current task.
// Other pending tasks will not be executed.
func (l *Loop) Close() error {
	l.err.Store(ErrClosed)

	close(l.done)
	<-l.taskLoopDone

	return nil
}

// RunContext serially executes the submitted callback.
// Blocking tasks must be cancelable by context.
func (l *Loop) RunContext(ctx context.Context, cb func(context.Context)) error {
	if err := l.Ok(); err != nil {
		return err
	}

	done := make(chan struct{})
	select {
	case <-ctx.Done():
		fmt.Println("cancelled")
		return ctx.Err()
	case l.tasks <- task{cb, done}:
		<-done
		return nil
	}
}

// Run serially executes the submitted callback.
func (l *Loop) Run(cb func(context.Context)) error {
	return l.RunContext(l, cb)
}

// Ok waits for the next task to complete and checks then if the loop is still running.
func (l *Loop) Ok() error {
	select {
	case <-l.done:
		return l.Err()
	default:
	}

	return nil
}

// runLoop handles registered tasks and agent close.
func (l *Loop) runLoop() {
	for {
		select {
		case <-l.done:
			close(l.taskLoopDone)
			l.onClose()
			return
		case t := <-l.tasks:
			t.fn(l)
			close(t.done)
		}
	}
}

// The following methods implement context.Context for TaskLoop

// Deadline returns the no valid time as task loops have no deadline.
func (l *Loop) Deadline() (deadline time.Time, ok bool) {
	return time.Time{}, false
}

// Done returns a channel that's closed when the task loop has been stopped.
func (l *Loop) Done() <-chan struct{} {
	return l.done
}

// Err returns nil if the task loop is still running.
// Otherwise it return ErrClosed if the loop has been closed/stopped.
func (l *Loop) Err() error {
	if err := l.err.Load(); err != nil {
		return err
	}

	return ErrClosed
}

// Value is not supported for task loops
func (l *Loop) Value(_ interface{}) interface{} {
	return nil
}
