package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"bot-builder-agent/internal/mcp"
	"bot-builder-agent/internal/store"
	"bot-builder-agent/internal/telegram/router"
	"bot-builder-agent/internal/telegram/ui"
)

func (b *Bot) servers(userID, chatID int64, res router.Result) {
	ctx := b.baseCtx()
	switch res.Kind {
	case router.KindListServers:
		b.showServers(ctx, userID, chatID)
	case router.KindAskServerName:
		b.session(userID).srvID = 0
		b.say(chatID, res.Text, ui.WaitKeyboard())
	case router.KindServerNamed:
		b.session(userID).srvName = strings.TrimSpace(res.Payload)
		b.session(userID).srvID = 0
		b.say(chatID, "Пришлите URL сервера. Путь и query сохранятся, /mcp сам не дописывается.", ui.WaitKeyboard())
	case router.KindServerURL:
		b.saveServerURL(ctx, userID, chatID, res.Payload)
	case router.KindSetTransport:
		b.saveTransport(ctx, userID, chatID, res)
	case router.KindSetAuthKind:
		b.session(userID).srvAuth = res.Payload
		if res.Payload == mcp.AuthNone {
			b.saveSecret(ctx, userID, chatID, "")
			return
		}
		b.say(chatID, res.Text, ui.WaitKeyboard())
	case router.KindSaveServerSecret:
		b.saveSecret(ctx, userID, chatID, res.Payload)
	case router.KindToggleServer:
		if err := b.store.SetServerEnabled(ctx, userID, res.ServerID, res.Enabled); err != nil {
			b.say(chatID, "Не удалось изменить сервер.", ui.AccountKeyboard())
			return
		}
		if b.hub != nil {
			b.hub.Forget(userID, res.ServerID)
		}
		b.showServer(ctx, userID, chatID, res.ServerID)
	case router.KindAskDeleteServer:
		b.say(chatID, res.Text, ui.ConfirmKeyboard(
			fmt.Sprintf("yes:sdel:%d", res.ServerID),
			fmt.Sprintf("no:sdel:%d", res.ServerID),
		))
	case router.KindDeleteServer:
		if err := b.store.DeleteServer(ctx, userID, res.ServerID); err != nil {
			b.say(chatID, "Не удалось удалить сервер.", ui.AccountKeyboard())
			return
		}
		if b.hub != nil {
			b.hub.Forget(userID, res.ServerID)
		}
		b.say(chatID, "Сервер удалён.", ui.AccountKeyboard())
		b.showServers(ctx, userID, chatID)
	case router.KindCheckServer:
		if b.hub == nil {
			b.say(chatID, "Проверка недоступна.", ui.AccountKeyboard())
			return
		}
		text, err := b.hub.Probe(ctx, userID, res.ServerID)
		if err != nil {
			b.say(chatID, "Не удалось проверить сервер.", ui.AccountKeyboard())
			return
		}
		b.say(chatID, text, ui.AccountKeyboard())
		b.showServer(ctx, userID, chatID, res.ServerID)
	case router.KindBeginOAuth:
		b.beginOAuth(ctx, userID, chatID, res.ServerID)
	case router.KindImportServers:
		b.importServers(ctx, userID, chatID, res.Payload)
	case router.KindServerCard:
		b.showServer(ctx, userID, chatID, res.ServerID)
	}
}

func (b *Bot) showServers(ctx context.Context, userID, chatID int64) {
	items, err := b.store.ListServers(ctx, userID)
	if err != nil {
		b.say(chatID, "Не удалось прочитать серверы.", ui.AccountKeyboard())
		return
	}
	if len(items) == 0 {
		b.say(chatID, "Серверов пока нет. Добавьте свой или пришлите блок mcpServers.", ui.ServerListKeyboard(nil))
		return
	}
	var lines []string
	rows := make([]ui.ServerRow, 0, len(items))
	for _, item := range items {
		mark := "вкл"
		if !item.Enabled {
			mark = "выкл"
		}
		host := item.URL
		if host == "" && item.Builder {
			host = "конструктор по умолчанию"
		}
		line := fmt.Sprintf("%s [%s, %s, %s] %s", item.Name, mark, item.Transport, item.AuthKind, mcp.Host(host))
		if item.LastError != "" {
			line += "\n" + item.LastError
		}
		lines = append(lines, line)
		rows = append(rows, ui.ServerRow{ID: item.ID, Name: item.Name, Enabled: item.Enabled})
	}
	b.say(chatID, strings.Join(lines, "\n\n"), ui.ServerListKeyboard(rows))
}

func (b *Bot) showServer(ctx context.Context, userID, chatID, id int64) {
	item, err := b.store.GetServer(ctx, userID, id)
	if err != nil || item.ID == 0 {
		b.say(chatID, "Сервер не найден.", ui.AccountKeyboard())
		return
	}
	b.session(userID).srvID = item.ID
	b.session(userID).srvName = item.Name
	b.session(userID).srvURL = item.URL
	b.session(userID).srvTransport = item.Transport
	b.session(userID).srvAuth = item.AuthKind
	creds, _ := mcp.ParseCredentials(item.AuthJSON)
	state := "включён"
	if !item.Enabled {
		state = "выключен"
	}
	host := item.URL
	if host == "" && item.Builder {
		host = "адрес конструктора из BOT_BUILDER_URL"
	}
	text := fmt.Sprintf("%s, %s\n%s\nтранспорт %s\n%s", item.Name, state, mcp.DisplayURL(host), item.Transport, creds.Masked(item.AuthKind))
	if item.LastError != "" {
		text += "\n" + item.LastError
	}
	b.say(chatID, text, ui.ServerCardKeyboard(item.ID, item.Enabled, item.AuthKind == mcp.AuthOAuth))
}

func (b *Bot) saveServerURL(ctx context.Context, userID, chatID int64, raw string) {
	sess := b.session(userID)
	preserved, err := mcp.PreserveURL(raw)
	if err != nil {
		b.say(chatID, err.Error(), ui.WaitKeyboard())
		return
	}
	if b.cfg != nil && !b.cfg.AllowPrivate && mcp.IsPrivateURL(preserved) {
		b.say(chatID, "Локальный адрес запрещён. Включите MCP_ALLOW_PRIVATE, если сервер на своей машине.", ui.WaitKeyboard())
		return
	}
	sess.srvURL = preserved
	if sess.srvID != 0 {
		item, err := b.store.GetServer(ctx, userID, sess.srvID)
		if err != nil || item.ID == 0 {
			b.say(chatID, "Сервер не найден.", ui.AccountKeyboard())
			return
		}
		item.URL = preserved
		if _, err := b.store.PutServer(ctx, item); err != nil {
			b.say(chatID, "Не удалось сохранить адрес.", ui.AccountKeyboard())
			return
		}
		if b.hub != nil {
			b.hub.Forget(userID, item.ID)
		}
		b.showServer(ctx, userID, chatID, item.ID)
		return
	}
	b.say(chatID, "Адрес сохранён. Выберите транспорт и доступ кнопками. Пока сервер не создан, сначала выберите доступ.", ui.AccountKeyboard())
	b.say(chatID, preserved, ui.ServerCardKeyboard(0, true, false))
}

func (b *Bot) saveTransport(ctx context.Context, userID, chatID int64, res router.Result) {
	sess := b.session(userID)
	id := res.ServerID
	if id == 0 {
		id = sess.srvID
	}
	sess.srvTransport = res.Payload
	if id == 0 {
		b.say(chatID, "Транспорт "+res.Payload+". Теперь выберите доступ.", ui.AccountKeyboard())
		return
	}
	item, err := b.store.GetServer(ctx, userID, id)
	if err != nil || item.ID == 0 {
		b.say(chatID, "Сервер не найден.", ui.AccountKeyboard())
		return
	}
	item.Transport = res.Payload
	if _, err := b.store.PutServer(ctx, item); err != nil {
		b.say(chatID, "Не удалось сохранить транспорт.", ui.AccountKeyboard())
		return
	}
	if b.hub != nil {
		b.hub.Forget(userID, item.ID)
	}
	b.showServer(ctx, userID, chatID, item.ID)
}

func (b *Bot) saveSecret(ctx context.Context, userID, chatID int64, raw string) {
	sess := b.session(userID)
	kind := sess.srvAuth
	if kind == "" {
		kind = mcp.AuthNone
	}
	creds, err := credsFromInput(kind, raw)
	if err != nil {
		b.say(chatID, err.Error(), ui.WaitKeyboard())
		return
	}
	encoded, err := creds.JSON()
	if err != nil {
		b.say(chatID, "Не удалось сохранить доступ.", ui.AccountKeyboard())
		return
	}
	if sess.srvID != 0 && sess.srvName != "" {
		item, err := b.store.GetServer(ctx, userID, sess.srvID)
		if err != nil || item.ID == 0 {
			b.say(chatID, "Сервер не найден.", ui.AccountKeyboard())
			return
		}
		item.AuthKind = kind
		item.AuthJSON = encoded
		if _, err := b.store.PutServer(ctx, item); err != nil {
			b.say(chatID, "Не удалось сохранить доступ.", ui.AccountKeyboard())
			return
		}
		if b.hub != nil {
			b.hub.Forget(userID, item.ID)
		}
		b.showServer(ctx, userID, chatID, item.ID)
		return
	}
	if strings.TrimSpace(sess.srvName) == "" || strings.TrimSpace(sess.srvURL) == "" {
		b.say(chatID, "Сначала имя и URL сервера.", ui.AccountKeyboard())
		return
	}
	transport := sess.srvTransport
	if transport == "" {
		transport = "auto"
	}
	item, err := b.store.PutServer(ctx, store.Server{
		UserID:    userID,
		Name:      sess.srvName,
		URL:       sess.srvURL,
		Transport: transport,
		AuthKind:  kind,
		AuthJSON:  encoded,
		Enabled:   true,
	})
	if err != nil {
		b.say(chatID, "Не удалось добавить сервер. Имя должно быть уникальным.", ui.AccountKeyboard())
		return
	}
	b.showServer(ctx, userID, chatID, item.ID)
}

func (b *Bot) beginOAuth(ctx context.Context, userID, chatID, serverID int64) {
	if b.hub == nil {
		b.say(chatID, "OAuth недоступен.", ui.AccountKeyboard())
		return
	}
	link, done, err := b.hub.BeginOAuth(ctx, userID, serverID)
	if err != nil {
		b.say(chatID, err.Error(), ui.AccountKeyboard())
		return
	}
	b.say(chatID, "Откройте ссылку и подтвердите доступ. Секрет в чат не попадёт.\n"+link, ui.AccountKeyboard())
	go func() {
		err := <-done
		if err != nil {
			b.say(chatID, "OAuth не завершился: "+err.Error(), ui.AccountKeyboard())
			return
		}
		b.say(chatID, "OAuth-токен сохранён.", ui.AccountKeyboard())
	}()
}

func (b *Bot) importServers(ctx context.Context, userID, chatID int64, raw string) {
	specs, ok := mcp.ParseServers(raw)
	if !ok {
		b.say(chatID, "Не разобрал блок mcpServers.", ui.AccountKeyboard())
		return
	}
	var names []string
	for _, spec := range specs {
		url := spec.URL
		var err error
		if spec.Builder {
			if url != "" {
				url, err = mcp.NormalizeURL(url)
			}
		} else {
			url, err = mcp.PreserveURL(spec.URL)
		}
		if err != nil {
			b.say(chatID, spec.Name+": "+err.Error(), ui.AccountKeyboard())
			return
		}
		if b.cfg != nil && !b.cfg.AllowPrivate && url != "" && mcp.IsPrivateURL(url) {
			b.say(chatID, spec.Name+": локальный адрес запрещён настройкой MCP_ALLOW_PRIVATE", ui.AccountKeyboard())
			return
		}
		encoded, err := spec.Creds.JSON()
		if err != nil {
			b.say(chatID, "Не удалось сохранить доступ.", ui.AccountKeyboard())
			return
		}
		transport := "auto"
		if spec.Builder {
			transport = "streamable"
		}
		if _, err := b.store.PutServer(ctx, store.Server{
			UserID:    userID,
			Name:      spec.Name,
			URL:       url,
			Transport: transport,
			AuthKind:  spec.AuthKind,
			AuthJSON:  encoded,
			Enabled:   true,
			Builder:   spec.Builder,
		}); err != nil {
			b.say(chatID, "Не удалось сохранить "+spec.Name, ui.AccountKeyboard())
			return
		}
		if spec.Builder && spec.Creds.Token != "" {
			_ = b.store.SaveAccess(ctx, userID, spec.Creds.Token, url)
		}
		names = append(names, spec.Name)
	}
	if b.hub != nil {
		if items, err := b.store.ListServers(ctx, userID); err == nil {
			for _, item := range items {
				b.hub.Forget(userID, item.ID)
			}
		}
	}
	b.say(chatID, "Сохранены серверы: "+strings.Join(names, ", "), ui.AccountKeyboard())
	b.showServers(ctx, userID, chatID)
}

func credsFromInput(kind, raw string) (mcp.Credentials, error) {
	raw = strings.TrimSpace(raw)
	switch kind {
	case mcp.AuthNone, "":
		return mcp.Credentials{}, nil
	case mcp.AuthBearer:
		if raw == "" || raw == "-" {
			return mcp.Credentials{}, fmt.Errorf("нужен токен")
		}
		return mcp.Credentials{Token: raw}, nil
	case mcp.AuthAPIKey:
		name, value, ok := strings.Cut(raw, " ")
		if !ok || strings.TrimSpace(value) == "" {
			return mcp.Credentials{}, fmt.Errorf("нужны имя заголовка и ключ через пробел")
		}
		return mcp.Credentials{Header: strings.TrimSpace(name), Token: strings.TrimSpace(value)}, nil
	case mcp.AuthBasic:
		user, pass, ok := strings.Cut(raw, " ")
		if !ok || user == "" {
			return mcp.Credentials{}, fmt.Errorf("нужны логин и пароль через пробел")
		}
		return mcp.Credentials{Username: user, Password: pass}, nil
	case mcp.AuthHeaders:
		var headers map[string]string
		if err := json.Unmarshal([]byte(raw), &headers); err != nil || len(headers) == 0 {
			return mcp.Credentials{}, fmt.Errorf("нужен JSON-объект заголовков")
		}
		return mcp.Credentials{Headers: headers}, nil
	case mcp.AuthOAuth:
		creds := mcp.Credentials{}
		if raw != "" && raw != "-" {
			id, secret, _ := strings.Cut(raw, " ")
			creds.ClientID = strings.TrimSpace(id)
			creds.ClientSecret = strings.TrimSpace(secret)
		}
		return creds, nil
	default:
		return mcp.Credentials{}, fmt.Errorf("неизвестный способ доступа")
	}
}

func (b *Bot) session(userID int64) *session {
	b.mu.Lock()
	defer b.mu.Unlock()
	sess := b.sess[userID]
	if sess == nil {
		sess = &session{state: router.StateMain}
		b.sess[userID] = sess
	}
	return sess
}
