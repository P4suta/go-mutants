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

type signalWatch struct {
	mu     sync.Mutex
	signal os.Signal
}

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

func watchSignals(parent context.Context) (context.Context, *signalWatch, func()) {
	ctx, cancel := context.WithCancel(parent)
	watch := &signalWatch{}

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
