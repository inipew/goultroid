package inline

import (
	"strings"

	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/tg"
)

// ResultSerializer converts a domain InlineResult into a Telegram MTProto InputBotInlineResultClass.
type ResultSerializer interface {
	Serialize(res InlineResult) (tg.InputBotInlineResultClass, error)
}

// SerializerFunc allows using a function as a ResultSerializer.
type SerializerFunc func(res InlineResult) (tg.InputBotInlineResultClass, error)

// Serialize invokes the wrapped function.
func (f SerializerFunc) Serialize(res InlineResult) (tg.InputBotInlineResultClass, error) {
	return f(res)
}

// SerializerRegistry maps InlineResultType to its dedicated ResultSerializer.
type SerializerRegistry struct {
	serializers map[InlineResultType]ResultSerializer
	fallback    ResultSerializer
}

// NewSerializerRegistry creates a registry populated with default serializers for all standard types.
func NewSerializerRegistry() *SerializerRegistry {
	reg := &SerializerRegistry{
		serializers: make(map[InlineResultType]ResultSerializer),
	}
	article := &articleSerializer{}
	reg.fallback = article

	reg.serializers[ResultArticle] = article
	reg.serializers[ResultPhoto] = &photoSerializer{}
	reg.serializers[ResultDocument] = &mediaSerializer{resultType: ResultDocument, defaultMime: "application/octet-stream"}
	reg.serializers[ResultVideo] = &mediaSerializer{resultType: ResultVideo, defaultMime: "video/mp4"}
	reg.serializers[ResultGif] = &mediaSerializer{resultType: ResultGif, defaultMime: "image/gif"}
	reg.serializers[ResultAudio] = &mediaSerializer{resultType: ResultAudio, defaultMime: "audio/mpeg"}
	reg.serializers[ResultGeo] = &geoSerializer{}
	reg.serializers[ResultVenue] = &venueSerializer{}
	reg.serializers[ResultContact] = &contactSerializer{}
	reg.serializers[ResultGame] = &gameSerializer{}

	return reg
}

// Register adds or overrides a serializer for a specific result type.
func (r *SerializerRegistry) Register(t InlineResultType, s ResultSerializer) {
	if s != nil {
		r.serializers[t] = s
	}
}

// Serialize dispatches the result to the appropriate serializer.
func (r *SerializerRegistry) Serialize(res InlineResult) tg.InputBotInlineResultClass {
	t := res.Type
	if t == "" {
		t = ResultArticle
	}
	serializer, ok := r.serializers[t]
	if !ok || serializer == nil {
		serializer = r.fallback
	}
	item, err := serializer.Serialize(res)
	if err != nil || item == nil {
		item, _ = r.fallback.Serialize(res)
	}
	return item
}

// SerializeAll converts a slice of domain results to Telegram MTProto classes.
func (r *SerializerRegistry) SerializeAll(results []InlineResult) []tg.InputBotInlineResultClass {
	tgResults := make([]tg.InputBotInlineResultClass, 0, len(results))
	for _, res := range results {
		tgResults = append(tgResults, r.Serialize(res))
	}
	return tgResults
}

func parseFormattedText(raw string) (string, []tg.MessageEntityClass) {
	if raw == "" {
		return "", nil
	}
	var b entity.Builder
	if err := html.HTML(strings.NewReader(raw), &b, html.Options{}); err == nil {
		text, entities := b.Complete()
		text = truncate(text, maxInlineTextLen)
		return text, entities
	}
	return truncate(raw, maxInlineTextLen), nil
}

func sanitizeID(id string) string {
	res := truncate(id, maxInlineIDLen)
	if res == "" {
		return "0"
	}
	return res
}

func toTelegramMarkup(res InlineResult) tg.ReplyMarkupClass {
	if res.Markup != nil {
		return res.Markup.ToTelegramMarkup()
	}
	return nil
}

// articleSerializer handles standard text articles.
type articleSerializer struct{}

func (s *articleSerializer) Serialize(res InlineResult) (tg.InputBotInlineResultClass, error) {
	id := sanitizeID(res.ID)
	title := truncate(res.Title, maxInlineTitleLen)
	desc := truncate(res.Description, maxInlineDescLen)
	text, entities := parseFormattedText(res.Text)

	msg := &tg.InputBotInlineMessageText{
		Message:  text,
		Entities: entities,
	}
	if markup := toTelegramMarkup(res); markup != nil {
		msg.ReplyMarkup = markup
	}
	msg.SetFlags()

	item := &tg.InputBotInlineResult{
		ID:          id,
		Type:        string(ResultArticle),
		Title:       title,
		Description: desc,
		SendMessage: msg,
	}
	if res.ThumbURL != "" {
		item.SetThumb(tg.InputWebDocument{URL: truncate(res.ThumbURL, 512), MimeType: "image/jpeg"})
	}
	if res.URL != "" {
		item.SetURL(truncate(res.URL, 512))
	}
	item.SetFlags()
	return item, nil
}

// photoSerializer handles photo inline results.
type photoSerializer struct{}

func (s *photoSerializer) Serialize(res InlineResult) (tg.InputBotInlineResultClass, error) {
	id := sanitizeID(res.ID)
	title := truncate(res.Title, maxInlineTitleLen)
	desc := truncate(res.Description, maxInlineDescLen)
	text, entities := parseFormattedText(res.Text)
	markup := toTelegramMarkup(res)

	var sendMsg tg.InputBotInlineMessageClass
	if res.MediaURL != "" {
		auto := &tg.InputBotInlineMessageMediaAuto{
			Message:  text,
			Entities: entities,
		}
		if markup != nil {
			auto.ReplyMarkup = markup
		}
		auto.SetFlags()
		sendMsg = auto
	} else {
		txt := &tg.InputBotInlineMessageText{
			Message:  text,
			Entities: entities,
		}
		if markup != nil {
			txt.ReplyMarkup = markup
		}
		txt.SetFlags()
		sendMsg = txt
	}

	mime := res.MediaMimeType
	if mime == "" {
		mime = "image/jpeg"
	}

	item := &tg.InputBotInlineResult{
		ID:          id,
		Type:        string(ResultPhoto),
		Title:       title,
		Description: desc,
		SendMessage: sendMsg,
	}
	if res.ThumbURL != "" {
		item.SetThumb(tg.InputWebDocument{URL: truncate(res.ThumbURL, 512), MimeType: "image/jpeg"})
	}
	if res.URL != "" {
		item.SetURL(truncate(res.URL, 512))
	}
	if res.MediaURL != "" {
		item.SetContent(tg.InputWebDocument{URL: truncate(res.MediaURL, 512), MimeType: mime})
	}
	item.SetFlags()
	return item, nil
}

// mediaSerializer handles document, video, gif, and audio inline results.
type mediaSerializer struct {
	resultType  InlineResultType
	defaultMime string
}

func (s *mediaSerializer) Serialize(res InlineResult) (tg.InputBotInlineResultClass, error) {
	id := sanitizeID(res.ID)
	title := truncate(res.Title, maxInlineTitleLen)
	desc := truncate(res.Description, maxInlineDescLen)
	text, entities := parseFormattedText(res.Text)
	markup := toTelegramMarkup(res)

	var sendMsg tg.InputBotInlineMessageClass
	if res.MediaURL != "" {
		auto := &tg.InputBotInlineMessageMediaAuto{
			Message:  text,
			Entities: entities,
		}
		if markup != nil {
			auto.ReplyMarkup = markup
		}
		auto.SetFlags()
		sendMsg = auto
	} else {
		txt := &tg.InputBotInlineMessageText{
			Message:  text,
			Entities: entities,
		}
		if markup != nil {
			txt.ReplyMarkup = markup
		}
		txt.SetFlags()
		sendMsg = txt
	}

	mime := res.MediaMimeType
	if mime == "" {
		mime = s.defaultMime
	}

	item := &tg.InputBotInlineResult{
		ID:          id,
		Type:        string(s.resultType),
		Title:       title,
		Description: desc,
		SendMessage: sendMsg,
	}
	if res.ThumbURL != "" {
		item.SetThumb(tg.InputWebDocument{URL: truncate(res.ThumbURL, 512), MimeType: "image/jpeg"})
	}
	if res.URL != "" {
		item.SetURL(truncate(res.URL, 512))
	}
	if res.MediaURL != "" {
		item.SetContent(tg.InputWebDocument{URL: truncate(res.MediaURL, 512), MimeType: mime})
	}
	item.SetFlags()
	return item, nil
}

// geoSerializer handles geographic point inline results.
type geoSerializer struct{}

func (s *geoSerializer) Serialize(res InlineResult) (tg.InputBotInlineResultClass, error) {
	id := sanitizeID(res.ID)
	title := truncate(res.Title, maxInlineTitleLen)
	desc := truncate(res.Description, maxInlineDescLen)
	markup := toTelegramMarkup(res)

	sendMsg := &tg.InputBotInlineMessageMediaGeo{
		GeoPoint: &tg.InputGeoPoint{
			Lat:  res.Latitude,
			Long: res.Longitude,
		},
	}
	if markup != nil {
		sendMsg.ReplyMarkup = markup
	}
	sendMsg.SetFlags()

	item := &tg.InputBotInlineResult{
		ID:          id,
		Type:        string(ResultGeo),
		Title:       title,
		Description: desc,
		SendMessage: sendMsg,
	}
	if res.ThumbURL != "" {
		item.SetThumb(tg.InputWebDocument{URL: truncate(res.ThumbURL, 512), MimeType: "image/jpeg"})
	}
	item.SetFlags()
	return item, nil
}

// venueSerializer handles venue location inline results.
type venueSerializer struct{}

func (s *venueSerializer) Serialize(res InlineResult) (tg.InputBotInlineResultClass, error) {
	id := sanitizeID(res.ID)
	title := truncate(res.Title, maxInlineTitleLen)
	desc := truncate(res.Description, maxInlineDescLen)
	markup := toTelegramMarkup(res)

	sendMsg := &tg.InputBotInlineMessageMediaVenue{
		GeoPoint: &tg.InputGeoPoint{
			Lat:  res.Latitude,
			Long: res.Longitude,
		},
		Title:   title,
		Address: truncate(res.Address, 256),
	}
	if markup != nil {
		sendMsg.ReplyMarkup = markup
	}
	sendMsg.SetFlags()

	item := &tg.InputBotInlineResult{
		ID:          id,
		Type:        string(ResultVenue),
		Title:       title,
		Description: desc,
		SendMessage: sendMsg,
	}
	if res.ThumbURL != "" {
		item.SetThumb(tg.InputWebDocument{URL: truncate(res.ThumbURL, 512), MimeType: "image/jpeg"})
	}
	item.SetFlags()
	return item, nil
}

// contactSerializer handles contact card inline results.
type contactSerializer struct{}

func (s *contactSerializer) Serialize(res InlineResult) (tg.InputBotInlineResultClass, error) {
	id := sanitizeID(res.ID)
	title := truncate(res.Title, maxInlineTitleLen)
	desc := truncate(res.Description, maxInlineDescLen)
	markup := toTelegramMarkup(res)

	sendMsg := &tg.InputBotInlineMessageMediaContact{
		PhoneNumber: res.PhoneNumber,
		FirstName:   res.FirstName,
		LastName:    res.LastName,
		Vcard:       res.VCard,
	}
	if markup != nil {
		sendMsg.ReplyMarkup = markup
	}
	sendMsg.SetFlags()

	item := &tg.InputBotInlineResult{
		ID:          id,
		Type:        string(ResultContact),
		Title:       title,
		Description: desc,
		SendMessage: sendMsg,
	}
	if res.ThumbURL != "" {
		item.SetThumb(tg.InputWebDocument{URL: truncate(res.ThumbURL, 512), MimeType: "image/jpeg"})
	}
	item.SetFlags()
	return item, nil
}

// gameSerializer handles Telegram HTML5 game inline results.
type gameSerializer struct{}

func (s *gameSerializer) Serialize(res InlineResult) (tg.InputBotInlineResultClass, error) {
	id := sanitizeID(res.ID)
	markup := toTelegramMarkup(res)

	sendMsg := &tg.InputBotInlineMessageGame{}
	if markup != nil {
		sendMsg.ReplyMarkup = markup
	}
	sendMsg.SetFlags()

	if res.GameShortName != "" {
		return &tg.InputBotInlineResultGame{
			ID:          id,
			ShortName:   res.GameShortName,
			SendMessage: sendMsg,
		}, nil
	}

	// Fallback to generic article if GameShortName is empty
	return (&articleSerializer{}).Serialize(res)
}
