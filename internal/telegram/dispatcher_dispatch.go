package telegram

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

func (d *Dispatcher) dispatch(ctx context.Context, e tg.Entities, msg *tg.Message) error {
	d.admissionMu.RLock()
	if !d.acceptingUpdates.Load() {
		d.admissionMu.RUnlock()
		return nil
	}
	d.inFlight.Add(1)
	d.admissionMu.RUnlock()
	defer d.inFlight.Done()

	parsed, isCmd, err := d.router.Parse(msg.Message)
	if err != nil { d.logger.Warn("command parse syntax error", zap.Error(err), zap.String("text", msg.Message)); return nil }
	cmdName := ""
	if isCmd { cmdName = parsed.Name }
	origin := core.ExecutionInteractive
	if msg.Out { svc := d.getService(); if svc != nil && svc.IsBotSent(msg.ID) { origin = core.ExecutionAutomation } }
	decision := core.NewMessageDecision(origin)
	ctx = core.WithMessageDecision(ctx, decision)

	if len(e.Users) > 0 || len(e.Channels) > 0 || len(e.Chats) > 0 {
		resolver := d.getResolver()
		if r, ok := resolver.(*Resolver); ok && r.storage != nil {
			users := make([]*tg.User, 0, len(e.Users)); for _, u := range e.Users { users = append(users, u) }
			channels := make([]*tg.Channel, 0, len(e.Channels)); for _, ch := range e.Channels { channels = append(channels, ch) }
			chats := make([]*tg.Chat, 0, len(e.Chats)); for _, c := range e.Chats { chats = append(chats, c) }
			job := peerUpdateJob{users: users, channels: channels, chats: chats}
			if !d.stopping.Load() { d.mu.RLock(); if !d.stopping.Load() && d.peerQueue != nil { select { case d.peerQueue <- job: d.peerEnqueued.Add(1); default: d.peerDropped.Add(1) }; d.mu.RUnlock() } else { d.mu.RUnlock(); if !d.stopping.Load() { go func(){ bgCtx,cancel:=context.WithTimeout(context.Background(),5*time.Second); defer cancel(); _=r.storage.SaveEntitiesBatch(bgCtx,job.users,job.channels,job.chats) }() } } }
		}
	}

	d.mu.RLock()
	var syncHandlers []MessageHandler
	var asyncHandlers []MessageHandler
	for _, ph := range d.messageHandlers { if ph.priority >= PriorityObservability { asyncHandlers = append(asyncHandlers, ph.handler) } else { syncHandlers = append(syncHandlers, ph.handler) } }
	d.mu.RUnlock()
	for _, h := range syncHandlers { if d.safeExecuteInterceptor(ctx, h, e, msg, isCmd, cmdName) { return nil } }
	if decision.IsHandled() || decision.IsSuppressedCommands() { return nil }
	coreMsg := extractCoreMessage(msg)
	if coreMsg.GroupedID != 0 && d.albumBuffer != nil { d.albumBuffer.Add(coreMsg) }
	if !isCmd { for _, h := range asyncHandlers { _ = d.safeExecuteInterceptor(ctx, h, e, msg, isCmd, cmdName) }; return nil }
	cmd, exists := d.router.Find(parsed.Name)
	if !exists { for _, h := range asyncHandlers { _ = d.safeExecuteInterceptor(ctx, h, e, msg, isCmd, cmdName) }; return nil }

	var peerInput tg.InputPeerClass
	chat := &core.Chat{}
	switch p := msg.PeerID.(type) {
	case *tg.PeerUser:
		chat.ID=p.UserID; chat.Type="private"; var accessHash int64
		if u,ok:=e.Users[p.UserID];ok{chat.Username=u.Username;chat.Title=u.FirstName+" "+u.LastName;accessHash=u.AccessHash}
		if p.UserID==d.getSelfID(){peerInput=&tg.InputPeerSelf{}}else if peerInput==nil{if accessHash==0&&d.getResolver()!=nil{if resolved,_,err:=d.getResolver().ResolveUser(ctx,strconv.FormatInt(p.UserID,10));err==nil{if ipu,ok:=resolved.(*tg.InputPeerUser);ok&&ipu.AccessHash!=0{accessHash=ipu.AccessHash}}};if accessHash!=0||d.getResolver()==nil{peerInput=&tg.InputPeerUser{UserID:p.UserID,AccessHash:accessHash}}}
	case *tg.PeerChat:
		chat.ID=p.ChatID;chat.Type="group";peerInput=&tg.InputPeerChat{ChatID:p.ChatID};if c,ok:=e.Chats[p.ChatID];ok{chat.Title=c.Title}
	case *tg.PeerChannel:
		chat.ID=p.ChannelID;chat.Type="supergroup";var accessHash int64
		if ch,ok:=e.Channels[p.ChannelID];ok{chat.Title=ch.Title;chat.Username=ch.Username;if ch.Megagroup{chat.Type="supergroup"}else{chat.Type="channel"};accessHash=ch.AccessHash}
		if accessHash==0&&d.getResolver()!=nil{if resolved,err:=d.getResolver().ResolveChat(ctx,fmt.Sprintf("-100%d",p.ChannelID));err==nil{if ipc,ok:=resolved.(*tg.InputPeerChannel);ok&&ipc.AccessHash!=0{accessHash=ipc.AccessHash}}}
		if accessHash!=0||d.getResolver()==nil{peerInput=&tg.InputPeerChannel{ChannelID:p.ChannelID,AccessHash:accessHash}}
	}
	d.logger.Debug("dispatch: peer resolved",zap.String("peerType",fmt.Sprintf("%T",msg.PeerID)),zap.String("chatType",chat.Type),zap.Int64("chatID",chat.ID),zap.Bool("msgOut",msg.Out),zap.String("command",cmdName))
	if peerInput==nil&&msg.Out{peerInput=&tg.InputPeerSelf{}}
	if peerInput==nil&&msg.PeerID!=nil{d.logger.Warn("dispatch: peer unresolvable without access hash, command execution skipped",zap.String("peerType",fmt.Sprintf("%T",msg.PeerID)),zap.Int64("chatID",chat.ID),zap.String("command",cmdName));return nil}

	sender:=&core.User{}
	if msg.Out{sender.ID=d.getSelfID()}else if msg.FromID!=nil{if u,ok:=msg.FromID.(*tg.PeerUser);ok{sender.ID=u.UserID;if userEntity,ok:=e.Users[u.UserID];ok{sender.FirstName=userEntity.FirstName;sender.LastName=userEntity.LastName;sender.Username=userEntity.Username;sender.IsBot=userEntity.Bot}}}
	coreMsg.SenderID=sender.ID
	root:=d.getRootContext();if root==nil{if d.logger!=nil{d.logger.Warn("dispatcher: root context is nil, falling back to background - lifecycle not correctly wired")};root=context.Background()}
	execCtx,cancel:=context.WithCancel(root)
	var album []*core.Message
	if coreMsg.GroupedID!=0&&d.albumBuffer!=nil{album=d.albumBuffer.Get(coreMsg.GroupedID)}
	coreCtx:=&core.Context{Ctx:execCtx,Command:parsed.Name,Args:parsed.Args,RawArgs:parsed.RawArgs,Message:coreMsg,Album:album,Chat:chat,Sender:sender,Perms:d.perms,Svc:d.getService(),PeerID:peerInput,Resolver:d.getResolver(),Localizer:d.getLocalizer(),EventBus:d.getEventBus()}
	select{case d.cmdSem<-struct{}{}:case <-execCtx.Done():cancel();return nil}
	d.cmdWG.Add(1);d.runningCommands.Add(1);d.totalCommands.Add(1)
	go func(){defer func(){d.runningCommands.Add(-1);<-d.cmdSem;d.cmdWG.Done();cancel()}();_=d.executor.Execute(coreCtx,cmd)}()
	for _,h:=range asyncHandlers{_=d.safeExecuteInterceptor(ctx,h,e,msg,isCmd,cmdName)}
	return nil
}

func extractCoreMessage(msg *tg.Message)*core.Message{coreMsg:=&core.Message{ID:msg.ID,Text:msg.Message,Date:time.Unix(int64(msg.Date),0),IsOutgoing:msg.Out,GroupedID:msg.GroupedID,Entities:msg.Entities};if msg.Media!=nil{coreMsg.Media=core.ExtractMediaFromTG(msg.Media);if coreMsg.Media!=nil{coreMsg.MediaType=coreMsg.Media.Type}};if msg.ReplyTo!=nil{if header,ok:=msg.ReplyTo.(*tg.MessageReplyHeader);ok{coreMsg.ReplyToID=header.ReplyToMsgID;if header.ForumTopic||header.ReplyToTopID!=0{if header.ReplyToTopID!=0{coreMsg.TopicID=header.ReplyToTopID}else{coreMsg.TopicID=header.ReplyToMsgID}}}};return coreMsg}
