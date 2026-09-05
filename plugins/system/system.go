package system

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
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
}

// New creates a new System plugin.
func New() *Plugin {
	return &Plugin{
		restartStatePath: "data/restart.json",
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
	}
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
	outBytes, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	output := string(outBytes)
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

	msgID := 0
	if ctx.Message != nil {
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

	// Save state to file
	if p.restartStatePath != "" {
		_ = os.MkdirAll(filepath.Dir(p.restartStatePath), 0755)
		data, _ := json.Marshal(state)
		_ = os.WriteFile(p.restartStatePath, data, 0644)
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

	if out, err := p.runCmd(pullCtx, "git", "pull"); err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ <code>git pull</code> failed: %v\n<pre>%s</pre>", err, escapeHTML(string(out))))
		return err
	}

	_ = ctx.Reply("🔨 <i>Rebuilding GoUltroid binary...</i>")

	buildCtx, cancelBuild := context.WithTimeout(ctx.Ctx, 120*time.Second)
	defer cancelBuild()

	if out, err := p.runCmd(buildCtx, "go", "build", "-o", "bin/goultroid", "./cmd/goultroid"); err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Rebuild failed: %v\n<pre>%s</pre>", err, escapeHTML(string(out))))
		return err
	}

	_ = ctx.Reply("✅ <i>Rebuild successful! Restarting GoUltroid...</i>")
	return p.handleRestart(ctx)
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
