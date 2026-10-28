package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"bot-builder-agent/internal/mcp"
	"bot-builder-agent/internal/store"
	"bot-builder-agent/internal/telegram/router"
	"bot-builder-agent/internal/telegram/ui"
	"bot-builder-agent/internal/text"
)

func serverKind(kind router.Kind) bool {
	switch kind {
	case router.KindListServers, router.KindServerCard, router.KindAskServerName, router.KindServerNamed,
		router.KindServerURL, router.KindSetTransport, router.KindSetAuthKind, router.KindSaveServerSecret,
		router.KindToggleServer, router.KindAskDeleteServer, router.KindDeleteServer, router.KindCheckServer,
		router.KindBeginOAuth, router.KindImportServers:
		return true
	default:
		return false
	}
}

func (b *Bot) execute(userID, chatID int64, res router.Result) {
	if serverKind(res.Kind) {
		b.servers(userID, chatID, res)
		return
	}
	ctx := b.baseCtx()
	switch res.Kind {
	case router.KindSaveMCPToken:
		if err := b.store.SaveAccess(ctx, userID, res.Payload, res.Endpoint); err != nil {
			b.say(chatID, "Не удалось сохранить токен.", ui.AccountKeyboard())
			return
		}
		line := "Токен сохранён: " + store.MaskToken(res.Payload)
		if res.Endpoint != "" {
			line += "\nКонструктор: " + mcp.Host(res.Endpoint)
		}
		b.say(chatID, line, ui.AccountKeyboard())
	case router.KindShowStatus:
		b.say(chatID, b.statusText(ctx, userID), ui.AccountKeyboard())
	case router.KindResetHistory:
		if err := b.store.ResetHistory(ctx, userID); err != nil {
			b.say(chatID, "Не удалось очистить диалог.", screenMarkup(res.Next))
			return
		}
		b.say(chatID, res.Text, screenMarkup(res.Next))
	case router.KindAskLogout:
		b.say(chatID, res.Text, ui.ConfirmKeyboard("yes:logout", "no:logout"))
	case router.KindLogout:
		if err := b.store.Logout(ctx, userID); err != nil {
			b.say(chatID, "Не удалось выйти.", ui.AccountKeyboard())
			return
		}
		b.say(chatID, res.Text, ui.AccountKeyboard())
	case router.KindListProjects:
		b.showProjects(ctx, userID, chatID, res)
	case router.KindOpenProject:
		if err := b.store.SetActiveProject(ctx, userID, res.ProjectID); err != nil {
			b.say(chatID, "Не удалось запомнить проект.", ui.ProjectsKeyboard())
			return
		}
		b.say(chatID, fmt.Sprintf("Активный проект: %d", res.ProjectID), ui.ProjectsKeyboard())
		b.say(chatID, "Действия с проектом", ui.ProjectCardKeyboard(res.ProjectID))
	case router.KindProjectSummary:
		raw, err := b.tool(ctx, userID, "db_project_summary", map[string]any{"project_id": res.ProjectID})
		if err != nil {
			b.say(chatID, err.Error(), ui.ProjectsKeyboard())
			return
		}
		b.say(chatID, raw, ui.ProjectCardKeyboard(res.ProjectID))
	case router.KindAskProjectName, router.KindAskRename, router.KindAskBotToken:
		b.say(chatID, res.Text, ui.WaitKeyboard())
	case router.KindCreateProject:
		raw, err := b.tool(ctx, userID, "db_create_project", map[string]any{"name": res.Payload})
		if err != nil {
			b.say(chatID, err.Error(), ui.ProjectsKeyboard())
			return
		}
		id, name, err := ParseCreatedProject(raw)
		if err != nil {
			b.say(chatID, text.Redact(raw), ui.ProjectsKeyboard())
			return
		}
		_ = b.store.SetActiveProject(ctx, userID, id)
		label := name
		if label == "" {
			label = res.Payload
		}
		b.say(chatID, fmt.Sprintf("Проект «%s» создан, id %d.", label, id), ui.ProjectsKeyboard())
	case router.KindRenameProject:
		raw, err := b.tool(ctx, userID, "db_rename_project", map[string]any{
			"project_id": res.ProjectID,
			"name":       res.Payload,
		})
		if err != nil {
			b.say(chatID, err.Error(), ui.ProjectsKeyboard())
			return
		}
		b.say(chatID, text.Redact(raw), ui.ProjectCardKeyboard(res.ProjectID))
	case router.KindArchiveProject:
		b.projectOp(ctx, userID, chatID, "db_archive_project", res.ProjectID, "Проект в архиве.")
	case router.KindUnarchiveProject:
		b.projectOp(ctx, userID, chatID, "db_unarchive_project", res.ProjectID, "Проект возвращён из архива.")
	case router.KindAskDeleteProject:
		b.say(chatID, res.Text, ui.ConfirmKeyboard(
			fmt.Sprintf("yes:pdel:%d", res.ProjectID),
			fmt.Sprintf("no:pdel:%d", res.ProjectID),
		))
	case router.KindDeleteProject:
		raw, err := b.tool(ctx, userID, "db_delete_project", map[string]any{
			"project_id": res.ProjectID,
			"confirm":    true,
		})
		if err != nil {
			b.say(chatID, err.Error(), ui.ProjectsKeyboard())
			return
		}
		if cur, _ := b.store.ActiveProject(ctx, userID); cur == res.ProjectID {
			_ = b.store.SetActiveProject(ctx, userID, 0)
		}
		b.say(chatID, "Проект удалён.\n"+clip(raw), ui.ProjectsKeyboard())
	case router.KindListBots:
		b.showBots(ctx, userID, chatID, res.ProjectID)
	case router.KindBotCard:
		_ = b.store.SetActiveProject(ctx, userID, res.ProjectID)
		b.say(chatID, fmt.Sprintf("Бот %d в проекте %d", res.TokenID, res.ProjectID), ui.BotsKeyboard())
		b.say(chatID, "Действия", ui.BotCardKeyboard(res.ProjectID, res.TokenID))
	case router.KindBotStatus:
		b.botOp(ctx, chatID, userID, "db_bot_status", res, map[string]any{"token_id": res.TokenID})
	case router.KindStartBot:
		b.botOp(ctx, chatID, userID, "db_start_bot", res, map[string]any{
			"project_id": res.ProjectID,
			"token_id":   res.TokenID,
		})
	case router.KindAskStopBot:
		b.say(chatID, res.Text, ui.ConfirmKeyboard(
			fmt.Sprintf("yes:stop:%d:%d", res.ProjectID, res.TokenID),
			fmt.Sprintf("no:stop:%d:%d", res.ProjectID, res.TokenID),
		))
	case router.KindStopBot:
		b.botOp(ctx, chatID, userID, "db_stop_bot", res, map[string]any{
			"project_id": res.ProjectID,
			"token_id":   res.TokenID,
			"confirm":    true,
		})
	case router.KindRestartBot:
		b.botOp(ctx, chatID, userID, "db_restart_bot", res, map[string]any{
			"project_id": res.ProjectID,
			"token_id":   res.TokenID,
		})
	case router.KindBotLogs:
		b.botOp(ctx, chatID, userID, "db_bot_logs", res, map[string]any{
			"project_id": res.ProjectID,
			"token_id":   res.TokenID,
			"limit":      50,
		})
	case router.KindAskDeleteBot:
		b.say(chatID, res.Text, ui.ConfirmKeyboard(
			fmt.Sprintf("yes:bdel:%d:%d", res.ProjectID, res.TokenID),
			fmt.Sprintf("no:bdel:%d:%d", res.ProjectID, res.TokenID),
		))
	case router.KindDeleteBot:
		b.botOp(ctx, chatID, userID, "db_delete_bot_token", res, map[string]any{
			"project_id": res.ProjectID,
			"token_id":   res.TokenID,
			"confirm":    true,
		})
	case router.KindSetRetention:
		b.botOp(ctx, chatID, userID, "db_set_messages_retention", res, map[string]any{
			"project_id":              res.ProjectID,
			"token_id":                res.TokenID,
			"messages_retention_days": res.Days,
		})
	case router.KindAddBotToken:
		raw, err := b.tool(ctx, userID, "db_add_bot_token", map[string]any{
			"project_id": res.ProjectID,
			"token":      res.Payload,
		})
		if err != nil {
			b.say(chatID, err.Error(), ui.BotsKeyboard())
			return
		}
		b.say(chatID, "Бот подключён.\n"+clip(raw), ui.BotsKeyboard())
	default:
		b.say(chatID, res.Text, screenMarkup(res.Next))
	}
}

func (b *Bot) statusText(ctx context.Context, userID int64) string {
	token, endpoint, _ := b.store.Access(ctx, userID)
	if endpoint == "" {
		endpoint = b.cfg.BuilderURL
	}
	project, _ := b.store.ActiveProject(ctx, userID)
	projectLabel := "не выбран"
	if project > 0 {
		projectLabel = fmt.Sprintf("%d", project)
	}
	lines := []string{
		fmt.Sprintf("Конструктор: %s\nТокен: %s\nПроект: %s", mcp.Host(endpoint), store.MaskToken(token), projectLabel),
	}
	if servers, err := b.store.ListServers(ctx, userID); err == nil {
		for _, item := range servers {
			creds, _ := mcp.ParseCredentials(item.AuthJSON)
			state := "вкл"
			if !item.Enabled {
				state = "выкл"
			}
			host := item.URL
			if host == "" {
				host = "адрес по умолчанию"
			}
			line := fmt.Sprintf("%s [%s, %s] %s, %s", item.Name, state, item.Transport, mcp.Host(host), creds.Masked(item.AuthKind))
			if item.LastError != "" {
				line += ": " + item.LastError
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func (b *Bot) showProjects(ctx context.Context, userID, chatID int64, res router.Result) {
	b.say(chatID, res.Text, ui.ProjectsKeyboard())
	args := map[string]any{"archived": res.Archived}
	raw, err := b.tool(ctx, userID, "db_list_projects", args)
	if err != nil {
		b.say(chatID, err.Error(), nil)
		return
	}
	items, err := ParseProjects(raw)
	if err != nil {
		b.say(chatID, err.Error(), nil)
		return
	}
	if len(items) == 0 {
		b.say(chatID, "Список пуст.", nil)
		return
	}
	var lines []string
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("%d. %s", item.ID, item.Name))
	}
	b.say(chatID, strings.Join(lines, "\n"), ui.ProjectListKeyboard(items, res.Page, res.Archived))
}

func (b *Bot) showBots(ctx context.Context, userID, chatID, projectID int64) {
	if projectID <= 0 {
		projectID, _ = b.store.ActiveProject(ctx, userID)
	}
	b.say(chatID, "Боты", ui.BotsKeyboard())
	raw, err := b.tool(ctx, userID, "db_list_bot_tokens", map[string]any{"project_id": projectID})
	if err != nil {
		b.say(chatID, err.Error(), nil)
		return
	}
	items, err := ParseTokens(raw)
	if err != nil {
		b.say(chatID, err.Error(), nil)
		return
	}
	if len(items) == 0 {
		b.say(chatID, "Токенов ботов нет. Нажмите «Подключить».", nil)
		return
	}
	b.say(chatID, fmt.Sprintf("Проект %d", projectID), ui.BotListKeyboard(projectID, items))
}

func (b *Bot) projectOp(ctx context.Context, userID, chatID int64, tool string, projectID int64, okText string) {
	raw, err := b.tool(ctx, userID, tool, map[string]any{"project_id": projectID})
	if err != nil {
		b.say(chatID, err.Error(), ui.ProjectsKeyboard())
		return
	}
	b.say(chatID, okText+"\n"+clip(raw), ui.ProjectCardKeyboard(projectID))
}

func (b *Bot) botOp(ctx context.Context, chatID, userID int64, tool string, res router.Result, args map[string]any) {
	raw, err := b.tool(ctx, userID, tool, args)
	if err != nil {
		b.say(chatID, err.Error(), ui.BotsKeyboard())
		return
	}
	b.say(chatID, clip(raw), ui.BotCardKeyboard(res.ProjectID, res.TokenID))
}

func (b *Bot) tool(ctx context.Context, userID int64, name string, args map[string]any) (string, error) {
	token, endpoint, err := b.store.Access(ctx, userID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(token) == "" {
		return "", errors.New("Сначала задайте MCP-токен в разделе «Аккаунт».")
	}
	payload, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	b.log.Info("ui tool=%s user=%d", name, userID)
	out, _, err := b.mcp.CallTool(ctx, endpoint, token, name, payload)
	if err != nil {
		if errors.Is(err, mcp.ErrUnauthorized) {
			return "", errors.New("Токен отклонён. Откройте «Аккаунт» и задайте новый.")
		}
		return "", err
	}
	return text.Redact(out), nil
}

func clip(s string) string {
	return text.Truncate(strings.TrimSpace(s), 3500)
}
