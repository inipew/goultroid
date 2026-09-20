package savedresponse

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestResponseDeliveryTextOnlyUsesSingleBoundedTextSend(t *testing.T) {
	svc, _ := newPreparedTestService(t)
	delivery := NewResponseDelivery(svc)
	mediaCalls := 0
	textCalls := 0
	var got string

	stage, err := delivery.Deliver(context.Background(), NewHTML("Hello {name}"), TemplateVars{Name: "Alice"}, DeliverySink{
		SendMedia: func(string, string, string) error {
			mediaCalls++
			return nil
		},
		SendText: func(text string) error {
			textCalls++
			got = text
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stage != DeliveryStageNone {
		t.Fatalf("stage=%v, want none", stage)
	}
	if mediaCalls != 0 || textCalls != 1 || got != "Hello Alice" {
		t.Fatalf("unexpected delivery: media=%d text=%d body=%q", mediaCalls, textCalls, got)
	}
}

func TestResponseDeliveryMediaFallbackSendsMediaThenTextAndCleansUp(t *testing.T) {
	svc, store := newPreparedTestService(t)
	asset := putPreparedAsset(t, store, "photo.png", "fake image bytes")
	response := NewPlainText(strings.Repeat("x", MaxCaptionRunes+20))
	response.Media = &MediaRef{AssetID: asset.ID, MediaType: "photo", Name: asset.Name, MIMEType: asset.MIME}
	delivery := NewResponseDelivery(svc)

	var order []string
	var mediaPath string
	stage, err := delivery.Deliver(context.Background(), response, TemplateVars{}, DeliverySink{
		SendMedia: func(mediaType, path, caption string) error {
			order = append(order, "media")
			mediaPath = path
			if mediaType != "photo" || caption != "" {
				t.Fatalf("unexpected media payload: type=%q caption=%q", mediaType, caption)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("materialized media missing during send: %v", err)
			}
			return nil
		},
		SendText: func(text string) error {
			order = append(order, "text")
			if len([]rune(text)) != MaxCaptionRunes+20 {
				t.Fatalf("standalone text runes=%d", len([]rune(text)))
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stage != DeliveryStageNone {
		t.Fatalf("stage=%v, want none", stage)
	}
	if strings.Join(order, ",") != "media,text" {
		t.Fatalf("delivery order=%v", order)
	}
	if mediaPath == "" {
		t.Fatal("media path was not observed")
	}
	if _, err := os.Stat(mediaPath); !os.IsNotExist(err) {
		t.Fatalf("materialized media survived delivery cleanup: %v", err)
	}
}

func TestResponseDeliveryCompiledReusesTemplate(t *testing.T) {
	svc, _ := newPreparedTestService(t)
	delivery := NewResponseDelivery(svc)
	response := NewHTML("Hi {name}")
	compiled, err := Compile(response)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	stage, err := delivery.DeliverCompiled(context.Background(), response, compiled, TemplateVars{Name: "Alice"}, DeliverySink{
		SendText: func(text string) error {
			got = text
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stage != DeliveryStageNone || got != "Hi Alice" {
		t.Fatalf("stage=%v text=%q", stage, got)
	}
}

func TestResponseDeliveryPreservesRawSinkErrorAndStage(t *testing.T) {
	svc, store := newPreparedTestService(t)
	asset := putPreparedAsset(t, store, "photo.png", "fake image bytes")
	response := NewHTML("caption")
	response.Media = &MediaRef{AssetID: asset.ID, MediaType: "photo", Name: asset.Name}
	delivery := NewResponseDelivery(svc)
	want := errors.New("send failed")

	stage, err := delivery.Deliver(context.Background(), response, TemplateVars{}, DeliverySink{
		SendMedia: func(string, string, string) error { return want },
	})
	if stage != DeliveryStageMedia {
		t.Fatalf("stage=%v, want media", stage)
	}
	if !errors.Is(err, want) || err.Error() != want.Error() {
		t.Fatalf("error=%v, want raw %v", err, want)
	}
}

func TestResponseDeliveryRejectsOversizedStandaloneTextBeforeSend(t *testing.T) {
	svc, _ := newPreparedTestService(t)
	delivery := NewResponseDelivery(svc)
	calls := 0
	stage, err := delivery.Deliver(
		context.Background(),
		NewHTML(strings.Repeat("x", DefaultMaxOutputRunes+1)),
		TemplateVars{},
		DeliverySink{SendText: func(string) error { calls++; return nil }},
	)
	if stage != DeliveryStagePrepare {
		t.Fatalf("stage=%v, want prepare", stage)
	}
	if !errors.Is(err, ErrRenderedTooLarge) {
		t.Fatalf("error=%v, want ErrRenderedTooLarge", err)
	}
	if calls != 0 {
		t.Fatalf("oversized response reached sink %d time(s)", calls)
	}
}

func TestResponseDeliveryReportsMissingSinkByStage(t *testing.T) {
	svc, _ := newPreparedTestService(t)
	delivery := NewResponseDelivery(svc)
	stage, err := delivery.Deliver(context.Background(), NewHTML("hello"), TemplateVars{}, DeliverySink{})
	if stage != DeliveryStageText || !errors.Is(err, ErrTextDeliveryUnavailable) {
		t.Fatalf("stage=%v error=%v", stage, err)
	}
}

func TestPreparePreservesEmptyResponseErrorPrecedence(t *testing.T) {
	svc := NewService(nil)
	response := Response{Format: Format("legacy-format")}
	_, err := svc.Prepare(context.Background(), response, TemplateVars{})
	if !errors.Is(err, ErrEmptyResponse) {
		t.Fatalf("error=%v, want ErrEmptyResponse", err)
	}
}
