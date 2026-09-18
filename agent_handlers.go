// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import "sync"

// OnConnectionStateChange sets a handler that is fired when the connection state changes.
func (a *Agent) OnConnectionStateChange(f func(ConnectionState)) error {
	a.onConnectionStateChangeHdlr.Store(f)

	return nil
}

// OnSelectedCandidatePairChange sets a handler that is fired when the final candidate.
// pair is selected.
func (a *Agent) OnSelectedCandidatePairChange(f func(Candidate, Candidate)) error {
	a.onSelectedCandidatePairChangeHdlr.Store(f)

	return nil
}

// OnCandidate sets a handler that is fired when new candidates gathered. When
// the gathering process complete the last candidate is nil.
func (a *Agent) OnCandidate(f func(Candidate)) error {
	a.onCandidateHdlr.Store(f)

	return nil
}

func (a *Agent) onSelectedCandidatePairChange(p *CandidatePair) {
	if h, ok := a.onSelectedCandidatePairChangeHdlr.Load().(func(Candidate, Candidate)); ok && h != nil {
		h(p.Local, p.Remote)
	}
}

func (a *Agent) onCandidate(c Candidate) {
	if onCandidateHdlr, ok := a.onCandidateHdlr.Load().(func(Candidate)); ok && onCandidateHdlr != nil {
		onCandidateHdlr(c)
	}
}

func (a *Agent) onConnectionStateChange(s ConnectionState) {
	if hdlr, ok := a.onConnectionStateChangeHdlr.Load().(func(ConnectionState)); ok && hdlr != nil {
		hdlr(s)
	}
}

type handlerNotifier[T any] struct {
	sync.Mutex
	running   bool
	notifiers sync.WaitGroup
	queue     []T
	handler   func(T)
	done      chan struct{}
}

func (h *handlerNotifier[T]) Close(graceful bool) {
	if graceful {
		// if we were closed ungracefully before, we now
		// want to wait.
		defer h.notifiers.Wait()
	}

	h.Lock()

	select {
	case <-h.done:
		h.Unlock()

		return
	default:
	}
	close(h.done)
	h.Unlock()
}

func (h *handlerNotifier[T]) Enqueue(value T) {
	h.Lock()
	defer h.Unlock()

	select {
	case <-h.done:
		return
	default:
	}

	notify := func() {
		defer h.notifiers.Done()
		for {
			h.Lock()
			if len(h.queue) == 0 {
				h.running = false
				h.Unlock()

				return
			}
			notification := h.queue[0]
			h.queue = h.queue[1:]
			h.Unlock()
			h.handler(notification)
		}
	}

	h.queue = append(h.queue, value)
	if !h.running {
		h.running = true
		h.notifiers.Add(1)
		go notify()
	}
}
