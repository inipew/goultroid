package system

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
)

// RestartState stores metadata across bot restarts.
type RestartState struct {
	PeerType   string `json:"peer_type"` // "self", "user", "channel", "chat"
	ChatID     int64  `json:"chat_id"`
	IsChannel  bool   `json:"is_channel,omitempty"`
	AccessHash int64  `json:"access_hash"`
	MsgID      int    `json:"msg_id"`
	Time       int64  `json:"time"`
}

// Plugin provides shell execution and system management commands.
type Plugin struct {
	restartStatePath string
	restartFunc      func(state RestartState) error
	cmdRunner        func(ctx context.Context, name string, args ...string) ([]byte, error)
	startTime        time.Time
}

// New creates a new System plugin.
func New() *Plugin {
	return &Plugin{
		restartStatePath: "data/restart.json",
		startTime:        time.Now(),
	}
}

// NewWithCustomRestart creates a System plugin with a custom restart handler (useful for testing).
func NewWithCustomRestart(statePath string, restartFn func(state RestartState) error) *Plugin {
	return &Plugin{
		restartStatePath: statePath,
		restartFunc:      restartFn,
	}
}

// SetCmdRunner overrides command execution for testing.
func (p *Plugin) SetCmdRunner(fn func(ctx context.Context, name string, args ...string) ([]byte, error)) {
	p.cmdRunner = fn
}

func (p *Plugin) runCmd(ctx context.Context, name string, args ...string) ([]byte, error) {
	if p.cmdRunner != nil {
		return p.cmdRunner(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return "system"
}

// Metadata returns rich information about the system plugin.
func (p *Plugin) Metadata() plugin.Metadata {
	return plugin.Metadata{
		Name:        "system",
		Version:     "1.0.0",
		Author:      "GoUltroid Team",
		Description: "Shell command execution and userbot lifecycle management",
	}
}

// Description returns a short summary of the plugin.
func (p *Plugin) Description() string {
	return "Shell command execution and userbot lifecycle management"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	return nil
}

// Shutdown cleans up resources.
func (p *Plugin) Shutdown() error {
	return nil
}

// Commands returns the list of registered commands.
func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "exec",
			Aliases:     []string{"sh", "bash", "cmd"},
			Description: "Execute a shell command on the host machine (Owner Only)",
			Usage:       ".exec <shell command>",
			Category:    "System",
			Permission:  core.PermissionOwner,
			Timeout:     65 * time.Second,
			Handler:     p.handleExec,
		},
		{
			Name:        "restart",
			Description: "Restart the GoUltroid userbot process (Owner Only)",
			Usage:       ".restart",
			Category:    "System",
			Permission:  core.PermissionOwner,
			Handler:     p.handleRestart,
		},
		{
			Name:        "update",
			Aliases:     []string{"gitupdate"},
			Description: "Check for updates from git remote or pull and rebuild (Owner Only)",
			Usage:       ".update [pull|now]",
			Category:    "System",
			Permission:  core.PermissionOwner,
			Timeout:     180 * time.Second,
			Handler:     p.handleUpdate,
		},
		{
			Name:        "health",
			Aliases:     []string{"runtime", "memstats"},
			Description: "Show runtime memory and goroutine health statistics (Owner Only)",
			Usage:       ".health",
			Category:    "System",
			Permission:  core.PermissionOwner,
			Handler:     p.handleHealth,
		},
	}
}

type limitedWriter struct {
	w         *bytes.Buffer
	remain    int
	truncated bool
}

func (lw *limitedWriter) Write(p []byte) (n int, err error) {
	if lw.remain <= 0 {
		lw.truncated = true
		return len(p), nil
	}
	if len(p) > lw.remain {
		lw.truncated = true
		p = p[:lw.remain]
	}
	n, err = lw.w.Write(p)
	lw.remain -= n
	return len(p), err
}

// handleExec executes a bash command with timeout and formats the result.
func (p *Plugin) handleExec(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		return ctx.Reply("⚠️ <b>Usage:</b> <code>.exec &lt;shell command&gt;</code>")
	}

	commandStr := strings.Join(ctx.Args, " ")

	_ = ctx.Reply("⏳ <i>Executing command...</i>")

	// Timeout protection (60s)
	execCtx, cancel := context.WithTimeout(ctx.Ctx, 60*time.Second)
	defer cancel()

	start := time.Now()

	shell := "bash"
	if _, err := exec.LookPath("bash"); err != nil {
		shell = "sh"
	}

	cmd := exec.CommandContext(execCtx, shell, "-c", commandStr)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil && cmd.Process.Pid > 0 {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}

	// Limit output capture to 2 MB to prevent memory exhaustion
	const maxOutputBytes = 2 * 1024 * 1024
	buf := new(bytes.Buffer)
	limitWriter := &limitedWriter{w: buf, remain: maxOutputBytes}
	cmd.Stdout = limitWriter
	cmd.Stderr = limitWriter

	err := cmd.Run()
	elapsed := time.Since(start)

	output := buf.String()
	if limitWriter.truncated {
		output += "\n\n[output truncated after 2MB]"
	}
	if output == "" {
		if err != nil {
			output = fmt.Sprintf("Error: %v", err)
		} else {
			output = "(no output)"
		}
	}

	// Telegram message length limit check (max 4096, safe threshold 3500)
	if len(output) <= 3500 {
		var sb strings.Builder
		sb.WriteString("💻 <b>Shell Execution</b>\n\n")
		sb.WriteString(fmt.Sprintf("• <b>Command:</b> <code>%s</code>\n", escapeHTML(commandStr)))
		sb.WriteString(fmt.Sprintf("• <b>Duration:</b> <i>%s</i>\n\n", elapsed.Round(time.Millisecond)))
		sb.WriteString(fmt.Sprintf("<pre><code class=\"language-bash\">%s</code></pre>", escapeHTML(output)))
		return ctx.Reply(sb.String())
	}

	// If output is too large, upload as a text file
	tmpFile, tmpErr := os.CreateTemp("", "exec-output-*.txt")
	if tmpErr != nil {
		return ctx.Reply(fmt.Sprintf("❌ Failed to create temp file for large output: %v", tmpErr))
	}
	defer os.Remove(tmpFile.Name())

	_, _ = tmpFile.WriteString(fmt.Sprintf("Command: %s\nDuration: %s\n\nOutput:\n%s", commandStr, elapsed, output))
	_ = tmpFile.Close()

	caption := fmt.Sprintf("📄 <b>Execution Output</b> (<code>%s</code>, took <i>%s</i>)", escapeHTML(commandStr), elapsed.Round(time.Millisecond))
	return ctx.SendFile(tmpFile.Name(), caption)
}

// handleRestart initiates a graceful restart of the bot.
func (p *Plugin) handleRestart(ctx *core.Context) error {
	_ = ctx.Reply("🔄 <i>Restarting GoUltroid...</i>")

	var chatID int64
	var peerType string
	var accessHash int64
	var isChannel bool

	if ctx.PeerID != nil {
		switch peer := ctx.PeerID.(type) {
		case *tg.InputPeerSelf:
			peerType = "self"
		case *tg.InputPeerUser:
			peerType = "user"
			chatID = peer.UserID
			accessHash = peer.AccessHash
		case *tg.InputPeerChannel:
			peerType = "channel"
			chatID = peer.ChannelID
			accessHash = peer.AccessHash
			isChannel = true
		case *tg.InputPeerChat:
			peerType = "chat"
			chatID = peer.ChatID
		default:
			if ctx.Chat != nil {
				chatID = ctx.Chat.ID
				if ctx.Chat.Type == "channel" || ctx.Chat.Type == "supergroup" {
					peerType = "channel"
					isChannel = true
				} else if ctx.Chat.Type == "private" {
					peerType = "user"
				} else {
					peerType = "chat"
				}
			}
		}
	} else if ctx.Chat != nil {
		chatID = ctx.Chat.ID
		if ctx.Chat.Type == "channel" || ctx.Chat.Type == "supergroup" {
			peerType = "channel"
			isChannel = true
		} else if ctx.Chat.Type == "private" {
			peerType = "user"
		} else {
			peerType = "chat"
		}
	}

	msgID := ctx.LastResponseID
	if msgID == 0 && ctx.Message != nil {
		msgID = ctx.Message.ID
	}

	state := RestartState{
		PeerType:   peerType,
		ChatID:     chatID,
		IsChannel:  isChannel,
		AccessHash: accessHash,
		MsgID:      msgID,
		Time:       time.Now().Unix(),
	}

	if p.restartFunc != nil {
		return p.restartFunc(state)
	}

	// Save state to file atomically
	if p.restartStatePath != "" {
		if err := os.MkdirAll(filepath.Dir(p.restartStatePath), 0700); err == nil {
			if data, err := json.Marshal(state); err == nil {
				tmpPath := p.restartStatePath + ".tmp"
				if f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600); err == nil {
					_, _ = f.Write(data)
					_ = f.Sync()
					_ = f.Close()
					_ = os.Rename(tmpPath, p.restartStatePath)
				}
			}
		}
	}

	// Trigger self-exec on Linux
	execPath, err := os.Executable()
	if err == nil {
		_ = syscall.Exec(execPath, os.Args, os.Environ())
	}

	os.Exit(0)
	return nil
}

func (p *Plugin) handleUpdate(ctx *core.Context) error {
	isPull := len(ctx.Args) > 0 && (strings.ToLower(ctx.Args[0]) == "pull" || strings.ToLower(ctx.Args[0]) == "now")

	if !isPull {
		_ = ctx.Reply("🔍 <i>Checking for updates from git remote...</i>")

		fetchCtx, cancel := context.WithTimeout(ctx.Ctx, 30*time.Second)
		defer cancel()

		if out, err := p.runCmd(fetchCtx, "git", "fetch"); err != nil {
			_ = ctx.Reply(fmt.Sprintf("❌ <code>git fetch</code> failed: %v\n<pre>%s</pre>", err, escapeHTML(string(out))))
			return err
		}

		// Get current short commit
		currHashOut, _ := p.runCmd(fetchCtx, "git", "rev-parse", "--short", "HEAD")
		currHash := strings.TrimSpace(string(currHashOut))

		// Check commits behind
		logOut, err := p.runCmd(fetchCtx, "git", "log", "HEAD..origin/main", "--oneline")
		if err != nil {
			logOut, err = p.runCmd(fetchCtx, "git", "log", "HEAD..@{u}", "--oneline")
		}

		commits := strings.TrimSpace(string(logOut))
		if err != nil || commits == "" {
			return ctx.Reply(fmt.Sprintf("✨ <b>GoUltroid is already up to date!</b>\n• <b>Commit:</b> <code>%s</code>", currHash))
		}

		commitLines := strings.Split(commits, "\n")
		var sb strings.Builder
		sb.WriteString("🔄 <b>New updates available!</b>\n")
		sb.WriteString(fmt.Sprintf("• <b>Current Commit:</b> <code>%s</code>\n", currHash))
		sb.WriteString(fmt.Sprintf("• <b>Pending Commits (%d):</b>\n", len(commitLines)))
		sb.WriteString(fmt.Sprintf("<pre>%s</pre>\n\n", escapeHTML(commits)))
		sb.WriteString("💡 <i>Run <code>.update pull</code> or <code>.update now</code> to pull changes, rebuild, and restart.</i>")
		return ctx.Reply(sb.String())
	}

	_ = ctx.Reply("⬇️ <i>Pulling latest updates from git...</i>")

	pullCtx, cancelPull := context.WithTimeout(ctx.Ctx, 60*time.Second)
	defer cancelPull()

	// Check for uncommitted working tree modifications
	statusOut, _ := p.runCmd(pullCtx, "git", "status", "--porcelain")
	if strings.TrimSpace(string(statusOut)) != "" {
		_ = ctx.Reply("❌ Cannot update: working directory has uncommitted modifications. Stash or commit your changes first.")
		return errors.New("dirty working tree")
	}

	if out, err := p.runCmd(pullCtx, "git", "pull", "--ff-only"); err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ <code>git pull --ff-only</code> failed: %v\n<pre>%s</pre>", err, escapeHTML(string(out))))
		return err
	}

	_ = ctx.Reply("🔨 <i>Rebuilding GoUltroid binary...</i>")

	buildCtx, cancelBuild := context.WithTimeout(ctx.Ctx, 120*time.Second)
	defer cancelBuild()

	tmpBin := filepath.Join("bin", "goultroid.tmp")
	if out, err := p.runCmd(buildCtx, "go", "build", "-o", tmpBin, "./cmd/goultroid"); err != nil {
		_ = os.Remove(tmpBin)
		_ = ctx.Reply(fmt.Sprintf("❌ Rebuild failed: %v\n<pre>%s</pre>", err, escapeHTML(string(out))))
		return err
	}

	finalBin := filepath.Join("bin", "goultroid")
	if err := os.Rename(tmpBin, finalBin); err != nil {
		_ = os.Remove(tmpBin)
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to replace binary: %v", err))
		return err
	}

	_ = ctx.Reply("✅ <i>Rebuild successful! Restarting GoUltroid...</i>")
	return p.handleRestart(ctx)
}

func escapeHTML(s string) string {
	return core.EscapeHTML(s)
}

// handleHealth collects runtime memory, goroutine, and GC stats and replies with a formatted report.
func (p *Plugin) handleHealth(ctx *core.Context) error {
	stats := core.GatherHealth(p.startTime)

	// Build a human-readable uptime string
	uptime := stats.Uptime
	days := int(uptime.Hours()) / 24
	hours := int(uptime.Hours()) % 24
	mins := int(uptime.Minutes()) % 60
	secs := int(uptime.Seconds()) % 60

	var uptimeStr string
	if days > 0 {
		uptimeStr = fmt.Sprintf("%dd %02dh %02dm %02ds", days, hours, mins, secs)
	} else if hours > 0 {
		uptimeStr = fmt.Sprintf("%dh %02dm %02ds", hours, mins, secs)
	} else {
		uptimeStr = fmt.Sprintf("%dm %02ds", mins, secs)
	}

	msg := fmt.Sprintf(
		"🔧 <b>GoUltroid Runtime Health</b>\n\n"+
			"⏱️ <b>Uptime:</b> <code>%s</code>\n"+
			"🧵 <b>Goroutines:</b> <code>%d</code>\n"+
			"💾 <b>Heap Alloc:</b> <code>%.2f MB</code>\n"+
			"🖥️ <b>Sys Memory:</b> <code>%.2f MB</code>\n"+
			"♻️ <b>GC Cycles:</b> <code>%d</code>\n"+
			"⏸️ <b>GC Pause Total:</b> <code>%.2f ms</code>\n"+
			"🔢 <b>Go Version:</b> <code>%s</code>",
		uptimeStr,
		stats.Goroutines,
		stats.HeapAllocMB,
		stats.SysMB,
		stats.NumGC,
		stats.PauseTotalMs,
		runtime.Version(),
	)

	return ctx.Reply(msg)
}
