package fun

import (
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"unicode"

	"github.com/inipew/goultroid/internal/core"
)

// Plugin provides fun and casual utility commands.
type Plugin struct{}

// New creates a new Fun plugin instance.
func New() *Plugin {
	return &Plugin{}
}

func (p *Plugin) Name() string {
	return "fun"
}

func (p *Plugin) Init() error {
	return nil
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "roll",
			Aliases:     []string{"dice"},
			Description: "Roll a random number or dice (e.g. .roll or .roll 20)",
			Usage:       ".roll [max]",
			Category:    "Fun",
			Permission:  core.PermissionSudo,
			Handler:     p.handleRoll,
		},
		{
			Name:        "shrug",
			Description: "Send shrug expression ¯\\_(ツ)_/¯",
			Usage:       ".shrug",
			Category:    "Fun",
			Permission:  core.PermissionSudo,
			Handler:     p.handleShrug,
		},
		{
			Name:        "tableflip",
			Description: "Send tableflip expression (╯°□°)╯︵ ┻━┻",
			Usage:       ".tableflip",
			Category:    "Fun",
			Permission:  core.PermissionSudo,
			Handler:     p.handleTableflip,
		},
		{
			Name:        "unflip",
			Description: "Send unflip expression ┬─┬ノ( º _ ºノ)",
			Usage:       ".unflip",
			Category:    "Fun",
			Permission:  core.PermissionSudo,
			Handler:     p.handleUnflip,
		},
		{
			Name:        "mock",
			Description: "Transform text into alternating mocking SpongeBob case",
			Usage:       ".mock <text> or reply to a message with .mock",
			Category:    "Fun",
			Permission:  core.PermissionSudo,
			Handler:     p.handleMock,
		},
	}
}

func sendOrEdit(ctx *core.Context, text string) error {
	if ctx.Message != nil && ctx.Message.ID != 0 {
		if err := ctx.Edit(text); err == nil {
			return nil
		}
	}
	return ctx.Reply(text)
}

func (p *Plugin) handleRoll(ctx *core.Context) error {
	max := 6
	if len(ctx.Args) > 0 {
		if n, err := strconv.Atoi(ctx.Args[0]); err == nil && n > 1 {
			max = n
		}
	}

	res := rand.Intn(max) + 1
	var diceVisual string
	if max == 6 {
		diceVisuals := []string{"⚀", "⚁", "⚂", "⚃", "⚄", "⚅"}
		diceVisual = diceVisuals[res-1] + " "
	}

	text := fmt.Sprintf("🎲 %sYou rolled a <b>%d</b> (1-%d)!", diceVisual, res, max)
	return sendOrEdit(ctx, text)
}

func (p *Plugin) handleShrug(ctx *core.Context) error {
	return sendOrEdit(ctx, `¯\_(ツ)_/¯`)
}

func (p *Plugin) handleTableflip(ctx *core.Context) error {
	return sendOrEdit(ctx, `(╯°□°)╯︵ ┻━┻`)
}

func (p *Plugin) handleUnflip(ctx *core.Context) error {
	return sendOrEdit(ctx, `┬─┬ノ( º _ ºノ)`)
}

func (p *Plugin) handleMock(ctx *core.Context) error {
	var input string
	if len(ctx.Args) > 0 {
		input = strings.TrimSpace(ctx.RawArgs)
	} else {
		reply, err := ctx.GetReply()
		if err != nil || reply == nil || reply.Text == "" {
			_ = ctx.Reply("⚠️ Usage: <code>.mock &lt;text&gt;</code> or reply to a message with <code>.mock</code>")
			return errors.New("missing text to mock")
		}
		input = reply.Text
	}

	mocked := mockText(input)
	return sendOrEdit(ctx, mocked)
}

func mockText(s string) string {
	var sb strings.Builder
	upper := false
	for _, r := range s {
		if unicode.IsLetter(r) {
			if upper {
				sb.WriteRune(unicode.ToUpper(r))
			} else {
				sb.WriteRune(unicode.ToLower(r))
			}
			upper = !upper
		} else {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}
