package ui

import (
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/callback"
)

func TestFormatHelpers(t *testing.T) {
	if got := EscapeHTML("hello <world> & test"); got != "hello &lt;world&gt; &amp; test" {
		t.Errorf("EscapeHTML mismatch, got: %s", got)
	}

	if got := Bold("title"); got != "<b>title</b>" {
		t.Errorf("Bold mismatch, got: %s", got)
	}

	if got := Italic("text"); got != "<i>text</i>" {
		t.Errorf("Italic mismatch, got: %s", got)
	}

	if got := Underline("text"); got != "<u>text</u>" {
		t.Errorf("Underline mismatch, got: %s", got)
	}

	if got := Strike("text"); got != "<s>text</s>" {
		t.Errorf("Strike mismatch, got: %s", got)
	}

	if got := Code(".help"); got != "<code>.help</code>" {
		t.Errorf("Code mismatch, got: %s", got)
	}

	if got := Pre("var a = 1"); got != "<pre>var a = 1</pre>" {
		t.Errorf("Pre mismatch, got: %s", got)
	}

	if got := Pre("var a = 1", "go"); got != `<pre><code class="language-go">var a = 1</code></pre>` {
		t.Errorf("Pre with lang mismatch, got: %s", got)
	}

	if got := Blockquote("quote content", false); got != "<blockquote>quote content</blockquote>" {
		t.Errorf("Blockquote mismatch, got: %s", got)
	}

	if got := Blockquote("quote content", true); got != "<blockquote expandable>quote content</blockquote>" {
		t.Errorf("Blockquote expandable mismatch, got: %s", got)
	}

	if got := KeyValue("Status", "Active"); got != "• <b>Status:</b> Active" {
		t.Errorf("KeyValue mismatch, got: %s", got)
	}
}

func TestBadge(t *testing.T) {
	if got := Badge(core.PermissionOwner); !strings.Contains(got, "Owner") {
		t.Errorf("expected Owner badge, got: %s", got)
	}
	if got := Badge(core.PermissionSudo); !strings.Contains(got, "Sudo") {
		t.Errorf("expected Sudo badge, got: %s", got)
	}
	if got := Badge(core.PermissionEveryone); !strings.Contains(got, "Everyone") {
		t.Errorf("expected Everyone badge, got: %s", got)
	}
	if got := Badge(core.Permission(99)); !strings.Contains(got, "Everyone") {
		t.Errorf("expected default Everyone badge, got: %s", got)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1024 * 1024 * 15, "15.0 MB"},
		{1024 * 1024 * 1024 * 2, "2.0 GB"},
	}

	for _, tt := range tests {
		got := FormatBytes(tt.bytes)
		if got != tt.expected {
			t.Errorf("FormatBytes(%d) = %s, expected %s", tt.bytes, got, tt.expected)
		}
	}
}

func TestProgressBar(t *testing.T) {
	// Standard 50%
	bar := ProgressBar(50, 100, 10)
	if !strings.Contains(bar, "50.0%") || !strings.Contains(bar, "■■■■■□□□□□") {
		t.Errorf("unexpected 50%% progress bar: %s", bar)
	}

	// 0%
	bar0 := ProgressBar(0, 100, 10)
	if !strings.Contains(bar0, "0.0%") || !strings.Contains(bar0, "□□□□□□□□□□") {
		t.Errorf("unexpected 0%% progress bar: %s", bar0)
	}

	// 100%
	bar100 := ProgressBar(100, 100, 10)
	if !strings.Contains(bar100, "100.0%") || !strings.Contains(bar100, "■■■■■■■■■■") {
		t.Errorf("unexpected 100%% progress bar: %s", bar100)
	}

	// Total <= 0 boundary
	barZeroTotal := ProgressBar(10, 0, 10)
	if !strings.Contains(barZeroTotal, "0.0%") {
		t.Errorf("unexpected zero total progress bar: %s", barZeroTotal)
	}

	// Current > Total clamped
	barOver := ProgressBar(150, 100, 10)
	if !strings.Contains(barOver, "100.0%") {
		t.Errorf("unexpected overflown progress bar: %s", barOver)
	}

	// Negative current clamped
	barNeg := ProgressBar(-5, 100, 10)
	if !strings.Contains(barNeg, "0.0%") {
		t.Errorf("unexpected negative progress bar: %s", barNeg)
	}

	// FormatProgress
	fp := FormatProgress(50*1024*1024, 100*1024*1024, 10)
	if !strings.Contains(fp, "50.0 MB / 100.0 MB") {
		t.Errorf("unexpected FormatProgress: %s", fp)
	}
}

func TestCard(t *testing.T) {
	card := NewCard("System Status").
		WithIcon("⚡").
		WithHeader("Realtime server metrics").
		AddField("Uptime", "2h 30m").
		AddField("Memory", "45.0 MB").
		WithCollapsible("CPU Details:\nCore 0: 20%\nCore 1: 15%").
		WithFooter("<i>Updated just now</i>")

	rendered := card.Render()

	if !strings.Contains(rendered, "⚡ <b>System Status</b>") {
		t.Errorf("missing icon or title in rendered card: %s", rendered)
	}
	if !strings.Contains(rendered, "Realtime server metrics") {
		t.Errorf("missing header in rendered card: %s", rendered)
	}
	if !strings.Contains(rendered, "• <b>Uptime:</b> 2h 30m") {
		t.Errorf("missing field in rendered card: %s", rendered)
	}
	if !strings.Contains(rendered, "<blockquote expandable>") {
		t.Errorf("missing expandable blockquote in rendered card: %s", rendered)
	}
	if !strings.Contains(rendered, "<i>Updated just now</i>") {
		t.Errorf("missing footer in rendered card: %s", rendered)
	}
}

func TestAlerts(t *testing.T) {
	if got := Success("File saved"); !strings.Contains(got, "Success:") || !strings.Contains(got, "File saved") {
		t.Errorf("unexpected Success alert: %s", got)
	}
	if got := Warning("Low disk space"); !strings.Contains(got, "Warning:") || !strings.Contains(got, "Low disk space") {
		t.Errorf("unexpected Warning alert: %s", got)
	}
	if got := Error("Connection failed"); !strings.Contains(got, "Error:") || !strings.Contains(got, "Connection failed") {
		t.Errorf("unexpected Error alert: %s", got)
	}
	if got := Processing("Downloading"); !strings.Contains(got, "Processing:") || !strings.Contains(got, "Downloading") {
		t.Errorf("unexpected Processing alert: %s", got)
	}
	if got := Information("No updates"); !strings.Contains(got, "Info:") || !strings.Contains(got, "No updates") {
		t.Errorf("unexpected Information alert: %s", got)
	}
}

func TestScreen_RenderScreen(t *testing.T) {
	screen := NewScreen("main", "Dashboard", "Welcome to dashboard")
	screen.AddRow(NewCallbackButton("Click", []byte("click_data")))

	rendered := screen.RenderScreen()
	if !strings.Contains(rendered.Text, "<b>Dashboard</b>") {
		t.Errorf("expected title in rendered screen text, got: %s", rendered.Text)
	}
	if rendered.Markup == nil {
		t.Errorf("expected non-nil markup in rendered screen")
	}
}

func TestMapUserErrorMessage(t *testing.T) {
	if got := MapUserErrorMessage(nil); got != "" {
		t.Errorf("expected empty string for nil error, got: %s", got)
	}
	if got := MapUserErrorMessage(core.ErrRateLimited); !strings.Contains(got, "Too many requests") {
		t.Errorf("expected rate limit message, got: %s", got)
	}
	if got := MapUserErrorMessage(callback.ErrUnauthorized); !strings.Contains(got, "not authorized") {
		t.Errorf("expected unauthorized message, got: %s", got)
	}
	if got := MapUserErrorMessage(callback.ErrStateExpired); !strings.Contains(got, "expired") {
		t.Errorf("expected expired message, got: %s", got)
	}
	if got := MapUserErrorMessage(callback.ErrInvalidCallbackData); !strings.Contains(got, "Invalid button action") {
		t.Errorf("expected invalid callback message, got: %s", got)
	}
}

