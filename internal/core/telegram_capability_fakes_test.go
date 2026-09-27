package core

import (
	"context"

	"github.com/gotd/td/tg"
)

type messageCapabilityFake struct{}

func (*messageCapabilityFake) SendMessage(context.Context, tg.InputPeerClass, string) (*tg.Message, error) {
	return nil, nil
}
func (*messageCapabilityFake) SendMessageWithMarkup(context.Context, tg.InputPeerClass, string, tg.ReplyMarkupClass) (*tg.Message, error) {
	return nil, nil
}
func (*messageCapabilityFake) EditMessage(context.Context, tg.InputPeerClass, int, string) error {
	return nil
}
func (*messageCapabilityFake) EditMessageMarkup(context.Context, tg.InputPeerClass, int, string, tg.ReplyMarkupClass) error {
	return nil
}
func (*messageCapabilityFake) EditMessageMarkupOnly(context.Context, tg.InputPeerClass, int, tg.ReplyMarkupClass) error {
	return nil
}
func (*messageCapabilityFake) DeleteMessage(context.Context, tg.InputPeerClass, []int) error {
	return nil
}
func (*messageCapabilityFake) React(context.Context, tg.InputPeerClass, int, string) error {
	return nil
}
func (*messageCapabilityFake) GetMessage(context.Context, tg.InputPeerClass, int) (*tg.Message, error) {
	return nil, nil
}
func (*messageCapabilityFake) PinMessage(context.Context, tg.InputPeerClass, int, bool) error {
	return nil
}
func (*messageCapabilityFake) UnpinMessage(context.Context, tg.InputPeerClass, int) error {
	return nil
}
func (*messageCapabilityFake) ForwardMessages(context.Context, tg.InputPeerClass, tg.InputPeerClass, []int) error {
	return nil
}
func (*messageCapabilityFake) PurgeMessages(context.Context, tg.InputPeerClass, int, int, int) (int, error) {
	return 0, nil
}

type adminCapabilityFake struct{}

func (*adminCapabilityFake) BanUser(context.Context, tg.InputPeerClass, tg.InputPeerClass, int) error {
	return nil
}
func (*adminCapabilityFake) UnbanUser(context.Context, tg.InputPeerClass, tg.InputPeerClass) error {
	return nil
}
func (*adminCapabilityFake) KickUser(context.Context, tg.InputPeerClass, tg.InputPeerClass) error {
	return nil
}
func (*adminCapabilityFake) MuteUser(context.Context, tg.InputPeerClass, tg.InputPeerClass, int) error {
	return nil
}
func (*adminCapabilityFake) UnmuteUser(context.Context, tg.InputPeerClass, tg.InputPeerClass) error {
	return nil
}
func (*adminCapabilityFake) PromoteAdmin(context.Context, tg.InputPeerClass, tg.InputPeerClass, string) error {
	return nil
}
func (*adminCapabilityFake) DemoteAdmin(context.Context, tg.InputPeerClass, tg.InputPeerClass) error {
	return nil
}
func (*adminCapabilityFake) EditChatDefaultBannedRights(context.Context, tg.InputPeerClass, tg.ChatBannedRights) error {
	return nil
}

type mediaCapabilityFake struct{}

func (*mediaCapabilityFake) DownloadFile(context.Context, tg.InputFileLocationClass, string) error {
	return nil
}
func (*mediaCapabilityFake) SendMedia(context.Context, tg.InputPeerClass, string, string, string) (*tg.Message, error) {
	return nil, nil
}

type peerCapabilityFake struct{}

func (*peerCapabilityFake) GetFullUser(context.Context, tg.InputUserClass) (*tg.UsersUserFull, error) {
	return nil, nil
}
func (*peerCapabilityFake) ResolveUsername(context.Context, string) (*tg.ContactsResolvedPeer, error) {
	return nil, nil
}
func (*peerCapabilityFake) GetFullChat(context.Context, tg.InputPeerClass) (*tg.MessagesChatFull, error) {
	return nil, nil
}
func (*peerCapabilityFake) BlockUser(context.Context, tg.InputPeerClass) error {
	return nil
}
func (*peerCapabilityFake) UnblockUser(context.Context, tg.InputPeerClass) error {
	return nil
}

type profileCapabilityFake struct{}

func (*profileCapabilityFake) UpdateProfile(context.Context, *string, *string, *string) error {
	return nil
}
func (*profileCapabilityFake) UploadProfilePhoto(context.Context, string) error {
	return nil
}
func (*profileCapabilityFake) DeleteProfilePhotos(context.Context, int) (int, error) {
	return 0, nil
}
func (*profileCapabilityFake) GetDialogs(context.Context, int) ([]*Chat, error) {
	return nil, nil
}
func (*profileCapabilityFake) GetContacts(context.Context) ([]*User, error) {
	return nil, nil
}

type contextualMessageCapabilityFake struct{}

func (*contextualMessageCapabilityFake) SendMessageContext(context.Context, tg.InputPeerClass, string, tg.ReplyMarkupClass, MessageSendContext) (*tg.Message, error) {
	return nil, nil
}

type contextualMediaCapabilityFake struct{}

func (*contextualMediaCapabilityFake) SendMediaContext(context.Context, tg.InputPeerClass, string, string, string, MessageSendContext) (*tg.Message, error) {
	return nil, nil
}

var (
	_ MessageServicer = (*messageCapabilityFake)(nil)
	_ AdminServicer   = (*adminCapabilityFake)(nil)
	_ MediaServicer   = (*mediaCapabilityFake)(nil)
	_ PeerServicer    = (*peerCapabilityFake)(nil)
	_ ProfileServicer = (*profileCapabilityFake)(nil)

	_ ContextualMessageServicer = (*contextualMessageCapabilityFake)(nil)
	_ ContextualMediaServicer   = (*contextualMediaCapabilityFake)(nil)
)
