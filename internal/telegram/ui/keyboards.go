package ui

import (
	"strconv"

	"github.com/mymmrac/telego"
)

const (
	BtnTask      = "💬 Задача"
	BtnProjects  = "📁 Проекты"
	BtnBots      = "🤖 Боты"
	BtnAccount   = "⚙️ Аккаунт"
	BtnServers   = "🔌 Серверы"
	BtnToken     = "🔑 MCP-токен"
	BtnStatus    = "📊 Статус"
	BtnReset     = "🧹 Сбросить диалог"
	BtnLogout    = "🚪 Выйти"
	BtnBack      = "⬅️ Назад"
	BtnCancel    = "❌ Отмена"
	BtnCreate    = "➕ Создать"
	BtnActive    = "📁 Активные"
	BtnArchive   = "📦 Архив"
	BtnConnect   = "➕ Подключить"
	BtnStopRun   = "⏹ Остановить"
	BtnYes       = "Да"
	BtnNo        = "Нет"
	BtnSummary   = "Сводка"
	BtnRename    = "Переименовать"
	BtnToArchive = "В архив"
	BtnUnarchive = "Вернуть"
	BtnDelete    = "Удалить"
	BtnBotStatus = "Статус"
	BtnStart     = "Старт"
	BtnStop      = "Стоп"
	BtnRestart   = "Перезапуск"
	BtnLogs      = "Логи"
	BtnBackList  = "К списку"
	BtnMenu      = "Меню"
)

// RetentionDays — допустимые сроки хранения сообщений.
var RetentionDays = []int{0, 7, 30, 60, 90, 180, 365}

// Project — строка списка проектов.
type Project struct {
	ID       int64
	Name     string
	Archived bool
}

// BotToken — строка списка токенов бота без секрета.
type BotToken struct {
	ID        int64
	Name      string
	Username  string
	Retention int
}

// MenuText сообщает, что строка совпадает с кнопкой меню и не должна уходить в модель.
func MenuText(s string) bool {
	switch s {
	case BtnTask, BtnProjects, BtnBots, BtnAccount, BtnServers, BtnToken, BtnStatus, BtnReset, BtnLogout,
		BtnBack, BtnCancel, BtnCreate, BtnActive, BtnArchive, BtnConnect:
		return true
	default:
		return false
	}
}

func reply(rows ...[]string) *telego.ReplyKeyboardMarkup {
	kb := make([][]telego.KeyboardButton, 0, len(rows))
	for _, row := range rows {
		line := make([]telego.KeyboardButton, 0, len(row))
		for _, label := range row {
			line = append(line, telego.KeyboardButton{Text: label})
		}
		kb = append(kb, line)
	}
	return &telego.ReplyKeyboardMarkup{
		Keyboard:       kb,
		ResizeKeyboard: true,
		IsPersistent:   true,
	}
}

// MainKeyboard — постоянное главное меню.
func MainKeyboard() *telego.ReplyKeyboardMarkup {
	return reply(
		[]string{BtnTask},
		[]string{BtnProjects, BtnBots},
		[]string{BtnAccount},
	)
}

// AccountKeyboard — раздел аккаунта.
func AccountKeyboard() *telego.ReplyKeyboardMarkup {
	return reply(
		[]string{BtnServers},
		[]string{BtnToken},
		[]string{BtnStatus},
		[]string{BtnReset},
		[]string{BtnLogout},
		[]string{BtnBack},
	)
}

// ProjectsKeyboard — действия со списком проектов.
func ProjectsKeyboard() *telego.ReplyKeyboardMarkup {
	return reply(
		[]string{BtnCreate},
		[]string{BtnActive, BtnArchive},
		[]string{BtnBack},
	)
}

// BotsKeyboard — действия со списком ботов.
func BotsKeyboard() *telego.ReplyKeyboardMarkup {
	return reply(
		[]string{BtnConnect},
		[]string{BtnBack},
	)
}

// WaitKeyboard — отмена ввода.
func WaitKeyboard() *telego.ReplyKeyboardMarkup {
	return reply([]string{BtnCancel})
}

// ScreenKeyboard возвращает reply-клавиатуру экрана.
func ScreenKeyboard(screen string) *telego.ReplyKeyboardMarkup {
	switch screen {
	case "account":
		return AccountKeyboard()
	case "projects":
		return ProjectsKeyboard()
	case "bots":
		return BotsKeyboard()
	case "wait":
		return WaitKeyboard()
	default:
		return MainKeyboard()
	}
}

func inline(rows ...[]telego.InlineKeyboardButton) *telego.InlineKeyboardMarkup {
	return &telego.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func btn(text, data string) telego.InlineKeyboardButton {
	return telego.InlineKeyboardButton{Text: text, CallbackData: data}
}

// ProjectListKeyboard — до 10 проектов и переход по страницам.
func ProjectListKeyboard(items []Project, page int, archived bool) *telego.InlineKeyboardMarkup {
	pageSize := 10
	if page < 0 {
		page = 0
	}
	start := page * pageSize
	if start > len(items) {
		start = 0
		page = 0
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	var rows [][]telego.InlineKeyboardButton
	for _, item := range items[start:end] {
		label := item.Name
		if label == "" {
			label = "без имени"
		}
		if len([]rune(label)) > 40 {
			label = string([]rune(label)[:37]) + "…"
		}
		rows = append(rows, []telego.InlineKeyboardButton{
			btn(label, formatID("p:open:", item.ID)),
		})
	}
	var nav []telego.InlineKeyboardButton
	prefix := "p:list:"
	if archived {
		prefix = "p:alist:"
	}
	if page > 0 {
		nav = append(nav, btn("←", prefix+itoa(page-1)))
	}
	if end < len(items) {
		nav = append(nav, btn("ещё", prefix+itoa(page+1)))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	return inline(rows...)
}

// ProjectCardKeyboard — действия с одним проектом.
func ProjectCardKeyboard(id int64) *telego.InlineKeyboardMarkup {
	return inline(
		[]telego.InlineKeyboardButton{
			btn(BtnSummary, formatID("p:sum:", id)),
			btn(BtnRename, formatID("p:ren:", id)),
		},
		[]telego.InlineKeyboardButton{
			btn(BtnToArchive, formatID("p:arch:", id)),
			btn(BtnUnarchive, formatID("p:unar:", id)),
		},
		[]telego.InlineKeyboardButton{
			btn(BtnDelete, formatID("p:del:", id)),
			btn(BtnBackList, "p:list:0"),
		},
	)
}

// BotListKeyboard — выбор токена бота.
func BotListKeyboard(projectID int64, items []BotToken) *telego.InlineKeyboardMarkup {
	var rows [][]telego.InlineKeyboardButton
	for _, item := range items {
		label := item.Name
		if item.Username != "" {
			label += " @" + trimAt(item.Username)
		}
		if label == "" {
			label = "бот"
		}
		if len([]rune(label)) > 40 {
			label = string([]rune(label)[:37]) + "…"
		}
		rows = append(rows, []telego.InlineKeyboardButton{
			btn(label, format2("b:card:", projectID, item.ID)),
		})
	}
	return inline(rows...)
}

// BotCardKeyboard — статус, запуск и срок хранения.
func BotCardKeyboard(projectID, tokenID int64) *telego.InlineKeyboardMarkup {
	rows := [][]telego.InlineKeyboardButton{
		{
			btn(BtnBotStatus, format2("b:stat:", projectID, tokenID)),
			btn(BtnStart, format2("b:start:", projectID, tokenID)),
		},
		{
			btn(BtnStop, format2("b:stop:", projectID, tokenID)),
			btn(BtnRestart, format2("b:re:", projectID, tokenID)),
		},
		{
			btn(BtnLogs, format2("b:logs:", projectID, tokenID)),
			btn(BtnDelete, format2("b:del:", projectID, tokenID)),
		},
	}
	var line []telego.InlineKeyboardButton
	for _, days := range RetentionDays {
		line = append(line, btn(itoa(days), format3("b:ret:", projectID, tokenID, days)))
		if len(line) == 4 {
			rows = append(rows, line)
			line = nil
		}
	}
	if len(line) > 0 {
		rows = append(rows, line)
	}
	rows = append(rows, []telego.InlineKeyboardButton{btn(BtnBackList, "b:list")})
	return inline(rows...)
}

// ConfirmKeyboard — Да / Нет. dataYes и dataNo уже готовые callback_data.
func ConfirmKeyboard(dataYes, dataNo string) *telego.InlineKeyboardMarkup {
	return inline([]telego.InlineKeyboardButton{
		btn(BtnYes, dataYes),
		btn(BtnNo, dataNo),
	})
}

// StopKeyboard — остановка текущего прогона агента.
func StopKeyboard() *telego.InlineKeyboardMarkup {
	return inline([]telego.InlineKeyboardButton{btn(BtnStopRun, "run:stop")})
}

// MenuButton — одна inline-кнопка, которая раскрывает действия экрана.
// И в личке, и в группе она заменяет постоянную reply-клавиатуру.
func MenuButton(screen string) *telego.InlineKeyboardMarkup {
	data := "menu"
	switch screen {
	case "account", "projects", "bots", "wait", "servers":
		data = "menu:" + screen
	}
	return inline([]telego.InlineKeyboardButton{btn(BtnMenu, data)})
}

// ScreenActions — inline-кнопки экрана. Те же действия, что у reply-клавиатуры.
func ScreenActions(screen string) *telego.InlineKeyboardMarkup {
	switch screen {
	case "account":
		return inline(
			[]telego.InlineKeyboardButton{btn(BtnServers, "nav:servers")},
			[]telego.InlineKeyboardButton{btn(BtnToken, "nav:token")},
			[]telego.InlineKeyboardButton{btn(BtnStatus, "nav:status")},
			[]telego.InlineKeyboardButton{btn(BtnReset, "nav:reset")},
			[]telego.InlineKeyboardButton{btn(BtnLogout, "nav:logout")},
			[]telego.InlineKeyboardButton{btn(BtnBack, "nav:back")},
		)
	case "projects":
		return inline(
			[]telego.InlineKeyboardButton{btn(BtnCreate, "nav:create")},
			[]telego.InlineKeyboardButton{btn(BtnActive, "nav:active"), btn(BtnArchive, "nav:archive")},
			[]telego.InlineKeyboardButton{btn(BtnBack, "nav:back")},
		)
	case "bots":
		return inline(
			[]telego.InlineKeyboardButton{btn(BtnConnect, "nav:connect")},
			[]telego.InlineKeyboardButton{btn(BtnBack, "nav:back")},
		)
	case "wait":
		return inline([]telego.InlineKeyboardButton{btn(BtnCancel, "nav:cancel")})
	case "servers":
		return ServerListKeyboard(nil)
	default:
		return inline(
			[]telego.InlineKeyboardButton{btn(BtnTask, "nav:task")},
			[]telego.InlineKeyboardButton{btn(BtnProjects, "nav:projects"), btn(BtnBots, "nav:bots")},
			[]telego.InlineKeyboardButton{btn(BtnAccount, "nav:account")},
		)
	}
}

// ActionsForMenu возвращает кнопки экрана для callback «menu» / «menu:экран».
func ActionsForMenu(data string) (*telego.InlineKeyboardMarkup, bool) {
	cb, ok := ParseCallback(data)
	if !ok || cb.Name != "menu" {
		return nil, false
	}
	return ScreenActions(cb.Screen), true
}

// InlineForGroup подменяет reply-клавиатуру одной кнопкой «Меню».
// Уже inline-клавиатура (подтверждение, стоп, списки) остаётся как есть.
// Так уходит каждое исходящее сообщение, и в личке, и в группе.
func InlineForGroup(markup telego.ReplyMarkup) *telego.InlineKeyboardMarkup {
	switch m := markup.(type) {
	case *telego.InlineKeyboardMarkup:
		return m
	case *telego.ReplyKeyboardMarkup:
		return MenuButton(screenOfReply(m))
	default:
		return nil
	}
}

func screenOfReply(kb *telego.ReplyKeyboardMarkup) string {
	if kb == nil {
		return "main"
	}
	has := map[string]bool{}
	for _, row := range kb.Keyboard {
		for _, button := range row {
			has[button.Text] = true
		}
	}
	switch {
	case has[BtnToken] || has[BtnLogout] || has[BtnReset] || has[BtnServers]:
		return "account"
	case has[BtnCreate] || has[BtnArchive] || has[BtnActive]:
		return "projects"
	case has[BtnConnect]:
		return "bots"
	case has[BtnCancel]:
		return "wait"
	default:
		return "main"
	}
}

// ServerRow — строка списка MCP без секретов.
type ServerRow struct {
	ID      int64
	Name    string
	Enabled bool
}

// ServerListKeyboard — серверы чата и кнопка добавления.
func ServerListKeyboard(items []ServerRow) *telego.InlineKeyboardMarkup {
	var rows [][]telego.InlineKeyboardButton
	for _, item := range items {
		label := item.Name
		if !item.Enabled {
			label = "выкл " + label
		}
		if len([]rune(label)) > 40 {
			label = string([]rune(label)[:37]) + "…"
		}
		rows = append(rows, []telego.InlineKeyboardButton{btn(label, formatID("s:open:", item.ID))})
	}
	rows = append(rows, []telego.InlineKeyboardButton{btn("Добавить", "s:add")})
	return inline(rows...)
}

// ServerCardKeyboard — действия с одним сервером.
func ServerCardKeyboard(id int64, enabled, oauth bool) *telego.InlineKeyboardMarkup {
	toggle := btn("Выключить", formatID("s:off:", id))
	if !enabled {
		toggle = btn("Включить", formatID("s:on:", id))
	}
	rows := [][]telego.InlineKeyboardButton{
		{btn("Проверить", formatID("s:chk:", id)), toggle},
		{btn("URL", formatID("s:url:", id)), btn("Удалить", formatID("s:del:", id))},
		{
			btn("авто", modeData("s:tr:", id, "auto")),
			btn("HTTP", modeData("s:tr:", id, "streamable")),
			btn("SSE", modeData("s:tr:", id, "sse")),
		},
		{
			btn("без авторизации", modeData("s:au:", id, "none")),
			btn("Bearer", modeData("s:au:", id, "bearer")),
		},
		{
			btn("API-ключ", modeData("s:au:", id, "api_key")),
			btn("Basic", modeData("s:au:", id, "basic")),
		},
		{
			btn("заголовки", modeData("s:au:", id, "headers")),
			btn("OAuth", modeData("s:au:", id, "oauth")),
		},
	}
	if oauth {
		rows = append(rows, []telego.InlineKeyboardButton{btn("Авторизовать OAuth", formatID("s:oa:", id))})
	}
	rows = append(rows, []telego.InlineKeyboardButton{btn(BtnBackList, "s:list")})
	return inline(rows...)
}

func modeData(prefix string, id int64, mode string) string {
	return prefix + strconv.FormatInt(id, 10) + ":" + mode
}

func trimAt(s string) string {
	if len(s) > 0 && s[0] == '@' {
		return s[1:]
	}
	return s
}
