package allocbench

import (
	"testing"
)

var (
	sinkInt            int
	sinkNext           func() int
	sinkStop           func()
	sinkPointerRecords []*record
	sinkValueRecords   []record
)

type record struct {
	id    int64
	count int64
	total int64
	flags int64
}

func buildPointerRecords(n int) []*record {
	out := make([]*record, 0, n)
	for i := range n {
		out = append(out, &record{
			id:    int64(i),
			count: int64(i % 17),
			total: int64(i * 3),
			flags: int64(i & 7),
		})
	}
	return out
}

func buildValueRecords(n int) []record {
	out := make([]record, 0, n)
	for i := range n {
		out = append(out, record{
			id:    int64(i),
			count: int64(i % 17),
			total: int64(i * 3),
			flags: int64(i & 7),
		})
	}
	return out
}

func BenchmarkPointerRecords(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		sinkPointerRecords = buildPointerRecords(4096)
	}
}

func BenchmarkValueRecords(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		sinkValueRecords = buildValueRecords(4096)
	}
}

func makeSeparateClosures() (func() int, func()) {
	var v int
	var ok bool
	var done bool
	var yieldNext bool
	var seqDone bool
	var racer int
	var panicValue any

	next := func() int {
		racer++
		if done {
			return 0
		}
		yieldNext = !yieldNext
		seqDone = true
		ok = !ok
		if panicValue != nil {
			return racer
		}
		v++
		if ok && seqDone {
			return v
		}
		return -v
	}
	stop := func() {
		done = true
		panicValue = nil
	}
	return next, stop
}

type pullLikeState struct {
	v          int
	ok         bool
	done       bool
	yieldNext  bool
	seqDone    bool
	racer      int
	panicValue any
}

func makeGroupedClosures() (func() int, func()) {
	var state pullLikeState

	next := func() int {
		state.racer++
		if state.done {
			return 0
		}
		state.yieldNext = !state.yieldNext
		state.seqDone = true
		state.ok = !state.ok
		if state.panicValue != nil {
			return state.racer
		}
		state.v++
		if state.ok && state.seqDone {
			return state.v
		}
		return -state.v
	}
	stop := func() {
		state.done = true
		state.panicValue = nil
	}
	return next, stop
}

func BenchmarkSeparateClosures(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		next, stop := makeSeparateClosures()
		sinkInt += next()
		stop()
		sinkNext = next
		sinkStop = stop
	}
}

func BenchmarkGroupedClosures(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		next, stop := makeGroupedClosures()
		sinkInt += next()
		stop()
		sinkNext = next
		sinkStop = stop
	}
}
