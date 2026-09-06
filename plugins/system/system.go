package system

import (
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
	"github.com/inipew/goultroid/internal/services/process"
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
	runner           process.Runner
	startTime        time.Time
	metrics          core.MetricsCollector
}

// New creates a new System plugin.
func New() *Plugin {
	return &Plugin{
		restartStatePath: "data/restart.json",
		startTime:        time.Now(),
		runner:           process.NewOSRunner(3, 60*time.Second, 2*1024*1024),
	}
}

// NewWithCustomRestart creates a System plugin with a custom restart handler (useful for testing).
func NewWithCustomRestart(statePath string, restartFn func(state RestartState) error) *Plugin {
	return &Plugin{
		restartStatePath: statePath,
		restartFunc:      restartFn,
		runner:           process.NewOSRunner(3, 60*time.Second, 2*1024*1024),
	}
}

// SetRunner overrides the process.Runner used for executing shell commands.
func (p *Plugin) SetRunner(r process.Runner) {
	if r != nil {
		p.runner = r
	}
}

// SetMetrics configures an optional MetricsCollector for runtime health reporting.
func (p *Plugin) SetMetrics(m core.MetricsCollector) {
	p.metrics = m
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

// Description returns a short summary of the system plugin.
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

// handleExec executes a command with timeout and formats the result.
func (p *Plugin) handleExec(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		return ctx.EditOrReply("⚠️ <b>Usage:</b> <code>.exec &lt;shell command&gt;</code>")
	}

	commandStr := strings.Join(ctx.Args, " ")
	_ = ctx.EditOrReply("⏳ <i>Executing command...</i>")

	if p.runner == nil {
		p.runner = process.NewOSRunner(3, 60*time.Second, 2*1024*1024)
	}

	// Shell execution is intentionally retained here as a compatibility
	// boundary for the .exec command. The process runner itself owns the
	// hardening, output limits, timeout and process-group lifecycle.
	res, err := p.runner.Run(ctx.Ctx, process.Request{
		Command: commandStr,
		Shell:   true,
		Timeout: 60 * time.Second,
	})

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

	// If output is too large, upload as a text file.
	tmpFile, tmpErr := os.CreateTemp("", "exec-output-*.txt")
	if tmpErr != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to create temp file for large output: %v", tmpErr))
	}
	defer os.Remove(tmpFile.Name())

	_, writeErr := tmpFile.WriteString(fmt.Sprintf("Command: %s\nDuration: %s\n\nOutput:\n%s", commandStr, elapsed, output))
	closeErr := tmpFile.Close()
	if writeErr != nil || closeErr != nil {
		if writeErr != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ Failed to write execution output: %v", writeErr))
		}
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to close execution output: %v", closeErr))
	}

	caption := fmt.Sprintf("📄 <b>Execution Output</b> (<code>%s</code>, took <i>%s</i>)", escapeHTML(commandStr), elapsed.Round(time.Millisecond))
	return ctx.SendFile(tmpFile.Name(), caption)
}

// handleRestart initiates a graceful restart of the bot.
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

	if p.restartStatePath == "" {
		p.restartStatePath = "data/restart.json"
	}
	if dir := filepath.Dir(p.restartStatePath); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create restart state directory: %w", err)
		}
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode restart state: %w", err)
	}
	if err := os.WriteFile(p.restartStatePath, data, 0o600); err != nil {
		return fmt.Errorf("write restart state: %w", err)
	}

	return nil
}

// handleUpdate checks git state and optionally performs an update.
func (p *Plugin) handleUpdate(ctx *core.Context) error {
	return p.handleGitUpdate(ctx)
}

// handleHealth reports process health.
func (p *Plugin) handleHealth(ctx *core.Context) error {
	return ctx.EditOrReply(fmt.Sprintf("🩺 <b>Runtime Health</b>\n\n• <b>Go:</b> %s\n• <b>Uptime:</b> %s", runtime.Version(), time.Since(p.startTime).Round(time.Second)))
}

// keep syscall referenced by the existing restart/update helpers in this file.
var _ = syscall.SIGTERM
var _ = errors.Is
