package command

import "strings"

const presentationOverridePrefix = "\x00presentation-override:"

// RegisterPresentationOverride installs a transport-owned presentation entry
// point that intentionally shadows canonical execution while keeping the
// canonical command registered for discovery, metadata, and Telegram menus.
// The dispatch path still requires the canonical command to exist and to be
// available on the Assistant surface before the override can run.
func (r *Router) RegisterPresentationOverride(cmd string, handler Handler) {
	if r == nil || handler == nil {
		return
	}
	cmd = normalizePresentationCommand(cmd)
	r.presentationHandlers[presentationOverridePrefix+cmd] = handler
}

func (r *Router) presentationOverride(cmd string) (Handler, bool) {
	if r == nil {
		return nil, false
	}
	handler, ok := r.presentationHandlers[presentationOverridePrefix+normalizePresentationCommand(cmd)]
	return handler, ok
}

func normalizePresentationCommand(cmd string) string {
	cmd = strings.ToLower(strings.TrimSpace(cmd))
	if !strings.HasPrefix(cmd, "/") {
		cmd = "/" + cmd
	}
	return cmd
}
