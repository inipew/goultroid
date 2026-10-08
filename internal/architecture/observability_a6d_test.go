package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"
)

// This inventory makes the final A6 privacy and lifecycle proofs explicit.
// Each test is executed by its owning package during go test ./...; this
// architecture gate prevents silent deletion/rename of the acceptance proof.
func TestA6DObservabilityAcceptanceInventory(t *testing.T) {
	root := filepath.Join("..", "..")
	proofs := map[string][]string{
		"internal/telegram/dispatcher_privacy_a6_test.go": {
			"TestA6MalformedCommandDoesNotLogPrivateMessageContent",
			"TestA6MessageHookErrorAndPanicAreRedactedWithoutChangingPolicy",
			"TestA6MessageHookStateGatePanicDoesNotLogPayload",
		},
		"internal/telegram/dispatcher_decision_failure_policy_test.go": {
			"TestDecisionHandlerInfrastructureFailureUsesRegisteredPolicy",
			"TestDecisionHandlerExecutionFailureUsesSameRegisteredPolicy",
		},
		"internal/telegram/dispatcher_observability_a6d_test.go": {
			"TestA6DDecisionAdmissionFailureIsPrivateAndRetainsPolicy",
			"TestA6DObserverAdmissionFailureIsPrivateAndNonBlocking",
		},
		"internal/platform/audit/audit_test.go": {
			"TestA6AuditDetailsAreSafeBoundedAndImmutable",
		},
		"internal/platform/process/manager_test.go": {
			"TestA6ProcessAuditNeverCapturesArgumentsOrOwnerText",
		},
		"internal/platform/secret/manager_test.go": {
			"TestA6SecretAuditDoesNotRetainRedactedCredentialFragments",
		},
		"internal/services/pmpermit/privacy_events_a6_test.go": {
			"TestA6PMPermitEventRedactsReasonsAndTelegramErrors",
		},
		"internal/services/userlog/privacy_a6_test.go": {
			"TestA6UserLogDeliveryErrorsNeverLeakToHealthOrStructuredLogs",
		},
		"internal/services/userlog/destination_categories_a6_test.go": {
			"TestA6C2UserLogClearFailureDoesNotSilentlyReenableAfterRestart",
			"TestA6C2UserLogCategoryTogglesAndDestinationValidation",
		},
		"plugins/userlog/userlog_test.go": {
			"TestUserLogLifecycleRestartRebindsEventBus",
			"TestUserLogWorkerStartsLazilyAndRetiresWhenIdle",
			"TestUserLogPlugin_HandleIncomingMessage",
		},
		"plugins/userlog/lifecycle_a6_test.go": {
			"TestA6UserLogFullQueueShutdownCancelsDeliveryWithoutDrainingRPCs",
		},
		"plugins/userlog/destination_a6_test.go": {
			"TestA6C2UserLogRestartPrimesDestinationBeforeMessageAdmission",
			"TestA6C2UserLogFailsActivationWhenDestinationCannotBeLoaded",
		},
		"plugins/blacklist/native_authorization_a5_test.go": {
			"TestA5FinalBlacklistFreshRoleIsCheckedInsideMutationLock",
		},
		"plugins/filters/durable_restart_test.go": {
			"TestD4FiltersConfirmedRemovalSurvivesDurableRestart",
		},
	}
	for rel, required := range proofs {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, 0)
		if err != nil {
			t.Errorf("%s: required owner test missing or unparseable: %v", rel, err)
			continue
		}
		found := make(map[string]bool)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && fn.Name != nil {
				found[fn.Name.Name] = true
			}
		}
		for _, name := range required {
			if !found[name] {
				t.Errorf("%s: required acceptance test %s missing", rel, strconv.Quote(name))
			}
		}
	}
}

// A6 diagnostics must never regress to zap.Error or zap.Any in the Telegram
// ingress/decision path or UserLog transport. These errors can carry PM text.
func TestA6DObservabilityRawLoggerFence(t *testing.T) {
	root := filepath.Join("..", "..")
	files := []string{
		"internal/telegram/dispatcher_dispatch.go",
		"internal/telegram/dispatcher_handlers.go",
		"internal/services/userlog/service.go",
	}
	for _, rel := range files {
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, rel), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if ok && pkg.Name == "zap" {
				switch selector.Sel.Name {
				case "Error", "Any":
					t.Errorf("%s: unsafe zap.%s in ingress/observer diagnostics", rel, selector.Sel.Name)
				case "String":
					if len(call.Args) < 1 {
						break
					}
					key, ok := call.Args[0].(*ast.BasicLit)
					if !ok || key.Kind != token.STRING {
						break
					}
					name, err := strconv.Unquote(key.Value)
					if err != nil {
						break
					}
					if name == "text" || name == "panic" || name == "error" {
						t.Errorf("%s: raw diagnostic field %q is forbidden", rel, name)
					}
				}
			}
			return true
		})
	}
}
