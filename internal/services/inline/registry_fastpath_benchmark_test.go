package inline

import (
	"fmt"
	"testing"
)

var (
	p0dBenchmarkResolved Resolved
	p0dBenchmarkOK       bool
)

type p0dBenchmarkMatcher struct {
	match bool
}

func (m *p0dBenchmarkMatcher) Match(string) ([]string, bool) {
	if !m.match {
		return nil, false
	}
	return []string{"arg"}, true
}

func BenchmarkRegistryResolveOwnedExplicitP0D(b *testing.B) {
	for _, count := range []int{1, 16, 64, 256} {
		b.Run(fmt.Sprintf("exact/%d", count), func(b *testing.B) {
			reg := NewRegistry()
			for i := 0; i < count; i++ {
				pattern := fmt.Sprintf("k%03d", i)
				if err := reg.Register(&p0dTestHandler{pattern: pattern}); err != nil {
					b.Fatal(err)
				}
			}
			query := fmt.Sprintf("k%03d arg", count-1)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p0dBenchmarkResolved, p0dBenchmarkOK = reg.ResolveOwnedExplicit(query)
			}
		})

		b.Run(fmt.Sprintf("custom/%d", count), func(b *testing.B) {
			reg := NewRegistry()
			for i := 0; i < count; i++ {
				pattern := fmt.Sprintf("c%03d", i)
				matcher := &p0dBenchmarkMatcher{match: i == count-1}
				if err := reg.RegisterMatcher(pattern, matcher, &p0dTestHandler{pattern: pattern}, 0); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p0dBenchmarkResolved, p0dBenchmarkOK = reg.ResolveOwnedExplicit("needle arg")
			}
		})
	}
}
