package ui

import (
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
)

// Callback — разобранная callback_data.
type Callback struct {
	Name      string
	Page      int
	ProjectID int64
	TokenID   int64
	ServerID  int64
	Days      int
	Archived  bool
	Yes       bool
	Screen    string
}

// ParseCallback разбирает короткие callback_data меню.
func ParseCallback(data string) (Callback, bool) {
	data = strings.TrimSpace(data)
	if data == "" {
		return Callback{}, false
	}
	if data == "menu" {
		return Callback{Name: "menu", Screen: "main"}, true
	}
	parts := strings.Split(data, ":")
	if len(parts) < 2 {
		return Callback{}, false
	}
	switch parts[0] {
	case "menu":
		return parseMenu(parts)
	case "nav":
		if len(parts) != 2 || !knownNav(parts[1]) {
			return Callback{}, false
		}
		return Callback{Name: "nav_" + parts[1]}, true
	case "run":
		if len(parts) == 2 && parts[1] == "stop" {
			return Callback{Name: "run_stop"}, true
		}
	case "yes", "no":
		yes := parts[0] == "yes"
		if len(parts) == 2 && parts[1] == "logout" {
			return Callback{Name: "logout", Yes: yes}, true
		}
		if len(parts) == 3 && parts[1] == "sdel" {
			id, ok := atoi64(parts[2])
			if !ok {
				return Callback{}, false
			}
			return Callback{Name: "srv_del", Yes: yes, ServerID: id}, true
		}
		if len(parts) == 3 && parts[1] == "pdel" {
			id, ok := atoi64(parts[2])
			if !ok {
				return Callback{}, false
			}
			return Callback{Name: "delete_project", Yes: yes, ProjectID: id}, true
		}
		if len(parts) == 4 && (parts[1] == "stop" || parts[1] == "bdel") {
			pid, ok1 := atoi64(parts[2])
			tid, ok2 := atoi64(parts[3])
			if !ok1 || !ok2 {
				return Callback{}, false
			}
			name := "stop_bot"
			if parts[1] == "bdel" {
				name = "delete_bot"
			}
			return Callback{Name: name, Yes: yes, ProjectID: pid, TokenID: tid}, true
		}
	case "p":
		return parseProject(parts)
	case "b":
		return parseBot(parts)
	case "s":
		return parseServer(parts)
	}
	return Callback{}, false
}

func parseServer(parts []string) (Callback, bool) {
	if len(parts) < 2 {
		return Callback{}, false
	}
	switch parts[1] {
	case "list":
		return Callback{Name: "srv_list"}, len(parts) == 2
	case "add":
		return Callback{Name: "srv_add"}, len(parts) == 2
	case "open", "on", "off", "chk", "url", "oa", "del":
		if len(parts) != 3 {
			return Callback{}, false
		}
		id, ok := atoi64(parts[2])
		if !ok {
			return Callback{}, false
		}
		name := map[string]string{
			"open": "srv_open",
			"on":   "srv_on",
			"off":  "srv_off",
			"chk":  "srv_check",
			"url":  "srv_url",
			"oa":   "srv_oauth",
			"del":  "srv_del_ask",
		}[parts[1]]
		return Callback{Name: name, ServerID: id}, true
	case "tr", "au":
		if len(parts) != 4 || parts[3] == "" {
			return Callback{}, false
		}
		id, ok := atoi64(parts[2])
		if !ok {
			return Callback{}, false
		}
		name := "srv_transport"
		if parts[1] == "au" {
			name = "srv_auth"
		}
		return Callback{Name: name, ServerID: id, Screen: parts[3]}, true
	default:
		return Callback{}, false
	}
}

func parseMenu(parts []string) (Callback, bool) {
	if len(parts) == 1 {
		return Callback{Name: "menu", Screen: "main"}, true
	}
	if len(parts) != 2 {
		return Callback{}, false
	}
	switch parts[1] {
	case "main", "chat":
		return Callback{Name: "menu", Screen: "main"}, true
	case "account", "projects", "bots", "wait", "servers":
		return Callback{Name: "menu", Screen: parts[1]}, true
	default:
		return Callback{}, false
	}
}

func knownNav(action string) bool {
	switch action {
	case "task", "projects", "bots", "account", "token", "status", "reset", "logout",
		"back", "cancel", "create", "active", "archive", "connect", "servers":
		return true
	default:
		return false
	}
}

func parseProject(parts []string) (Callback, bool) {
	if len(parts) < 2 {
		return Callback{}, false
	}
	switch parts[1] {
	case "list", "alist":
		if len(parts) != 3 {
			return Callback{}, false
		}
		page, err := strconv.Atoi(parts[2])
		if err != nil || page < 0 {
			return Callback{}, false
		}
		return Callback{Name: "list_projects", Page: page, Archived: parts[1] == "alist"}, true
	case "new":
		if len(parts) != 2 {
			return Callback{}, false
		}
		return Callback{Name: "new_project"}, true
	case "open", "sum", "ren", "arch", "unar", "del":
		if len(parts) != 3 {
			return Callback{}, false
		}
		id, ok := atoi64(parts[2])
		if !ok {
			return Callback{}, false
		}
		name := map[string]string{
			"open": "open_project",
			"sum":  "summary",
			"ren":  "rename",
			"arch": "archive",
			"unar": "unarchive",
			"del":  "ask_delete_project",
		}[parts[1]]
		return Callback{Name: name, ProjectID: id}, true
	default:
		return Callback{}, false
	}
}

func parseBot(parts []string) (Callback, bool) {
	if len(parts) < 2 {
		return Callback{}, false
	}
	switch parts[1] {
	case "list":
		return Callback{Name: "list_bots"}, true
	case "add":
		return Callback{Name: "add_bot"}, true
	case "card", "stat", "start", "stop", "re", "logs", "del":
		if len(parts) != 4 {
			return Callback{}, false
		}
		pid, ok1 := atoi64(parts[2])
		tid, ok2 := atoi64(parts[3])
		if !ok1 || !ok2 {
			return Callback{}, false
		}
		name := map[string]string{
			"card":  "bot_card",
			"stat":  "bot_status",
			"start": "start_bot",
			"stop":  "ask_stop_bot",
			"re":    "restart_bot",
			"logs":  "bot_logs",
			"del":   "ask_delete_bot",
		}[parts[1]]
		return Callback{Name: name, ProjectID: pid, TokenID: tid}, true
	case "ret":
		if len(parts) != 5 {
			return Callback{}, false
		}
		pid, ok1 := atoi64(parts[2])
		tid, ok2 := atoi64(parts[3])
		days, err := strconv.Atoi(parts[4])
		if !ok1 || !ok2 || err != nil || !allowedDays(days) {
			return Callback{}, false
		}
		return Callback{Name: "retention", ProjectID: pid, TokenID: tid, Days: days}, true
	default:
		return Callback{}, false
	}
}

func allowedDays(days int) bool {
	for _, d := range RetentionDays {
		if d == days {
			return true
		}
	}
	return false
}

func atoi64(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func formatID(prefix string, id int64) string {
	return prefix + strconv.FormatInt(id, 10)
}

func format2(prefix string, a, b int64) string {
	return prefix + strconv.FormatInt(a, 10) + ":" + strconv.FormatInt(b, 10)
}

func format3(prefix string, a, b int64, days int) string {
	return format2(prefix, a, b) + ":" + strconv.Itoa(days)
}

func itoa(n int) string { return strconv.Itoa(n) }

// CallbacksOf собирает все callback_data клавиатуры.
func CallbacksOf(kb *telego.InlineKeyboardMarkup) []string {
	if kb == nil {
		return nil
	}
	var out []string
	for _, row := range kb.InlineKeyboard {
		for _, button := range row {
			out = append(out, button.CallbackData)
		}
	}
	return out
}
