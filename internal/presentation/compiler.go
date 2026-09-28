package presentation

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/interaction"
)

type CompiledButton struct {
	Type        ButtonType
	Text        string
	Data        []byte
	URL         string
	InlineQuery string
	SamePeer    bool
}

type CompiledRow []CompiledButton

type CompiledView struct {
	Text string
	Rows []CompiledRow
}

type Compiler struct {
	sessions *interaction.Runtime
}

func NewCompiler(sessions *interaction.Runtime) *Compiler {
	return &Compiler{sessions: sessions}
}

func (c *Compiler) Compile(ctx context.Context, sessionID string, view View) (CompiledView, error) {
	if c == nil || c.sessions == nil {
		return CompiledView{}, fmt.Errorf("%w: session runtime unavailable", ErrInvalidView)
	}
	if err := view.Validate(); err != nil {
		return CompiledView{}, err
	}
	rows, err := c.CompileRows(ctx, sessionID, view.Rows)
	if err != nil {
		return CompiledView{}, err
	}
	return CompiledView{Text: view.Text, Rows: rows}, nil
}

// CompileRows compiles semantic buttons without requiring message text.
// Only action buttons consume a2 callback tokens; URL and switch-inline buttons
// remain stateless transport metadata.
func (c *Compiler) CompileRows(ctx context.Context, sessionID string, rows []Row) ([]CompiledRow, error) {
	if c == nil || c.sessions == nil {
		return nil, fmt.Errorf("%w: session runtime unavailable", ErrInvalidView)
	}

	actionIDs := make([]string, 0)
	for _, row := range rows {
		if len(row) == 0 {
			return nil, ErrInvalidView
		}
		for _, button := range row {
			if err := button.Validate(); err != nil {
				return nil, err
			}
			if button.Type == ButtonAction {
				actionIDs = append(actionIDs, button.ActionID)
			}
		}
	}

	var callbackData [][]byte
	if len(actionIDs) > 0 {
		var err error
		callbackData, err = c.sessions.CallbackDataBatch(ctx, sessionID, actionIDs)
		if err != nil {
			return nil, err
		}
	}
	callbackIndex := 0
	out := make([]CompiledRow, 0, len(rows))
	for _, row := range rows {
		compiled := make(CompiledRow, 0, len(row))
		for _, button := range row {
			result := CompiledButton{
				Type:        button.Type,
				Text:        button.Text,
				URL:         button.URL,
				InlineQuery: button.InlineQuery,
				SamePeer:    button.SamePeer,
			}
			if button.Type == ButtonAction {
				result.Data = callbackData[callbackIndex]
				callbackIndex++
			}
			compiled = append(compiled, result)
		}
		out = append(out, compiled)
	}
	return out, nil
}
