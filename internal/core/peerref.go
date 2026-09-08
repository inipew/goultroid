package core

import (
	"fmt"

	"github.com/gotd/td/tg"
)

type PeerKind uint8
const (
	PeerKindUser PeerKind = iota + 1
	PeerKindChat
	PeerKindChannel
)
func (k PeerKind) String() string { switch k { case PeerKindUser:return "user"; case PeerKindChat:return "chat"; case PeerKindChannel:return "channel"; default:return "unknown" } }

// PeerRef is the canonical internal Telegram peer identity. Username is not an
// identity field; it is only a resolver hint and therefore intentionally absent.
type PeerRef struct { Kind PeerKind; ID int64; AccessHash int64 }
func (p PeerRef) Valid() bool { if p.ID<=0{return false}; switch p.Kind { case PeerKindUser,PeerKindChannel:return p.AccessHash!=0; case PeerKindChat:return true; default:return false } }
func (p PeerRef) InputPeer()(tg.InputPeerClass,error){ if p.ID<=0{return nil,fmt.Errorf("peer %s: invalid id %d",p.Kind,p.ID)}; switch p.Kind { case PeerKindUser: if p.AccessHash==0{return nil,fmt.Errorf("user %d: access hash is required",p.ID)}; return &tg.InputPeerUser{UserID:p.ID,AccessHash:p.AccessHash},nil; case PeerKindChat:return &tg.InputPeerChat{ChatID:p.ID},nil; case PeerKindChannel:if p.AccessHash==0{return nil,fmt.Errorf("channel %d: access hash is required",p.ID)};return &tg.InputPeerChannel{ChannelID:p.ID,AccessHash:p.AccessHash},nil; default:return nil,fmt.Errorf("unsupported peer kind %d",p.Kind)} }
func PeerRefFromInputPeer(p tg.InputPeerClass)(PeerRef,error){ switch v:=p.(type){case *tg.InputPeerUser:if v.AccessHash==0{return PeerRef{},fmt.Errorf("user %d: missing access hash",v.UserID)};return PeerRef{Kind:PeerKindUser,ID:v.UserID,AccessHash:v.AccessHash},nil;case *tg.InputPeerChat:return PeerRef{Kind:PeerKindChat,ID:v.ChatID},nil;case *tg.InputPeerChannel:if v.AccessHash==0{return PeerRef{},fmt.Errorf("channel %d: missing access hash",v.ChannelID)};return PeerRef{Kind:PeerKindChannel,ID:v.ChannelID,AccessHash:v.AccessHash},nil;default:return PeerRef{},fmt.Errorf("unsupported input peer %T",p)} }
