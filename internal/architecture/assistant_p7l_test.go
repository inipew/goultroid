package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP7LAcceptanceMatrixCoverageRemainsExecutable(t *testing.T) {
	root := repositoryRoot(t)
	matrix := map[string][]string{
		filepath.Join(root, "internal", "assistant", "command", "p7l_acceptance_test.go"): {
			"TestP7LAssistantGroupSurfaceMatrix",
			"TestP7LGlobalPrivilegeDoesNotReplaceTelegramGroupRole",
			"TestP7LTaskAdmissionRejectionNeverExecutesGroupHandler",
			"TestP7LHighCardinalityTopicsShareChatQuotaButKeepOrderingIdentity",
		},
		filepath.Join(root, "internal", "assistant", "command", "group_authorization_test.go"): {
			"TestP7CFreshRevalidationRejectsRoleDowngradeAfterAdmission",
			"TestP7CContextualAuthorizationCachedPreflightThenFreshAfterAdmission",
		},
		filepath.Join(root, "internal", "assistant", "client", "group_mutation_test.go"): {
			"TestP7GSupergroupBanRevalidatesTargetBotActorBeforeRPC",
			"TestP7GBotRightsFailurePreventsPhysicalMutation",
			"TestP7GActorRightsFailurePreventsPhysicalMutation",
			"TestP7GProtectsCreatorAndHigherAdminTargets",
			"TestP7GPurgeIsTopicAwareBoundedAndRevalidatesDeleteRPC",
		},
		filepath.Join(root, "internal", "assistant", "groupauth", "resolver_test.go"): {
			"TestTelegramRoleResolverVerificationFailureIsNotCached",
			"TestTelegramRoleResolverFreshBypassesCachedRole",
			"TestTelegramRoleResolverSaturationFailsWithoutRPC",
			"TestP7LHighCardinalityRoleCacheRemainsBounded",
		},
		filepath.Join(root, "internal", "services", "groupstate", "store_test.go"): {
			"TestSQLiteStoreCASRestartAndChatIsolation",
			"TestSQLiteStoreCapacityLazilyReclaimsOnlyExpiredState",
			"TestSQLiteStoreListNamespaceIsBoundedAndScoped",
		},
		filepath.Join(root, "internal", "assistant", "groupevents", "service_test.go"): {
			"TestP7HRestartPreloadRestoresInterestAndSubscription",
			"TestP7KTransportLifecycleOwnsGroupEventSubscription",
			"TestP7KBoundedGroupEventPayloadPreservesReportedCount",
		},
		filepath.Join(root, "internal", "assistant", "client", "group_rules_updates_test.go"): {
			"TestP7LGroupRuleAdmissionRejectionStopsBeforeExecution",
			"TestP7LHighCardinalityIrrelevantGroupsStayCold",
			"BenchmarkP7LIrrelevantGroupMessageHotPath",
		},
		filepath.Join(root, "internal", "core", "context_p7j_test.go"): {
			"TestP7JGetReplyRejectsLinkedPeerBeforeRPC",
			"TestP7JGetReplyRejectsCrossTopicTarget",
			"TestP7JTopicReplyFailsClosedWithoutContextualTransport",
			"TestP7JTopicMediaFailsClosedWithoutContextualTransport",
		},
		filepath.Join(root, "internal", "admission", "controller_test.go"): {
			"TestP7LAdmissionIndependentTopicsDoNotHeadOfLineBlock",
		},
		filepath.Join(root, "internal", "telegram", "rpc_executor_test.go"): {
			"TestRPCExecutor_Case9_FloodWaitBelowThreshold",
			"TestRPCExecutor_Case10_FloodWaitAboveThreshold",
			"TestRPCExecutor_DurableContextYieldsShortFloodWait",
			"TestRPCExecutor_Case6_CanceledDuringLimiterWait",
		},
		filepath.Join(root, "internal", "telegram", "rpc_limiter_test.go"): {
			"TestHierarchicalRPCLimiter_HardBounds",
			"TestHierarchicalRPCLimiter_BucketSaturationFailsClosedWithoutResettingLiveState",
			"TestHierarchicalRPCLimiter_PenaltyOverflowFailsClosedWithoutDroppingFloodWait",
			"TestHierarchicalRPCLimiter_HighCardinalityPeersCollapseAfterSafeRefill",
		},
	}

	for path, required := range matrix {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, testName := range required {
			if !strings.Contains(source, "func "+testName+"(") {
				t.Errorf("P7-L acceptance coverage missing from %s: %s", path, testName)
			}
		}
	}
}

func TestP7LSingleExecutionAndRPCAuthority(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "internal", "assistant", "command", "router.go"): {
			"tasks.Client",
			"r.tasks.Submit",
			"telegram:chat:%d",
			"core.GroupOrderingKey",
		},
		filepath.Join(root, "internal", "assistant", "client", "managed_api.go"): {
			"managedValue(",
			"a.executor.Do(",
		},
		filepath.Join(root, "internal", "app", "assistant_rpc.go"): {
			"executor *telegram.RPCExecutor",
			"return a.executor.Do(ctx, meta, operation)",
		},
		filepath.Join(root, "internal", "admission", "controller.go"): {
			"rq.FirstMatching(",
			"orderingLocks",
			"compactOwnerCounters",
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
				t.Errorf("P7-L authority invariant missing from %s: %q", path, invariant)
			}
		}
	}

	assistantClient, err := os.ReadFile(filepath.Join(root, "internal", "assistant", "client", "client.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(assistantClient)
	for _, forbidden := range []string{
		"telegram.NewRPCExecutor(",
		"taskengine.New(",
		"admission.NewController(",
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("Assistant group plane created a second execution/RPC authority: %q", forbidden)
		}
	}
}

func TestP7LAcceptanceHarnessExists(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "tools", "accept-p7l.sh")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"go vet ./...",
		"go build",
		"go test ./...",
		"go test -race",
		"BenchmarkP7LIrrelevantGroupMessageHotPath",
		"-benchmem",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("P7-L harness missing %q", required)
		}
	}
}
