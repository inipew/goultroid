package savedresponse

import (
	"context"
	"errors"
)

var (
	ErrResponseDeliveryUnavailable = errors.New("saved response: response delivery is not configured")
	ErrTextDeliveryUnavailable     = errors.New("saved response: text delivery is not configured")
	ErrMediaSenderUnavailable      = errors.New("saved response: media sender is not configured")
)

// DeliveryStage identifies which phase failed without wrapping the underlying
// error. Callers can preserve their existing user-facing error semantics while
// sharing the delivery lifecycle.
type DeliveryStage uint8

const (
	DeliveryStageNone DeliveryStage = iota
	DeliveryStagePrepare
	DeliveryStageMedia
	DeliveryStageText
)

// DeliverySink adapts a concrete Telegram delivery surface to ResponseDelivery.
// ResponseDelivery invokes SendMedia first and SendText second when a prepared
// response requires both.
type DeliverySink struct {
	SendMedia func(mediaType, path, caption string) error
	SendText  func(text string) error
}

// ResponseDelivery owns the bounded prepare/materialize/send/cleanup lifecycle
// for saved responses. Rendering remains capped by Service preparation:
// captions at MaxCaptionRunes, standalone text at DefaultMaxOutputRunes, and
// persisted media at MaxPersistentMediaBytes.
type ResponseDelivery struct {
	responses *Service
}

func NewResponseDelivery(responses *Service) *ResponseDelivery {
	return &ResponseDelivery{responses: responses}
}

// Deliver compiles the response once, then delegates to the same compiled
// delivery path used by cached callers.
func (d *ResponseDelivery) Deliver(
	ctx context.Context,
	response Response,
	vars TemplateVars,
	sink DeliverySink,
) (DeliveryStage, error) {
	if d == nil || d.responses == nil {
		return DeliveryStagePrepare, ErrResponseDeliveryUnavailable
	}
	prepared, err := d.responses.Prepare(ctx, response, vars)
	if err != nil {
		return DeliveryStagePrepare, err
	}
	return deliverPrepared(prepared, sink)
}

// DeliverCompiled reuses an immutable compiled template and guarantees cleanup
// of any materialized media after the send attempt completes.
func (d *ResponseDelivery) DeliverCompiled(
	ctx context.Context,
	response Response,
	compiled *CompiledTemplate,
	vars TemplateVars,
	sink DeliverySink,
) (DeliveryStage, error) {
	if d == nil || d.responses == nil {
		return DeliveryStagePrepare, ErrResponseDeliveryUnavailable
	}
	prepared, err := d.responses.PrepareCompiled(ctx, response, compiled, vars)
	if err != nil {
		return DeliveryStagePrepare, err
	}
	return deliverPrepared(prepared, sink)
}

func deliverPrepared(prepared *Prepared, sink DeliverySink) (DeliveryStage, error) {
	defer prepared.Cleanup()

	if prepared.MediaPath != "" {
		if sink.SendMedia == nil {
			return DeliveryStageMedia, ErrMediaSenderUnavailable
		}
		if err := sink.SendMedia(prepared.MediaType, prepared.MediaPath, prepared.Caption); err != nil {
			return DeliveryStageMedia, err
		}
	}
	if prepared.Text != "" {
		if sink.SendText == nil {
			return DeliveryStageText, ErrTextDeliveryUnavailable
		}
		if err := sink.SendText(prepared.Text); err != nil {
			return DeliveryStageText, err
		}
	}
	return DeliveryStageNone, nil
}
