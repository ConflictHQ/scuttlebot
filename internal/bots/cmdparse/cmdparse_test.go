package cmdparse

import (
	"strings"
	"testing"
)

func testRouter() *CommandRouter {
	r := NewRouter("scroll")
	r.Register(Command{
		Name:        "replay",
		Usage:       "replay #channel [last=N]",
		Description: "replay channel history",
		Handler: func(ctx *Context, args string) string {
			return "replaying: " + args
		},
	})
	r.Register(Command{
		Name:        "status",
		Usage:       "status",
		Description: "show bot status",
		Handler: func(ctx *Context, args string) string {
			return "ok"
		},
	})
	return r
}

// --- DM input form ---

func TestDM_BasicCommand(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "scroll", "replay #general last=10")
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if reply.Target != "alice" {
		t.Errorf("target = %q, want %q", reply.Target, "alice")
	}
	if reply.Text != "replaying: #general last=10" {
		t.Errorf("text = %q, want %q", reply.Text, "replaying: #general last=10")
	}
}

func TestDM_CaseInsensitive(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "scroll", "REPLAY #general")
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if reply.Text != "replaying: #general" {
		t.Errorf("text = %q, want %q", reply.Text, "replaying: #general")
	}
}

func TestDM_NoArgs(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "scroll", "status")
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if reply.Text != "ok" {
		t.Errorf("text = %q, want %q", reply.Text, "ok")
	}
}

func TestDM_EmptyMessage(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "scroll", "")
	if reply != nil {
		t.Errorf("expected nil for empty message, got %+v", reply)
	}
}

func TestDM_WhitespaceOnly(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "scroll", "   ")
	if reply != nil {
		t.Errorf("expected nil for whitespace-only, got %+v", reply)
	}
}

func TestDM_ContextFields(t *testing.T) {
	r := NewRouter("testbot")
	var gotCtx *Context
	r.Register(Command{
		Name:        "ping",
		Usage:       "ping",
		Description: "ping",
		Handler: func(ctx *Context, args string) string {
			gotCtx = ctx
			return "pong"
		},
	})
	r.Dispatch("bob", "testbot", "ping")
	if gotCtx == nil {
		t.Fatal("handler not called")
	}
	if !gotCtx.IsDM {
		t.Error("expected IsDM=true")
	}
	if gotCtx.Channel != "" {
		t.Errorf("channel = %q, want empty", gotCtx.Channel)
	}
	if gotCtx.Nick != "bob" {
		t.Errorf("nick = %q, want %q", gotCtx.Nick, "bob")
	}
}

// --- Fantasy input form ---

func TestFantasy_BasicCommand(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "#general", "!replay #logs last=20")
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if reply.Target != "#general" {
		t.Errorf("target = %q, want %q", reply.Target, "#general")
	}
	if reply.Text != "replaying: #logs last=20" {
		t.Errorf("text = %q, want %q", reply.Text, "replaying: #logs last=20")
	}
}

func TestFantasy_CaseInsensitive(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "#general", "!STATUS")
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if reply.Text != "ok" {
		t.Errorf("text = %q, want %q", reply.Text, "ok")
	}
}

func TestFantasy_ContextFields(t *testing.T) {
	r := NewRouter("testbot")
	var gotCtx *Context
	r.Register(Command{
		Name:        "ping",
		Usage:       "ping",
		Description: "ping",
		Handler: func(ctx *Context, args string) string {
			gotCtx = ctx
			return "pong"
		},
	})
	r.Dispatch("bob", "#dev", "!ping")
	if gotCtx == nil {
		t.Fatal("handler not called")
	}
	if gotCtx.IsDM {
		t.Error("expected IsDM=false")
	}
	if gotCtx.Channel != "#dev" {
		t.Errorf("channel = %q, want %q", gotCtx.Channel, "#dev")
	}
}

func TestFantasy_BangOnly(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "#general", "!")
	if reply != nil {
		t.Errorf("expected nil for bare !, got %+v", reply)
	}
}

func TestFantasy_NotAddressed(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "#general", "just a normal message")
	if reply != nil {
		t.Errorf("expected nil for unaddressed channel message, got %+v", reply)
	}
}

// --- Addressed input form ---

func TestAddressed_ColonSpace(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "#general", "scroll: replay #logs")
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if reply.Target != "#general" {
		t.Errorf("target = %q, want %q", reply.Target, "#general")
	}
	if reply.Text != "replaying: #logs" {
		t.Errorf("text = %q, want %q", reply.Text, "replaying: #logs")
	}
}

func TestAddressed_ColonNoSpace(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "#general", "scroll:replay #logs")
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if reply.Text != "replaying: #logs" {
		t.Errorf("text = %q, want %q", reply.Text, "replaying: #logs")
	}
}

func TestAddressed_Comma(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "#general", "scroll, status")
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if reply.Text != "ok" {
		t.Errorf("text = %q, want %q", reply.Text, "ok")
	}
}

func TestAddressed_CaseInsensitiveBotNick(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "#general", "Scroll: status")
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if reply.Text != "ok" {
		t.Errorf("text = %q, want %q", reply.Text, "ok")
	}
}

func TestAddressed_ContextFields(t *testing.T) {
	r := NewRouter("testbot")
	var gotCtx *Context
	r.Register(Command{
		Name:        "ping",
		Usage:       "ping",
		Description: "ping",
		Handler: func(ctx *Context, args string) string {
			gotCtx = ctx
			return "pong"
		},
	})
	r.Dispatch("bob", "#ops", "testbot: ping")
	if gotCtx == nil {
		t.Fatal("handler not called")
	}
	if gotCtx.IsDM {
		t.Error("expected IsDM=false")
	}
	if gotCtx.Channel != "#ops" {
		t.Errorf("channel = %q, want %q", gotCtx.Channel, "#ops")
	}
}

// --- HELP generation ---

func TestHelp_DM(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "scroll", "help")
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if reply.Target != "alice" {
		t.Errorf("target = %q, want %q", reply.Target, "alice")
	}
	if !strings.Contains(reply.Text, "REPLAY") {
		t.Errorf("help should list REPLAY, got: %s", reply.Text)
	}
	if !strings.Contains(reply.Text, "STATUS") {
		t.Errorf("help should list STATUS, got: %s", reply.Text)
	}
	if !strings.Contains(reply.Text, "commands for scroll") {
		t.Errorf("help should include bot name, got: %s", reply.Text)
	}
}

func TestHelp_Fantasy(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "#general", "!help")
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if reply.Target != "#general" {
		t.Errorf("target = %q, want %q", reply.Target, "#general")
	}
	if !strings.Contains(reply.Text, "REPLAY") {
		t.Errorf("help should list REPLAY, got: %s", reply.Text)
	}
}

func TestHelp_Addressed(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "#general", "scroll: help")
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if reply.Target != "#general" {
		t.Errorf("target = %q, want %q", reply.Target, "#general")
	}
}

func TestHelp_SpecificCommand(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "scroll", "help replay")
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if !strings.Contains(reply.Text, "replay channel history") {
		t.Errorf("help replay should show description, got: %s", reply.Text)
	}
	if !strings.Contains(reply.Text, "replay #channel [last=N]") {
		t.Errorf("help replay should show usage, got: %s", reply.Text)
	}
}

func TestHelp_SpecificCommandCaseInsensitive(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "scroll", "HELP REPLAY")
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if !strings.Contains(reply.Text, "replay channel history") {
		t.Errorf("expected description, got: %s", reply.Text)
	}
}

func TestHelp_UnknownCommand(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "scroll", "help nosuchcmd")
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if !strings.Contains(reply.Text, "unknown command") {
		t.Errorf("expected unknown command message, got: %s", reply.Text)
	}
}

// --- Unknown command handling ---

func TestUnknown_DM(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "scroll", "frobnicate something")
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if !strings.Contains(reply.Text, `unknown command "frobnicate"`) {
		t.Errorf("expected unknown command message, got: %s", reply.Text)
	}
	if !strings.Contains(reply.Text, "REPLAY") {
		t.Errorf("should list available commands, got: %s", reply.Text)
	}
	if !strings.Contains(reply.Text, "STATUS") {
		t.Errorf("should list available commands, got: %s", reply.Text)
	}
}

func TestUnknown_Fantasy(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "#general", "!frobnicate")
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if reply.Target != "#general" {
		t.Errorf("target = %q, want %q", reply.Target, "#general")
	}
	if !strings.Contains(reply.Text, `unknown command "frobnicate"`) {
		t.Errorf("expected unknown command message, got: %s", reply.Text)
	}
}

func TestUnknown_Addressed(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "#general", "scroll: frobnicate")
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if !strings.Contains(reply.Text, `unknown command "frobnicate"`) {
		t.Errorf("expected unknown command message, got: %s", reply.Text)
	}
}

// --- Edge cases ---

func TestRegister_EmptyNamePanics(t *testing.T) {
	r := NewRouter("bot")
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for empty command name")
		}
	}()
	r.Register(Command{Name: "", Handler: func(*Context, string) string { return "" }})
}

func TestRegister_DuplicatePanics(t *testing.T) {
	r := NewRouter("bot")
	r.Register(Command{Name: "ping", Handler: func(*Context, string) string { return "" }})
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for duplicate command")
		}
	}()
	r.Register(Command{Name: "ping", Handler: func(*Context, string) string { return "" }})
}

func TestHandlerReturnsEmpty(t *testing.T) {
	r := NewRouter("bot")
	r.Register(Command{
		Name:    "quiet",
		Handler: func(*Context, string) string { return "" },
	})
	reply := r.Dispatch("alice", "bot", "quiet")
	if reply != nil {
		t.Errorf("expected nil reply for empty handler return, got %+v", reply)
	}
}

func TestLeadingTrailingWhitespace(t *testing.T) {
	r := testRouter()
	reply := r.Dispatch("alice", "scroll", "  status  ")
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if reply.Text != "ok" {
		t.Errorf("text = %q, want %q", reply.Text, "ok")
	}
}
