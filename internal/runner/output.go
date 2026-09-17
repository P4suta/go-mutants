// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"strconv"
	"sync"
)

const DefaultOutputLimit = 1 << 20

const MinOutputLimit = 256

const OutputTruncatedPrefix = "[go-mutants] output truncated"

func truncationNotice(total int64) string {
	return OutputTruncatedPrefix + ": the process produced " +
		strconv.FormatInt(total, 10) + " bytes, only the tail is kept\n"
}

type tailWriter struct {
	limit int

	mu    sync.Mutex
	buf   []byte
	total int64
}

func newTailWriter(limit int) *tailWriter {
	return &tailWriter{limit: limit}
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.total += int64(len(p))
	if len(p) >= w.limit {
		w.buf = append(w.buf[:0], p[len(p)-w.limit:]...)
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	if len(w.buf) > 2*w.limit {
		n := copy(w.buf, w.buf[len(w.buf)-w.limit:])
		w.buf = w.buf[:n]
	}
	return len(p), nil
}

func (w *tailWriter) capture() (kept []byte, total int64, truncated bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.total <= int64(w.limit) {
		out := make([]byte, len(w.buf))
		copy(out, w.buf)
		return out, w.total, false
	}

	notice := truncationNotice(w.total)
	room := max(w.limit-len(notice), 0)
	tail := w.buf
	if len(tail) > room {
		tail = tail[len(tail)-room:]
	}
	out := make([]byte, 0, len(notice)+len(tail))
	out = append(out, notice...)
	out = append(out, tail...)
	return out, w.total, true
}
