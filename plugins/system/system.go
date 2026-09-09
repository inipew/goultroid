package system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/process"
)

type RestartState struct {
	PeerType   string `json:"peer_type"`
	ChatID     int64  `json:"chat_id"`
	IsChannel  bool   `json:"is_channel,omitempty"`
	AccessHash int64  `json:"access_hash"`
	MsgID      int    `json:"msg_id"`
	Time       int64  `json:"time"`
}

type Plugin struct {
	restartStatePath string
	restartFunc      func(state RestartState) error
	cmdRunner        func(ctx context.Context, name string, args ...string) ([]byte, error)
	runner           process.Runner
	startTime        time.Time
	metrics          core.MetricsCollector
}

func New() *Plugin {
	return &Plugin{restartStatePath: "data/restart.json", startTime: time.Now(), runner: process.NewOSRunner(3, 60*time.Second, 2*1024*1024)}
}
func NewWithCustomRestart(statePath string, restartFn func(state RestartState) error) *Plugin {
	return &Plugin{restartStatePath: statePath, restartFunc: restartFn, runner: process.NewOSRunner(3, 60*time.Second, 2*1024*1024)}
}
func (p *Plugin) SetRunner(r process.Runner) {
	if r != nil {
		p.runner = r
	}
}
func (p *Plugin) SetMetrics(m core.MetricsCollector) { p.metrics = m }
func (p *Plugin) SetCmdRunner(fn func(ctx context.Context, name string, args ...string) ([]byte, error)) {
	p.cmdRunner = fn
}
func (p *Plugin) runCmd(ctx context.Context, name string, args ...string) ([]byte, error) {
	if p.cmdRunner != nil {
		return p.cmdRunner(ctx, name, args...)
	}
	if p.runner == nil {
		p.runner = process.NewOSRunner(3, 60*time.Second, 2*1024*1024)
	}
	res, err := p.runner.Run(ctx, process.Request{
		Command: name,
		Args:    args,
		Timeout: 60 * time.Second,
	})
	if res != nil {
		return []byte(res.Combined), err
	}
	return nil, err
}
func (p *Plugin) Name() string { return "system" }
func (p *Plugin) Metadata() plugin.Metadata {
	return plugin.Metadata{Name: "system", Version: "1.0.0", Author: "GoUltroid Team", Description: "Shell command execution and userbot lifecycle management"}
}
func (p *Plugin) Description() string {
	return "Shell command execution and userbot lifecycle management"
}
func (p *Plugin) Init() error     { return nil }
func (p *Plugin) Shutdown() error { return nil }

func (p *Plugin) Capabilities() []execution.Capability {
	return []execution.Capability{
		{
			ID:          "system",
			Name:        "System Management",
			Description: "Host health, update, and process lifecycle",
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
		},
	}
}

func (p *Plugin) Commands() []core.Command {
	sysSurfaces := execution.SurfaceUserbot | execution.SurfaceAssistant
	return []core.Command{
		{Name: "exec", Aliases: []string{"sh", "bash", "cmd"}, Description: "Execute a shell command on the host machine (Owner Only)", Usage: ".exec <shell command>", Category: "System", Permission: core.PermissionOwner, Timeout: 65 * time.Second, Surfaces: execution.SurfaceUserbot, Handler: p.handleExec},
		{Name: "restart", Description: "Restart the GoUltroid userbot process (Owner Only)", Usage: ".restart", Category: "System", Permission: core.PermissionOwner, Surfaces: sysSurfaces, Handler: p.handleRestart},
		{Name: "update", Aliases: []string{"gitupdate"}, Description: "Check for updates from git remote or pull and rebuild (Owner Only)", Usage: ".update [pull|now]", Category: "System", Permission: core.PermissionOwner, Timeout: 180 * time.Second, Surfaces: sysSurfaces, Handler: p.handleUpdate},
		{Name: "health", Aliases: []string{"runtime", "memstats"}, Description: "Show runtime memory and goroutine health statistics (Owner Only)", Usage: ".health", Category: "System", Permission: core.PermissionOwner, Surfaces: sysSurfaces, Handler: p.handleHealth},
	}
}

func (p *Plugin) handleExec(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		return ctx.EditOrReply("⚠️ <b>Usage:</b> <code>.exec &lt;command&gt; [args...]</code>\nExample: <code>.exec ls -la</code>")
	}
	commandStr := strings.Join(ctx.Args, " ")
	_ = ctx.EditOrReply("⏳ <i>Executing command...</i>")
	if p.runner == nil {
		p.runner = process.NewOSRunner(3, 60*time.Second, 2*1024*1024)
	}
	// P0-02: Use direct argv execution instead of shell. The router already
	// handles quoted arguments, so ctx.Args is already properly split.
	cmdName := ctx.Args[0]
	cmdArgs := []string{}
	if len(ctx.Args) > 1 {
		cmdArgs = ctx.Args[1:]
	}
	res, err := p.runner.Run(ctx.Ctx, process.Request{Command: cmdName, Args: cmdArgs, Timeout: 60 * time.Second})
	var output string
	var elapsed time.Duration
	if res != nil {
		output = res.Combined
		elapsed = res.Duration
		if res.Truncated {
			output += "\n\n[output truncated after 2MB]"
		}
	}
	if output == "" {
		if err != nil {
			output = fmt.Sprintf("Error: %v", err)
		} else {
			output = "(no output)"
		}
	}
	if len(output) <= 3500 {
		var sb strings.Builder
		sb.WriteString("💻 <b>Shell Execution</b>\n\n")
		sb.WriteString(fmt.Sprintf("• <b>Command:</b> <code>%s</code>\n", escapeHTML(commandStr)))
		sb.WriteString(fmt.Sprintf("• <b>Duration:</b> <i>%s</i>\n\n", elapsed.Round(time.Millisecond)))
		sb.WriteString(fmt.Sprintf("<pre><code class=\"language-bash\">%s</code></pre>", escapeHTML(output)))
		return ctx.EditOrReply(sb.String())
	}
	tmpFile, tmpErr := os.CreateTemp("", "exec-output-*.txt")
	if tmpErr != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to create temp file for large output: %v", tmpErr))
	}
	defer os.Remove(tmpFile.Name())
	_, _ = tmpFile.WriteString(fmt.Sprintf("Command: %s\nDuration: %s\n\nOutput:\n%s", commandStr, elapsed, output))
	_ = tmpFile.Close()
	caption := fmt.Sprintf("📄 <b>Execution Output</b> (<code>%s</code>, took <i>%s</i>)", escapeHTML(commandStr), elapsed.Round(time.Millisecond))
	return ctx.SendFile(tmpFile.Name(), caption)
}

func (p *Plugin) handleRestart(ctx *core.Context) error {
	_ = ctx.EditOrReply("🔄 <i>Restarting GoUltroid...</i>")
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
	state := RestartState{PeerType: peerType, ChatID: chatID, IsChannel: isChannel, AccessHash: accessHash, MsgID: msgID, Time: time.Now().Unix()}
	if p.restartFunc != nil {
		return p.restartFunc(state)
	}
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
	// P1-12: Prefer known installed artifact over os.Executable() which may
	// point to a temp binary when running via `go run`.
	execPath := filepath.Join("bin", "goultroid")
	if _, err := os.Stat(execPath); err != nil {
		if p, err := os.Executable(); err == nil {
			execPath = p
		} else {
			os.Exit(0)
			return nil
		}
	} else {
		if abs, err := filepath.Abs(execPath); err == nil {
			execPath = abs
		}
	}
	_ = syscall.Exec(execPath, os.Args, os.Environ())
	os.Exit(0)
	return nil
}

func (p *Plugin) handleUpdate(ctx *core.Context) error {
	isPull := len(ctx.Args) > 0 && (strings.ToLower(ctx.Args[0]) == "pull" || strings.ToLower(ctx.Args[0]) == "now")
	if !isPull {
		_ = ctx.EditOrReply("🔍 <i>Checking for updates from git remote...</i>")
		fetchCtx, cancel := context.WithTimeout(ctx.Ctx, 30*time.Second)
		defer cancel()
		if out, err := p.runCmd(fetchCtx, "git", "fetch"); err != nil {
			_ = ctx.EditOrReply(fmt.Sprintf("❌ <code>git fetch</code> failed: %v\n<pre>%s</pre>", err, escapeHTML(string(out))))
			return err
		}
		currHashOut, _ := p.runCmd(fetchCtx, "git", "rev-parse", "--short", "HEAD")
		currHash := strings.TrimSpace(string(currHashOut))
		logOut, err := p.runCmd(fetchCtx, "git", "log", "HEAD..origin/main", "--oneline")
		if err != nil {
			logOut, err = p.runCmd(fetchCtx, "git", "log", "HEAD..@{u}", "--oneline")
		}
		commits := strings.TrimSpace(string(logOut))
		if err != nil || commits == "" {
			return ctx.EditOrReply(fmt.Sprintf("✨ <b>GoUltroid is already up to date!</b>\n• <b>Commit:</b> <code>%s</code>", currHash))
		}
		commitLines := strings.Split(commits, "\n")
		var sb strings.Builder
		sb.WriteString("🔄 <b>New updates available!</b>\n")
		sb.WriteString(fmt.Sprintf("• <b>Current Commit:</b> <code>%s</code>\n", currHash))
		sb.WriteString(fmt.Sprintf("• <b>Pending Commits (%d):</b>\n", len(commitLines)))
		sb.WriteString(fmt.Sprintf("<pre>%s</pre>\n\n", escapeHTML(commits)))
		sb.WriteString("💡 <i>Run <code>.update pull</code> or <code>.update now</code> to pull changes, rebuild, and restart.</i>")
		return ctx.EditOrReply(sb.String())
	}
	_ = ctx.EditOrReply("⬇️ <i>Pulling latest updates from git...</i>")
	pullCtx, cancelPull := context.WithTimeout(ctx.Ctx, 60*time.Second)
	defer cancelPull()
	statusOut, _ := p.runCmd(pullCtx, "git", "status", "--porcelain")
	if strings.TrimSpace(string(statusOut)) != "" {
		_ = ctx.EditOrReply("❌ Cannot update: working directory has uncommitted modifications. Stash or commit your changes first.")
		return errors.New("dirty working tree")
	}
	if out, err := p.runCmd(pullCtx, "git", "pull", "--ff-only"); err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ <code>git pull --ff-only</code> failed: %v\n<pre>%s</pre>", err, escapeHTML(string(out))))
		return err
	}
	_ = ctx.EditOrReply("🔨 <i>Rebuilding GoUltroid binary...</i>")
	buildCtx, cancelBuild := context.WithTimeout(ctx.Ctx, 120*time.Second)
	defer cancelBuild()
	tmpBin := filepath.Join("bin", "goultroid.tmp")
	if out, err := p.runCmd(buildCtx, "go", "build", "-o", tmpBin, "./cmd/goultroid"); err != nil {
		_ = os.Remove(tmpBin)
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Rebuild failed: %v\n<pre>%s</pre>", err, escapeHTML(string(out))))
		return err
	}
	finalBin := filepath.Join("bin", "goultroid")
	if err := os.Rename(tmpBin, finalBin); err != nil {
		_ = os.Remove(tmpBin)
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to replace binary: %v", err))
		return err
	}
	_ = ctx.EditOrReply("✅ <i>Rebuild successful! Restarting GoUltroid...</i>")
	return p.handleRestart(ctx)
}

func escapeHTML(s string) string { return core.EscapeHTML(s) }

func (p *Plugin) handleHealth(ctx *core.Context) error {
	stats := core.GatherHealth(p.startTime)
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
	msg := fmt.Sprintf("🔧 <b>GoUltroid Runtime Health</b>\n\n"+"⏱️ <b>Uptime:</b> <code>%s</code>\n"+"🧵 <b>Goroutines:</b> <code>%d</code>\n"+"💾 <b>Heap Alloc:</b> <code>%.2f MB</code>\n"+"🖥️ <b>Sys Memory:</b> <code>%.2f MB</code>\n"+"♻️ <b>GC Cycles:</b> <code>%d</code>\n"+"⏸️ <b>GC Pause Total:</b> <code>%.2f ms</code>\n"+"🔢 <b>Go Version:</b> <code>%s</code>", uptimeStr, stats.Goroutines, stats.HeapAllocMB, stats.SysMB, stats.NumGC, stats.PauseTotalMs, runtime.Version())
	if p.metrics != nil {
		snap := p.metrics.Snapshot()
		msg += fmt.Sprintf("\n\n📊 <b>Operational Telemetry</b>\n"+"• <b>Commands:</b> <code>%d</code> (errors: <code>%d</code>)\n"+"• <b>Scheduled Jobs:</b> <code>%d</code> (failed: <code>%d</code>)\n"+"• <b>Telegram Reqs:</b> <code>%d</code> (errors: <code>%d</code>)", snap.TotalCommands, snap.TotalErrors, snap.SchedulerJobsRun, snap.SchedulerJobsFail, snap.TelegramRequests, snap.TelegramErrors)
	}
	return ctx.EditOrReply(msg)
}

func buildSanitizedEnv() []string { return process.SanitizeEnv(nil) }
