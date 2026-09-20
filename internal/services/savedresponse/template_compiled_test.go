package savedresponse

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCompileCountsOnlySupportedPlaceholders(t *testing.T) {
	source := strings.Repeat(`{"key":1} `, MaxTemplateTokens+32) + "{name}"
	compiled, err := Compile(NewPlainText(source))
	if err != nil {
		t.Fatalf("Compile rejected literal brace content: %v", err)
	}
	if compiled.TokenCount() != 1 {
		t.Fatalf("TokenCount=%d, want 1", compiled.TokenCount())
	}

	out, err := compiled.Render(TemplateVars{Name: "Alice"}, DefaultMaxOutputRunes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "{&#34;key&#34;:1}") {
		t.Fatalf("plain literal JSON was not preserved/escaped: %q", out[:min(len(out), 160)])
	}
	if !strings.HasSuffix(out, "Alice") {
		t.Fatalf("known placeholder was not expanded: %q", out[len(out)-min(len(out), 64):])
	}
}

func TestCompileEscapedKnownPlaceholder(t *testing.T) {
	compiled, err := Compile(NewHTML("literal {{name}} / expanded {name}"))
	if err != nil {
		t.Fatal(err)
	}
	if compiled.TokenCount() != 1 {
		t.Fatalf("TokenCount=%d, want 1", compiled.TokenCount())
	}
	out, err := compiled.Render(TemplateVars{Name: "Alice"}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if out != "literal {name} / expanded Alice" {
		t.Fatalf("rendered=%q", out)
	}
}

func TestCompileUnknownPlaceholderRemainsLiteralAndUncounted(t *testing.T) {
	source := strings.Repeat("{unknown}", MaxTemplateTokens+64)
	compiled, err := Compile(NewHTML(source))
	if err != nil {
		t.Fatalf("Compile rejected unknown literal placeholders: %v", err)
	}
	if compiled.TokenCount() != 0 {
		t.Fatalf("TokenCount=%d, want 0", compiled.TokenCount())
	}
	out, err := compiled.Render(TemplateVars{}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if out != source {
		t.Fatalf("unknown placeholders changed during render")
	}
}

func TestCompileRejectsTooManySupportedPlaceholders(t *testing.T) {
	source := strings.Repeat("{name}", MaxTemplateTokens+1)
	_, err := Compile(NewHTML(source))
	if !errors.Is(err, ErrTooManyTokens) {
		t.Fatalf("Compile error=%v, want ErrTooManyTokens", err)
	}
}

func TestCompiledTemplateReusableAcrossVariableSets(t *testing.T) {
	compiled, err := Compile(NewHTML("<b>{name}</b> @ {date} {time}"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 20, 8, 9, 10, 0, time.FixedZone("WIB", 7*3600))

	alice, err := compiled.Render(TemplateVars{Name: "Alice", Now: now}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := compiled.Render(TemplateVars{Name: "Bob", Now: now}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if alice != "<b>Alice</b> @ 2026-09-20 08:09:10" {
		t.Fatalf("alice render=%q", alice)
	}
	if bob != "<b>Bob</b> @ 2026-09-20 08:09:10" {
		t.Fatalf("bob render=%q", bob)
	}
}

func TestCompiledTemplatePreservesMalformedBraces(t *testing.T) {
	source := "before {name after } and {this_token_name_is_far_too_long_to_be_valid} end"
	compiled, err := Compile(NewHTML(source))
	if err != nil {
		t.Fatal(err)
	}
	if compiled.TokenCount() != 0 {
		t.Fatalf("TokenCount=%d, want 0", compiled.TokenCount())
	}
	out, err := compiled.Render(TemplateVars{Name: "Alice"}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if out != source {
		t.Fatalf("malformed literal changed: got %q want %q", out, source)
	}
}

func TestCompiledTemplateHardCapsRequestedRenderLimit(t *testing.T) {
	compiled, err := Compile(NewHTML(strings.Repeat("x", DefaultMaxOutputRunes+1)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = compiled.Render(TemplateVars{}, DefaultMaxOutputRunes*10)
	if !errors.Is(err, ErrRenderedTooLarge) {
		t.Fatalf("render error=%v, want ErrRenderedTooLarge", err)
	}
}

func TestCompiledTemplateRespectsSmallerRenderLimit(t *testing.T) {
	compiled, err := Compile(NewHTML("123456"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = compiled.Render(TemplateVars{}, 5)
	if !errors.Is(err, ErrRenderedTooLarge) {
		t.Fatalf("render error=%v, want ErrRenderedTooLarge", err)
	}
}

func TestVariableValueIsTruncatedBeforeEscaping(t *testing.T) {
	name := strings.Repeat("界", MaxVariableRunes+100)
	compiled, err := Compile(NewHTML("{name}"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := compiled.Render(TemplateVars{Name: name}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if got := utf8.RuneCountInString(out); got != MaxVariableRunes {
		t.Fatalf("rendered variable runes=%d, want %d", got, MaxVariableRunes)
	}
}

func TestNilCompiledTemplateReturnsTypedError(t *testing.T) {
	var compiled *CompiledTemplate
	if _, err := compiled.Render(TemplateVars{}, 4096); !errors.Is(err, ErrNilTemplate) {
		t.Fatalf("nil template error=%v, want ErrNilTemplate", err)
	}
}

func TestCompilePlainEscapesStaticHTMLOnce(t *testing.T) {
	compiled, err := Compile(NewPlainText("<b>{name}</b> & text"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := compiled.Render(TemplateVars{Name: "A&B"}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	want := "&lt;b&gt;A&amp;B&lt;/b&gt; &amp; text"
	if out != want {
		t.Fatalf("plain output=%q, want %q", out, want)
	}
}

func TestCompileWithVariablesPreservesDefaultUnknownSemantics(t *testing.T) {
	response := NewHTML("default {reason} / custom {reason}")

	defaultCompiled, err := Compile(response)
	if err != nil {
		t.Fatal(err)
	}
	defaultOut, err := defaultCompiled.Render(TemplateVars{Extra: map[string]string{"reason": "away"}}, DefaultMaxOutputRunes)
	if err != nil {
		t.Fatal(err)
	}
	if defaultOut != "default {reason} / custom {reason}" {
		t.Fatalf("default Compile changed unknown placeholder semantics: %q", defaultOut)
	}

	customCompiled, err := CompileWithVariables(response, "reason")
	if err != nil {
		t.Fatal(err)
	}
	customOut, err := customCompiled.Render(TemplateVars{Extra: map[string]string{"reason": "<away>"}}, DefaultMaxOutputRunes)
	if err != nil {
		t.Fatal(err)
	}
	if customOut != "default &lt;away&gt; / custom &lt;away&gt;" {
		t.Fatalf("custom variable render=%q", customOut)
	}
}

func TestCompileWithVariablesSupportsEscapedCustomPlaceholder(t *testing.T) {
	compiled, err := CompileWithVariables(NewHTML("literal {{reason}} / expanded {reason}"), "reason")
	if err != nil {
		t.Fatal(err)
	}
	out, err := compiled.Render(TemplateVars{Extra: map[string]string{"reason": "away"}}, DefaultMaxOutputRunes)
	if err != nil {
		t.Fatal(err)
	}
	if out != "literal {reason} / expanded away" {
		t.Fatalf("custom placeholder render=%q", out)
	}
}

func TestCompileWithVariablesRejectsInvalidOrReservedNames(t *testing.T) {
	for _, variable := range []string{"", "Reason", "_reason", "name", "this_variable_name_is_too_long"} {
		if _, err := CompileWithVariables(NewHTML("ok"), variable); !errors.Is(err, ErrInvalidTemplateVariable) {
			t.Fatalf("variable %q error=%v, want ErrInvalidTemplateVariable", variable, err)
		}
	}
}

func TestCompileWithVariablesUsesSameVariableBounds(t *testing.T) {
	compiled, err := CompileWithVariables(NewHTML("{reason}"), "reason")
	if err != nil {
		t.Fatal(err)
	}
	out, err := compiled.Render(TemplateVars{
		Extra: map[string]string{"reason": strings.Repeat("界", MaxVariableRunes+100)},
	}, DefaultMaxOutputRunes)
	if err != nil {
		t.Fatal(err)
	}
	if got := utf8.RuneCountInString(out); got != MaxVariableRunes {
		t.Fatalf("custom variable runes=%d, want %d", got, MaxVariableRunes)
	}
}

func BenchmarkCompileTemplate(b *testing.B) {
	response := NewHTML(strings.Repeat("hello {name} in {chat} at {time}\n", 16))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Compile(response); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRenderCompiledTemplate(b *testing.B) {
	compiled, err := Compile(NewHTML(strings.Repeat("hello {name} in {chat}\n", 16)))
	if err != nil {
		b.Fatal(err)
	}
	vars := TemplateVars{Name: "Alice", Chat: "Group"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := compiled.Render(vars, 4096); err != nil {
			b.Fatal(err)
		}
	}
}
