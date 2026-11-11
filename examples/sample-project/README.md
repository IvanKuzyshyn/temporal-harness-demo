# Sample project

This independent Go module is a deliberately broken fixture for the harness.
`calc.Add` intentionally subtracts instead of adding, so its single test fails:
`Add(2, 3)` returns `-1` instead of `5`. The two TODO comments identify follow-up
work for the harness; the bug is not meant to be fixed in this initial fixture.

With Go 1.27.3 available, run the test from this nested directory:

```sh
cd examples/sample-project
go test ./...
```

The assertion failure is expected. This directory has its own `go.mod`, so
running `go test ./...` from the repository root excludes this sample module.
