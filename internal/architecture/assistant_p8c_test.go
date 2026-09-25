package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP8CCalculatorUsesCanonicalInlineAndA2Runtime(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "plugins", "calculator", "calculator.go"): {
			"Surfaces:    execution.SurfaceUserbot",
			"Kind:        feature.InteractionInline",
			"Kind:        feature.InteractionAction",
			"InteractionState: []byte(expression)",
			"ActionRows:       view.Rows",
			"rt.Engine.RegisterAction(",
			"ctx.Transition(",
			"p.renderer.Render(",
			"selfinline.FallbackSafe(",
			"handleNativeCommand",
			"evaluateExpression(expression)",
		},
		filepath.Join(root, "plugins", "calculator", "calculator_test.go"): {
			"TestCalculatorCommandFallsBackToNativeWithoutRenderer",
			"TestCalculatorCommandFallsBackAfterSafeSelfInlineFailure",
			"TestCalculatorCommandSendStageFailureDoesNotEmitNativeDuplicate",
			"TestCalculatorCommandWithoutExpressionPrefersSelfInlineKeypad",
			"TestCalculatorTypedCallbackUsesSharedSessionRevisionAndActorBinding",
			"rootinteraction.ErrStaleToken",
			"rootinteraction.ErrBindingMismatch",
		},
		filepath.Join(root, "plugins", "calculator", "module.go"): {
			"plugin.CapTelegramRead",
			"plugin.CapTelegramSendMessage",
		},
		filepath.Join(root, "internal", "app", "selfinline_features.go"): {
			"selfinline.Authorized(",
			"gate.Check(pluginID, plugin.CapTelegramRead)",
			"gate.Check(pluginID, plugin.CapTelegramSendMessage)",
		},
		filepath.Join(root, "internal", "assistant", "client", "interaction_drivers.go"): {
			"case presentationtelegram.InlineTarget:",
			"execution.SourceInline",
		},
		filepath.Join(root, "internal", "plugin", "features.go"): {
			"registry.actions.UnregisterScope(scope)",
			"registry.interactions.CancelScope(scope)",
		},
		filepath.Join(root, "internal", "interaction", "runtime_callback_test.go"): {
			"TestCallbackRevisionRejectsOldButtons",
			"TestResolveCallbackClaimsFirstConcreteInlineTarget",
		},
		filepath.Join(root, "internal", "app", "generated_modules.go"): {
			"calculator.Module",
		},
	}
	for path, required := range checks {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, invariant := range required {
			if !strings.Contains(source, invariant) {
				t.Errorf("P8-C invariant missing from %s: %q", path, invariant)
			}
		}
	}
}

func TestP8CCalculatorAddsNoEvalOrRetainedWorkerState(t *testing.T) {
	root := repositoryRoot(t)
	for _, rel := range []string{
		filepath.Join("plugins", "calculator", "calculator.go"),
		filepath.Join("plugins", "calculator", "evaluator.go"),
	} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, forbidden := range []string{
			"os/exec",
			"go/parser",
			"go func(",
			"time.NewTicker(",
			"time.Tick(",
			"time.AfterFunc(",
			"map[string]",
			"eval(",
		} {
			if strings.Contains(source, forbidden) {
				t.Errorf("P8-C calculator contains forbidden mechanism %q in %s", forbidden, rel)
			}
		}
	}
}
