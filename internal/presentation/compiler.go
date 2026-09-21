package presentation

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/interaction"
)

type CompiledButton struct {
	Text string
	Data []byte
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
	out := CompiledView{Text: view.Text, Rows: make([]CompiledRow, 0, len(view.Rows))}
	for _, row := range view.Rows {
		compiled := make(CompiledRow, 0, len(row))
		for _, button := range row {
			data, err := c.sessions.CallbackData(ctx, sessionID, button.ActionID)
			if err != nil {
				return CompiledView{}, err
			}
			compiled = append(compiled, CompiledButton{Text: button.Text, Data: data})
		}
		out.Rows = append(out.Rows, compiled)
	}
	return out, nil
}
