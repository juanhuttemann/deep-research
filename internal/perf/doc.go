// Package perf holds the benchmark of a whole run with the model and the
// network taken out (see BenchmarkPipelineRun). The hot paths inside a run
// are benchmarked next to their code, in benchmark_test.go: the streamed
// phase and the answer parsing in internal/agent, ranking, folding and
// excerpting in internal/tools.
//
// Every benchmark runs on artifacts captured from a real run, under
// internal/testdata/perf: the run's .json sidecar, the analysis and the
// fact-check as the model sent them, and the largest page it scraped. A
// benchmark measured on a synthetic fixture tunes the code for that fixture.
//
// Nothing here runs under `go test ./...`. To compare a change, run the
// benchmarks ten times before and after it and let benchstat judge the
// difference:
//
//	go test ./internal/agent/ ./internal/tools/ ./internal/perf/ \
//	    -run=NONE -bench=. -count=10 -benchmem > old.txt
//	# make the change, then the same into new.txt
//	go run golang.org/x/perf/cmd/benchstat@latest old.txt new.txt
//
// A commit that is about performance carries that comparison in its message.
package perf
