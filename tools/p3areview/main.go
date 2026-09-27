package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const expectedRuns = 5

type sample struct {
	name     string
	nsOp     *float64
	bytesOp  *float64
	allocsOp *float64
}

type aggregate struct {
	name     string
	runs     int
	nsOp     []float64
	bytesOp  []float64
	allocsOp []float64
}

type evidenceFile struct {
	name           string
	requiredPrefix []string
}

var evidenceFiles = []evidenceFile{
	{name: "inline.txt", requiredPrefix: []string{"BenchmarkRegistryResolveOwnedExplicitP0D/", "BenchmarkCache"}},
	{name: "ratelimit.txt", requiredPrefix: []string{"BenchmarkLimiter"}},
	{name: "telegram-rpc-limiter.txt", requiredPrefix: []string{"BenchmarkHierarchicalRPCLimiter"}},
}

func main() {
	var bundle string
	var expectHead string
	var allowIncomplete bool
	flag.StringVar(&bundle, "bundle", "", "P3-A evidence bundle directory")
	flag.StringVar(&expectHead, "expect-head", "", "optional exact Git HEAD required in manifest")
	flag.BoolVar(&allowIncomplete, "allow-incomplete", false, "render summary even when acceptance validation fails")
	flag.Parse()

	if bundle == "" && flag.NArg() == 1 {
		bundle = flag.Arg(0)
	}
	if strings.TrimSpace(bundle) == "" {
		fatalf("usage: p3areview -bundle <evidence-dir> [-expect-head <sha>] [-allow-incomplete]")
	}

	summary, err := reviewBundle(bundle, expectHead)
	if err != nil && !allowIncomplete {
		fatalf("P3-A evidence rejected: %v", err)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: P3-A evidence incomplete: %v\n", err)
	}
	fmt.Print(summary)
	if writeErr := os.WriteFile(filepath.Join(bundle, "summary.md"), []byte(summary), 0o644); writeErr != nil {
		fatalf("write summary: %v", writeErr)
	}
}

func reviewBundle(bundle, expectHead string) (string, error) {
	manifest, err := readManifest(filepath.Join(bundle, "manifest.txt"))
	if err != nil {
		return "", err
	}

	var validation []error
	if manifest["dirty"] != "no" {
		validation = append(validation, fmt.Errorf("manifest dirty=%q, want no", manifest["dirty"]))
	}
	if head := strings.TrimSpace(manifest["head"]); head == "" {
		validation = append(validation, errors.New("manifest head is missing"))
	} else if expectHead != "" && head != expectHead {
		validation = append(validation, fmt.Errorf("manifest head=%s, want %s", head, expectHead))
	}

	var out strings.Builder
	out.WriteString("# Goultroid P3-A benchmark evidence summary\n\n")
	out.WriteString("This summary validates evidence shape and reports medians. It does not apply a performance pass/fail threshold.\n\n")
	out.WriteString("## Manifest\n\n")
	for _, key := range []string{"timestamp_utc", "head", "branch", "commit", "dirty", "go_version", "goos", "goarch", "cgo_enabled", "gomaxprocs_env", "logical_cpus", "cpu_model", "uname"} {
		if value := manifest[key]; value != "" {
			fmt.Fprintf(&out, "- **%s:** `%s`\n", key, escapeMarkdown(value))
		}
	}

	for _, spec := range evidenceFiles {
		path := filepath.Join(bundle, spec.name)
		aggs, parseErr := parseBenchmarkFile(path)
		if parseErr != nil {
			validation = append(validation, fmt.Errorf("%s: %w", spec.name, parseErr))
			continue
		}
		for _, prefix := range spec.requiredPrefix {
			if !hasBenchmarkPrefix(aggs, prefix) {
				validation = append(validation, fmt.Errorf("%s missing benchmark family %q", spec.name, prefix))
			}
		}
		for _, agg := range aggs {
			if agg.runs != expectedRuns {
				validation = append(validation, fmt.Errorf("%s %s has %d runs, want %d", spec.name, agg.name, agg.runs, expectedRuns))
			}
		}
		renderBenchmarkTable(&out, spec.name, aggs)
	}

	out.WriteString("## Evidence validation\n\n")
	if len(validation) == 0 {
		out.WriteString("- Evidence shape: **COMPLETE**\n")
		out.WriteString("- Checkout cleanliness: **CLEAN**\n")
		out.WriteString("- Performance decision: **NOT AUTOMATED**\n")
	} else {
		out.WriteString("- Evidence shape: **INCOMPLETE**\n")
		for _, item := range validation {
			fmt.Fprintf(&out, "- %s\n", escapeMarkdown(item.Error()))
		}
	}

	return out.String(), errors.Join(validation...)
}

func readManifest(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.Contains(line, "=") {
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func parseBenchmarkFile(path string) ([]aggregate, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	grouped := make(map[string]*aggregate)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		s, ok := parseBenchmarkLine(scanner.Text())
		if !ok {
			continue
		}
		agg := grouped[s.name]
		if agg == nil {
			agg = &aggregate{name: s.name}
			grouped[s.name] = agg
		}
		agg.runs++
		if s.nsOp != nil {
			agg.nsOp = append(agg.nsOp, *s.nsOp)
		}
		if s.bytesOp != nil {
			agg.bytesOp = append(agg.bytesOp, *s.bytesOp)
		}
		if s.allocsOp != nil {
			agg.allocsOp = append(agg.allocsOp, *s.allocsOp)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(grouped) == 0 {
		return nil, errors.New("no benchmark samples found")
	}

	aggs := make([]aggregate, 0, len(grouped))
	for _, agg := range grouped {
		aggs = append(aggs, *agg)
	}
	sort.Slice(aggs, func(i, j int) bool { return aggs[i].name < aggs[j].name })
	return aggs, nil
}

func parseBenchmarkLine(line string) (sample, bool) {
	fields := strings.Fields(line)
	if len(fields) < 4 || !strings.HasPrefix(fields[0], "Benchmark") {
		return sample{}, false
	}
	if _, err := strconv.ParseUint(fields[1], 10, 64); err != nil {
		return sample{}, false
	}

	s := sample{name: trimCPUSuffix(fields[0])}
	for i := 2; i+1 < len(fields); i += 2 {
		value, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			continue
		}
		switch fields[i+1] {
		case "ns/op":
			s.nsOp = floatPtr(value)
		case "B/op":
			s.bytesOp = floatPtr(value)
		case "allocs/op":
			s.allocsOp = floatPtr(value)
		}
	}
	return s, true
}

func trimCPUSuffix(name string) string {
	idx := strings.LastIndexByte(name, '-')
	if idx < 0 || idx == len(name)-1 {
		return name
	}
	if _, err := strconv.Atoi(name[idx+1:]); err == nil {
		return name[:idx]
	}
	return name
}

func renderBenchmarkTable(out *strings.Builder, file string, aggs []aggregate) {
	fmt.Fprintf(out, "\n## %s\n\n", file)
	out.WriteString("| Benchmark | Runs | Median ns/op | Median B/op | Median allocs/op |\n")
	out.WriteString("|---|---:|---:|---:|---:|\n")
	for _, agg := range aggs {
		fmt.Fprintf(out, "| `%s` | %d | %s | %s | %s |\n",
			escapeMarkdown(agg.name),
			agg.runs,
			formatMedian(agg.nsOp),
			formatMedian(agg.bytesOp),
			formatMedian(agg.allocsOp),
		)
	}
}

func hasBenchmarkPrefix(aggs []aggregate, prefix string) bool {
	for _, agg := range aggs {
		if strings.HasPrefix(agg.name, prefix) {
			return true
		}
	}
	return false
}

func formatMedian(values []float64) string {
	if len(values) == 0 {
		return "—"
	}
	return strconv.FormatFloat(median(values), 'f', -1, 64)
}

func median(values []float64) float64 {
	copyValues := append([]float64(nil), values...)
	sort.Float64s(copyValues)
	mid := len(copyValues) / 2
	if len(copyValues)%2 == 1 {
		return copyValues[mid]
	}
	return (copyValues[mid-1] + copyValues[mid]) / 2
}

func escapeMarkdown(value string) string {
	return strings.ReplaceAll(value, "|", "\\|")
}

func floatPtr(value float64) *float64 { return &value }

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
