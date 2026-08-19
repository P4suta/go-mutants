// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"strconv"
	"sync"
)

// DefaultOutputLimit is how many bytes of combined output [Run] keeps when
// [Spec.OutputLimit] does not say. One mebibyte is far more than a readable
// test failure needs and far less than a runaway logger can produce, and the
// whole point of the cap is that a mutant which prints in a loop cannot take
// the run's memory with it.
const DefaultOutputLimit = 1 << 20

// MinOutputLimit is the smallest cap [Run] will honour. The truncation notice
// has to fit inside the budget for len(Output) <= OutputLimit to hold, so a
// limit smaller than this is raised to it rather than producing a result that
// is all notice and no output.
const MinOutputLimit = 256

// OutputTruncatedPrefix begins the first line of a [Result.Output] that lost
// bytes. Renderers match on this prefix to style the notice differently from
// the process's own output; it is a stable string because reports quote it.
const OutputTruncatedPrefix = "[go-mutants] output truncated"

// truncationNotice renders the line prepended to a capped capture.
//
// It reports only the total the child produced, which is a number the writer
// already knows before it decides how much to keep. Naming the kept count here
// instead would be circular — the notice's own length is part of the budget
// the kept count is computed from — and a fixed-point loop to resolve that
// would be a lot of cleverness for a diagnostic line. Whoever needs the kept
// count can subtract.
func truncationNotice(total int64) string {
	return OutputTruncatedPrefix + ": the process produced " +
		strconv.FormatInt(total, 10) + " bytes, only the tail is kept\n"
}

// tailWriter captures the last limit bytes written to it and counts the rest.
//
// It is the io.Writer handed to both Cmd.Stdout and Cmd.Stderr. os/exec
// notices that the two are the same value and gives the child a single pipe
// for both streams, so the interleaving in the capture is the child's own and
// not an artefact of two readers racing. The mutex is therefore uncontended in
// practice; it is here so that this type stays correct if a future caller
// wires the streams up separately.
type tailWriter struct {
	limit int

	mu    sync.Mutex
	buf   []byte
	total int64
}

func newTailWriter(limit int) *tailWriter {
	return &tailWriter{limit: limit}
}

// Write never fails and never blocks on anything but the mutex: a capture that
// could error would make the child's own writes fail, and a test binary that
// cannot print is a test binary that behaves differently under go-mutants than
// it does under `go test`.
//
// The buffer is allowed to grow to twice the limit before it is compacted, so
// the copy cost is amortised over at least limit bytes rather than paid on
// every write.
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

// capture returns the output as [Result.Output] carries it: the bytes as
// written when nothing was lost, otherwise the truncation notice followed by
// as much of the tail as the remaining budget allows.
//
// len(capture()) <= limit always holds, notice included. That is the invariant
// downstream report writers are entitled to assume, which is why the notice is
// paid for out of the budget rather than added on top of it.
func (w *tailWriter) capture() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.total <= int64(w.limit) {
		out := make([]byte, len(w.buf))
		copy(out, w.buf)
		return out
	}

	notice := truncationNotice(w.total)
	// Unreachable while [MinOutputLimit] holds — the notice is at most about a
	// hundred bytes even with a 19-digit total — but the invariant should not
	// depend on arithmetic done in a different file.
	keep := max(w.limit-len(notice), 0)
	tail := w.buf
	if len(tail) > keep {
		tail = tail[len(tail)-keep:]
	}
	out := make([]byte, 0, len(notice)+len(tail))
	out = append(out, notice...)
	out = append(out, tail...)
	return out
}
