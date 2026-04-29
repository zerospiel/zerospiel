# Go Memory Allocator Presentation

Files:

- [slides.md](slides.md): Markdown slide deck, written for [Marp-style](https://github.com/marp-team/marp-cli) slide separators.
- [examples](examples): small benchmarks used for the optimization examples.

```sh
brew install marp-cli
marp slides.md && open slides.html
```

Run the example benchmarks:

```sh
cd examples
go test -run '^$' -bench=. -benchmem
```
