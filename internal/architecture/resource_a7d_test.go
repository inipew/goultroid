package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// A7-D is a source-level inventory of the executable, owning-package gates.
// This test alone does not execute the proofs: go test -race ./... is required
// to validate their behavior. No duplicate integration runtime is introduced.
func TestA7DResourceAndRestartAcceptanceInventory(t *testing.T) {
	root := filepath.Join("..", "..")
	proofs := map[string][]string{
		"plugins/pmpermit/resource_a7_test.go": {
			"TestA7PMPermitSQLiteHighCardinalitySaturationAndRestart",
			"TestA7PMPermitSQLiteFloodWaitCancellationDoesNotSerializeUnrelatedPM",
		},
		"plugins/afk/resource_a7b_test.go": {
			"TestA7BAFKManagedConcurrentAutoUnAFKAndGenerationIsolatedWelcome",
			"TestA7BAFKSQLiteFailureKeepsActiveStateAndTaskRejectionHasNoFallback",
			"TestA7BAFKSQLiteInactiveFallbackPropagatesWriteFailure",
		},
		"internal/telegram/dispatcher_resource_a7c_test.go": {
			"TestA7CMixedObserverBurstIsolatedFromSecurityAndCallbackClaims",
		},
		"plugins/userlog/resource_a7c_test.go": {
			"TestA7CUserLogMixedChatBurstBoundedAndReloadSettles",
		},
		"internal/app/resource_a7c2_test.go": {
			"TestA7C2FourFeatureDurableRestartAndScopedCallbackPressure",
		},
		"internal/app/resource_a7c2_manager_test.go": {
			"TestA7C2ManagerReloadOneFeatureKeepsSiblingDurableCallbacks",
		},
		"internal/app/resource_a7d_test.go": {
			"TestA7DRepeatedFourFeatureDurableRestartAndSettling",
		},
		"internal/architecture/observability_a6d_test.go": {
			"TestA6DObservabilityAcceptanceInventory",
			"TestA6DObservabilityRawLoggerFence",
		},
		"internal/architecture/durable_feature_inventory_test.go": {
			"TestDurableFeatureInventory",
		},
		"internal/telegram/dispatcher_privacy_a6_test.go": {
			"TestA6MessageHookErrorAndPanicAreRedactedWithoutChangingPolicy",
		},
		"plugins/blacklist/native_authorization_a5_test.go": {
			"TestA5FinalBlacklistFreshRoleIsCheckedInsideMutationLock",
		},
		"plugins/filters/durable_restart_test.go": {
			"TestD4FiltersConfirmedRemovalSurvivesDurableRestart",
		},
		"plugins/userlog/lifecycle_a6_test.go": {
			"TestA6UserLogFullQueueShutdownCancelsDeliveryWithoutDrainingRPCs",
		},
	}
	for rel, required := range proofs {
		source, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, rel), nil, 0)
		if err != nil {
			t.Errorf("%s: required owner test missing or unparseable: %v", rel, err)
			continue
		}
		found := make(map[string]bool, len(source.Decls))
		for _, declaration := range source.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name == nil {
				continue
			}
			found[fn.Name.Name] = true
		}
		for _, name := range required {
			if !found[name] {
				t.Errorf("%s: required resource/restart proof %s missing", rel, name)
			}
		}
	}
}
