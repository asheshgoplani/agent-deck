package core

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type echoIn struct {
	Text string `json:"text"`
}

type echoOut struct {
	Text string `json:"text"`
}

func echoDef(id string, cli ...string) Def {
	return Def{
		ID: id, CLI: cli, Class: Query, Remote: RemoteDeny,
		In: echoIn{}, Out: echoOut{},
		Exec: Typed(func(ctx context.Context, in echoIn) (echoOut, error) {
			return echoOut{Text: in.Text}, nil
		}),
	}
}

func TestRegisterRejectsInvalidDefs(t *testing.T) {
	cases := map[string]Def{
		"empty id":   {CLI: []string{"x"}, Class: Query, Remote: RemoteDeny, In: echoIn{}, Out: echoOut{}, Exec: echoDef("x").Exec},
		"nil exec":   {ID: "x", Class: Query, Remote: RemoteDeny, In: echoIn{}, Out: echoOut{}},
		"no in/out":  {ID: "x", Class: Query, Remote: RemoteDeny, Exec: echoDef("x").Exec},
		"bad class":  {ID: "x", Class: "write", Remote: RemoteDeny, In: echoIn{}, Out: echoOut{}, Exec: echoDef("x").Exec},
		"bad remote": {ID: "x", Class: Query, Remote: "maybe", In: echoIn{}, Out: echoOut{}, Exec: echoDef("x").Exec},
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			if err := NewRegistry().Register(d); err == nil {
				t.Fatalf("Register(%s) = nil, want error", name)
			}
		})
	}
}

func TestRegisterRejectsDuplicateIDAndCLIPath(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(echoDef("a.one", "a", "one")); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(echoDef("a.one", "a", "other")); err == nil || !strings.Contains(err.Error(), "duplicate id") {
		t.Fatalf("duplicate id: got %v", err)
	}
	if err := r.Register(echoDef("a.two", "a", "one")); err == nil || !strings.Contains(err.Error(), "already used by a.one") {
		t.Fatalf("duplicate CLI path: got %v", err)
	}
}

func TestLookupByIDAndCLIPath(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(echoDef("b.echo", "b", "echo"))
	r.MustRegister(echoDef("a.echo", "a", "echo"))
	if d, ok := r.Lookup("b.echo"); !ok || d.CLIPath() != "b echo" {
		t.Fatalf("Lookup = %+v, %v", d, ok)
	}
	if d, ok := r.LookupCLI("a", "echo"); !ok || d.ID != "a.echo" {
		t.Fatalf("LookupCLI = %+v, %v", d, ok)
	}
	if _, ok := r.LookupCLI("c", "echo"); ok {
		t.Fatal("LookupCLI found an unregistered path")
	}
	var ids []string
	for _, d := range r.Defs() {
		ids = append(ids, d.ID)
	}
	if !reflect.DeepEqual(ids, []string{"a.echo", "b.echo"}) {
		t.Fatalf("Defs order = %v", ids)
	}
}

func TestRunChecksInputTypeAndUnknownID(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(echoDef("e.echo", "e"))

	res := r.Run(context.Background(), "e.echo", echoIn{Text: "hi"})
	if res.Err != nil || res.Out.(echoOut).Text != "hi" {
		t.Fatalf("value input: %+v", res)
	}
	res = r.Run(context.Background(), "e.echo", &echoIn{Text: "ptr"})
	if res.Err != nil || res.Out.(echoOut).Text != "ptr" {
		t.Fatalf("pointer input: %+v", res)
	}
	res = r.Run(context.Background(), "e.echo", "wrong")
	if ce := AsError(res.Err); ce == nil || ce.Code != CodeInvalidInput {
		t.Fatalf("wrong input type: %v", res.Err)
	}
	res = r.Run(context.Background(), "nope", echoIn{})
	if ce := AsError(res.Err); ce == nil || ce.Code != CodeUnknownCommand {
		t.Fatalf("unknown id: %v", res.Err)
	}
}

func TestInvokeReturnsTypedOutput(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(echoDef("e.echo"))
	out, res := Invoke[echoOut](context.Background(), r, "e.echo", echoIn{Text: "x"})
	if res.Err != nil || out.Text != "x" {
		t.Fatalf("Invoke = %+v, %v", out, res.Err)
	}
	_, res = Invoke[echoIn](context.Background(), r, "e.echo", echoIn{})
	if ce := AsError(res.Err); ce == nil || ce.Code != CodeInternal {
		t.Fatalf("Invoke with wrong Out type: %v", res.Err)
	}
}

func TestRemoteAllowed(t *testing.T) {
	r := NewRegistry()
	allow := echoDef("r.allow")
	allow.Remote = RemoteAllow
	r.MustRegister(allow)
	r.MustRegister(echoDef("r.deny"))
	if !r.RemoteAllowed("r.allow") || r.RemoteAllowed("r.deny") || r.RemoteAllowed("r.unknown") {
		t.Fatal("RemoteAllowed policy mismatch")
	}
}

func TestWarnEmitAndAfterFunc(t *testing.T) {
	r := NewRegistry()
	var order []string
	r.MustRegister(Def{
		ID: "w.cmd", Class: Mutate, Remote: RemoteDeny, In: echoIn{}, Out: echoOut{},
		Exec: Typed(func(ctx context.Context, in echoIn) (echoOut, error) {
			Warn(ctx, "careful")
			Emit(ctx, Event{Kind: EventRestartBegin, Title: in.Text})
			AfterFunc(ctx, func() { order = append(order, "after") })
			order = append(order, "exec")
			return echoOut{}, nil
		}),
	})
	var events []Event
	ctx := WithObserver(context.Background(), func(ev Event) { events = append(events, ev) })
	res := r.Run(ctx, "w.cmd", echoIn{Text: "t"})
	if len(events) != 1 || events[0].Title != "t" {
		t.Fatalf("events = %+v", events)
	}
	if !reflect.DeepEqual(res.Warnings, []string{"careful"}) {
		t.Fatalf("warnings = %v", res.Warnings)
	}
	if !reflect.DeepEqual(order, []string{"exec"}) {
		t.Fatalf("after-func ran before Finish: %v", order)
	}
	res.Finish()
	res.Finish()
	if !reflect.DeepEqual(order, []string{"exec", "after"}) {
		t.Fatalf("Finish order = %v", order)
	}
}

func TestAfterFuncOutsideRunRunsImmediately(t *testing.T) {
	ran := false
	AfterFunc(context.Background(), func() { ran = true })
	if !ran {
		t.Fatal("AfterFunc outside Run did not run")
	}
	Emit(context.Background(), Event{}) // no observer: must not panic
	Warn(context.Background(), "x")     // no run state: must not panic
}

func TestAsErrorWrapsPlainErrors(t *testing.T) {
	plain := errors.New("boom")
	ce := AsError(plain)
	if ce.Code != CodeInternal || ce.Message != "boom" || !errors.Is(ce, plain) {
		t.Fatalf("AsError = %+v", ce)
	}
	coded := Errorf(CodeNotFound, "x %d", 1)
	if AsError(coded) != coded || coded.Error() != "x 1" {
		t.Fatal("AsError must return a *Error unchanged")
	}
	if AsError(nil) != nil {
		t.Fatal("AsError(nil) != nil")
	}
}

func TestBuiltinsRegisterFiveCommands(t *testing.T) {
	r := NewRegistry()
	if err := RegisterBuiltins(r, Deps{}); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		cli   string
		class Class
	}{
		IDGroupList:      {"group list", Query},
		IDSessionList:    {"list", Query},
		IDSessionRestart: {"session restart", Mutate},
		IDSessionStart:   {"session start", Mutate},
		IDSessionStop:    {"session stop", Mutate},
	}
	defs := r.Defs()
	if len(defs) != len(want) {
		t.Fatalf("registered %d commands, want %d", len(defs), len(want))
	}
	for _, d := range defs {
		w, ok := want[d.ID]
		if !ok {
			t.Fatalf("unexpected command %s", d.ID)
		}
		if d.CLIPath() != w.cli || d.Class != w.class || d.Remote != RemoteAllow {
			t.Errorf("%s: cli=%q class=%s remote=%s", d.ID, d.CLIPath(), d.Class, d.Remote)
		}
	}
}
