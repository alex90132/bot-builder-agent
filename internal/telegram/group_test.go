package telegram

import (
	"context"
	"testing"
	"unicode/utf16"

	"github.com/mymmrac/telego"

	"bot-builder-agent/internal/agent"
	"bot-builder-agent/internal/openrouter"
)

func TestDirectedAtGroup(t *testing.T) {
	me := telego.User{ID: 99, Username: "builder", IsBot: true}
	group := func(text string) *telego.Message {
		return &telego.Message{
			From: &telego.User{ID: 7},
			Chat: telego.Chat{ID: -100, Type: telego.ChatTypeGroup},
			Text: text,
		}
	}

	if !directedAt(&telego.Message{Chat: telego.Chat{ID: 7, Type: telego.ChatTypePrivate}, Text: "привет"}, me) {
		t.Fatal("личка")
	}
	if !directedAt(&telego.Message{Chat: telego.Chat{ID: 7}, Text: "привет"}, me) {
		t.Fatal("чат без типа, как в старых апдейтах")
	}
	if directedAt(group("привет всем"), me) {
		t.Fatal("обычный текст группы")
	}
	if directedAt(group(""), me) {
		t.Fatal("стикер или служебное сообщение")
	}
	if !directedAt(group("/start"), me) {
		t.Fatal("/start в группе")
	}
	if !directedAt(group("/help@BuIlDeR"), me) {
		t.Fatal("команда с нашим @")
	}
	if directedAt(group("/start@otherbot"), me) {
		t.Fatal("команда чужому боту")
	}

	mention := group("эй @builder сделай бота")
	mention.Entities = []telego.MessageEntity{{
		Type:   telego.EntityTypeMention,
		Offset: len(utf16.Encode([]rune("эй "))),
		Length: len("@builder"),
	}}
	if !directedAt(mention, me) {
		t.Fatal("упоминание")
	}

	named := group("смотри")
	named.Entities = []telego.MessageEntity{{
		Type: telego.EntityTypeTextMention,
		User: &telego.User{ID: me.ID, IsBot: true},
	}}
	if !directedAt(named, me) {
		t.Fatal("text_mention")
	}

	reply := group("поправь кнопку")
	reply.ReplyToMessage = &telego.Message{From: &telego.User{ID: me.ID, IsBot: true}}
	if !directedAt(reply, me) {
		t.Fatal("ответ боту")
	}
	other := group("поправь кнопку")
	other.ReplyToMessage = &telego.Message{From: &telego.User{ID: 5}}
	if directedAt(other, me) {
		t.Fatal("ответ не боту")
	}

	emoji := "привет 👍 @builder"
	// 👍 is one rune and two UTF-16 units. Offset must follow Telegram, not len().
	off := len(utf16.Encode([]rune("привет 👍 ")))
	wide := group(emoji)
	wide.Chat.Type = telego.ChatTypeSupergroup
	wide.Entities = []telego.MessageEntity{{
		Type:   telego.EntityTypeMention,
		Offset: off,
		Length: len("@builder"),
	}}
	if !directedAt(wide, me) {
		t.Fatal("упоминание после символа вне BMP")
	}
}

func TestGroupProcessSkipsPlainText(t *testing.T) {
	b, api, run := newTestBot(t)
	b.self = telego.User{ID: 99, Username: "builder", IsBot: true}
	var calls int
	run.fn = func(context.Context, agent.Request) (agent.Result, error) {
		calls++
		return agent.Result{Reply: "ок"}, nil
	}
	b.process(telego.Update{Message: &telego.Message{
		From: &telego.User{ID: 7},
		Chat: telego.Chat{ID: -100, Type: telego.ChatTypeSupergroup},
		Text: "просто болтаем",
	}})
	if calls != 0 || len(api.snapshot()) != 0 {
		t.Fatalf("бот ответил на чужую реплику: %+v", api.snapshot())
	}

	b.process(telego.Update{Message: &telego.Message{
		From: &telego.User{ID: 7},
		Chat: telego.Chat{ID: -100, Type: telego.ChatTypeGroup},
		Text: "@builder собери бота",
		Entities: []telego.MessageEntity{{
			Type:   telego.EntityTypeMention,
			Offset: 0,
			Length: len("@builder"),
		}},
	}})
	lines := api.snapshot()
	if len(lines) == 0 || lines[0].chat != -100 {
		t.Fatalf("на упоминание нет ответа в группу: %+v", lines)
	}
}

func TestGroupReplyRunsAgent(t *testing.T) {
	b, api, run := newTestBot(t)
	b.self = telego.User{ID: 99, Username: "builder", IsBot: true}
	if err := b.store.SaveToken(context.Background(), 7, "mcp_groupuser_xx"); err != nil {
		t.Fatal(err)
	}
	run.fn = func(_ context.Context, req agent.Request) (agent.Result, error) {
		return agent.Result{
			Reply:      "сделал",
			Transcript: []openrouter.Message{{Role: "user", Content: req.UserText}},
		}, nil
	}
	b.process(telego.Update{Message: &telego.Message{
		From: &telego.User{ID: 7},
		Chat: telego.Chat{ID: -100, Type: telego.ChatTypeGroup},
		Text: "добавь /start",
		ReplyToMessage: &telego.Message{
			From: &telego.User{ID: 99, IsBot: true, Username: "builder"},
		},
	}})
	if !hasText(api.snapshot(), -100, "сделал") {
		t.Fatalf("ответ на реплай: %+v", api.snapshot())
	}
}
