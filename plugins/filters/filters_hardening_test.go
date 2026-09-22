package filters

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestCommandsAreGroupOnly(t *testing.T) {
	p := New(nil, nil)
	for _, cmd := range p.Commands() {
		if !cmd.GroupOnly {
			t.Fatalf("command %q must be group-only", cmd.Name)
		}
	}
}

func TestMatchFilterUsesWholeTokenBoundaries(t *testing.T) {
	cases := []struct {
		text, keyword string
		want          bool
	}{
		{"hello foo world", "foo", true},
		{"foobar", "foo", false},
		{"FOO!", "foo", true},
		{"café foo_bar", "foo", false},
	}
	for _, tc := range cases {
		if got := matchFilter(tc.text, tc.keyword); got != tc.want {
			t.Errorf("matchFilter(%q, %q) = %v, want %v", tc.text, tc.keyword, got, tc.want)
		}
	}
}

type captureTaskClient struct {
	tasks.Client
	specs []tasks.WorkSpec
}

func (c *captureTaskClient) Submit(_ context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.specs = append(c.specs, spec)
	return nil, nil
}

func TestFilterDeliveryResourcePlanning(t *testing.T) {
	client := &captureTaskClient{}
	p := New(nil, nil)
	p.tasks = client
	svc := &core.MockTelegramServicer{}
	peer := &tg.InputPeerSelf{}

	textResponse := savedresponse.NewHTML("hello")
	textTemplate, err := savedresponse.Compile(textResponse)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.submitDelivery(
		context.Background(), svc, peer, 10, 0, 20,
		textResponse, textTemplate, savedresponse.TemplateVars{},
	); err != nil {
		t.Fatal(err)
	}
	if len(client.specs) != 1 {
		t.Fatalf("submitted specs=%d, want 1", len(client.specs))
	}
	if len(client.specs[0].Resources) != 0 {
		t.Fatalf("text-only filter resources=%+v, want none", client.specs[0].Resources)
	}
	if client.specs[0].Pool != tasks.PoolID("general") || client.specs[0].Class != tasks.PriorityNormal {
		t.Fatalf("unexpected text delivery scheduling: %+v", client.specs[0])
	}

	media := savedresponse.NewHTML("caption")
	media.Media = &savedresponse.MediaRef{AssetID: "asset-1", MediaType: "photo"}
	mediaTemplate, err := savedresponse.Compile(media)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.submitDelivery(
		context.Background(), svc, peer, 10, 0, 21,
		media, mediaTemplate, savedresponse.TemplateVars{},
	); err != nil {
		t.Fatal(err)
	}
	if len(client.specs) != 2 {
		t.Fatalf("submitted specs=%d, want 2", len(client.specs))
	}
	resources := client.specs[1].Resources
	if len(resources) != 1 || resources[0].Name != "media" || resources[0].Amount != 1 {
		t.Fatalf("media filter resources=%+v, want media:1", resources)
	}
	if client.specs[1].ExecutionTimeout != filterDeliveryTimeout {
		t.Fatalf("media delivery timeout=%v, want %v", client.specs[1].ExecutionTimeout, filterDeliveryTimeout)
	}
}

func TestResponseCloneDetachesMediaPointer(t *testing.T) {
	original := savedresponse.NewHTML("hello")
	original.Media = &savedresponse.MediaRef{AssetID: "one", MediaType: "photo"}
	cloned := original.Clone()
	cloned.Media.AssetID = "two"
	if original.Media.AssetID != "one" {
		t.Fatalf("clone mutated original media ref: %+v", original.Media)
	}
}
