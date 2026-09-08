package telegram

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type RPCErrorClass uint8
const (
	RPCUnknown RPCErrorClass = iota
	RPCTransient
	RPCFloodWait
	RPCStalePeer
	RPCPermission
	RPCAuth
	RPCInvalidRequest
)
func (c RPCErrorClass) String() string { switch c {case RPCTransient:return "transient";case RPCFloodWait:return "flood_wait";case RPCStalePeer:return "stale_peer";case RPCPermission:return "permission";case RPCAuth:return "auth";case RPCInvalidRequest:return "invalid_request";default:return "unknown"} }
var floodWaitRE=regexp.MustCompile(`(?i)(?:FLOOD_WAIT|FLOOD_PREMIUM_WAIT|SLOWMODE_WAIT)_(\d+)`)
func ClassifyRPCError(err error) RPCErrorClass {
	if err==nil{return RPCUnknown};if IsStalePeerError(err){return RPCStalePeer};s:=strings.ToUpper(err.Error());if floodWaitRE.MatchString(s){return RPCFloodWait}
	for _,code:=range []string{"CHAT_ADMIN_REQUIRED","USER_BANNED_IN_CHANNEL","CHANNEL_PRIVATE","CHAT_WRITE_FORBIDDEN","USER_IS_BLOCKED","MESSAGE_AUTHOR_REQUIRED","RIGHT_FORBIDDEN"}{if strings.Contains(s,code){return RPCPermission}}
	for _,code:=range []string{"AUTH_KEY_UNREGISTERED","SESSION_REVOKED","SESSION_PASSWORD_NEEDED","AUTH_RESTART"}{if strings.Contains(s,code){return RPCAuth}}
	for _,code:=range []string{"BAD_REQUEST","MESSAGE_ID_INVALID","MSG_ID_INVALID","USERNAME_INVALID","USERNAME_NOT_OCCUPIED","REQUEST_TOKEN_INVALID"}{if strings.Contains(s,code){return RPCInvalidRequest}}
	for _,code:=range []string{"TIMEOUT","DEADLINE_EXCEEDED","CONNECTION_RESET","CONNECTION_CLOSED","EOF","TEMPORARY","UNAVAILABLE","INTERNAL_SERVER_ERROR","502","503","504"}{if strings.Contains(s,code){return RPCTransient}}
	if errors.Is(err,context.DeadlineExceeded)||errors.Is(err,context.Canceled){return RPCTransient};return RPCUnknown
}
func FloodWaitDuration(err error)(time.Duration,bool){if err==nil{return 0,false};m:=floodWaitRE.FindStringSubmatch(err.Error());if len(m)!=2{return 0,false};sec,e:=strconv.ParseInt(m[1],10,64);if e!=nil||sec<0{return 0,false};return time.Duration(sec)*time.Second,true}
type RPCPolicy struct{MaxAttempts int;BaseDelay time.Duration;MaxDelay time.Duration}
var DefaultRPCPolicy=RPCPolicy{MaxAttempts:3,BaseDelay:500*time.Millisecond,MaxDelay:10*time.Second}
func(p RPCPolicy)Do(ctx context.Context,fn func(context.Context)error,recoverStale func(context.Context)error)error{if ctx==nil{ctx=context.Background()};if fn==nil{return fmt.Errorf("nil RPC function")};max:=p.MaxAttempts;if max<1{max=1};base:=p.BaseDelay;if base<=0{base=500*time.Millisecond};maxDelay:=p.MaxDelay;if maxDelay<=0{maxDelay=10*time.Second};for attempt:=1;attempt<=max;attempt++{err:=fn(ctx);if err==nil{return nil};switch ClassifyRPCError(err){case RPCStalePeer:if recoverStale==nil||attempt>=max{return err};if e:=recoverStale(ctx);e!=nil{return fmt.Errorf("stale-peer recovery: %w",e)};case RPCFloodWait:wait,ok:=FloodWaitDuration(err);if !ok{return err};if e:=sleepContext(ctx,wait);e!=nil{return e};case RPCTransient:if attempt>=max{return err};delay:=base << (attempt-1);if delay>maxDelay{delay=maxDelay};if e:=sleepContext(ctx,delay);e!=nil{return e};default:return err}};return fmt.Errorf("RPC retry exhausted")}
func sleepContext(ctx context.Context,d time.Duration)error{t:=time.NewTimer(d);defer t.Stop();select{case<-ctx.Done():return ctx.Err();case<-t.C:return nil}}
