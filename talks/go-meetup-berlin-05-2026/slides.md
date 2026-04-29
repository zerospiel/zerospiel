---
marp: true
title: About Go Memory Allocator
paginate: true
theme: uncover
class:
  - lead
  - invert
---

<style>
section {
  font-size: 28px;
  padding: 54px 72px;
}
section.lead h1 {
  font-size: 2.25em;
}
section.compact {
  font-size: 23px;
}
section.compact pre {
  font-size: 0.66em;
}
section.code-compact {
  font-size: 24px;
}
section.code-compact pre {
  font-size: 0.68em;
}
section.references {
  font-size: 23px;
}
.title-meta {
  font-size: 0.58em;
  line-height: 1.35;
  opacity: 0.82;
  margin-top: 1.3rem;
}
.title-meta p {
  margin: 0.25rem 0;
}
.title-meta .based {
  font-size: 0.82em;
  opacity: 0.7;
  margin-top: 1rem;
}
.contacts {
  display: inline-grid;
  gap: 0.7rem;
  text-align: left;
  margin-top: 1.4rem;
}
.contacts p {
  align-items: center;
  display: flex;
  gap: 0.65rem;
  margin: 0;
}
.contacts img {
  height: 1.15em;
  width: 1.15em;
}
</style>

# About Go Memory Allocator

<div class="title-meta">
  <p>GDG Golang Meetup - 13.05.2026</p>
  <p>Michael Morgen - SWE@Mirantis</p>
  <p class="based">Based on Go 1.26.2</p>
</div>

<!--
Hello everyone. Today I want to explain how the Go memory allocator works.

The goal is not to memorize runtime structs. The goal is to build a useful mental model. After this talk, I want you to understand why most allocations are fast, when the runtime needs to do more work, and how this knowledge can help us write faster Go code.

The slides are based on Go 1.26.2 source code.
-->

---

## Roadmap

- Why Go has its own allocator
- How memory is divided: arenas, pages, spans, slots
- The hot path: `mcache -> mcentral -> mheap`
- How GC and scavenging reuse memory
- Two practical optimization examples

<!--
We will start with the reason Go has its own allocator instead of asking the operating system every time.

Then we will look at the memory layout: arenas, pages, spans, and slots.

After that we will follow the normal allocation path. The important chain is `mcache`, then `mcentral`, then `mheap`, and finally the operating system.

Then we will connect this to garbage collection and memory scavenging.

At the end, we will look at two examples where reducing allocator work gives real performance wins.
-->

---

## Analogy: A Workshop

A small workshop builds many similar things.

The worker does not visit the hardware store for every screw.

Common parts are kept close:

- tiny bins on the workbench
- labeled drawers by screw size
- larger boxes in the storeroom
- bulk orders from the hardware store

<!--
Let us start with an analogy.

Imagine a small workshop. The worker builds many similar things and constantly needs small parts: screws, washers, bolts, and clips.

The worker could go to the hardware store for every screw, but that would be too slow.

Instead, common parts are kept close. A few are in bins on the workbench. More are in labeled drawers. Bigger boxes are in the storeroom. Only sometimes does the workshop place a bulk order.

Go does the same thing with memory. It does not ask the OS for every small allocation. It asks for larger chunks and then manages small requests internally.
-->

---

## Analogy Diagram

```text
need one screw
    |
    v
workbench bin        fast, local
    |
    v
labeled drawer       shared by size
    |
    v
storeroom            big storage, more coordination
    |
    v
hardware store       slow, bulk delivery
```

The allocator has the same shape.

<!--
This diagram is the main shape of the allocator.

The request for one screw is like a request from our Go program: "I need N bytes."

The workbench bin is the fastest place. If the right part is already there, the request is almost immediate.

If not, the worker opens the labeled drawer. If the drawer is empty, they go to the storeroom. If the storeroom cannot satisfy the request, the workshop orders from the hardware store.

In Go, most allocations should be served near the top of this diagram.
-->

---

## Runtime Map

| Workshop | Go runtime |
| --- | --- |
| screw size | size class |
| box of equal screws | span |
| workbench bin | `mcache`, per `P` |
| labeled drawer | `mcentral`, per span class |
| storeroom | `mheap`, page allocator |
| hardware store | OS memory APIs |
| sorting returned parts | GC sweep and scavenger |

<!--
Now we map the analogy to the runtime.

A screw size is a size class. Go does not have a unique bucket for every possible allocation size. It rounds sizes to predefined classes.

A box of equal screws is a span. A span contains objects of one size class.

The workbench bin is `mcache`. It is local to a scheduler `P`, so it avoids locks on the hot path.

The labeled drawer is `mcentral`. It manages spans for one span class.

The storeroom is `mheap`. It manages pages and gets more memory from the OS.

Sorting returned parts is not one exact component, but it is close to the garbage collector sweep phase and the scavenger.
-->

---

## When Allocation Happens

Not every variable uses the heap allocator.

```text
Go source
   |
   v
compiler escape analysis
   |------------------|
   v                  v
goroutine stack       heap -> runtime.mallocgc
```

The allocator manages heap objects and the memory used for goroutine stacks.

Source: [runtime/malloc.go](https://github.com/golang/go/blob/go1.26.2/src/runtime/malloc.go#L1119-L1209)

<!--
Not every Go variable goes through the heap allocator.

The compiler runs escape analysis. If a value can safely live inside a function call frame, it can stay on the goroutine stack. This is usually very cheap.

If a value must live longer, or its address escapes, or we allocate backing storage for some dynamic data structures, then it may go to the heap.

The runtime function we keep coming back to is `mallocgc`. It is the main heap allocation entry point.

One extra detail: goroutine stacks themselves are allocated from runtime-managed memory. But local variables inside a stack frame are not allocated one by one by `mallocgc`.
-->

---

## From OS Memory to Go Pages

Go asks the OS for large address ranges, then manages them itself.

```text
heap arena, usually 64 MiB on 64-bit non-Windows

+--------+--------+--------+--------+  ...
| 8 KiB  | 8 KiB  | 8 KiB  | 8 KiB  |
| page   | page   | page   | page   |
+--------+--------+--------+--------+
```

Arena sizes vary by platform: 64 MiB, 4 MiB, or 512 KiB.

Sources: [arena constants](https://github.com/golang/go/blob/go1.26.2/src/runtime/malloc.go#L238-L264), [memory states](https://github.com/golang/go/blob/go1.26.2/src/runtime/mem.go#L9-L31)

<!--
At the bottom, the OS owns the real virtual memory APIs: `mmap` on Unix-like systems, `VirtualAlloc` on Windows.

Go asks for large address ranges. On most 64-bit non-Windows platforms, a heap arena is 64 MiB. On Windows and 32-bit platforms it is smaller, and on Wasm it is smaller again.

Inside that arena, Go uses its own page size: 8 KiB. This is a runtime page, not necessarily the same as the OS page.

The important idea is that Go reserves and prepares memory in larger pieces, then serves small object requests from this managed space.
-->

---

## Spans and Size Classes

A span is one or more contiguous runtime pages.

Each span stores objects of one size class.

```text
span for 1024-byte objects

+------+------+------+------+------+------+------+------+
| slot | slot | slot | slot | slot | slot | slot | slot |
+------+------+------+------+------+------+------+------+
```

Go 1.26 has 68 size classes: class 0 for large spans, classes 1..67 up to 32 KiB.

Source: [size class table](https://github.com/golang/go/blob/go1.26.2/src/internal/runtime/gc/sizeclasses.go#L6-L99)

<!--
A span is one or more contiguous runtime pages.

Every span is dedicated to one object size. For example, a span for 1024-byte objects can be split into eight slots if the span is one 8 KiB page.

If your object is 300 bytes, Go does not make a special 300-byte span. It rounds the request up to the next size class.

This has some internal waste, but it makes allocation much faster. The allocator does not search arbitrary holes. It looks for a free slot in the right kind of span.
-->

---

## Scan or Noscan

Each size class has two span classes:

- scan: objects may contain pointers
- noscan: objects contain no pointers

```text
noscan span: GC can skip object contents
scan span:   GC must find and follow pointers
```

68 size classes * 2 = 136 span classes.

Source: [spanClass](https://github.com/golang/go/blob/go1.26.2/src/runtime/mheap.go#L576-L590)

<!--
Size is not the only important property. The garbage collector also cares whether objects contain pointers.

If an object has no pointers, the GC does not need to scan inside it. That is a big win.

So Go has two span classes for each size class. One is scan, one is noscan.

This is why the talk uses "span class" a lot. A span class means size plus pointer information.

For small pointer-containing objects, the runtime also stores pointer layout information using heap bits or a malloc header. Heap bits are metadata that tell the GC which words inside an object are pointers. For small scan objects up to 512 bytes on 64-bit systems, these bits can live at the end of the span. For larger small scan objects, Go prepends a malloc header that points to type metadata.
-->

---

<!-- _class: compact invert -->

## Allocation Paths

```text
mallocgc(size, type)
    |
    +-- size == 0 -----------------> zerobase
    |
    +-- large ---------------------> mheap span
    |
    +-- small
           |
           +-- no pointers?
           |      +-- size < 16 B -> tiny allocator
           |      +-- otherwise --> noscan span
           |
           +-- has pointers?
           |      +-- size <= 512 B -> scan span, heap bits
           |      +-- otherwise -----> scan span, malloc header
```

Small means about 32 KiB or less.

Source: [mallocgc decision](https://github.com/golang/go/blob/go1.26.2/src/runtime/malloc.go#L1126-L1209)

<!--
Now we can look at the main paths.

Zero-sized allocations return a shared address called `zerobase`.

Large objects take the large allocation path by size. They skip `mcache` and `mcentral`, and go directly to `mheap`, because they need their own page run. The pointer layout still matters later for the garbage collector, but it is not a branch in this allocation-path diagram.

If the type has no pointers, and the size is smaller than 16 bytes, the object may use the tiny allocator.

If the type has no pointers but is not tiny, it uses a noscan span class. That means the garbage collector can skip the object contents.

If the type has pointers, the allocator must also make sure the GC can find those pointers. On 64-bit systems, objects up to 512 bytes can use heap bits stored in the span. Larger small scan objects use a malloc header, which stores type information before the user object.

The exact cutoff is slightly below 32 KiB in the default `mallocgc` path because of the malloc header. For a talk, "about 32 KiB" is the useful memory hook.
-->

---

## Tiny Allocator

For pointer-free objects smaller than 16 bytes.

```text
one 16-byte block

+---+---+------+--------+
| 1 | 2 | 4    | free   |
+---+---+------+--------+
```

Several tiny objects share one 16-byte slot.

Tradeoff: the whole block stays alive while any subobject is alive.

Source: [mallocgcTiny](https://github.com/golang/go/blob/go1.26.2/src/runtime/malloc.go#L1254-L1329)

<!--
The tiny allocator is for pointer-free objects smaller than 16 bytes.

Without it, a one-byte object could still consume an eight-byte or sixteen-byte slot. That is a lot of waste if this happens many times.

So Go packs several tiny objects into one 16-byte block.

This is fast and saves memory, but it has a tradeoff. If one tiny subobject is still reachable, the whole 16-byte block stays alive.

This is still usually a good tradeoff for small strings and small escaping pointer-free values.
-->

---

## Fast Path: `mcache`

Each scheduler `P` owns an `mcache`.

```text
P.mcache[span class]
        |
        v
      mspan
        |
        v
 next free slot
```

No lock on the normal small-object path.

Source: [mcache](https://github.com/golang/go/blob/go1.26.2/src/runtime/mcache.go#L14-L49), [nextFreeFast](https://github.com/golang/go/blob/go1.26.2/src/runtime/malloc.go#L1019-L1037)

<!--
This is the hot path.

Each `P` has an `mcache`. A goroutine running on that `P` can allocate from the `mcache` without taking a lock.

The `mcache` has an array of current spans, indexed by span class. If the current span has a free slot, allocation is mostly bit operations and pointer arithmetic.

The runtime keeps a cached view of free bits, so finding the next free object can be very cheap.

This is why many heap allocations in Go are surprisingly fast.
-->

---

<!-- _class: compact invert -->

## Middle Layer: `mcentral`

`mheap` owns one `mcentral` per span class.

```text
mheap.central[136]
        |
        +-- mcentral for 16 B noscan
        +-- mcentral for 16 B scan
        +-- ...

one mcentral:
  partial swept    partial unswept
  full swept       full unswept
```

Source: [mcentral](https://github.com/golang/go/blob/go1.26.2/src/runtime/mcentral.go#L21-L45)

<!--
This is the layer that is easy to skip, but it is important.

`mcentral` sits between local `mcache` spans and global page allocation in `mheap`.

The `mheap` has exactly one `mcentral` for every span class. Go has 68 size classes, and each size class has scan and noscan variants, so that gives 136 `mcentral` instances.

Each `mcentral` owns spans of one span class. It does not store individual free objects itself. The free slots live inside the spans.

The central layer has four span sets: partial swept, partial unswept, full swept, and full unswept. Partial means the span still has at least one free object. Full means the span has no free slots. Swept and unswept describe whether the span has already been processed for the current GC cycle.

When `mcache` needs a fresh span, `mcentral` first tries a swept partial span. If needed, it can also sweep an unswept span on the allocation path.
-->

---

<!-- _class: compact invert -->

## Refill Path

When the local span is full:

```text
mcache
  |
  | needs a new span
  v
mcentral for this span class
  |
  | no partial span found
  v
mheap
  |
  | no pages available
  v
OS
```

Most allocations stop at the top.

Sources: [mcache.refill](https://github.com/golang/go/blob/go1.26.2/src/runtime/mcache.go#L155-L239), [mcentral.cacheSpan](https://github.com/golang/go/blob/go1.26.2/src/runtime/mcentral.go#L81-L198)

<!--
When the current span in `mcache` is full, Go refills it.

The old full span goes back to `mcentral`. Then `mcache` asks the `mcentral` for that span class for a span with free slots.

`mcentral` is shared, but it is split by span class. Allocations of different sizes do not all fight over one global list.

If `mcentral` cannot find a useful span, it asks `mheap` for fresh pages. `mheap` creates a new span from those pages.

This is more expensive than the local `mcache` path, but it happens much less often.
-->

---

<!-- _class: compact invert -->

## `mheap`: Page Allocator

`mheap` manages runtime pages.

```text
page bitmap:
1 = page belongs to a span
0 = page is free

radix summaries:
start free | max free run | end free
```

Go also has a per-`P` page cache: 64 pages, or 512 KiB.

Sources: [page allocator](https://github.com/golang/go/blob/go1.26.2/src/runtime/mpagealloc.go#L5-L34), [page cache](https://github.com/golang/go/blob/go1.26.2/src/runtime/mpagecache.go#L12-L21)

<!--
`mheap` manages pages, not individual small objects.

The modern page allocator uses a bitmap over the heap address space. A bit tells whether a page belongs to a span.

To avoid scanning huge bitmaps all the time, the runtime keeps radix-tree summaries. Each summary says how many free pages are at the start, how many at the end, and the largest free run inside that region. The allocator can use those summaries to skip regions that cannot satisfy the request.

There is also a per-`P` page cache. It represents 64 runtime pages with a 64-bit bitmap: one bit per page. In that page-cache bitmap, 1 means free. This lets small page-run allocations avoid the global heap lock in the lucky case.
-->

---

<!-- _class: compact invert -->

## `mheap` Span Allocation

```text
mheap.alloc(npages)
  |
  +-- sweep/reclaim if needed
  v
allocSpan
  |
  +-- small run? try per-P page cache
  |
  +-- lock heap
        pages.alloc/find(searchAddr)
        if not found: grow heap
        arena hint -> sysReserve/mmap
      unlock
  |
  v
initialize span metadata
```

Source: [mheap.allocSpan](https://github.com/golang/go/blob/go1.26.2/src/runtime/mheap.go#L1207-L1412)

<!--
This is a simplified view of what happens when `mheap` needs to allocate pages for a span.

First, `mheap.alloc` runs on the system stack. If sweeping is not done, it may reclaim pages before allocating more.

Then `allocSpan` decides whether the request is small enough to try the per-`P` page cache. In Go 1.26, the cache has 64 pages, and `allocSpan` uses it for small page runs when the request is less than a quarter of that cache. So this is for requests below 16 runtime pages.

If the page cache cannot satisfy the request, the allocator takes the heap lock.

Under the lock, it asks the page allocator for a contiguous run. The page allocator first uses `searchAddr`, then may walk the radix summaries to find a large enough free run.

If no existing page run can satisfy the request, `mheap` grows the heap. It tries arena hints first, and then asks the OS for reserved address space through the runtime OS memory layer. On Unix-like systems this eventually uses `mmap`.

After it has pages and an `mspan`, it releases the lock, may do scavenging work, initializes span metadata, and accounts for pages that have to become ready again.
-->

---

## GC Handshake

The allocator and GC share span metadata.

```text
allocation bitmap:  allocBits
GC live bitmap:     gcmarkBits

after marking:
  allocBits = gcmarkBits
  gcmarkBits = empty bitmap for next cycle
```

Sweeping turns dead objects back into free slots.

Sources: [mspan bits](https://github.com/golang/go/blob/go1.26.2/src/runtime/mheap.go#L468-L491), [sweep swap](https://github.com/golang/go/blob/go1.26.2/src/runtime/mgcsweep.go#L678-L698)

<!--
The allocator and garbage collector are tightly connected.

Each span has allocation bits and mark bits.

During GC, live objects are marked in `gcmarkBits`.

During sweeping, the runtime uses those marks to decide which slots are still allocated. Dead slots become available for future allocations.

Then the mark bits become the new allocation bits, and a fresh empty mark bitmap is prepared for the next GC cycle.

This is why freeing in Go usually does not mean returning memory to the OS. It often just means making slots reusable.
-->

---

## Returning Memory to the OS

Freed objects usually do not go straight back to the OS.

```text
dead object -> free slot in span
empty span  -> free pages in mheap
idle pages  -> scavenger may release physical memory
```

The address space usually remains reserved so Go can reuse it later.

Sources: [scavenger overview](https://github.com/golang/go/blob/go1.26.2/src/runtime/mgcscavenge.go#L5-L20), [sysUnused](https://github.com/golang/go/blob/go1.26.2/src/runtime/mem.go#L63-L71)

<!--
If an object dies, its slot can be reused.

If a whole span becomes empty, its pages can go back to `mheap`.

Only later, if pages stay unused or if the memory limit requires it, the scavenger may release physical memory back to the OS.

The address range usually stays reserved by the Go runtime. That makes future reuse cheaper because Go may not need a brand new mapping.

So when you look at RSS, remember: garbage collection and returning memory to the OS are related, but they are not the same operation.
-->

---

## Goroutine Stacks

Goroutine stacks are also backed by runtime-managed memory.

- new goroutines start with a small stack, usually 2 KiB
- small stacks use per-`P` stack cache and global pools
- when a stack grows, Go allocates a larger one and copies
- stacks may shrink during GC

Sources: [stackMin](https://github.com/golang/go/blob/go1.26.2/src/runtime/stack.go#L70-L89), [stackalloc](https://github.com/golang/go/blob/go1.26.2/src/runtime/stack.go#L338-L397), [newstack](https://github.com/golang/go/blob/go1.26.2/src/runtime/stack.go#L1014-L1026)

<!--
Goroutine stacks are another important part of runtime memory.

A new goroutine starts with a small stack, usually 2 KiB. That stack can grow when a function needs more stack space.

Go no longer uses old segmented stacks. It allocates a larger contiguous stack and copies the old stack to the new one.

Small stack allocations use a per-`P` stack cache and global pools. Large stacks use larger spans.

Stacks can also shrink during garbage collection if they are mostly unused.
-->

---

<!-- _class: code-compact invert -->

## Practical Win 1: Many Heap Objects

Before:

```go
func build(n int) []*record {
 out := make([]*record, 0, n)
 for i := range n {
  out = append(out, &record{id: int64(i)})
 }
 return out
}
```

4096 records = 1 slice backing array + 4096 record objects

<!--
The first practical lesson is: avoid turning one logical collection into thousands of tiny heap objects when a contiguous value slice works.

In the before version, the slice stores pointers. The slice backing array is one allocation, and every `&record{...}` is another allocation. For 4096 records, that is 4097 allocations.

This means many trips through `mallocgc`, many object headers or span bits to manage, and more work later for the garbage collector.
-->

---

<!-- _class: code-compact invert -->

## Practical Win 1: One Backing Array

After:

```go
func build(n int) []record {
 out := make([]record, 0, n)
 for i := range n {
  out = append(out, record{id: int64(i)})
 }
 return out
}
```

Local benchmark:

`64 us -> 12.8 us`, `4097 allocs/op -> 1`

Benchmark source: [examples/allocator_examples_test.go](examples/allocator_examples_test.go)

<!--
In the after version, the slice stores records directly. The records are in one contiguous backing array, so the allocator has much less work.

In my local benchmark, this changed about 4097 allocations into 1 allocation, and runtime dropped from about 64 microseconds to about 12.8 microseconds. Memory also dropped from 160 KiB per operation to about 128 KiB per operation.

The allocator lesson is simple: fewer heap objects means fewer `mallocgc` calls and less metadata for the runtime to manage.
-->

---

<!-- _class: code-compact invert -->

## Practical Win 2: Group Escaping State

Before:

```go
var v V
var ok, done, yieldNext, seqDone bool
var racer int
var panicValue any
```

Several captured variables may become several heap objects.

<!--
The second example is from the Go standard library.

Closures capture variables. If several captured variables must live on the heap, the compiler may create several heap objects.

In `iter.Pull`, the implementation had multiple pieces of shared state captured by closures.
-->

---

<!-- _class: code-compact invert -->

## Practical Win 2: Group Escaping State

After:

```go
var pull struct {
 v V
 ok, done, yieldNext, seqDone bool
 racer int
 panicValue any
}
```

In `iter.Pull`, this reduced allocations by grouping captured closure state.

Source: [Go commit ba7b8ca](https://github.com/golang/go/commit/ba7b8ca336123017e43a2ab3310fd4a82122ef9d)

<!--
The optimization grouped that state into one struct. This reduced the number of heap objects, reduced metadata, and packed some fields more efficiently.

This is not a rule to always group everything. It is useful when the values have the same lifetime and the allocation is on a hot path.
-->

---

<!-- _class: compact invert -->

## Real Benchmark From `iter.Pull`

Official commit benchmark:

```text
Pull-12
  218.6 ns/op -> 146.1 ns/op   -33%
  288 B/op    -> 176 B/op      -39%
  11 allocs   -> 5 allocs      -55%

Pull2-12
  239.8 ns/op -> 155.0 ns/op   -35%
  312 B/op    -> 176 B/op      -44%
  12 allocs   -> 5 allocs      -58%
```

The allocator lesson: fewer escaping heap objects means fewer `mallocgc` calls and less GC metadata.

<!--
These are the benchmark numbers from the Go commit.

For `Pull`, runtime went down by about 33 percent. Bytes per operation went down by about 39 percent. Allocations went from 11 to 5.

For `Pull2`, the result was similar.

The reason is not magic. It removed heap objects. That means fewer allocator calls and less work for the garbage collector.

The tradeoff is that grouping can keep some fields alive together. Here that was acceptable because the fields naturally had similar lifetimes.
-->

---

## Takeaways

- Go avoids OS calls on the allocation hot path
- Small objects are rounded into size classes and placed in spans
- `mcache` is the reason common allocations are fast
- `mcentral` and `mheap` refill lower levels
- GC does not usually return memory directly to the OS
- Knowing the allocator helps you remove avoidable heap work

<!--
The main idea is that Go avoids OS calls on the hot allocation path.

Small objects are rounded into size classes. Spans hold fixed-size slots. `mcache` serves the common case without locks.

When `mcache` cannot serve the request, the runtime climbs the hierarchy to `mcentral`, then `mheap`, then sometimes the OS.

The garbage collector and allocator cooperate through span metadata. The scavenger is what may return physical memory to the OS.

For application code, the practical lesson is to reduce unnecessary heap allocation in hot paths.
-->

---

<!-- _class: references invert -->

## References

- [Go 1.26 Release Notes](https://go.dev/doc/go1.26)
- [Go 1.26.2 runtime source](https://github.com/golang/go/tree/go1.26.2/src/runtime)
- [Scaling the Go Page Allocator](https://go.googlesource.com/proposal/+/refs/changes/57/202857/2/design/35112-scaling-the-page-allocator.md)
- [Memory Allocation in Go by Melatoni](https://nghiant3223.github.io/2025/06/03/memory_allocation_in_go.html)

<!--
These are the main references I used.

The two articles are good starting points. The second article is deeper, and most of it still matches Go 1.26.2.

For exact details, the runtime source is the final source of truth. The allocator comments in `runtime/malloc.go` are especially useful.

The page allocator proposal is also worth reading if you want to understand why the modern page allocator uses bitmaps, summaries, and per-`P` page caches.
-->

---

<!-- _class: invert -->

## Contacts

<div class="contacts">
  <p>
    <a href="https://www.linkedin.com/in/morgenmg" aria-label="LinkedIn">
      <img src="assets/linkedin.svg" alt="" />
    </a>
    <a href="https://www.linkedin.com/in/morgenmg">LinkedIn</a>
  </p>
  <p>
    <a href="https://github.com/zerospiel" aria-label="GitHub">
      <img src="assets/github.svg" alt="" />
    </a>
    <a href="https://github.com/zerospiel">GitHub</a>
  </p>
  <p>
    <a href="https://github.com/zerospiel/zerospiel/tree/main/talks/go-meetup-berlin-05-2026" aria-label="Slides on GitHub">
      <img src="assets/github.svg" alt="" />
    </a>
    <a href="https://github.com/zerospiel/zerospiel/tree/main/talks/go-meetup-berlin-05-2026">Slides on GitHub</a>
  </p>
</div>
