package core

import "log/slog"

// state.mu keeps the result drain from starting a queued turn while delivery is
// being decided. SteerMessage must use a bounded request and must not wait for
// the engine to consume an event; it produces no additional result event.
func (e *Engine) steerBusyMessageLocked(state *interactiveState, p Platform, msg *Message, sessions *SessionManager) bool {
	sender, ok := state.agentSession.(MessageSteerer)
	if !ok || len(state.pendingMessages) > 0 || state.stopped {
		return false
	}
	prompt := e.buildSenderPrompt(msg.Content, msg.UserID, msg.UserName, msg.Platform, msg.SessionKey, msg.ChannelKey)
	accepted, err := sender.SteerMessage(prompt, msg.MessageID, msg.Images, msg.Files)
	if err != nil {
		slog.Warn("busy message steer outcome uncertain", "session", msg.SessionKey, "error", err)
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return true
	}
	if !accepted {
		return false
	}
	if state.busySession != nil {
		state.busySession.TouchUserActivity()
		state.busySession.AddHistory("user", msg.Content)
		sessions.Save()
	}
	if msg.UserMessageTimeMs > state.currentTurnUserMessageTimeMs {
		state.currentTurnUserMessageTimeMs = msg.UserMessageTimeMs
	}
	runMessageAccepted(msg)
	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMessageSteered))
	return true
}
