package calculator

import (
	"math"
	"strings"
	"testing"
)

func TestEvaluateExpression(t *testing.T) {
	tests := []struct {
		expr string
		want float64
	}{
		{"1+2*3", 7},
		{"(1+2)*3", 9},
		{"2^3^2", 512},
		{"10%4", 2},
		{"-2.5*4", -10},
		{"1/4", 0.25},
		{"1e+12+2", 1000000000002},
	}
	for _, tc := range tests {
		got, err := evaluateExpression(tc.expr)
		if err != nil {
			t.Fatalf("evaluateExpression(%q): %v", tc.expr, err)
		}
		if math.Abs(got-tc.want) > 1e-9 {
			t.Fatalf("evaluateExpression(%q) = %v, want %v", tc.expr, got, tc.want)
		}
	}
}

func TestEvaluateExpressionFailsClosed(t *testing.T) {
	for _, expr := range []string{"", "1/0", "1+", "sqrt(4)", "1;2", strings.Repeat("1", maxExpressionBytes+1)} {
		if _, err := evaluateExpression(expr); err == nil {
			t.Fatalf("evaluateExpression(%q) unexpectedly succeeded", expr)
		}
	}
}
