package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"

	"bot-builder-agent/internal/agent"
	"bot-builder-agent/internal/config"
	"bot-builder-agent/internal/logger"
	"bot-builder-agent/internal/mcp"
	"bot-builder-agent/internal/mcphub"
	"bot-builder-agent/internal/openrouter"
	"bot-builder-agent/internal/store"
	"bot-builder-agent/internal/telegram/router"
	"bot-builder-agent/internal/telegram/ui"
	"bot-builder-agent/internal/text"
)

// tgAPI — методы Telegram, которые нужны адаптеру.
type tgAPI interface {
	GetMe(ctx context.Context) (*telego.User, error)
	SetMyCommands(ctx context.Context, params *telego.SetMyCommandsParams) error
	UpdatesViaLongPolling(ctx context.Context, params *telego.GetUpdatesParams, options ...telego.LongPollingOption) (<-chan telego.Update, error)
	AnswerCallbackQuery(ctx context.Context, params *telego.AnswerCallbackQueryParams) error
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
	EditMessageText(ctx context.Context, params *telego.EditMessageTextParams) (*telego.Message, error)
	EditMessageReplyMarkup(ctx context.Context, params *telego.EditMessageReplyMarkupParams) (*telego.Message, error)
	SendRichMessage(ctx context.Context, params *RichSendParams) (*telego.Message, error)
	EditRichMessage(ctx context.Context, params *RichEditParams) (*telego.Message, error)
	DeleteMessage(ctx context.Context, params *telego.DeleteMessageParams) error
	Download(ctx context.Context, fileID string) ([]byte, string, error)
}

type agentRunner interface {
	Run(ctx context.Context, req agent.Request) (agent.Result, error)
}

// Bot — Telegram-адаптер поверх маршрутизатора.
type Bot struct {
	api    tgAPI
	cfg    *config.Config
	log    *logger.Logger
	store  *store.Store
	mcp    *mcp.Client
	hub    *mcphub.Hub
	runner agentRunner

	root context.Context
	self telego.User

	mu             sync.Mutex
	locks          map[int64]*sync.Mutex
	sess           map[int64]*session
	keyboardHidden map[int64]struct{}
}

type session struct {
	state        router.State
	renameID     int64
	busy         bool
	cancel       context.CancelFunc
	srvID        int64
	srvName      string
	srvURL       string
	srvTransport string
	srvAuth      string
}

var _ tgAPI = (*liveAPI)(nil)

// New создаёт бота. Сеть на этом шаге не нужна.
func New(cfg *config.Config, log *logger.Logger, st *store.Store, mcpClient *mcp.Client, runner *agent.Runner, hub *mcphub.Hub) (*Bot, error) {
	api, err := newLiveAPI(cfg.TelegramBotToken)
	if err != nil {
		return nil, fmt.Errorf("telegram: %w", err)
	}
	return &Bot{
		api:    api,
		cfg:    cfg,
		log:    log,
		store:  st,
		mcp:    mcpClient,
		hub:    hub,
		runner: runner,
		locks:  map[int64]*sync.Mutex{},
		sess:   map[int64]*session{},
	}, nil
}

// Start регистрирует команды и читает long polling, пока жив ctx.
func (b *Bot) Start(ctx context.Context) error {
	b.root = ctx
	if me, err := b.api.GetMe(ctx); err != nil {
		b.log.Warn("не удалось узнать имя бота, в группе отвечу только на команды без @чужого: %v", err)
	} else if me != nil {
		b.self = *me
	}
	if err := b.api.SetMyCommands(ctx, &telego.SetMyCommandsParams{
		Commands: []telego.BotCommand{
			{Command: "start", Description: "Начало и меню"},
			{Command: "help", Description: "Что умеет бот"},
			{Command: "token", Description: "Сохранить mcp-токен"},
			{Command: "status", Description: "Статус подключения"},
			{Command: "projects", Description: "Список проектов"},
			{Command: "bots", Description: "Боты активного проекта"},
			{Command: "reset", Description: "Очистить диалог"},
			{Command: "logout", Description: "Забыть токен"},
			{Command: "servers", Description: "MCP-серверы чата"},
			{Command: "cancel", Description: "Отменить ввод"},
		},
	}); err != nil {
		b.log.Warn("не удалось записать меню команд: %v", err)
	}
	updates, err := b.api.UpdatesViaLongPolling(ctx, &telego.GetUpdatesParams{Timeout: 30})
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case upd, ok := <-updates:
			if !ok {
				return nil
			}
			go b.process(upd)
		}
	}
}

func (b *Bot) process(upd telego.Update) {
	if upd.CallbackQuery == nil && !directedAt(upd.Message, b.self) {
		return
	}
	uid, chat, textValue, callback, ok := identity(upd)
	if !ok {
		return
	}
	if callback != "" {
		b.answer(upd)
	}
	sess, um := b.lockUser(uid)
	defer func() {
		if um != nil {
			um.Unlock()
		}
	}()

	res := router.Handle(router.Input{
		State:           sess.state,
		Text:            textValue,
		Callback:        callback,
		HasToken:        b.hasToken(uid),
		HasImage:        len(imageFileIDs(upd.Message)) > 0,
		Allowed:         b.cfg.Allowed(uid),
		Busy:            sess.busy,
		Group:           chat < 0,
		ActiveProject:   b.activeProject(uid),
		RenameProjectID: sess.renameID,
	})
	sess.state = res.Next
	if res.ServerID != 0 {
		sess.srvID = res.ServerID
	}
	if res.Next != router.StateWaitRename {
		sess.renameID = 0
	}
	if res.Kind == router.KindAskRename {
		sess.renameID = res.ProjectID
	}

	switch res.Kind {
	case router.KindAskAgent:
		sess.busy = true
		ctx, cancel := context.WithCancel(b.baseCtx())
		sess.cancel = cancel
		um.Unlock()
		um = nil
		b.runAgent(ctx, uid, chat, res.Payload, imageFileIDs(upd.Message))
		cancel()
		sess2, um2 := b.lockUser(uid)
		sess2.busy = false
		sess2.cancel = nil
		um2.Unlock()
	case router.KindStopRun:
		cancel := sess.cancel
		um.Unlock()
		um = nil
		if cancel != nil {
			cancel()
		}
	case router.KindOpenMenu:
		um.Unlock()
		um = nil
		b.openMenu(chat, upd, res)
	case router.KindBusy, router.KindNotAllowed, router.KindNeedAccount, router.KindReply:
		um.Unlock()
		um = nil
		b.say(chat, res.Text, screenMarkup(res.Next))
	default:
		um.Unlock()
		um = nil
		b.execute(uid, chat, res)
	}
}

func (b *Bot) runAgent(ctx context.Context, userID, chatID int64, task string, fileIDs []string) {
	// «Думаю…» и «Вызываю …» — обычное сообщение с кнопкой «Стоп».
	// send сюда нельзя: ReplyKeyboardRemove делает сообщение нередактируемым,
	// и правка на «Вызываю» не проходит. В конце статус удаляется. Если delete
	// не прошёл, последнее статусное сообщение остаётся как есть — «Готово»
	// не пишем, ответ всё равно отправляем.
	status, err := b.sendStatus(chatID, "Думаю…")
	statusID := 0
	if err == nil && status != nil {
		statusID = status.MessageID
	}
	defer b.deleteStatus(chatID, statusID)
	history, err := b.store.History(ctx, userID)
	if err != nil {
		b.say(chatID, "Не удалось прочитать диалог.", ui.MainKeyboard())
		return
	}
	token, endpoint, err := b.store.Access(ctx, userID)
	if err != nil {
		b.say(chatID, "Не удалось прочитать подключения.", ui.AccountKeyboard())
		return
	}
	projectID, _ := b.store.ActiveProject(ctx, userID)
	result, err := b.runner.Run(ctx, agent.Request{
		Token:         token,
		Endpoint:      endpoint,
		UserID:        userID,
		History:       toOR(history),
		UserText:      task,
		Images:        b.loadImages(ctx, fileIDs),
		ActiveProject: projectID,
		OnTool: func(name string) {
			if statusID == 0 {
				return
			}
			if err := b.edit(chatID, statusID, "Вызываю "+name, ui.StopKeyboard()); err != nil {
				b.log.Warn("chat=%d: не удалось обновить статус: %v", chatID, err)
			}
		},
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			b.say(chatID, "Остановил.", screenMarkup(router.StateChat))
			return
		}
		if errors.Is(err, mcp.ErrUnauthorized) {
			b.say(chatID, "Токен отклонён. Откройте «Аккаунт» и задайте новый.", ui.AccountKeyboard())
			return
		}
		b.log.Error("агент user=%d: %v", userID, err)
		b.say(chatID, "Не удалось получить ответ: "+err.Error(), ui.MainKeyboard())
		return
	}
	if err := b.store.Append(b.baseCtx(), userID, fromOR(result.Transcript)); err != nil {
		b.log.Error("запись истории user=%d: %v", userID, err)
	}
	b.sayParts(chatID, text.Redact(result.Reply), ui.MainKeyboard())
}

func (b *Bot) lockUser(id int64) (*session, *sync.Mutex) {
	b.mu.Lock()
	um := b.locks[id]
	if um == nil {
		um = &sync.Mutex{}
		b.locks[id] = um
	}
	sess := b.sess[id]
	if sess == nil {
		sess = &session{state: router.StateMain}
		b.sess[id] = sess
	}
	b.mu.Unlock()
	um.Lock()
	return sess, um
}

func (b *Bot) baseCtx() context.Context {
	if b.root != nil {
		return b.root
	}
	return context.Background()
}

func (b *Bot) hasToken(userID int64) bool {
	token, err := b.store.Token(b.baseCtx(), userID)
	return err == nil && strings.TrimSpace(token) != ""
}

func (b *Bot) activeProject(userID int64) int64 {
	id, err := b.store.ActiveProject(b.baseCtx(), userID)
	if err != nil {
		return 0
	}
	return id
}

func identity(upd telego.Update) (userID, chatID int64, textValue, callback string, ok bool) {
	if upd.CallbackQuery != nil {
		cb := upd.CallbackQuery
		if cb.From.ID == 0 {
			return 0, 0, "", "", false
		}
		chat := cb.From.ID
		if cb.Message != nil {
			chat = cb.Message.GetChat().ID
		}
		return cb.From.ID, chat, "", cb.Data, true
	}
	if upd.Message != nil && upd.Message.From != nil {
		body := upd.Message.Text
		if body == "" {
			body = upd.Message.Caption
		}
		return upd.Message.From.ID, upd.Message.Chat.ID, body, "", true
	}
	return 0, 0, "", "", false
}

func (b *Bot) answer(upd telego.Update) {
	if upd.CallbackQuery == nil {
		return
	}
	_ = b.api.AnswerCallbackQuery(b.baseCtx(), &telego.AnswerCallbackQueryParams{
		CallbackQueryID: upd.CallbackQuery.ID,
	})
}

func screenMarkup(state router.State) telego.ReplyMarkup {
	switch state {
	case router.StateAccount, router.StateWaitMCP:
		if state == router.StateWaitMCP {
			return ui.WaitKeyboard()
		}
		return ui.AccountKeyboard()
	case router.StateProjects, router.StateWaitProjectName, router.StateWaitRename:
		if state == router.StateWaitProjectName || state == router.StateWaitRename {
			return ui.WaitKeyboard()
		}
		return ui.ProjectsKeyboard()
	case router.StateBots, router.StateWaitBotToken:
		if state == router.StateWaitBotToken {
			return ui.WaitKeyboard()
		}
		return ui.BotsKeyboard()
	case router.StateServers:
		return ui.AccountKeyboard()
	case router.StateWaitSrvName, router.StateWaitSrvURL, router.StateWaitSrvSecret, router.StateWaitSrvChoice:
		return ui.WaitKeyboard()
	default:
		return ui.MainKeyboard()
	}
}

func (b *Bot) say(chatID int64, body string, markup telego.ReplyMarkup) {
	body = strings.TrimSpace(text.Redact(body))
	if body == "" {
		body = "Готово."
	}
	parts := text.Split(body, 4096)
	for i, part := range parts {
		var mark telego.ReplyMarkup
		if i == 0 {
			mark = markup
		} else if markup != nil {
			if _, inline := markup.(*telego.InlineKeyboardMarkup); !inline {
				mark = markup
			}
		}
		if _, err := b.send(chatID, part, mark); err != nil {
			b.log.Error("send chat=%d: %v", chatID, err)
			return
		}
	}
}

func (b *Bot) sayParts(chatID int64, body string, markup telego.ReplyMarkup) {
	b.say(chatID, body, markup)
}

// sendStatus шлёт живой статус обычным sendMessage и только inline-кнопкой
// остановки. Без ReplyKeyboardRemove и без последующей правки клавиатуры:
// сообщение с ReplyKeyboardRemove Telegram не даёт редактировать.
func (b *Bot) sendStatus(chatID int64, body string) (*telego.Message, error) {
	return b.api.SendMessage(b.baseCtx(), &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: chatID},
		Text:        body,
		ReplyMarkup: ui.StopKeyboard(),
	})
}

func (b *Bot) deleteStatus(chatID int64, messageID int) {
	if messageID == 0 {
		return
	}
	_ = b.api.DeleteMessage(b.baseCtx(), &telego.DeleteMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		MessageID: messageID,
	})
}

func (b *Bot) send(chatID int64, body string, markup telego.ReplyMarkup) (*telego.Message, error) {
	// У сообщения одно поле reply_markup. Сообщение с ReplyKeyboardRemove
	// Telegram не даёт редактировать, поэтому «Да»/«Нет» и «Меню» ставим сразу.
	// Залипшую reply-клавиатуру снимаем отдельным сообщением и тут же удаляем.
	inline := ui.InlineForGroup(markup)
	if inline == nil {
		return b.deliver(chatID, body, &telego.ReplyKeyboardRemove{RemoveKeyboard: true})
	}
	msg, err := b.deliver(chatID, body, inline)
	if err != nil || msg == nil {
		return msg, err
	}
	b.dropReplyKeyboard(chatID)
	return msg, nil
}

func (b *Bot) deliver(chatID int64, body string, markup telego.ReplyMarkup) (*telego.Message, error) {
	out := prepareOutbound(body)
	if out.rich && utf8.RuneCountInString(out.html) <= richTextLimit {
		msg, err := b.api.SendRichMessage(b.baseCtx(), &RichSendParams{
			ChatID:      chatID,
			HTML:        out.html,
			ReplyMarkup: markup,
		})
		if err == nil || !htmlRejected(err) {
			return msg, err
		}
		b.log.Warn("telegram не принял rich html, пробую markdown: %v", err)
		msg, err = b.api.SendRichMessage(b.baseCtx(), &RichSendParams{
			ChatID:      chatID,
			Markdown:    body,
			ReplyMarkup: markup,
		})
		if err == nil || !htmlRejected(err) {
			return msg, err
		}
		b.log.Warn("telegram не принял rich markdown, отправляю без разметки: %v", err)
	}
	if !out.rich && utf8.RuneCountInString(out.html) <= 4096 {
		msg, err := b.api.SendMessage(b.baseCtx(), &telego.SendMessageParams{
			ChatID:      telego.ChatID{ID: chatID},
			Text:        out.html,
			ParseMode:   telego.ModeHTML,
			ReplyMarkup: markup,
		})
		if err == nil || !htmlRejected(err) {
			return msg, err
		}
		b.log.Warn("telegram не принял html, отправляю без разметки: %v", err)
	}
	return b.api.SendMessage(b.baseCtx(), &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: chatID},
		Text:        out.plain,
		ReplyMarkup: markup,
	})
}

func (b *Bot) openMenu(chatID int64, upd telego.Update, res router.Result) {
	screen := res.Payload
	if screen == "" {
		screen = "main"
	}
	kb := ui.ScreenActions(screen)
	msgID := callbackMessageID(upd)
	if msgID != 0 {
		_, err := b.api.EditMessageReplyMarkup(b.baseCtx(), &telego.EditMessageReplyMarkupParams{
			ChatID:      telego.ChatID{ID: chatID},
			MessageID:   msgID,
			ReplyMarkup: kb,
		})
		if err == nil {
			return
		}
		b.log.Warn("chat=%d: не удалось открыть меню: %v", chatID, err)
	}
	b.say(chatID, "Меню", kb)
}

func callbackMessageID(upd telego.Update) int {
	if upd.CallbackQuery == nil || upd.CallbackQuery.Message == nil {
		return 0
	}
	return upd.CallbackQuery.Message.GetMessageID()
}

func (b *Bot) edit(chatID int64, messageID int, body string, markup *telego.InlineKeyboardMarkup) error {
	out := prepareOutbound(body)
	if out.rich && utf8.RuneCountInString(out.html) <= richTextLimit {
		_, err := b.api.EditRichMessage(b.baseCtx(), &RichEditParams{
			ChatID:      chatID,
			MessageID:   messageID,
			HTML:        out.html,
			ReplyMarkup: markup,
		})
		if err == nil || !htmlRejected(err) {
			return err
		}
		b.log.Warn("telegram не принял rich html, пробую markdown: %v", err)
		_, err = b.api.EditRichMessage(b.baseCtx(), &RichEditParams{
			ChatID:      chatID,
			MessageID:   messageID,
			Markdown:    body,
			ReplyMarkup: markup,
		})
		if err == nil || !htmlRejected(err) {
			return err
		}
		b.log.Warn("telegram не принял rich markdown, правлю без разметки: %v", err)
	}
	if !out.rich && utf8.RuneCountInString(out.html) <= 4096 {
		_, err := b.api.EditMessageText(b.baseCtx(), &telego.EditMessageTextParams{
			ChatID:      telego.ChatID{ID: chatID},
			MessageID:   messageID,
			Text:        out.html,
			ParseMode:   telego.ModeHTML,
			ReplyMarkup: markup,
		})
		if err == nil || !htmlRejected(err) {
			return err
		}
		b.log.Warn("telegram не принял html, правлю без разметки: %v", err)
	}
	_, err := b.api.EditMessageText(b.baseCtx(), &telego.EditMessageTextParams{
		ChatID:      telego.ChatID{ID: chatID},
		MessageID:   messageID,
		Text:        out.plain,
		ReplyMarkup: markup,
	})
	return err
}

func prepareOutbound(body string) preparedText {
	htmlText := text.ToTelegramHTML(body)
	plain := text.Plain(body)
	if strings.Contains(htmlText, "<table") {
		return preparedText{html: text.WrapRichHTML(htmlText), plain: plain, rich: true}
	}
	return preparedText{html: htmlText, plain: plain}
}

type preparedText struct {
	html  string
	plain string
	rich  bool
}

func htmlRejected(err error) bool {
	if err == nil {
		return false
	}
	desc := err.Error()
	var apiErr *telegoapi.Error
	if errors.As(err, &apiErr) && apiErr != nil && apiErr.Description != "" {
		desc = apiErr.Description
	}
	d := strings.ToLower(desc)
	return strings.Contains(d, "parse") ||
		strings.Contains(d, "entit") ||
		strings.Contains(d, "too long") ||
		strings.Contains(d, "too_long") ||
		strings.Contains(d, "unsupported") ||
		strings.Contains(d, "rich_message") ||
		strings.Contains(d, "rich message")
}

func toOR(msgs []store.Message) []openrouter.Message {
	out := make([]openrouter.Message, 0, len(msgs))
	for _, m := range msgs {
		msg := openrouter.Message{
			Role:       m.Role,
			Content:    m.Content,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
		}
		if m.ToolCallsJSON != "" {
			_ = json.Unmarshal([]byte(m.ToolCallsJSON), &msg.ToolCalls)
		}
		out = append(out, msg)
	}
	return out
}

func fromOR(msgs []openrouter.Message) []store.Message {
	out := make([]store.Message, 0, len(msgs))
	for _, m := range msgs {
		raw := ""
		if len(m.ToolCalls) > 0 {
			b, _ := json.Marshal(m.ToolCalls)
			raw = string(b)
		}
		out = append(out, store.Message{
			Role:          m.Role,
			Content:       m.Content,
			ToolCallID:    m.ToolCallID,
			Name:          m.Name,
			ToolCallsJSON: raw,
		})
	}
	return out
}
