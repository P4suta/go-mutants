// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// A signalWatch cancels a context on the first interrupt or termination signal
// and remembers which one arrived.
//
// signal.NotifyContext would be shorter and is not enough: it cancels without
// saying why, and the exit code contract distinguishes 130 from 143. Keeping
// the signal is the whole reason this type exists.
//
// Only the first signal is acted on. A second Ctrl-C does not escalate here,
// because the shutdown path it would interrupt is the one that removes the
// snapshot and publishes the partial report; the operating system's own
// escalation — a third Ctrl-C in a terminal, a SIGKILL — remains available and
// is the right tool for a process that really is stuck.
type signalWatch struct {
	mu     sync.Mutex
	signal os.Signal
}

// Signal returns the signal that ended the run, or nil if none did.
func (w *signalWatch) Signal() os.Signal {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.signal
}

func (w *signalWatch) record(s os.Signal) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.signal == nil {
		w.signal = s
	}
}

// watchSignals returns a context cancelled by the first interrupt or SIGTERM, a
// watch that names it, and a stop function.
//
// stop must be called — deferred at the call site — and it both unregisters the
// handler and joins the goroutine, so that a test running several invocations
// in one process does not accumulate live handlers.
//
// SIGTERM is registered on Windows too. It is never delivered there, and
// os/signal accepts it happily; a build tag to omit it would buy nothing but a
// second file to keep in step.
func watchSignals(parent context.Context) (context.Context, *signalWatch, func()) {
	ctx, cancel := context.WithCancel(parent)
	watch := &signalWatch{}

	// Buffered, as os/signal requires: an unbuffered channel would let a
	// signal arriving before the goroutine is scheduled be dropped.
	notify := make(chan os.Signal, 1)
	signal.Notify(notify, os.Interrupt, syscall.SIGTERM)

	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case s := <-notify:
			watch.record(s)
			cancel()
		case <-ctx.Done():
		}
	}()

	stop := func() {
		signal.Stop(notify)
		cancel()
		<-done
	}
	return ctx, watch, stop
}
