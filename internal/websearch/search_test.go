package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchSkipsArtifactsAndFallsBack(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Query().Get("engines"), "bing") {
			_, _ = w.Write([]byte(`{"results":[{"title":"Курс","url":"https://news.example/usd","content":"96 рублей","engine":"mojeek"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"title":"конвертер","url":"https://yandex.ru/currency_converter","content":"x","engine":"yandex"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	out, err := c.Search(context.Background(), "курс доллара", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].URL != "https://news.example/usd" {
		t.Fatalf("%+v", out)
	}
	if hits != 2 {
		t.Fatalf("запросов %d", hits)
	}
	if queryLanguage("bitcoin") != "en" || queryLanguage("биткоин") != "ru" {
		t.Fatal("язык запроса")
	}
}

func TestFetchURLReadsArticle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Новость</title></head><body><nav>меню</nav><article><h1>Заголовок</h1><p>Цена 96 рублей. ` + strings.Repeat("Дальше в статье про курс. ", 20) + `</p></article></body></html>`))
	}))
	defer srv.Close()
	c := New("http://127.0.0.1:1", "")
	page, err := c.FetchURL(context.Background(), srv.URL, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if page.Title != "Новость" || !strings.Contains(page.Text, "96 рублей") || strings.Contains(page.Text, "меню") {
		t.Fatalf("%+v", page)
	}
}
