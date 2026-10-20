package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type histProvider struct{ seen [][]providers.Message }

func (p *histProvider) Chat(
	_ context.Context, messages []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any,
) (*providers.LLMResponse, error) {
	p.seen = append(p.seen, append([]providers.Message(nil), messages...))
	return &providers.LLMResponse{Content: "ok", ToolCalls: []providers.ToolCall{}}, nil
}

func (p *histProvider) GetDefaultModel() string { return "recording-model" }

func newGroupRoutingLoop(t *testing.T, channel string) (*AgentLoop, *histProvider) {
	t.Helper()
	ws := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{Workspace: ws, ModelName: "test-model"},
			List: []config.AgentConfig{
				{ID: "main", Default: true, Workspace: filepath.Join(ws, "main")},
				{ID: "grp", Workspace: filepath.Join(ws, "grp")},
			},
			Dispatch: &config.DispatchConfig{Rules: []config.DispatchRule{{
				Name:  "grp-chat",
				Agent: "grp",
				When:  config.DispatchSelector{Channel: channel, Chat: "group:1203630000000000@g.us"},
			}}},
		},
		Session: config.SessionConfig{Dimensions: []string{"chat"}},
	}
	p := &histProvider{}
	return NewAgentLoop(cfg, bus.NewMessageBus(), p), p
}

func TestRoutedAgentSeesOwnHistory(t *testing.T) {
	al, p := newGroupRoutingLoop(t, "whatsapp")
	msg := func(text string) bus.InboundMessage {
		return testInboundMessage(bus.InboundMessage{
			Context: bus.InboundContext{
				Channel: "whatsapp", ChatID: "1203630000000000@g.us", ChatType: "group", SenderID: "628000000001",
			},
			Content: text,
		})
	}
	for _, text := range []string{"first-turn-marker", "second turn"} {
		if _, err := al.processMessage(context.Background(), msg(text)); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.seen) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(p.seen))
	}
	var found bool
	for _, m := range p.seen[1] {
		found = found || strings.Contains(m.Content, "first-turn-marker")
	}
	if !found {
		t.Fatal("second turn did not see first turn's history of the routed agent")
	}
}

func TestDirectTurnToGroupTargetUsesGroupAgent(t *testing.T) {
	al, _ := newGroupRoutingLoop(t, "whatsapp")
	if _, err := al.ProcessDirectWithChannel(
		context.Background(), "cron-ping", "agent:cron-1-x", "whatsapp", "1203630000000000@g.us",
	); err != nil {
		t.Fatal(err)
	}
	grp, _ := al.registry.GetAgent("grp")
	main, _ := al.registry.GetAgent("main")
	if got := grp.Sessions.ListSessions(); len(got) == 0 {
		t.Fatal("group agent has no session after group-targeted direct turn")
	}
	for _, k := range main.Sessions.ListSessions() {
		if len(main.Sessions.GetHistory(k)) > 0 {
			t.Fatalf("default agent got history in session %q", k)
		}
	}
	for _, k := range grp.Sessions.ListSessions() {
		if strings.HasPrefix(k, "agent:cron-") {
			t.Fatalf("group turn used ephemeral cron session %q", k)
		}
	}
}

func TestSystemMessageToGroupTargetUsesGroupAgent(t *testing.T) {
	al, _ := newGroupRoutingLoop(t, "whatsapp")
	_, err := al.processSystemMessage(context.Background(), testInboundMessage(bus.InboundMessage{
		Context: bus.InboundContext{Channel: "system", ChatID: "whatsapp:1203630000000000@g.us", SenderID: "async:x"},
		Content: "done",
	}))
	if err != nil {
		t.Fatal(err)
	}
	grp, _ := al.registry.GetAgent("grp")
	var n int
	for _, k := range grp.Sessions.ListSessions() {
		n += len(grp.Sessions.GetHistory(k))
	}
	if n == 0 {
		t.Fatal("group agent session has no history after system message to group")
	}
}

func TestChatTypeForTarget(t *testing.T) {
	for _, tt := range []struct{ ch, id, want string }{
		{"whatsapp", "1203630000000000@g.us", "group"},
		{"whatsapp", "628000000000@s.whatsapp.net", "direct"},
		{"telegram", "-100123", "group"},
		{"telegram", "12345", "direct"},
		{"cli", "direct", "direct"},
	} {
		if got := chatTypeForTarget(tt.ch, tt.id); got != tt.want {
			t.Errorf("%s/%s = %s, want %s", tt.ch, tt.id, got, tt.want)
		}
	}
}

func TestPublishResponseIfNeeded_GroupCronDedupsAgainstRouteSession(t *testing.T) {
	const chat = "1203630000000000@g.us"
	for _, sent := range []bool{true, false} {
		al, _ := newGroupRoutingLoop(t, "whatsapp")
		cm := &recordingChannelManager{}
		al.channelManager = cm
		grp, _ := al.registry.GetAgent("grp")
		mt := tools.NewMessageTool()
		mt.SetSendCallback(func(context.Context, string, string, string, string, []bus.MediaPart) error { return nil })
		grp.Tools.Register(mt)

		msg := bus.InboundMessage{Context: bus.InboundContext{
			Channel: "whatsapp", ChatID: chat, ChatType: "group", SenderID: "cron",
		}}
		route, _, _ := al.resolveMessageRoute(msg)
		routeKey := al.allocateRouteSession(route, msg).SessionKey
		if sent {
			mt.Execute(tools.WithToolSessionContext(context.Background(), "grp", routeKey, nil),
				map[string]any{"content": "x", "channel": "whatsapp", "chat_id": chat})
		}
		al.PublishResponseIfNeeded(context.Background(), "whatsapp", chat, "agent:cron-1-x", "final")
		if got := len(cm.dismissed) == 1; got != sent {
			t.Fatalf("sent=%v: dismissed=%v", sent, cm.dismissed)
		}
	}
}
