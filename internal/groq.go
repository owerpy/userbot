package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ParsedAd — то, что ИИ вытащил из текста объявления.
type ParsedAd struct {
	IsAd            bool     `json:"is_ad"`            // вообще ли это объявление о перевозке
	Kind            string   `json:"kind"`             // "cargo" | "truck"
	FromRegion      string   `json:"from_region"`      // откуда
	ToRegion        string   `json:"to_region"`        // куда
	FromCountry     string   `json:"from_country"`     // страна отправления (если ясно)
	ToCountry       string   `json:"to_country"`       // страна назначения
	CargoDesc       string   `json:"cargo_desc"`       // что за груз
	WeightKg        *float64 `json:"weight_kg"`        // вес в кг (nil если нет)
	VehicleType     string   `json:"vehicle_type"`     // open/closed/refrigerator/tank/flatbed
	PriceText       string   `json:"price_text"`       // цена как в тексте
	DateText        string   `json:"date_text"`        // когда
	ContactPhone    string   `json:"contact_phone"`    // телефон
	ContactUsername string   `json:"contact_username"` // @username
	Lang            string   `json:"lang"`             // uz/ru/kz/other
}

type GroqClient struct {
	apiKey string
	model  string
	http   *http.Client
}

func NewGroqClient(apiKey, model string) *GroqClient {
	if model == "" {
		// сильная и быстрая модель для разбора коротких текстов
		model = "llama-3.3-70b-versatile"
	}
	return &GroqClient{
		apiKey: apiKey,
		model:  model,
		http:   &http.Client{Timeout: 25 * time.Second},
	}
}

const systemPrompt = `Ты — парсер объявлений о грузоперевозках из Telegram-каналов СНГ.
Текст может быть на русском, узбекском (латиница/кириллица) или казахском, в свободной форме.

Верни СТРОГО один JSON-объект без markdown, без пояснений, по схеме:
{
  "is_ad": true|false,          // это объявление о перевозке груза/поиске машины?
  "kind": "cargo"|"truck",       // cargo = ищут машину под груз; truck = свободная машина ищет груз
  "from_region": "город/регион отправления",
  "to_region": "город/регион назначения",
  "from_country": "страна отправления или пусто",
  "to_country": "страна назначения или пусто",
  "cargo_desc": "что за груз или пусто",
  "weight_kg": число в кг или null,   // "20 тонн" -> 20000; "5 т" -> 5000
  "vehicle_type": "open|closed|refrigerator|tank|flatbed или пусто",
  "price_text": "цена как в тексте или пусто",
  "date_text": "когда (завтра, 15 марта) или пусто",
  "contact_phone": "телефон в тексте или пусто",
  "contact_username": "@username в тексте или пусто",
  "lang": "ru|uz|kz|other"
}

Правила:
- Если это НЕ объявление о перевозке (реклама, чат, новости) — верни {"is_ad": false}.
- vehicle_type: тент/бортовой -> flatbed или closed; реф/холодильник -> refrigerator; цистерна -> tank; открытый -> open; крытый/фургон -> closed.
- Телефон нормализуй как в тексте (можно с +998, +7 и т.п.).
- Никогда не выдумывай данные. Чего нет — оставляй пустым/null.`

type groqReq struct {
	Model       string       `json:"model"`
	Messages    []groqMsg    `json:"messages"`
	Temperature float64      `json:"temperature"`
	ResponseFmt groqRespType `json:"response_format"`
}
type groqMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type groqRespType struct {
	Type string `json:"type"`
}
type groqResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Parse — разобрать текст объявления в структуру.
func (g *GroqClient) Parse(ctx context.Context, text string) (*ParsedAd, error) {
	body := groqReq{
		Model: g.model,
		Messages: []groqMsg{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: text},
		},
		Temperature: 0,
		ResponseFmt: groqRespType{Type: "json_object"},
	}
	buf, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.groq.com/openai/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.apiKey)

	resp, err := g.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var gr groqResp
	if err := json.Unmarshal(raw, &gr); err != nil {
		return nil, fmt.Errorf("groq decode: %w (%s)", err, string(raw))
	}
	if gr.Error != nil {
		return nil, fmt.Errorf("groq error: %s", gr.Error.Message)
	}
	if len(gr.Choices) == 0 {
		return nil, fmt.Errorf("groq: empty choices")
	}
	content := strings.TrimSpace(gr.Choices[0].Message.Content)
	// на всякий случай срезаем markdown-ограждения, если модель их добавила
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var parsed ParsedAd
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return nil, fmt.Errorf("parse json: %w (%s)", err, content)
	}
	return &parsed, nil
}
