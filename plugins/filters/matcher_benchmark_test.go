package filters

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

var filterBenchmarkSizes = []int{1, 10, 100, 1000, 4096}

func benchmarkCompiledFilters(n int) []compiledFilter {
	filters := make([]compiledFilter, n)
	for i := range filters {
		filters[i].keyword = fmt.Sprintf("keyword-%04d", i)
	}
	return filters
}

func benchmarkLegacyRegexes(n int) []*regexp.Regexp {
	regexes := make([]*regexp.Regexp, n)
	for i := range regexes {
		keyword := fmt.Sprintf("keyword-%04d", i)
		pattern := `(?:^|[^\p{L}\p{N}_])` + regexp.QuoteMeta(keyword) + `(?:$|[^\p{L}\p{N}_])`
		regexes[i] = regexp.MustCompile(pattern)
	}
	return regexes
}

func BenchmarkFilterMatcherNoMatch(b *testing.B) {
	const text = "this is a normal telegram message with no configured trigger anywhere and some punctuation."
	for _, size := range filterBenchmarkSizes {
		matcher := newKeywordMatcher(benchmarkCompiledFilters(size))
		b.Run(fmt.Sprintf("N=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if got := matcher.firstMatch(text); got != -1 {
					b.Fatalf("match=%d", got)
				}
			}
		})
	}
}

func BenchmarkFilterMatcherLateMatch(b *testing.B) {
	for _, size := range filterBenchmarkSizes {
		matcher := newKeywordMatcher(benchmarkCompiledFilters(size))
		text := "please show keyword-" + fmt.Sprintf("%04d", size-1) + " now"
		b.Run(fmt.Sprintf("N=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if got := matcher.firstMatch(text); got != size-1 {
					b.Fatalf("match=%d", got)
				}
			}
		})
	}
}

func BenchmarkLegacyFilterMatcherNoMatch(b *testing.B) {
	const text = "this is a normal telegram message with no configured trigger anywhere and some punctuation."
	for _, size := range filterBenchmarkSizes {
		regexes := benchmarkLegacyRegexes(size)
		b.Run(fmt.Sprintf("N=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				for _, re := range regexes {
					if re.MatchString(text) {
						b.Fatal("unexpected match")
					}
				}
			}
		})
	}
}

func BenchmarkLegacyFilterMatcherLateMatch(b *testing.B) {
	for _, size := range filterBenchmarkSizes {
		regexes := benchmarkLegacyRegexes(size)
		text := strings.ToLower("please show keyword-" + fmt.Sprintf("%04d", size-1) + " now")
		b.Run(fmt.Sprintf("N=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				matched := -1
				for index, re := range regexes {
					if re.MatchString(text) {
						matched = index
						break
					}
				}
				if matched != size-1 {
					b.Fatalf("match=%d", matched)
				}
			}
		})
	}
}

func BenchmarkFilterMatcherBuild(b *testing.B) {
	for _, size := range filterBenchmarkSizes {
		filters := benchmarkCompiledFilters(size)
		b.Run(fmt.Sprintf("N=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = newKeywordMatcher(filters)
			}
		})
	}
}
