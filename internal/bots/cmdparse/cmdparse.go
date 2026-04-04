// Package cmdparse provides a shared command framework for system bots.
//
// It handles three IRC input forms:
//   - DM: /msg botname COMMAND args
//   - Fantasy: !command args (in a channel)
//   - Addressed: botname: command args (in a channel)
//
// Bots register commands into a CommandRouter, which dispatches incoming
// messages and auto-generates HELP output.
package cmdparse

import (
	"fmt"
	"sort"
	"strings"
)

// HandlerFunc is called when a command is matched.
// args is everything after the command name, already trimmed.
// Returns the text to send back (may be multi-line).
type HandlerFunc func(ctx *Context, args string) string

// Command describes a single bot command.
type Command struct {
	// Name is the canonical command name (case-insensitive matching).
	Name string
	// Usage is a one-line usage string shown in help, e.g. "replay #channel [last=N]".
	Usage string
	// Description is a short description shown in help.
	Description string
	// Handler is called when the command is matched.
	Handler HandlerFunc
}

// Context carries information about the parsed incoming message.
type Context struct {
	// Nick is the sender's IRC nick.
	Nick string
	// Channel is the channel the message was sent in, or "" for a DM.
	Channel string
	// IsDM is true when the message was sent as a private message.
	IsDM bool
}

// Reply is a response produced by the router after dispatching a message.
type Reply struct {
	// Target is where the reply should be sent (nick for DM, channel for channel).
	Target string
	// Text is the reply text (may be multi-line).
	Text string
}

// CommandRouter dispatches IRC messages to registered command handlers.
type CommandRouter struct {
	botNick  string
	commands map[string]*Command // lowercase name → command
}

// NewRouter creates a CommandRouter for a bot with the given IRC nick.
func NewRouter(botNick string) *CommandRouter {
	return &CommandRouter{
		botNick:  strings.ToLower(botNick),
		commands: make(map[string]*Command),
	}
}

// Register adds a command to the router. Panics if name is empty or duplicate.
func (r *CommandRouter) Register(cmd Command) {
	key := strings.ToLower(cmd.Name)
	if key == "" {
		panic("cmdparse: command name must not be empty")
	}
	if _, ok := r.commands[key]; ok {
		panic(fmt.Sprintf("cmdparse: duplicate command %q", cmd.Name))
	}
	r.commands[key] = &cmd
}

// Dispatch parses an IRC message and dispatches it to the appropriate handler.
// Returns nil if the message is not a command addressed to this bot.
//
// Parameters:
//   - nick: sender's IRC nick
//   - target: IRC target param (channel name for channel messages, bot nick for DMs)
//   - text: the message body
func (r *CommandRouter) Dispatch(nick, target, text string) *Reply {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	var ctx Context
	ctx.Nick = nick

	isChannel := strings.HasPrefix(target, "#")

	var cmdLine string

	if !isChannel {
		// DM — entire text is the command line.
		ctx.IsDM = true
		cmdLine = text
	} else {
		ctx.Channel = target
		if strings.HasPrefix(text, "!") {
			// Fantasy: !command args
			cmdLine = text[1:]
		} else if r.isAddressed(text) {
			// Addressed: botname: command args
			cmdLine = r.stripAddress(text)
		} else {
			return nil
		}
	}

	cmdLine = strings.TrimSpace(cmdLine)
	if cmdLine == "" {
		return nil
	}

	cmdName, args := splitFirst(cmdLine)
	cmdKey := strings.ToLower(cmdName)

	// Built-in HELP.
	if cmdKey == "help" {
		return r.helpReply(&ctx, args)
	}

	cmd, ok := r.commands[cmdKey]
	if !ok {
		return r.unknownReply(&ctx, cmdName)
	}

	response := cmd.Handler(&ctx, args)
	if response == "" {
		return nil
	}

	return &Reply{
		Target: r.replyTarget(&ctx),
		Text:   response,
	}
}

func splitFirst(s string) (first, rest string) {
	s = strings.TrimSpace(s)
	idx := strings.IndexAny(s, " \t")
	if idx < 0 {
		return s, ""
	}
	return s[:idx], strings.TrimSpace(s[idx+1:])
}

func (r *CommandRouter) isAddressed(text string) bool {
	lower := strings.ToLower(text)
	for _, sep := range []string{": ", ","} {
		prefix := r.botNick + sep
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	// Also handle "botname:" with no space after colon.
	if strings.HasPrefix(lower, r.botNick+":") {
		return true
	}
	return false
}

func (r *CommandRouter) stripAddress(text string) string {
	lower := strings.ToLower(text)
	for _, sep := range []string{": ", ","} {
		prefix := r.botNick + sep
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(text[len(prefix):])
		}
	}
	// Bare "botname:" with no space.
	prefix := r.botNick + ":"
	if strings.HasPrefix(lower, prefix) {
		return strings.TrimSpace(text[len(prefix):])
	}
	return text
}

func (r *CommandRouter) replyTarget(ctx *Context) string {
	if ctx.IsDM {
		return ctx.Nick
	}
	return ctx.Channel
}

func (r *CommandRouter) helpReply(ctx *Context, args string) *Reply {
	args = strings.TrimSpace(args)

	if args != "" {
		cmdKey := strings.ToLower(args)
		cmd, ok := r.commands[cmdKey]
		if !ok {
			return &Reply{
				Target: r.replyTarget(ctx),
				Text:   fmt.Sprintf("unknown command %q — type HELP for a list of commands", args),
			}
		}
		return &Reply{
			Target: r.replyTarget(ctx),
			Text:   fmt.Sprintf("%s — %s\nusage: %s", strings.ToUpper(cmd.Name), cmd.Description, cmd.Usage),
		}
	}

	names := make([]string, 0, len(r.commands))
	for k := range r.commands {
		names = append(names, k)
	}
	sort.Strings(names)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("commands for %s:\n", r.botNick))
	for _, name := range names {
		cmd := r.commands[name]
		sb.WriteString(fmt.Sprintf("  %-12s %s\n", strings.ToUpper(cmd.Name), cmd.Description))
	}
	sb.WriteString("type HELP <command> for details")

	return &Reply{
		Target: r.replyTarget(ctx),
		Text:   sb.String(),
	}
}

func (r *CommandRouter) unknownReply(ctx *Context, cmdName string) *Reply {
	names := make([]string, 0, len(r.commands))
	for k := range r.commands {
		names = append(names, strings.ToUpper(k))
	}
	sort.Strings(names)

	return &Reply{
		Target: r.replyTarget(ctx),
		Text: fmt.Sprintf("unknown command %q — available commands: %s. Type HELP for details.",
			cmdName, strings.Join(names, ", ")),
	}
}
