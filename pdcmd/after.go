package pdcmd

import (
	"context"
	"fmt"

	"github.com/lesomnus/xli"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// Call is one call a command of the tree made, as [Tree.After] hands it over.
type Call struct {
	// Conn is the connection the call was made on, and it is still open: the
	// context `f` is handed is the command's, with the caller on it, so a
	// second call on Conn is asked of the same server as the same caller.
	Conn grpc.ClientConnInterface

	// Method is the method called, in full: "/app.RobotService/Add".
	Method string

	// Sent is the request as it went, the arguments and the trailing JSON
	// merged, and Answered is what came back. Both are the messages the call
	// used and not copies: `f` reads them and does not change them.
	Sent     proto.Message
	Answered proto.Message
}

// After has `f` called once the command at path has made its call, the call
// succeeded and its answer was printed.
//
//	t.After("role/add", func(ctx context.Context, c pdcmd.Call) {
//		if wide(c.Answered.(*app.Role)) {
//			fmt.Fprintln(os.Stderr, "note: this role hands over more than it reads as")
//		}
//	})
//
// It is for the line an app has to say about one of the tree's own commands --
// a note after a write -- where replacing the command to say it would be writing
// the command again. The path is read as every other path here is, by name or
// alias at each step, and [Tree.Alias] and [Tree.Hide] keep it: what was said is
// of the command and not of the words it was reached by.
//
// # It does not change the command's answer
//
// `f` returns nothing. The call was made, and what an app says after it is not
// that call failing: a write that was made and then reported as failed is a
// write somebody makes twice. So the command exits as it would have, and `f`
// writes where it likes -- a note goes to stderr, where it does not join an
// answer somebody is piping.
//
// It is not called when nothing was sent -- a flag that is none, an `-o` that is
// no format, a `--help` -- nor when the server refused the call.
//
// # What is refused
//
// Said at the call that would mean it, naming the path: a path that is none; a
// stream, `watch`, which has no one answer to be after; a group; and a command
// the app put there itself with [Tree.Add] or [Tree.Replace], which is the app's
// and does what it likes after its own call. A command [Tree.Unary] made is the
// tree's in this sense, wherever the app added it. A second After on one command
// is refused too: one function, and an app that wants two things done writes
// both in it.
//
// It is said before [Tree.Mount], as [Tree.Alias] is: the tree is finished
// there, and every method that would change what it does refuses after it.
func (t *Tree) After(path string, f func(ctx context.Context, call Call)) error {
	if err := t.mayChange(path); err != nil {
		return err
	}
	if f == nil {
		return fmt.Errorf("pdcmd: %s: nothing to call", path)
	}

	_, c := t.find(path)
	if c == nil {
		return fmt.Errorf("pdcmd: %s: nothing to run after", path)
	}

	m, ok := t.made[c]
	switch {
	case ok && m.stream:
		return fmt.Errorf("pdcmd: %s: is a stream, and After is for a command that makes one call", path)
	case ok && m.group, len(c.Commands) > 0:
		return fmt.Errorf("pdcmd: %s: is a group, and After is for a command that makes one call", path)
	case !ok:
		return fmt.Errorf("pdcmd: %s: is the app's own command, and does what it likes after its own call", path)
	case m.after.f != nil:
		return fmt.Errorf("pdcmd: %s: has a function to call after it already; one function does both", path)
	}

	m.after.f = f
	return nil
}

// made is what the tree knows of a command it built: which kind, and for one
// that makes one call, where [Tree.After] puts its function.
//
// Kept by the command rather than by its path, for the reason [Tree.top] is
// walked: a path has many spellings, and a command keeps what was said of it
// whichever it is reached by.
type made struct {
	stream bool
	group  bool
	after  *after
}

// after is the function [Tree.After] gave one command, nil until it does. The
// command's handler holds the same one, so that it is read when the command
// runs and not when it was built.
type after struct {
	f func(ctx context.Context, call Call)
}

// unary is a command the tree built that makes one call, and records it.
func (t *Tree) unary(c *xli.Command, a *after) *xli.Command {
	t.record(c, &made{after: a})
	return c
}

// record keeps what the tree built. The map is made here rather than only in
// [NewIn], so that a Tree not made there -- this package's own tests build one
// as a literal -- builds a [Tree.Unary] all the same.
func (t *Tree) record(c *xli.Command, m *made) {
	if t.made == nil {
		t.made = map[*xli.Command]*made{}
	}

	t.made[c] = m
}

// tell calls the function, if there is one. Nil-safe, so a command built with
// nowhere to be told is the same code.
func (a *after) tell(ctx context.Context, call Call) {
	if a == nil || a.f == nil {
		return
	}

	a.f(ctx, call)
}
