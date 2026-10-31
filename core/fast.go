package core

import (
	"log/slog"
	"strings"
)

// FastModeSwitcher controls a request preference for future turns. Enabling it
// does not certify that an account or model will accept the requested tier.
type FastModeSwitcher interface {
	FastModeSupported() error
	SetFastMode(bool)
	FastModeEnabled() bool
}

func (e *Engine) cmdFast(p Platform, msg *Message, args []string) {
	e.fastMu.Lock()
	defer e.fastMu.Unlock()
	if len(args) > 1 || (len(args) == 1 && !strings.EqualFold(args[0], "on") && !strings.EqualFold(args[0], "off")) {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgFastUsage))
		return
	}
	agent, _, _, err := e.commandContext(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
		return
	}
	fs, ok := agent.(FastModeSwitcher)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgFastNotSupported))
		return
	}
	if err := fs.FastModeSupported(); err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgFastFailed, err))
		return
	}
	if len(args) == 1 {
		enabled := strings.EqualFold(args[0], "on")
		tier := "default"
		if enabled {
			tier = "fast"
		}
		workspace := ""
		if e.multiWorkspace && agent != e.agent {
			workspace = workspaceModelOverrideKey(e.interactiveKeyForSessionKey(msg.SessionKey), msg.SessionKey, agent)
		}
		if e.projectState != nil {
			if err := e.projectState.SaveServiceTierOverride(workspace, tier); err != nil {
				slog.Error("fast: persist preference", "error", err)
				e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgFastFailed, err))
				return
			}
		}
		fs.SetFastMode(enabled)
	}
	state := "off"
	if fs.FastModeEnabled() {
		state = "on"
	}
	e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgFastCurrent, state))
}
