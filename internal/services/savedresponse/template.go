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
	maxTokenNameBytes     = 16
)

var (
	ErrTemplateTooLarge         = errors.New("saved response template too large")
	ErrRenderedTooLarge         = errors.New("rendered saved response too large")
	ErrTooManyTokens            = errors.New("saved response template has too many tokens")
	ErrUnsupportedFormat        = errors.New("unsupported saved response format")
	ErrNilTemplate              = errors.New("saved response compiled template is nil")
	ErrInvalidTemplateVariable = errors.New("invalid saved response template variable")
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
	// Extra supplies feature-local values accepted by CompileWithVariables.
	// Render treats the map as read-only and never retains it.
	Extra map[string]string
}

type templateToken uint8

const (
	tokenLiteral templateToken = iota
	tokenName
	tokenFirst
	tokenLast
	tokenUsername
	tokenMention
	tokenID
	tokenChat
	tokenChatID
	tokenDate
	tokenTime
	tokenExtra
)

type templatePart struct {
	literal string
	token   templateToken
	extra   string
}

// CompiledTemplate is an immutable parsed saved-response template.
// It can be rendered repeatedly without rescanning placeholder syntax.
type CompiledTemplate struct {
	format     Format
	parts      []templatePart
	tokenCount int
	usesClock  bool
}

func (t *CompiledTemplate) Format() Format {
	if t == nil {
		return ""
	}
	return t.format
}

func (t *CompiledTemplate) TokenCount() int {
	if t == nil {
		return 0
	}
	return t.tokenCount
}

func (t *CompiledTemplate) Variables() []string {
	if t == nil || t.tokenCount == 0 {
		return nil
	}
	seen := make(map[string]struct{}, t.tokenCount)
	variables := make([]string, 0, t.tokenCount)
	for _, part := range t.parts {
		if part.token == tokenLiteral {
			continue
		}
		name := part.extra
		if part.token != tokenExtra {
			name = templateTokenName(part.token)
		}
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		variables = append(variables, name)
	}
	return variables
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
	_, err := Compile(response)
	return err
}

func Compile(response Response) (*CompiledTemplate, error) {
	return compileTemplate(response, nil)
}

// CompileWithVariables extends one compiled template with explicitly allowed
// feature-local variables. Compile() remains unchanged, so unknown placeholders
// in persisted user templates continue to render literally.
func CompileWithVariables(response Response, variables ...string) (*CompiledTemplate, error) {
	if len(variables) == 0 {
		return Compile(response)
	}
	extra := make(map[string]struct{}, len(variables))
	for _, variable := range variables {
		if !validTemplateVariableName(variable) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidTemplateVariable, variable)
		}
		if _, reserved := parseTemplateToken(variable); reserved {
			return nil, fmt.Errorf("%w: %q is reserved", ErrInvalidTemplateVariable, variable)
		}
		extra[variable] = struct{}{}
	}
	return compileTemplate(response, extra)
}

func compileTemplate(response Response, extra map[string]struct{}) (*CompiledTemplate, error) {
	if len(response.Text) > MaxTemplateBytes {
		return nil, fmt.Errorf("%w: max %d bytes", ErrTemplateTooLarge, MaxTemplateBytes)
	}
	format := response.EffectiveFormat()
	if format != FormatHTML && format != FormatPlain {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedFormat, format)
	}

	compiled := &CompiledTemplate{
		format: format,
		parts:  make([]templatePart, 0, 8),
	}
	appendLiteral := func(value string) {
		if value == "" {
			return
		}
		if format == FormatPlain {
			value = html.EscapeString(value)
		}
		compiled.parts = append(compiled.parts, templatePart{literal: value})
	}

	source := response.Text
	literalStart := 0
	for i := 0; i < len(source); {
		if source[i] != '{' {
			i++
			continue
		}

		if name, consumed, ok := escapedTemplateVariableAt(source, i, extra); ok {
			appendLiteral(source[literalStart:i])
			appendLiteral("{" + name + "}")
			i += consumed
			literalStart = i
			continue
		}

		end, ok := templateTokenEnd(source, i)
		if !ok {
			i++
			continue
		}
		name := source[i+1 : end]
		token, known := parseTemplateToken(name)
		part := templatePart{token: token}
		if !known {
			if _, allowed := extra[name]; !allowed {
				i = end + 1
				continue
			}
			part.token = tokenExtra
			part.extra = name
		}

		appendLiteral(source[literalStart:i])
		compiled.parts = append(compiled.parts, part)
		compiled.tokenCount++
		if compiled.tokenCount > MaxTemplateTokens {
			return nil, fmt.Errorf("%w: max %d tokens", ErrTooManyTokens, MaxTemplateTokens)
		}
		if token == tokenDate || token == tokenTime {
			compiled.usesClock = true
		}
		i = end + 1
		literalStart = i
	}
	appendLiteral(source[literalStart:])
	return compiled, nil
}

func Render(response Response, vars TemplateVars, maxRunes int) (string, error) {
	compiled, err := Compile(response)
	if err != nil {
		return "", err
	}
	return compiled.Render(vars, maxRunes)
}

func RenderHTML(template string, vars TemplateVars, maxRunes int) (string, error) {
	return Render(NewHTML(template), vars, maxRunes)
}

func RenderPlain(template string, vars TemplateVars, maxRunes int) (string, error) {
	return Render(NewPlainText(template), vars, maxRunes)
}

func (t *CompiledTemplate) Render(vars TemplateVars, maxRunes int) (string, error) {
	if t == nil {
		return "", ErrNilTemplate
	}
	maxRunes = normalizeRenderLimit(maxRunes)
	if t.usesClock && vars.Now.IsZero() {
		vars.Now = time.Now()
	}

	var out strings.Builder
	out.Grow(min(MaxTemplateBytes, maxRunes+64))
	writtenRunes := 0
	write := func(value string) error {
		count := utf8.RuneCountInString(value)
		if count > maxRunes-writtenRunes {
			return fmt.Errorf("%w: max %d characters", ErrRenderedTooLarge, maxRunes)
		}
		out.WriteString(value)
		writtenRunes += count
		return nil
	}

	for _, part := range t.parts {
		if part.token == tokenLiteral {
			if err := write(part.literal); err != nil {
				return "", err
			}
			continue
		}
		value := ""
		if part.token == tokenExtra {
			value = escapeVar(vars.Extra[part.extra])
		} else {
			value = renderCompiledToken(part.token, vars)
		}
		if err := write(value); err != nil {
			return "", err
		}
	}
	return out.String(), nil
}

func normalizeRenderLimit(maxRunes int) int {
	if maxRunes <= 0 || maxRunes > DefaultMaxOutputRunes {
		return DefaultMaxOutputRunes
	}
	return maxRunes
}

func templateTokenEnd(source string, start int) (int, bool) {
	limit := min(len(source), start+1+maxTokenNameBytes+1)
	for i := start + 1; i < limit; i++ {
		if source[i] == '}' {
			return i, i > start+1
		}
		if source[i] == '{' {
			return 0, false
		}
	}
	return 0, false
}

func escapedTemplateVariableAt(source string, start int, extra map[string]struct{}) (string, int, bool) {
	if start+4 > len(source) || source[start] != '{' || source[start+1] != '{' {
		return "", 0, false
	}
	limit := min(len(source), start+2+maxTokenNameBytes+2)
	for end := start + 2; end+1 < limit; end++ {
		if source[end] != '}' || source[end+1] != '}' {
			continue
		}
		name := source[start+2 : end]
		if _, known := parseTemplateToken(name); known {
			return name, end + 2 - start, true
		}
		if _, allowed := extra[name]; allowed {
			return name, end + 2 - start, true
		}
		return "", 0, false
	}
	return "", 0, false
}

func validTemplateVariableName(name string) bool {
	if name == "" || len(name) > maxTokenNameBytes {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (i > 0 && c >= '0' && c <= '9') || (i > 0 && c == '_') {
			continue
		}
		return false
	}
	return true
}

func parseTemplateToken(name string) (templateToken, bool) {
	switch name {
	case "name":
		return tokenName, true
	case "first":
		return tokenFirst, true
	case "last":
		return tokenLast, true
	case "username":
		return tokenUsername, true
	case "mention":
		return tokenMention, true
	case "id":
		return tokenID, true
	case "chat":
		return tokenChat, true
	case "chat_id":
		return tokenChatID, true
	case "date":
		return tokenDate, true
	case "time":
		return tokenTime, true
	default:
		return tokenLiteral, false
	}
}

func templateTokenName(token templateToken) string {
	switch token {
	case tokenName:
		return "name"
	case tokenFirst:
		return "first"
	case tokenLast:
		return "last"
	case tokenUsername:
		return "username"
	case tokenMention:
		return "mention"
	case tokenID:
		return "id"
	case tokenChat:
		return "chat"
	case tokenChatID:
		return "chat_id"
	case tokenDate:
		return "date"
	case tokenTime:
		return "time"
	default:
		return ""
	}
}

func renderCompiledToken(token templateToken, vars TemplateVars) string {
	switch token {
	case tokenName:
		return escapeVar(vars.Name)
	case tokenFirst:
		return escapeVar(vars.First)
	case tokenLast:
		return escapeVar(vars.Last)
	case tokenUsername:
		username := strings.TrimPrefix(strings.TrimSpace(vars.Username), "@")
		if username == "" {
			return ""
		}
		return "@" + escapeVar(username)
	case tokenMention:
		label := vars.Name
		if strings.TrimSpace(label) == "" {
			label = vars.Username
		}
		if strings.TrimSpace(label) == "" && vars.UserID != 0 {
			label = strconv.FormatInt(vars.UserID, 10)
		}
		if vars.UserID == 0 {
			return escapeVar(label)
		}
		return fmt.Sprintf("<a href=\"tg://user?id=%d\">%s</a>", vars.UserID, escapeVar(label))
	case tokenID:
		if vars.UserID == 0 {
			return ""
		}
		return strconv.FormatInt(vars.UserID, 10)
	case tokenChat:
		return escapeVar(vars.Chat)
	case tokenChatID:
		if vars.ChatID == 0 {
			return ""
		}
		return strconv.FormatInt(vars.ChatID, 10)
	case tokenDate:
		return vars.Now.Format("2006-01-02")
	case tokenTime:
		return vars.Now.Format("15:04:05")
	default:
		return ""
	}
}

func escapeVar(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > MaxVariableRunes {
		runes = runes[:MaxVariableRunes]
	}
	return html.EscapeString(string(runes))
}
