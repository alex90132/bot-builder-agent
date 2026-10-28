package router

import (
	"strings"
	"testing"

	"bot-builder-agent/internal/telegram/ui"
)

func TestCommandsMatchButtons(t *testing.T) {
	base := Input{State: StateMain, HasToken: true, Allowed: true, ActiveProject: 4}
	pairs := []struct {
		cmd, button string
		kind        Kind
	}{
		{"/help", "", KindReply},
		{"/status", BtnStatus, KindShowStatus},
		{"/projects", BtnProjects, KindListProjects},
		{"/bots", BtnBots, KindListBots},
		{"/reset", BtnReset, KindResetHistory},
		{"/logout", BtnLogout, KindAskLogout},
	}
	for _, p := range pairs {
		fromCmd := Handle(withText(base, p.cmd))
		if fromCmd.Kind != p.kind {
			t.Fatalf("%s -> %s", p.cmd, fromCmd.Kind)
		}
		if p.button == "" {
			continue
		}
		fromBtn := Handle(withText(base, p.button))
		if fromBtn.Kind != fromCmd.Kind {
			t.Fatalf("%s и %s разошлись: %s vs %s", p.cmd, p.button, fromCmd.Kind, fromBtn.Kind)
		}
	}
	tokenCmd := Handle(withText(base, "/token mcp_abcdef"))
	if tokenCmd.Kind != KindSaveMCPToken || tokenCmd.Payload != "mcp_abcdef" || tokenCmd.Endpoint != "" {
		t.Fatalf("/token %+v", tokenCmd)
	}
	if Handle(withText(base, "/cancel")).Next != StateMain {
		t.Fatal("/cancel")
	}
	start := Handle(withText(base, "/start"))
	if start.Kind != KindReply || start.Next != StateMain {
		t.Fatal("/start")
	}
}

func TestMenuTextIsNotAgentTask(t *testing.T) {
	in := Input{State: StateChat, HasToken: true, Allowed: true, Text: BtnProjects}
	res := Handle(in)
	if res.Kind == KindAskAgent {
		t.Fatal("кнопка ушла в агента")
	}
	task := Handle(Input{State: StateChat, HasToken: true, Allowed: true, Text: "собери бота с /start"})
	if task.Kind != KindAskAgent || task.Payload != "собери бота с /start" {
		t.Fatalf("%+v", task)
	}
	if Handle(Input{State: StateMain, HasToken: false, Allowed: true, Text: "привет"}).Kind != KindAskAgent {
		t.Fatal("без токена конструктора задача всё равно уходит агенту")
	}
}

func TestFreeTextAsksAgentFromMenuScreens(t *testing.T) {
	text := "собери магазин"
	for _, state := range []State{StateMain, StateAccount, StateProjects, StateBots} {
		res := Handle(Input{State: state, HasToken: true, Allowed: true, Text: text})
		if res.Kind != KindAskAgent || res.Payload != text || res.Next != StateChat {
			t.Fatalf("%s: %+v", state, res)
		}
	}
	account := Handle(Input{State: StateAccount, HasToken: false, Allowed: true, Text: text})
	if account.Kind != KindAskAgent {
		t.Fatalf("без токена конструктора: %+v", account)
	}
	created := Handle(Input{State: StateWaitProjectName, HasToken: true, Allowed: true, Text: "Магазин"})
	if created.Kind != KindCreateProject || created.Payload != "Магазин" || created.Next != StateProjects {
		t.Fatalf("имя проекта: %+v", created)
	}
}

func TestAllowlistAndTokenWait(t *testing.T) {
	if Handle(Input{Allowed: false, Text: "/start"}).Kind != KindNotAllowed {
		t.Fatal("allowlist")
	}
	wait := Handle(Input{State: StateWaitMCP, Allowed: true, Text: "не-токен-но-текст"})
	if wait.Kind != KindReply || wait.Next != StateWaitMCP {
		t.Fatalf("ожидание токена: %+v", wait)
	}
	snippet := "```json\n{\"mcpServers\":{\"botcraft-builder\":{\"url\":\"https://telegram-bot-builder-e3u-production.up.railway.app/mcp\",\"headers\":{\"Authorization\":\"Bearer mcp_exampletoken\"}}}}\n```"
	saved := Handle(Input{State: StateChat, Allowed: true, HasToken: true, Text: snippet})
	if saved.Kind != KindSaveMCPToken || saved.Payload != "mcp_exampletoken" {
		t.Fatalf("блок mcpServers: %+v", saved)
	}
	if saved.Endpoint != "https://telegram-bot-builder-e3u-production.up.railway.app/mcp" {
		t.Fatalf("адрес: %s", saved.Endpoint)
	}
	if Handle(Input{State: StateWaitBotToken, Allowed: true, HasToken: true, ActiveProject: 1, Text: "hello"}).Kind == KindAddBotToken {
		t.Fatal("без двоеточия это не токен BotFather")
	}
	add := Handle(Input{State: StateWaitBotToken, Allowed: true, HasToken: true, ActiveProject: 9, Text: "123:ABC"})
	if add.Kind != KindAddBotToken || add.ProjectID != 9 {
		t.Fatalf("%+v", add)
	}
	photo := Handle(Input{State: StateMain, Allowed: true, HasToken: true, HasImage: true})
	if photo.Kind != KindAskAgent || photo.Payload != "Что на изображении?" {
		t.Fatalf("фото: %+v", photo)
	}
	bare := Handle(Input{State: StateProjects, Allowed: true, HasToken: true, Text: "mcp_zzzzzzzz"})
	if bare.Kind != KindSaveMCPToken {
		t.Fatal("голый mcp-токен")
	}
	several := `{"mcpServers":{"notes":{"url":"https://notes.example.com/mcp?x=1"},"docs":{"url":"https://docs.example.com/v1","headers":{"Authorization":"Bearer plain-token"}}}}`
	imported := Handle(Input{State: StateMain, Allowed: true, Text: several})
	if imported.Kind != KindImportServers || imported.Payload != several {
		t.Fatalf("импорт: %+v", imported)
	}
}

func TestConfirmsAndPaging(t *testing.T) {
	in := Input{State: StateProjects, Allowed: true, HasToken: true, ActiveProject: 3}
	if Handle(withCB(in, "p:del:8")).Kind != KindAskDeleteProject {
		t.Fatal("delete ask")
	}
	if Handle(withCB(in, "yes:pdel:8")).Kind != KindDeleteProject {
		t.Fatal("delete yes")
	}
	if Handle(withCB(in, "no:pdel:8")).Kind != KindReply {
		t.Fatal("delete no")
	}
	stop := Handle(withCB(Input{State: StateBots, Allowed: true, HasToken: true}, "b:stop:3:4"))
	if stop.Kind != KindAskStopBot || stop.TokenID != 4 {
		t.Fatalf("%+v", stop)
	}
	if Handle(withCB(in, "yes:stop:3:4")).Kind != KindStopBot {
		t.Fatal("stop yes")
	}
	ret := Handle(withCB(in, "b:ret:3:4:90"))
	if ret.Kind != KindSetRetention || ret.Days != 90 {
		t.Fatalf("%+v", ret)
	}
	open := Handle(withCB(in, "p:open:15"))
	if open.Kind != KindOpenProject || open.ProjectID != 15 {
		t.Fatalf("%+v", open)
	}
	arch := Handle(withText(in, BtnArchive))
	if arch.Kind != KindListProjects || !arch.Archived {
		t.Fatalf("%+v", arch)
	}
	if Handle(Input{State: StateBots, Allowed: true, HasToken: true, ActiveProject: 0, Text: BtnBots}).Kind != KindListProjects {
		t.Fatal("боты без проекта ведут в проекты")
	}
	busy := Handle(Input{Allowed: true, Busy: true, Text: "ещё"})
	if busy.Kind != KindBusy {
		t.Fatal("busy")
	}
	if Handle(Input{Allowed: true, Busy: true, Callback: "run:stop"}).Kind != KindStopRun {
		t.Fatal("stop run")
	}
	if Handle(Input{Allowed: true, Busy: true, Callback: "yes:agent"}).Kind != KindBusy {
		t.Fatal("yes:agent больше не отдельное подтверждение")
	}
	if Handle(Input{Allowed: true, Busy: true, Callback: "no:agent"}).Kind != KindBusy {
		t.Fatal("no:agent больше не отдельное подтверждение")
	}
	ren := Handle(Input{State: StateWaitRename, Allowed: true, HasToken: true, RenameProjectID: 6, Text: "Новое"})
	if ren.Kind != KindRenameProject || ren.ProjectID != 6 || ren.Payload != "Новое" {
		t.Fatalf("%+v", ren)
	}
}

func TestMenuCallbackReturnsMainActions(t *testing.T) {
	kb, ok := ui.ActionsForMenu("menu")
	if !ok {
		t.Fatal("menu")
	}
	base := Input{State: StateChat, HasToken: true, Allowed: true, ActiveProject: 4, Group: true}
	opened := Handle(withCB(base, "menu"))
	if opened.Kind != KindOpenMenu || opened.Payload != "main" {
		t.Fatalf("открытие меню: %+v", opened)
	}
	for _, data := range ui.CallbacksOf(kb) {
		got := Handle(withCB(base, data))
		switch data {
		case "nav:task":
			if got.Kind != KindReply || got.Next != StateChat {
				t.Fatalf("задача: %+v", got)
			}
			low := strings.ToLower(got.Text)
			if !strings.Contains(low, "ответ") || !strings.Contains(low, "упомян") {
				t.Fatalf("в группе задача должна просить ответ или упоминание: %q", got.Text)
			}
			if got.Kind == KindAskAgent {
				t.Fatal("задача в группе ушла в агента")
			}
		case "nav:projects":
			sameKind(t, got, Handle(withText(base, ui.BtnProjects)))
		case "nav:bots":
			sameKind(t, got, Handle(withText(base, ui.BtnBots)))
		case "nav:account":
			sameKind(t, got, Handle(withText(base, ui.BtnAccount)))
		default:
			t.Fatalf("лишняя кнопка главного меню %s", data)
		}
	}
	account, ok := ui.ActionsForMenu("menu:account")
	if !ok {
		t.Fatal("account")
	}
	for _, data := range ui.CallbacksOf(account) {
		got := Handle(withCB(base, data))
		label := map[string]string{
			"nav:servers": ui.BtnServers,
			"nav:token":   ui.BtnToken,
			"nav:status":  ui.BtnStatus,
			"nav:reset":   ui.BtnReset,
			"nav:logout":  ui.BtnLogout,
			"nav:back":    ui.BtnBack,
		}[data]
		if label == "" {
			t.Fatalf("лишняя кнопка аккаунта %s", data)
		}
		sameKind(t, got, Handle(withText(base, label)))
	}
	if Handle(withCB(base, "nav:archive")).Archived != Handle(withText(base, ui.BtnArchive)).Archived {
		t.Fatal("архив")
	}
	if Handle(withCB(base, "nav:create")).Kind != Handle(withText(base, ui.BtnCreate)).Kind {
		t.Fatal("создать")
	}
	if Handle(withCB(base, "nav:connect")).Kind != KindAskBotToken {
		t.Fatal("подключить")
	}
}

func TestPrivateTaskMenuAsksToType(t *testing.T) {
	in := Input{State: StateMain, HasToken: true, Allowed: true}
	got := Handle(withCB(in, "nav:task"))
	want := Handle(withText(in, ui.BtnTask))
	if got.Kind != want.Kind || got.Next != want.Next || got.Text != want.Text {
		t.Fatalf("личка: %+v, кнопка: %+v", got, want)
	}
	if strings.Contains(strings.ToLower(got.Text), "упомян") || strings.Contains(strings.ToLower(got.Text), "ответ") {
		t.Fatalf("личке нельзя про упоминание: %q", got.Text)
	}
	next := Handle(Input{State: got.Next, HasToken: true, Allowed: true, Text: "собери бота"})
	if next.Kind != KindAskAgent || next.Payload != "собери бота" {
		t.Fatalf("следующий текст должен уйти агенту: %+v", next)
	}
}

func sameKind(t *testing.T, got, want Result) {
	t.Helper()
	if got.Kind != want.Kind || got.Next != want.Next {
		t.Fatalf("%+v vs %+v", got, want)
	}
}

func withText(in Input, text string) Input {
	in.Text = text
	in.Callback = ""
	return in
}

func withCB(in Input, data string) Input {
	in.Callback = data
	in.Text = ""
	return in
}

const (
	BtnStatus   = "📊 Статус"
	BtnProjects = "📁 Проекты"
	BtnBots     = "🤖 Боты"
	BtnReset    = "🧹 Сбросить диалог"
	BtnLogout   = "🚪 Выйти"
	BtnArchive  = "📦 Архив"
)
