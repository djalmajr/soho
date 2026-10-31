package hcbackend

import (
	"context"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/hc"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

// TestSurfaceCoversEveryMethod covers every method of the four transport
// interfaces with the hc decision this slice made for it: a Window no-op
// (Valid, zero value, no error), an unsupported refusal (naming the method
// and "with transport=hc"), or the implemented behavior (recorded here;
// asserted by the dedicated tests). A method added to the contracts later
// without an hc decision must be added to this table, and the count check
// below keeps the table whole. No no-op or unsupported call may launch hc.
func TestSurfaceCoversEveryMethod(t *testing.T) {
	type outcome struct {
		valid bool
		kind  string
		msg   string
		zero  bool
	}
	kind := func(err *transport.Failure) string {
		if err == nil {
			return ""
		}
		return string(err.Kind)
	}
	msg := func(err *transport.Failure) string {
		if err == nil {
			return ""
		}
		return err.Message
	}

	// zeroView reports whether the view is the zero value (it holds a json
	// RawMessage, so it cannot be compared directly).
	zeroView := func(v transport.View) bool {
		return v.ID == "" && v.PaneID == "" && v.TabID == "" && v.WorkspaceID == "" && v.Label == "" && v.RootPane == "" && v.Revision == 0 && v.Raw == nil
	}

	const (
		wantNoOp        = "no-op"
		wantUnsupported = "unsupported"
		wantImplemented = "implemented"
	)

	entries := []struct {
		name string
		want string
		call func(b *backend) outcome
	}{
		// Transport.
		{"Transport.Caller", wantImplemented, nil},
		{
			"Transport.Current", wantUnsupported, func(b *backend) outcome {
				rep := b.Current(context.Background(), transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == (transport.Caller{})}
			},
		},
		{
			"Transport.CheckCaller", wantUnsupported, func(b *backend) outcome {
				rep := b.CheckCaller(context.Background(), transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == (transport.CallerCheck{})}
			},
		},
		{"Transport.List", wantImplemented, nil},
		{"Transport.Get", wantImplemented, nil},
		{
			"Transport.Send", wantUnsupported, func(b *backend) outcome {
				rep := b.Send(context.Background(), transport.Target{ID: "t7"}, transport.Send{Text: "x"}, transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{"Transport.Keys", wantImplemented, nil},
		{"Transport.Read", wantImplemented, nil},
		{"Transport.Close", wantImplemented, nil},

		// Window.
		{
			"Window.Layout", wantNoOp, func(b *backend) outcome {
				rep := b.Layout(context.Background(), "", transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == nil}
			},
		},
		{
			"Window.ListPanes", wantNoOp, func(b *backend) outcome {
				rep := b.ListPanes(context.Background(), transport.Scope{}, transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == nil}
			},
		},
		{
			"Window.ListTabs", wantNoOp, func(b *backend) outcome {
				rep := b.ListTabs(context.Background(), "ws", transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == nil}
			},
		},
		{
			"Window.Tab", wantNoOp, func(b *backend) outcome {
				rep := b.Tab(context.Background(), "tab1", transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: zeroView(rep.Value)}
			},
		},
		{
			"Window.CreateTab", wantUnsupported, func(b *backend) outcome {
				rep := b.CreateTab(context.Background(), transport.Open{}, transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: zeroView(rep.Value)}
			},
		},
		{
			"Window.RenameTab", wantNoOp, func(b *backend) outcome {
				rep := b.RenameTab(context.Background(), "tab1", "label", transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: !rep.Value}
			},
		},
		{
			"Window.Split", wantUnsupported, func(b *backend) outcome {
				rep := b.Split(context.Background(), transport.Open{}, transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: zeroView(rep.Value)}
			},
		},
		{
			"Window.Move", wantNoOp, func(b *backend) outcome {
				rep := b.Move(context.Background(), transport.Move{}, transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: zeroView(rep.Value)}
			},
		},
		{
			"Window.Focus", wantNoOp, func(b *backend) outcome {
				rep := b.Focus(context.Background(), transport.Focus{}, transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: !rep.Value}
			},
		},
		{
			"Window.Title", wantNoOp, func(b *backend) outcome {
				rep := b.Title(context.Background(), "pane1", nil, transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: !rep.Value}
			},
		},
		{
			"Window.Notify", wantNoOp, func(b *backend) outcome {
				rep := b.Notify(context.Background(), "title", "body", transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: !rep.Value}
			},
		},

		// LegacyTransport.
		{
			"Legacy.ProcessInfo", wantUnsupported, func(b *backend) outcome {
				rep := b.ProcessInfo(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == nil}
			},
		},
		{
			"Legacy.Rename", wantUnsupported, func(b *backend) outcome {
				rep := b.Rename(context.Background(), transport.Target{ID: "t7"}, "name", transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: !rep.Value}
			},
		},
		{"Legacy.AgentGetOK", wantImplemented, nil},
		{
			"Legacy.AgentGetKnown", wantUnsupported, func(b *backend) outcome {
				rep := b.AgentGetKnown(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: !rep.Value}
			},
		},
		{
			"Legacy.AgentReadRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.AgentReadRaw(context.Background(), transport.Target{ID: "t7"}, "visible", "40", transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.AgentListRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.AgentListRaw(context.Background(), transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == nil}
			},
		},
		{
			"Legacy.AgentGetRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.AgentGetRaw(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.TabGetRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.TabGetRaw(context.Background(), "tab1", transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == (transport.TabFields{})}
			},
		},
		{
			"Legacy.PaneCurrentRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.PaneCurrentRaw(context.Background(), transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.PaneGetRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.PaneGetRaw(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.ClaudeTranscriptPath", wantUnsupported, func(b *backend) outcome {
				rep := b.ClaudeTranscriptPath(context.Background(), transport.Target{ID: "t7"})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.PiSessionPath", wantUnsupported, func(b *backend) outcome {
				rep := b.PiSessionPath(context.Background(), transport.Target{ID: "t7"})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.WorkspaceCreateRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.WorkspaceCreateRaw(context.Background(), "cwd", "label", nil, transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.WorkspaceCloseRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.WorkspaceCloseRaw(context.Background(), "ws1", transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.WorkspaceListRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.WorkspaceListRaw(context.Background(), transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.PaneListRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.PaneListRaw(context.Background(), "ws1", transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.PaneRunRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.PaneRunRaw(context.Background(), "pane1", nil, transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.AgentGetMachineRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.AgentGetMachineRaw(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.AgentWaitRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.AgentWaitRaw(context.Background(), transport.Target{ID: "t7"}, 1000, transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.AgentPromptWaitRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.AgentPromptWaitRaw(context.Background(), transport.Target{ID: "t7"}, "body", 1000, transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.AgentPromptRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.AgentPromptRaw(context.Background(), transport.Target{ID: "t7"}, "body", transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.AgentReadSourceRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.AgentReadSourceRaw(context.Background(), transport.Target{ID: "t7"}, "visible", nil, transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.AgentSendKeysRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.AgentSendKeysRaw(context.Background(), transport.Target{ID: "t7"}, "enter", transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: !rep.Value}
			},
		},
		{
			"Legacy.SnapshotRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.SnapshotRaw(context.Background(), transport.Target{}, transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.MachineListRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.MachineListRaw(context.Background(), transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.VersionRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.VersionRaw(context.Background(), transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.StatusServerRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.StatusServerRaw(context.Background(), transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.SkillRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.SkillRaw(context.Background(), transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.PluginListRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.PluginListRaw(context.Background(), transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.PluginInstallRefRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.PluginInstallRefRaw(context.Background(), "ref", transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.PluginConfigDirRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.PluginConfigDirRaw(context.Background(), "plugin1", transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.PluginRemoveRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.PluginRemoveRaw(context.Background(), "uninstall", "plugin1", transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{
			"Legacy.ServerReloadConfigRaw", wantUnsupported, func(b *backend) outcome {
				rep := b.ServerReloadConfigRaw(context.Background(), transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},

		// Runtime.
		{
			"Runtime.Start", wantUnsupported, func(b *backend) outcome {
				rep := b.Start(context.Background(), transport.Start{Name: "n"}, transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: rep.Value == ""}
			},
		},
		{"Runtime.CallerAgentName", wantImplemented, nil},
		{
			"Runtime.SendText", wantUnsupported, func(b *backend) outcome {
				rep := b.SendText(context.Background(), transport.Target{ID: "t7"}, "text", transport.Call{})
				return outcome{valid: rep.Valid, kind: kind(rep.Err), msg: msg(rep.Err), zero: !rep.Value}
			},
		},
		{"Runtime.GetGone", wantImplemented, nil},
	}

	// The contracts' five method sets (Transport 9, Window 11, LegacyTransport
	// 33, Runtime 4): keep the table whole when a contract changes.
	if len(entries) != 9+11+33+4 {
		t.Fatalf("table covers %d methods want %d", len(entries), 9+11+33+4)
	}

	var launches int
	env := platform.Env{"HC_TERMINAL_ID": "t1"}
	b := &backend{env: env, client: hc.New(env)}
	b.client.Launch = func(ctx context.Context, env platform.Env, args ...string) platform.RunResult {
		launches++
		zero := 0
		return platform.RunResult{Status: &zero, Stdout: `{}`}
	}

	for _, entry := range entries {
		entry := entry
		t.Run(entry.name, func(t *testing.T) {
			if entry.want == wantImplemented {
				// The implemented behavior is asserted by the dedicated
				// tests; the entry records the decision.
				return
			}
			outcome := entry.call(b)
			if entry.want == wantNoOp {
				if outcome.kind != "" {
					t.Fatalf("err %q (%s) want none", outcome.kind, outcome.msg)
				}
				if !outcome.valid {
					t.Fatal("not valid")
				}
				if !outcome.zero {
					t.Fatal("value is not the zero value")
				}
				return
			}
			if outcome.kind != string(transport.FailureUnsupported) {
				t.Fatalf("kind %q want unsupported", outcome.kind)
			}
			if outcome.valid {
				t.Fatal("valid on an unsupported refusal")
			}
			method := entry.name
			if i := strings.LastIndexByte(method, '.'); i >= 0 {
				method = method[i+1:]
			}
			if !strings.Contains(outcome.msg, method+" is not supported") {
				t.Fatalf("message %q does not name %s", outcome.msg, method)
			}
			if !strings.Contains(outcome.msg, "with transport=hc") {
				t.Fatalf("message %q lacks the transport", outcome.msg)
			}
		})
	}
	if launches != 0 {
		t.Fatalf("%d hc launch(es) from no-op or unsupported calls want 0", launches)
	}
}
