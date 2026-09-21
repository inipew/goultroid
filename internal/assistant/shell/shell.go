package shell

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"

	appstatus "github.com/inipew/goultroid/internal/application/status"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/ui"
)

const (
	FeatureID = "assistant_shell"

	InteractionStart = "start"
	InteractionHome  = "home"

	ActionRefresh = "refresh"
	ActionPing    = "ping"
	ActionLegacy  = "legacy"
)

// Feature is the first production feature migrated onto the P0-P4 interaction
// foundation. It intentionally owns no goroutine or transport resource.
type Feature struct{}

func NewFeature() *Feature { return &Feature{} }

func (*Feature) Name() string             { return FeatureID }
func (*Feature) Commands() []core.Command { return nil }
func (*Feature) Init() error              { return nil }

func (*Feature) FeatureSpec() feature.Spec {
	assistant := execution.SurfaceAssistant
	startPolicy := feature.PublicPolicy(assistant)
	startPolicy.PrivateOnly = true
	ownerPolicy := feature.OwnerPolicy(assistant)
	ownerPolicy.PrivateOnly = true

	return feature.Spec{
		ID:          FeatureID,
		Name:        "Assistant Shell",
		Description: "Root Assistant control surface migrated to interaction protocol a2.",
		Category:    "Assistant",
		Interactions: []feature.Interaction{
			{ID: InteractionStart, Kind: feature.InteractionDeepLink, Description: "Telegram /start entry point", Surfaces: assistant, Policy: startPolicy},
			{ID: InteractionHome, Kind: feature.InteractionScreen, Description: "Owner root/home screen", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionRefresh, Kind: feature.InteractionAction, Description: "Refresh shell state and presentation", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionPing, Kind: feature.InteractionAction, Description: "Acknowledge shell liveness", Surfaces: assistant, Policy: ownerPolicy},
			{ID: ActionLegacy, Kind: feature.InteractionAction, Description: "Handoff to the legacy a1 menu", Surfaces: assistant, Policy: ownerPolicy},
		},
	}
}

const stateBytes = 8

func InitialState() []byte { return make([]byte, stateBytes) }

func RefreshCount(state []byte) uint64 {
	if len(state) != stateBytes {
		return 0
	}
	return binary.BigEndian.Uint64(state)
}

func NextRefreshState(state []byte) []byte {
	out := make([]byte, stateBytes)
	binary.BigEndian.PutUint64(out, RefreshCount(state)+1)
	return out
}

type HomeModel struct {
	Username  string
	Uptime    time.Duration
	Refreshes uint64
}

func HomeView(model HomeModel) presentation.View {
	username := strings.TrimSpace(model.Username)
	if username == "" {
		username = "GoUltroidBot"
	}
	card := ui.NewCard("GoUltroid Assistant").
		WithIcon("🤖").
		WithHeader("Control center for your userbot and assistant.").
		AddField("Bot", "@"+username).
		AddField("Status", "🟢 Online & ready").
		AddField("Uptime", appstatus.FormatDuration(model.Uptime))
	if model.Refreshes > 0 {
		card.AddField("Session refreshes", strconv.FormatUint(model.Refreshes, 10))
	}
	card.WithFooter("<i>Settings and full navigation remain available through Classic menu while migration continues.</i>")

	return presentation.View{
		Text: card.Render(),
		Rows: []presentation.Row{
			{{Text: "🔄 Refresh", ActionID: ActionRefresh}, {Text: "🏓 Ping", ActionID: ActionPing}},
			{{Text: "🧭 Classic menu", ActionID: ActionLegacy}},
		},
	}
}

func ValidateSpec() error {
	spec := NewFeature().FeatureSpec()
	bound, err := feature.BindCanonicalCommands(spec, nil)
	if err != nil {
		return err
	}
	if bound.ID != FeatureID {
		return fmt.Errorf("assistant shell feature id = %q", bound.ID)
	}
	return HomeView(HomeModel{}).Validate()
}
