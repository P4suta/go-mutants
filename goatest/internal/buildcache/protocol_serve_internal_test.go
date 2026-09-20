// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	statsOfOne = 1
	statsScale = 10
	statsSum   = statsOfOne + statsScale

	repliesLongerThanTheWriteBuffer = 8192
)

type writerThatGivesOut struct {
	after int
	err   error
}

func (writer *writerThatGivesOut) Write(data []byte) (int, error) {
	if writer.after <= 0 {
		return 0, writer.err
	}
	writer.after--
	return len(data), nil
}

func servedLine(t *testing.T, message request) string {
	t.Helper()
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded) + "\n"
}

func TestAddingStatsAddsEveryCounterToItsOwn(t *testing.T) {
	t.Parallel()
	one := Stats{
		Gets: statsOfOne, HitsScratch: statsOfOne, HitsBase: statsOfOne, HitsNative: statsOfOne,
		NativeBytes: statsOfOne, Misses: statsOfOne, Puts: statsOfOne, PutBytes: statsOfOne,
		PrunedBytes: statsOfOne,
	}
	other := Stats{
		Gets: statsScale, HitsScratch: statsScale, HitsBase: statsScale, HitsNative: statsScale,
		NativeBytes: statsScale, Misses: statsScale, Puts: statsScale, PutBytes: statsScale,
		PrunedBytes: statsScale,
	}
	want := Stats{
		Gets: statsSum, HitsScratch: statsSum, HitsBase: statsSum, HitsNative: statsSum,
		NativeBytes: statsSum, Misses: statsSum, Puts: statsSum, PutBytes: statsSum, PrunedBytes: statsSum,
	}
	sum := one
	sum.Add(other)
	if sum != want {
		t.Fatalf("adding %+v to %+v read %+v, want %+v", other, one, sum, want)
	}
	counters := reflect.TypeOf(Stats{}).NumField()
	named := strings.Count(Stats{}.Detail(), "=")
	if named != counters {
		t.Errorf("the detail names %d counters, want the %d this type holds", named, counters)
	}
}

func TestServingWithoutACounterStillServes(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	stream := strings.NewReader(servedLine(t, request{ID: 1, Command: commandClose}))
	if err := serveWithHooks(t.Context(), stream, &written, Layers{}, nil, serveHooks{}); err != nil {
		t.Fatalf("serving without a counter reported %v, want nothing", err)
	}
	if written.Len() == 0 {
		t.Error("serving without a counter wrote nothing")
	}
}

func TestServingStopsAtAReplyItCannotWrite(t *testing.T) {
	t.Parallel()
	failure := errors.New("the reply could not be written")
	for _, test := range []struct {
		name  string
		after int
	}{
		{name: "the commands it announces", after: 0},
		{name: "the answer to a request", after: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			stream := strings.NewReader(servedLine(t, request{ID: 1, Command: commandClose}))
			writer := &writerThatGivesOut{after: test.after, err: failure}
			err := serveWithHooks(t.Context(), stream, writer, Layers{}, &Stats{}, serveHooks{})
			if !errors.Is(err, failure) {
				t.Fatalf("serving reported %v when it could not write %s, want %v", err, test.name, failure)
			}
		})
	}
}

func TestServingStopsWhenItsContextIsDone(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	stream := strings.NewReader(servedLine(t, request{ID: 1, Command: commandClose}))
	err := serveWithHooks(ctx, stream, io.Discard, Layers{}, &Stats{}, serveHooks{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("serving reported %v for a context that was done, want %v", err, context.Canceled)
	}
}

func TestServingReportsAStreamThatEndedForAReasonThatIsNotTheEnd(t *testing.T) {
	t.Parallel()
	failure := errors.New("the stream would not answer")
	err := serveWithHooks(t.Context(), iotest(failure), io.Discard, Layers{}, &Stats{}, serveHooks{})
	if !errors.Is(err, failure) {
		t.Fatalf("serving reported %v, want %v", err, failure)
	}
	if !strings.Contains(err.Error(), "read cacheprog request") {
		t.Errorf("serving reported %v, want it to name the read that failed", err)
	}
}

func iotest(err error) io.Reader { return failingReader{err: err} }

func TestServingCountsAHitAgainstTheLayerItCameFrom(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		holder Source
		want   func(Stats) int64
	}{
		{name: "the scratch layer", holder: SourceScratch, want: func(s Stats) int64 { return s.HitsScratch }},
		{name: "the base layer", holder: SourceBase, want: func(s Stats) int64 { return s.HitsBase }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			layers := Layers{Scratch: preparedLayer(t), Base: preparedLayer(t)}
			holder := layers.layer(test.holder)
			if _, err := (Layers{Scratch: holder}).Put(key(1), key(2),
				strings.NewReader(routingBody), routingObjectSize, faultsMoment); err != nil {
				t.Fatal(err)
			}
			stats := &Stats{}
			answer := serveGet(layers, request{ID: 1, ActionID: key(1)}, stats, serveHooks{}.resolved())
			if answer.Miss || answer.Err != "" {
				t.Fatalf("a get from %s answered %+v, want the entry", test.name, answer)
			}
			if test.want(*stats) != 1 {
				t.Fatalf("a get from %s counted %+v, want one hit against it", test.name, stats)
			}
			if stats.Misses != 0 || stats.Gets != 1 {
				t.Errorf("a get from %s counted %+v, want one get and no miss", test.name, stats)
			}
		})
	}
}

func TestServingCountsAGetItCouldNotAnswerAsAMiss(t *testing.T) {
	t.Parallel()
	failure := errors.New("the object could not be inspected")
	layers := Layers{Scratch: preparedLayer(t)}
	if _, err := layers.Put(key(1), key(2), strings.NewReader(routingBody), routingObjectSize, faultsMoment); err != nil {
		t.Fatal(err)
	}
	stats := &Stats{}
	hooks := serveHooks{layer: failingObjectStat(failure)}.resolved()
	answer := serveGet(layers, request{ID: 1, ActionID: key(1)}, stats, hooks)
	if answer.Err == "" {
		t.Fatalf("a get that failed answered %+v, want the failure", answer)
	}
	if stats.Misses != 1 || stats.Gets != 1 {
		t.Errorf("a get that failed counted %+v, want one get and one miss", stats)
	}
}

func TestServingRefusesABodyThatDoesNotOpenWithAQuote(t *testing.T) {
	t.Parallel()
	stream := strings.NewReader(
		servedLine(t, request{ID: 1, Command: commandPut, ActionID: key(1), OutputID: key(2), BodySize: 1}) + "7\n")
	err := serveWithHooks(t.Context(), stream, io.Discard, Layers{Scratch: preparedLayer(t)}, &Stats{}, serveHooks{})
	if err == nil {
		t.Fatal("a body that is not a quoted string was accepted")
	}
	if !strings.Contains(err.Error(), "rather than a quoted string") {
		t.Errorf("serving reported %v, want it to say what the body opened with", err)
	}
}

func TestServingRefusesABodyThatStopsBeforeItsClosingQuote(t *testing.T) {
	t.Parallel()
	body := base64.StdEncoding.EncodeToString([]byte(routingBody))
	stream := strings.NewReader(
		servedLine(t, request{
			ID: 1, Command: commandPut, ActionID: key(1), OutputID: key(2), BodySize: routingObjectSize,
		}) + `"` + body)
	err := serveWithHooks(t.Context(), stream, io.Discard, Layers{Scratch: preparedLayer(t)}, &Stats{}, serveHooks{})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("serving reported %v for a body with no closing quote, want %v", err, io.ErrUnexpectedEOF)
	}
}

func TestAClosingCollectionCountsOnlyWhatItActuallyPruned(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		recent bool
		hooks  layerHooks
		pruned bool
	}{
		{name: "a collection that ran", pruned: true},
		{name: "a collection within its interval", recent: true},
		{name: "a collection that failed", hooks: layerHooks{
			readDir: func(string) ([]os.DirEntry, error) { return nil, errors.New("the layer could not be read") },
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			layer := preparedLayer(t)
			storeCollectedEntries(t, layer, collectedEntry{action: 0xaa, output: 1, aged: layer.MinIdle() + oneHour})
			if test.recent {
				marker := layer.collectionMarkerPath()
				writeStoredFile(t, marker, "")
				if err := os.Chtimes(marker, collectMoment, collectMoment); err != nil {
					t.Fatal(err)
				}
			}
			stats := &Stats{}
			hooks := serveHooks{now: func() time.Time { return collectMoment }, layer: test.hooks}.resolved()
			serveClose(Layers{Scratch: layer, MaxBytes: 1}, stats, hooks)
			if pruned := stats.PrunedBytes > 0; pruned != test.pruned {
				t.Fatalf("%s counted %d pruned bytes, want pruned=%t", test.name, stats.PrunedBytes, test.pruned)
			}
		})
	}
}

func TestAQuotedBodyAnswersEveryEndItCanReach(t *testing.T) {
	t.Parallel()
	body := func(text string) *quotedReader {
		return &quotedReader{reader: bufio.NewReader(strings.NewReader(text))}
	}
	if read, err := body(`abc"`).Read(nil); read != 0 || err != nil {
		t.Errorf("a read of nothing answered (%d, %v), want (0, nil)", read, err)
	}
	empty := body(`"`)
	destination := make([]byte, 1)
	if read, err := empty.Read(destination); read != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("an empty quoted body answered (%d, %v), want the end", read, err)
	}
	ended := body(`ab"`)
	if read, err := ended.Read(destination); read != 1 || err != nil {
		t.Fatalf("a quoted body answered (%d, %v), want one byte", read, err)
	}
	if _, err := io.ReadAll(ended); err != nil {
		t.Fatalf("reading the rest of a quoted body reported %v", err)
	}
	if read, err := ended.Read(destination); read != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("a quoted body read past its close answered (%d, %v), want the end", read, err)
	}
	if _, err := io.ReadAll(body("abc")); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("a quoted body with no close reported %v, want %v", err, io.ErrUnexpectedEOF)
	}
}

func TestDrainingAQuotedBodyReportsWhatItCouldNotRead(t *testing.T) {
	t.Parallel()
	body := &quotedReader{reader: bufio.NewReader(strings.NewReader("abc"))}
	err := body.drain()
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("draining a body with no close reported %v, want %v", err, io.ErrUnexpectedEOF)
	}
	if !strings.HasPrefix(err.Error(), "goatest: ") {
		t.Errorf("draining reported %v, want it named as goatest's", err)
	}
}

func TestACountingWriterAcceptsEveryByteAndCountsIt(t *testing.T) {
	t.Parallel()
	counter := &countingWriter{}
	written, err := counter.Write([]byte("abcd"))
	if written != len("abcd") || err != nil {
		t.Fatalf("a counting write answered (%d, %v), want every byte accepted", written, err)
	}
	if _, err := io.Copy(counter, strings.NewReader(routingBody)); err != nil {
		t.Fatalf("copying into a counting writer reported %v", err)
	}
	if counter.written != int64(len("abcd")+routingObjectSize) {
		t.Fatalf("the counter reads %d, want every byte it was given", counter.written)
	}
}

func TestWritingStatsWritesNothingWithoutBothAScratchAndAName(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		scratch string
		stats   string
	}{
		{name: "no scratch at all", stats: "stats.json"},
		{name: "no name at all", scratch: "scratch"},
		{name: "neither"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			hooks := layerHooks{mkdirAll: func(path string, _ os.FileMode) error {
				t.Errorf("writing statistics with %s made %s", test.name, path)
				return nil
			}}
			if err := writeStats(test.scratch, test.stats, Stats{}, hooks); err != nil {
				t.Fatalf("writing statistics with %s reported %v, want nothing", test.name, err)
			}
		})
	}
	scratch := t.TempDir()
	if err := writeStats(scratch, "stats.json", Stats{Gets: 1}, layerHooks{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(scratch, statsDirectory, "stats.json")); err != nil {
		t.Fatalf("writing statistics with both wrote nothing: %v", err)
	}
}

func TestSummarizingReadsOnlyTheStatisticsItWrote(t *testing.T) {
	t.Parallel()
	scratch := t.TempDir()
	directory := filepath.Join(scratch, statsDirectory)
	writeStoredFile(t, filepath.Join(directory, "one.json"), `{"gets":1}`)
	writeStoredFile(t, filepath.Join(directory, "two.json"), `{"gets":10}`)
	writeStoredFile(t, filepath.Join(directory, "notes.txt"), `{"gets":100}`)
	writeStoredFile(t, filepath.Join(directory, "broken.json"), `{`)
	if err := os.MkdirAll(filepath.Join(directory, "nested.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	total, err := summarizeWithHooks(scratch, layerHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if total.Gets != statsSum {
		t.Fatalf("the summary reads %d gets, want the %d it wrote as statistics", total.Gets, statsSum)
	}
	if summary, err := summarizeWithHooks("", layerHooks{}); err != nil || summary != (Stats{}) {
		t.Errorf("summarizing without a scratch answered (%+v, %v), want nothing", summary, err)
	}
	absent := filepath.Join(t.TempDir(), "absent")
	if summary, err := summarizeWithHooks(absent, layerHooks{}); err != nil || summary != (Stats{}) {
		t.Errorf("summarizing a layer that is not there answered (%+v, %v), want nothing", summary, err)
	}
}

func TestSummarizingReportsWhatItCouldNotRead(t *testing.T) {
	t.Parallel()
	failure := errors.New("the statistics could not be read")
	scratch := t.TempDir()
	writeStoredFile(t, filepath.Join(scratch, statsDirectory, "one.json"), `{"gets":1}`)
	for _, test := range []struct {
		name  string
		hooks layerHooks
	}{
		{
			name:  "a directory it cannot open",
			hooks: layerHooks{readDir: func(string) ([]os.DirEntry, error) { return nil, failure }},
		},
		{
			name:  "a file it cannot read",
			hooks: layerHooks{readFile: func(string) ([]byte, error) { return nil, failure }},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := summarizeWithHooks(scratch, test.hooks); !errors.Is(err, failure) {
				t.Fatalf("summarizing past %s reported %v, want %v", test.name, err, failure)
			}
		})
	}
	vanished := layerHooks{readFile: func(string) ([]byte, error) { return nil, os.ErrNotExist }}
	if summary, err := summarizeWithHooks(scratch, vanished); err != nil || summary != (Stats{}) {
		t.Errorf("summarizing past a file that vanished answered (%+v, %v), want nothing", summary, err)
	}
}

var _ fs.FileInfo = stubLayerInfo{}

type countingSource struct {
	reader io.Reader
	reads  int
}

func (source *countingSource) Read(destination []byte) (int, error) {
	source.reads++
	return source.reader.Read(destination)
}

func TestServingNamesTheReplyItCouldNotEncode(t *testing.T) {
	t.Parallel()
	failure := errors.New("the reply could not be written")
	layers := Layers{Scratch: preparedLayer(t)}
	if _, err := layers.Put(key(1), key(2), strings.NewReader(routingBody),
		routingObjectSize, faultsMoment); err != nil {
		t.Fatal(err)
	}
	longer := errors.New(strings.Repeat("s", repliesLongerThanTheWriteBuffer))
	hooks := serveHooks{layer: failingObjectStat(longer)}
	stream := strings.NewReader(servedLine(t, request{ID: 1, Command: commandGet, ActionID: key(1)}))
	err := serveWithHooks(t.Context(), stream, &writerThatGivesOut{after: 1, err: failure}, layers, &Stats{}, hooks)
	if !errors.Is(err, failure) {
		t.Fatalf("serving reported %v, want %v", err, failure)
	}
	if !strings.Contains(err.Error(), "write cacheprog response") {
		t.Errorf("serving reported %v, want it to name the response it could not write", err)
	}
}

func TestServingEndsCleanlyWhenTheGoCommandJustStops(t *testing.T) {
	t.Parallel()
	layers := Layers{Scratch: preparedLayer(t)}
	stream := strings.NewReader(servedLine(t, request{ID: 1, Command: commandGet, ActionID: key(1)}))
	if err := serveWithHooks(t.Context(), stream, io.Discard, layers, &Stats{}, serveHooks{}); err != nil {
		t.Fatalf("serving a stream that simply ended reported %v, want nothing", err)
	}
}

func TestServingRefusesABodyOfAnotherLengthEvenWhereTheObjectIsAlreadyStored(t *testing.T) {
	t.Parallel()
	layers := Layers{Scratch: preparedLayer(t)}
	if _, err := layers.Put(key(9), key(2), strings.NewReader(routingBody), routingObjectSize, faultsMoment); err != nil {
		t.Fatal(err)
	}
	shorter := base64.StdEncoding.EncodeToString([]byte("0"))
	stream := strings.NewReader(
		servedLine(t, request{
			ID: 1, Command: commandPut, ActionID: key(1), OutputID: key(2), BodySize: routingObjectSize,
		}) + `"` + shorter + "\"\n")
	var written bytes.Buffer
	if err := serveWithHooks(t.Context(), stream, &written, layers, &Stats{}, serveHooks{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(written.String(), "the go command declared") {
		t.Fatalf("serving answered %q, want it to refuse a body of another length", written.String())
	}
}

func TestAQuotedBodyReadsNothingForARequestOfNothing(t *testing.T) {
	t.Parallel()
	source := &countingSource{reader: strings.NewReader(`abcd"`)}
	body := &quotedReader{reader: bufio.NewReader(source)}
	if read, err := body.Read(nil); read != 0 || err != nil {
		t.Fatalf("a read of nothing answered (%d, %v), want (0, nil)", read, err)
	}
	if source.reads != 0 {
		t.Fatalf("a read of nothing asked the stream %d times, want it left alone", source.reads)
	}
}

func TestAQuotedBodyReportsAStreamFailureAsItself(t *testing.T) {
	t.Parallel()
	failure := errors.New("the stream would not answer")
	body := &quotedReader{reader: bufio.NewReader(failingReader{err: failure})}
	_, err := body.Read(make([]byte, 1))
	if !errors.Is(err, failure) {
		t.Fatalf("a quoted body reported %v, want %v rather than an end it did not reach", err, failure)
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("a quoted body reported %v as an unexpected end", err)
	}
}

func TestSummarizingWithoutAScratchAsksTheFilesystemNothing(t *testing.T) {
	t.Parallel()
	hooks := layerHooks{readDir: func(path string) ([]os.DirEntry, error) {
		t.Errorf("summarizing without a scratch read %s", path)
		return nil, os.ErrNotExist
	}}
	if summary, err := summarizeWithHooks("", hooks); err != nil || summary != (Stats{}) {
		t.Fatalf("summarizing without a scratch answered (%+v, %v), want nothing", summary, err)
	}
}

func TestSummarizingReadsNothingFromStatisticsThatAreNotSome(t *testing.T) {
	t.Parallel()
	scratch := t.TempDir()
	directory := filepath.Join(scratch, statsDirectory)
	writeStoredFile(t, directory+string(filepath.Separator)+"half.json", `{"gets":1,"puts":"none"}`)
	total, err := summarizeWithHooks(scratch, layerHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if total != (Stats{}) {
		t.Fatalf("the summary read %+v from a record it cannot decode, want nothing", total)
	}
}
