package router

import (
	"strings"

	"bot-builder-agent/internal/mcp"
	"bot-builder-agent/internal/telegram/ui"
)

const accessHint = "Пришлите блок mcpServers из вкладки «Агент» или токен mcp_… одним сообщением."

// State — экран пользователя.
type State string

const (
	StateMain            State = "main"
	StateChat            State = "chat"
	StateAccount         State = "account"
	StateProjects        State = "projects"
	StateBots            State = "bots"
	StateWaitMCP         State = "wait_mcp"
	StateWaitProjectName State = "wait_project"
	StateWaitRename      State = "wait_rename"
	StateWaitBotToken    State = "wait_bot"
)

// Kind — что сделать адаптеру Telegram, не вызывая модель самостоятельно.
type Kind string

const (
	KindReply            Kind = "reply"
	KindOpenMenu         Kind = "open_menu"
	KindNotAllowed       Kind = "not_allowed"
	KindBusy             Kind = "busy"
	KindNeedAccount      Kind = "need_account"
	KindSaveMCPToken     Kind = "save_mcp_token"
	KindShowStatus       Kind = "show_status"
	KindResetHistory     Kind = "reset_history"
	KindAskLogout        Kind = "ask_logout"
	KindLogout           Kind = "logout"
	KindListProjects     Kind = "list_projects"
	KindOpenProject      Kind = "open_project"
	KindProjectSummary   Kind = "project_summary"
	KindAskProjectName   Kind = "ask_project_name"
	KindCreateProject    Kind = "create_project"
	KindAskRename        Kind = "ask_rename"
	KindRenameProject    Kind = "rename_project"
	KindArchiveProject   Kind = "archive_project"
	KindUnarchiveProject Kind = "unarchive_project"
	KindAskDeleteProject Kind = "ask_delete_project"
	KindDeleteProject    Kind = "delete_project"
	KindListBots         Kind = "list_bots"
	KindBotCard          Kind = "bot_card"
	KindBotStatus        Kind = "bot_status"
	KindStartBot         Kind = "start_bot"
	KindAskStopBot       Kind = "ask_stop_bot"
	KindStopBot          Kind = "stop_bot"
	KindRestartBot       Kind = "restart_bot"
	KindBotLogs          Kind = "bot_logs"
	KindAskDeleteBot     Kind = "ask_delete_bot"
	KindDeleteBot        Kind = "delete_bot"
	KindSetRetention     Kind = "set_retention"
	KindAskBotToken      Kind = "ask_bot_token"
	KindAddBotToken      Kind = "add_bot_token"
	KindAskAgent         Kind = "ask_agent"
	KindStopRun          Kind = "stop_run"
)

// Input — снимок экрана и входящее сообщение.
type Input struct {
	State           State
	Text            string
	Callback        string
	HasToken        bool
	HasImage        bool
	Allowed         bool
	Busy            bool
	Group           bool
	ActiveProject   int64
	RenameProjectID int64
}

// Result — одно действие и следующий экран.
type Result struct {
	Kind      Kind
	Next      State
	Text      string
	Payload   string
	Endpoint  string
	ProjectID int64
	TokenID   int64
	Page      int
	Days      int
	Archived  bool
}

// Handle разбирает команду, кнопку или callback. Текст кнопок меню не становится задачей агента.
func Handle(in Input) Result {
	in.Text = strings.TrimSpace(in.Text)
	if in.State == "" {
		in.State = StateMain
	}
	if !in.Allowed {
		return Result{Kind: KindNotAllowed, Next: in.State, Text: "Нет доступа."}
	}
	if in.Busy {
		return busy(in)
	}
	if in.Callback != "" {
		return fromCallback(in)
	}
	if cmd, args, ok := parseCommand(in.Text); ok {
		return fromCommand(in, cmd, args)
	}
	if ui.MenuText(in.Text) {
		return fromButton(in, in.Text)
	}
	if token, endpoint, ok := mcp.ParseAccess(in.Text); ok {
		return saveAccess(token, endpoint)
	}
	return fromFreeText(in)
}

func saveAccess(token, endpoint string) Result {
	return Result{Kind: KindSaveMCPToken, Next: StateAccount, Payload: token, Endpoint: endpoint, Text: "Токен сохранён."}
}

func busy(in Input) Result {
	if in.Callback != "" {
		cb, ok := ui.ParseCallback(in.Callback)
		if ok && cb.Name == "run_stop" {
			return Result{Kind: KindStopRun, Next: in.State, Text: "Останавливаю."}
		}
	}
	return Result{Kind: KindBusy, Next: in.State, Text: "Уже работаю над предыдущим запросом."}
}

func fromCommand(in Input, cmd, args string) Result {
	switch cmd {
	case "start":
		return Result{Kind: KindReply, Next: StateMain, Text: startText}
	case "help":
		return Result{Kind: KindReply, Next: StateMain, Text: helpText}
	case "token":
		if args == "" {
			return Result{Kind: KindReply, Next: StateWaitMCP, Text: accessHint}
		}
		if token, endpoint, ok := mcp.ParseAccess(args); ok {
			return saveAccess(token, endpoint)
		}
		return Result{Kind: KindReply, Next: StateWaitMCP, Text: accessHint}
	case "status":
		return Result{Kind: KindShowStatus, Next: StateAccount, Text: "Статус"}
	case "projects":
		return listProjects(in, false, 0)
	case "bots":
		return listBots(in)
	case "reset":
		return Result{Kind: KindResetHistory, Next: in.State, Text: "Диалог очищен."}
	case "logout":
		return Result{Kind: KindAskLogout, Next: in.State, Text: "Удалить сохранённый токен?"}
	case "cancel":
		return cancel(in)
	default:
		return Result{Kind: KindReply, Next: in.State, Text: "Неизвестная команда. Откройте /help."}
	}
}

func fromButton(in Input, label string) Result {
	switch label {
	case ui.BtnTask:
		return Result{Kind: KindReply, Next: StateChat, Text: "Опишите, какого бота собрать или что изменить."}
	case ui.BtnProjects, ui.BtnActive:
		return listProjects(in, false, 0)
	case ui.BtnArchive:
		return listProjects(in, true, 0)
	case ui.BtnBots:
		return listBots(in)
	case ui.BtnAccount:
		return Result{Kind: KindReply, Next: StateAccount, Text: "Аккаунт"}
	case ui.BtnToken:
		return Result{Kind: KindReply, Next: StateWaitMCP, Text: accessHint}
	case ui.BtnStatus:
		return Result{Kind: KindShowStatus, Next: StateAccount, Text: "Статус"}
	case ui.BtnReset:
		return Result{Kind: KindResetHistory, Next: StateAccount, Text: "Диалог очищен."}
	case ui.BtnLogout:
		return Result{Kind: KindAskLogout, Next: StateAccount, Text: "Удалить сохранённый токен?"}
	case ui.BtnCreate:
		if !in.HasToken {
			return needAccount(in)
		}
		return Result{Kind: KindAskProjectName, Next: StateWaitProjectName, Text: "Введите имя нового проекта."}
	case ui.BtnConnect:
		if !in.HasToken {
			return needAccount(in)
		}
		if in.ActiveProject <= 0 {
			return listProjects(in, false, 0)
		}
		return Result{Kind: KindAskBotToken, Next: StateWaitBotToken, Text: "Пришлите токен от @BotFather."}
	case ui.BtnBack, ui.BtnCancel:
		return cancel(in)
	default:
		return Result{Kind: KindReply, Next: in.State, Text: "Выберите кнопку меню."}
	}
}

func fromFreeText(in Input) Result {
	switch in.State {
	case StateWaitMCP:
		return Result{Kind: KindReply, Next: StateWaitMCP, Text: accessHint}
	case StateWaitProjectName:
		if in.Text == "" {
			return Result{Kind: KindReply, Next: StateWaitProjectName, Text: "Введите имя нового проекта."}
		}
		return Result{Kind: KindCreateProject, Next: StateProjects, Payload: in.Text}
	case StateWaitRename:
		if in.Text == "" || in.RenameProjectID <= 0 {
			return Result{Kind: KindReply, Next: StateProjects, Text: "Нечего переименовывать."}
		}
		return Result{Kind: KindRenameProject, Next: StateProjects, Payload: in.Text, ProjectID: in.RenameProjectID}
	case StateWaitBotToken:
		if !strings.Contains(in.Text, ":") {
			return Result{Kind: KindReply, Next: StateWaitBotToken, Text: "Нужен токен BotFather вида 123456:ABC…"}
		}
		return Result{Kind: KindAddBotToken, Next: StateBots, Payload: in.Text, ProjectID: in.ActiveProject}
	case StateMain, StateChat, StateAccount, StateProjects, StateBots:
		return askFromText(in)
	default:
		return Result{Kind: KindReply, Next: in.State, Text: "Выберите кнопку меню."}
	}
}

func askFromText(in Input) Result {
	if !in.HasToken {
		return needAccount(in)
	}
	if in.Text == "" {
		if in.HasImage {
			return Result{Kind: KindAskAgent, Next: StateChat, Payload: "Что на изображении?"}
		}
		return Result{Kind: KindReply, Next: StateChat, Text: "Опишите задачу текстом."}
	}
	return Result{Kind: KindAskAgent, Next: StateChat, Payload: in.Text, Text: in.Text}
}

func fromCallback(in Input) Result {
	cb, ok := ui.ParseCallback(in.Callback)
	if !ok {
		return Result{Kind: KindReply, Next: in.State, Text: "Не понял кнопку."}
	}
	if cb.Name == "menu" {
		return openMenu(in, cb.Screen)
	}
	if label, ok := navButton(cb.Name); ok {
		if cb.Name == "nav_task" && in.Group {
			return Result{Kind: KindReply, Next: StateChat, Text: groupTaskText}
		}
		return fromButton(in, label)
	}
	if cb.Name != "run_stop" && cb.Name != "logout" && !in.HasToken {
		return needAccount(in)
	}
	switch cb.Name {
	case "run_stop":
		return Result{Kind: KindReply, Next: in.State, Text: "Сейчас нечего останавливать."}
	case "logout":
		if !cb.Yes {
			return Result{Kind: KindReply, Next: StateAccount, Text: "Токен на месте."}
		}
		return Result{Kind: KindLogout, Next: StateAccount, Text: "Токен удалён."}
	case "list_projects":
		return listProjects(in, cb.Archived, cb.Page)
	case "new_project":
		return fromButton(in, ui.BtnCreate)
	case "open_project":
		if !in.HasToken {
			return needAccount(in)
		}
		return Result{Kind: KindOpenProject, Next: StateProjects, ProjectID: cb.ProjectID}
	case "summary":
		return Result{Kind: KindProjectSummary, Next: StateProjects, ProjectID: cb.ProjectID}
	case "rename":
		return Result{Kind: KindAskRename, Next: StateWaitRename, ProjectID: cb.ProjectID, Text: "Введите новое имя проекта."}
	case "archive":
		return Result{Kind: KindArchiveProject, Next: StateProjects, ProjectID: cb.ProjectID}
	case "unarchive":
		return Result{Kind: KindUnarchiveProject, Next: StateProjects, ProjectID: cb.ProjectID}
	case "ask_delete_project":
		return Result{Kind: KindAskDeleteProject, Next: StateProjects, ProjectID: cb.ProjectID, Text: "Удалить проект? Это необратимо."}
	case "delete_project":
		if !cb.Yes {
			return Result{Kind: KindReply, Next: StateProjects, Text: "Удаление отменено."}
		}
		return Result{Kind: KindDeleteProject, Next: StateProjects, ProjectID: cb.ProjectID}
	case "list_bots":
		return listBots(in)
	case "add_bot":
		return fromButton(in, ui.BtnConnect)
	case "bot_card":
		return Result{Kind: KindBotCard, Next: StateBots, ProjectID: cb.ProjectID, TokenID: cb.TokenID}
	case "bot_status":
		return Result{Kind: KindBotStatus, Next: StateBots, ProjectID: cb.ProjectID, TokenID: cb.TokenID}
	case "start_bot":
		return Result{Kind: KindStartBot, Next: StateBots, ProjectID: cb.ProjectID, TokenID: cb.TokenID}
	case "ask_stop_bot":
		return Result{Kind: KindAskStopBot, Next: StateBots, ProjectID: cb.ProjectID, TokenID: cb.TokenID, Text: "Остановить бота?"}
	case "stop_bot":
		if !cb.Yes {
			return Result{Kind: KindReply, Next: StateBots, Text: "Остановка отменена."}
		}
		return Result{Kind: KindStopBot, Next: StateBots, ProjectID: cb.ProjectID, TokenID: cb.TokenID}
	case "restart_bot":
		return Result{Kind: KindRestartBot, Next: StateBots, ProjectID: cb.ProjectID, TokenID: cb.TokenID}
	case "bot_logs":
		return Result{Kind: KindBotLogs, Next: StateBots, ProjectID: cb.ProjectID, TokenID: cb.TokenID}
	case "ask_delete_bot":
		return Result{Kind: KindAskDeleteBot, Next: StateBots, ProjectID: cb.ProjectID, TokenID: cb.TokenID, Text: "Удалить токен бота? Это необратимо."}
	case "delete_bot":
		if !cb.Yes {
			return Result{Kind: KindReply, Next: StateBots, Text: "Удаление токена отменено."}
		}
		return Result{Kind: KindDeleteBot, Next: StateBots, ProjectID: cb.ProjectID, TokenID: cb.TokenID}
	case "retention":
		return Result{Kind: KindSetRetention, Next: StateBots, ProjectID: cb.ProjectID, TokenID: cb.TokenID, Days: cb.Days}
	default:
		return Result{Kind: KindReply, Next: in.State, Text: "Не понял кнопку."}
	}
}

func openMenu(in Input, screen string) Result {
	next := in.State
	switch screen {
	case "account":
		next = StateAccount
	case "projects":
		next = StateProjects
	case "bots":
		next = StateBots
	case "wait":
	default:
		screen = "main"
		next = StateMain
	}
	return Result{Kind: KindOpenMenu, Next: next, Payload: screen, Text: "Меню"}
}

func navButton(name string) (string, bool) {
	switch name {
	case "nav_task":
		return ui.BtnTask, true
	case "nav_projects":
		return ui.BtnProjects, true
	case "nav_bots":
		return ui.BtnBots, true
	case "nav_account":
		return ui.BtnAccount, true
	case "nav_token":
		return ui.BtnToken, true
	case "nav_status":
		return ui.BtnStatus, true
	case "nav_reset":
		return ui.BtnReset, true
	case "nav_logout":
		return ui.BtnLogout, true
	case "nav_back":
		return ui.BtnBack, true
	case "nav_cancel":
		return ui.BtnCancel, true
	case "nav_create":
		return ui.BtnCreate, true
	case "nav_active":
		return ui.BtnActive, true
	case "nav_archive":
		return ui.BtnArchive, true
	case "nav_connect":
		return ui.BtnConnect, true
	default:
		return "", false
	}
}

func listProjects(in Input, archived bool, page int) Result {
	if !in.HasToken {
		return needAccount(in)
	}
	return Result{Kind: KindListProjects, Next: StateProjects, Archived: archived, Page: page, Text: "Проекты"}
}

func listBots(in Input) Result {
	if !in.HasToken {
		return needAccount(in)
	}
	if in.ActiveProject <= 0 {
		return Result{Kind: KindListProjects, Next: StateProjects, Text: "Сначала выберите проект."}
	}
	return Result{Kind: KindListBots, Next: StateBots, ProjectID: in.ActiveProject, Text: "Боты"}
}

func needAccount(in Input) Result {
	return Result{Kind: KindNeedAccount, Next: StateAccount, Text: "Сначала задайте MCP-токен в разделе «Аккаунт»."}
}

func cancel(in Input) Result {
	switch in.State {
	case StateWaitProjectName, StateWaitRename:
		return Result{Kind: KindReply, Next: StateProjects, Text: "Отменено."}
	case StateWaitBotToken:
		return Result{Kind: KindReply, Next: StateBots, Text: "Отменено."}
	case StateWaitMCP:
		return Result{Kind: KindReply, Next: StateAccount, Text: "Отменено."}
	default:
		return Result{Kind: KindReply, Next: StateMain, Text: "Главное меню"}
	}
}

func parseCommand(text string) (cmd, args string, ok bool) {
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", "", false
	}
	head := fields[0]
	if i := strings.IndexByte(head, '@'); i >= 0 {
		head = head[:i]
	}
	cmd = strings.TrimPrefix(head, "/")
	cmd = strings.ToLower(cmd)
	if len(fields) > 1 {
		args = strings.TrimSpace(text[len(fields[0]):])
	}
	return cmd, args, cmd != ""
}

const startText = "Я собираю и правлю ботов в конструкторе.\n\nОткройте «Аккаунт» и пришлите блок mcpServers из вкладки «Агент». Затем напишите задачу своими словами: какого бота собрать или что изменить. «Проекты» и «Боты» открывают списки."

const groupTaskText = "Напишите задачу ответом на сообщение бота или упомяните его. Сообщение без обращения в группе бот не читает."

const helpText = "Напишите задачу обычным сообщением — я начну её сразу.\nКнопки внизу повторяют команды.\n/token сохранить блок mcpServers или токен mcp_…\n/status статус\n/projects проекты\n/bots боты активного проекта\n/reset очистить диалог\n/logout забыть токен\n/cancel выйти из ввода"
