// Package slack implements the gt slack unified router daemon.
//
// The daemon connects one Slack app (Socket Mode) to all Gas Town agents.
// Inbound Slack messages are routed to agents via internal/nudge. Agent
// replies are written to a file-based outbox and posted to Slack with
// per-agent display names via chat:write.customize.
//
// See docs/superpowers/specs/2026-04-10-gt-slack-router-design.md for the
// full design.
package slack
