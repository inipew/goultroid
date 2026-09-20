package savedresponse

import (
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inipew/goultroid/internal/core"
)

const (
	MaxTemplateBytes      = 16 << 10
	DefaultMaxOutputRunes = 4096
	MaxVariableRunes      = 512
	MaxTemplateTokens     = 128
)

var (
	ErrTemplateTooLarge  = errors.New("saved response template too large")
	ErrRenderedTooLarge  = errors.New("rendered saved response too large")
	ErrTooManyTokens     = errors.New("saved response template has too many tokens")
	ErrUnsupportedFormat = errors.New("unsupported saved response format")
)

type TemplateVars struct {
	Name     string
	First    string
	Last     string
	Username string
	UserID   int64
	Chat     string
	ChatID   int64
	Now      time.Time
}

func VarsFromContext(ctx *core.Context, now time.Time) TemplateVars {
	var vars TemplateVars
	vars.Now = now
	if ctx == nil {
		return vars
	}
	if ctx.Sender != nil {
		vars.First = ctx.Sender.FirstName
		vars.Last = ctx.Sender.LastName
		vars.Name = strings.TrimSpace(ctx.Sender.FirstName + " " + ctx.Sender.LastName)
		if vars.Name == "" {
			vars.Name = ctx.Sender.Username
		}
		vars.Username = ctx.Sender.Username
		vars.UserID = ctx.Sender.ID
	}
	if ctx.Chat != nil {
		vars.Chat = ctx.Chat.Title
		if vars.Chat == "" {
			vars.Chat = ctx.Chat.Username
		}
		vars.ChatID = ctx.Chat.ID
	}
	return vars
}

func VarsFromEnvelope(message *core.MessageEnvelope, now time.Time) TemplateVars {
	var vars TemplateVars
	vars.Now = now
	if message == nil {
		return vars
	}
	vars.First = message.Sender.FirstName
	vars.Last = message.Sender.LastName
	vars.Name = message.SenderName()
	vars.Username = message.Sender.Username
	vars.UserID = message.Sender.ID
	vars.Chat = message.Chat.Title
	if vars.Chat == "" {
		vars.Chat = message.Chat.Username
	}
	vars.ChatID = message.ChatID
	return vars
}

func Validate(response Response) error {
	if len(response.Text) > MaxTemplateBytes {
		return fmt.Errorf("%w: max %d bytes", ErrTemplateTooLarge, MaxTemplateBytes)
	}
	format := response.EffectiveFormat()
	if format != FormatHTML && format != FormatPlain {
		return fmt.Errorf("%w: %q", ErrUnsupportedFormat, format)
	}
	tokens := 0
	for i := 0; i < len(response.Text); {
		start := strings.IndexByte(response.Text[i:], '{')
		if start < 0 {
			break
		}
		start += i
		end := strings.IndexByte(response.Text[start:], '}')
		if end <= 1 {
			i = start + 1
			continue
		}
		tokens++
		if tokens > MaxTemplateTokens {
			return fmt.Errorf("%w: max %d tokens", ErrTooManyTokens, MaxTemplateTokens)
		}
		i = start + end + 1
	}
	return nil
}

func Render(response Response, vars TemplateVars, maxRunes int) (string, error) {
	if err := Validate(response); err != nil {
		return "", err
	}
	format := response.EffectiveFormat()
	switch format {
	case FormatHTML:
		return renderTemplate(response.Text, vars, maxRunes, false)
	case FormatPlain:
		return renderTemplate(response.Text, vars, maxRunes, true)
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedFormat, format)
	}
}

func RenderHTML(template string, vars TemplateVars, maxRunes int) (string, error) {
	return Render(NewHTML(template), vars, maxRunes)
}

func RenderPlain(template string, vars TemplateVars, maxRunes int) (string, error) {
	return Render(NewPlainText(template), vars, maxRunes)
}

func renderTemplate(template string, vars TemplateVars, maxRunes int, escapeLiteral bool) (string, error) {
	if len(template) > MaxTemplateBytes {
		return "", fmt.Errorf("%w: max %d bytes", ErrTemplateTooLarge, MaxTemplateBytes)
	}
	if maxRunes <= 0 {
		maxRunes = DefaultMaxOutputRunes
	}
	if vars.Now.IsZero() {
		vars.Now = time.Now()
	}

	var out strings.Builder
	out.Grow(min(len(template)+64, MaxTemplateBytes))
	writtenRunes := 0
	tokenCount := 0

	write := func(value string) error {
		count := utf8.RuneCountInString(value)
		if count > maxRunes-writtenRunes {
			return fmt.Errorf("%w: max %d characters", ErrRenderedTooLarge, maxRunes)
		}
		out.WriteString(value)
		writtenRunes += count
		return nil
	}
	writeLiteral := func(value string) error {
		if escapeLiteral {
			value = html.EscapeString(value)
		}
		return write(value)
	}

	for i := 0; i < len(template); {
		if template[i] != '{' {
			next := strings.IndexByte(template[i:], '{')
			if next < 0 {
				if err := writeLiteral(template[i:]); err != nil {
					return "", err
				}
				break
			}
			if err := writeLiteral(template[i : i+next]); err != nil {
				return "", err
			}
			i += next
			continue
		}

		end := strings.IndexByte(template[i:], '}')
		if end <= 1 {
			if err := writeLiteral(template[i : i+1]); err != nil {
				return "", err
			}
			i++
			continue
		}
		end += i
		tokenCount++
		if tokenCount > MaxTemplateTokens {
			return "", fmt.Errorf("%w: max %d tokens", ErrTooManyTokens, MaxTemplateTokens)
		}

		token := template[i+1 : end]
		value, ok := renderToken(token, vars)
		if !ok {
			if err := writeLiteral(template[i : end+1]); err != nil {
				return "", err
			}
		} else if err := write(value); err != nil {
			return "", err
		}
		i = end + 1
	}
	return out.String(), nil
}

func renderToken(token string, vars TemplateVars) (string, bool) {
	switch token {
	case "name":
		return escapeVar(vars.Name), true
	case "first":
		return escapeVar(vars.First), true
	case "last":
		return escapeVar(vars.Last), true
	case "username":
		username := strings.TrimPrefix(strings.TrimSpace(vars.Username), "@")
		if username == "" {
			return "", true
		}
		return "@" + escapeVar(username), true
	case "mention":
		label := vars.Name
		if strings.TrimSpace(label) == "" {
			label = vars.Username
		}
		if strings.TrimSpace(label) == "" && vars.UserID != 0 {
			label = strconv.FormatInt(vars.UserID, 10)
		}
		if vars.UserID == 0 {
			return escapeVar(label), true
		}
		return fmt.Sprintf("<a href=\"tg://user?id=%d\">%s</a>", vars.UserID, escapeVar(label)), true
	case "id":
		if vars.UserID == 0 {
			return "", true
		}
		return strconv.FormatInt(vars.UserID, 10), true
	case "chat":
		return escapeVar(vars.Chat), true
	case "chat_id":
		if vars.ChatID == 0 {
			return "", true
		}
		return strconv.FormatInt(vars.ChatID, 10), true
	case "date":
		return vars.Now.Format("2006-01-02"), true
	case "time":
		return vars.Now.Format("15:04:05"), true
	default:
		return "", false
	}
}

func escapeVar(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > MaxVariableRunes {
		runes = runes[:MaxVariableRunes]
	}
	return html.EscapeString(string(runes))
}
