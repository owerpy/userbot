package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Gemini как запасной провайдер: у него бесплатные лимиты устроены иначе —
// минутный запас токенов огромный (сотни тысяч), а ограничение идёт по числу
// запросов в сутки. При нашей пакетной обработке это даёт на порядок больше
// объявлений, чем у Groq.
type GeminiClient struct {
	apiKey string
	models []string
	http   *http.Client

	mu        sync.Mutex
	current   int
	exhausted map[string]time.Time
}

func NewGeminiClient(apiKey, model string) *GeminiClient {
	var models []string
	for _, m := range strings.Split(model, ",") {
		if m = strings.TrimSpace(m); m != "" {
			models = append(models, m)
		}
	}
	if len(models) == 0 {
		// Сначала умная Flash, затем Flash-Lite (у неё больше запросов
		// в сутки — подхватит, когда у старшей кончится квота).
		// Псевдонимы «latest» сами указывают на свежую версию, поэтому
		// смена поколений моделей не потребует правок.
		models = []string{"gemini-flash-latest", "gemini-flash-lite-latest"}
	}
	return &GeminiClient{
		apiKey:    apiKey,
		models:    models,
		http:      &http.Client{Timeout: 60 * time.Second},
		exhausted: map[string]time.Time{},
	}
}

func (g *GeminiClient) Name() string    { return "gemini" }
func (g *GeminiClient) Models() []string { return g.models }

// MaxBatchTokens — у Gemini минутный лимит токенов очень большой,
// поэтому пачки можно делать заметно крупнее, чем у Groq.
func (g *GeminiClient) MaxBatchTokens() int { return 12000 }

func (g *GeminiClient) activeModel() (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	for i := 0; i < len(g.models); i++ {
		idx := (g.current + i) % len(g.models)
		m := g.models[idx]
		if until, bad := g.exhausted[m]; bad && now.Before(until) {
			continue
		}
		delete(g.exhausted, m)
		g.current = idx
		return m, true
	}
	return "", false
}

func (g *GeminiClient) markExhausted(model string, retryAfter time.Duration) string {
	g.mu.Lock()
	if retryAfter < time.Minute {
		retryAfter = time.Minute
	}
	g.exhausted[model] = time.Now().Add(retryAfter)
	g.current = (g.current + 1) % len(g.models)
	g.mu.Unlock()
	next, ok := g.activeModel()
	if !ok {
		return ""
	}
	return next
}

type gemReq struct {
	Contents          []gemContent `json:"contents"`
	SystemInstruction *gemContent  `json:"systemInstruction,omitempty"`
	GenerationConfig  gemGenCfg    `json:"generationConfig"`
}
type gemContent struct {
	Parts []gemPart `json:"parts"`
}
type gemPart struct {
	Text string `json:"text"`
}
type gemGenCfg struct {
	Temperature      float64      `json:"temperature"`
	ResponseMIMEType string       `json:"responseMimeType"`
	MaxOutputTokens  int          `json:"maxOutputTokens,omitempty"`
	ThinkingConfig   *gemThinking `json:"thinkingConfig,omitempty"`
}

// Модели Gemini 3.x по умолчанию «размышляют», и эти токены идут в тот же
// лимит ответа. Для разбора объявления рассуждать не над чем — просим минимум.
type gemThinking struct {
	ThinkingLevel string `json:"thinkingLevel,omitempty"`
}
type gemResp struct {
	Candidates []struct {
		Content struct {
			Parts []gemPart `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// Parse — разбор одного объявления.
func (g *GeminiClient) Parse(ctx context.Context, text string) (*ParsedAd, error) {
	out, err := g.ParseBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("gemini: пустой ответ")
	}
	return out[0], nil
}

// ParseBatch — разбор пачки объявлений одним запросом.
func (g *GeminiClient) ParseBatch(ctx context.Context, texts []string) ([]*ParsedAd, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	var sb strings.Builder
	for i, t := range texts {
		fmt.Fprintf(&sb, "=== %d ===\n%s\n\n", i+1, strings.TrimSpace(t))
	}

	const maxRetries = 6
	model, ok := g.activeModel()
	if !ok {
		return nil, fmt.Errorf("gemini: суточная квота исчерпана у всех моделей")
	}
	// Настройка глубины «размышлений» есть не у всех поколений моделей.
	// Если версия её не понимает — повторяем запрос без неё.
	withThinking := true
	for attempt := 0; ; attempt++ {
		out, wait, err := g.once(ctx, sb.String(), len(texts), model, withThinking)
		if err == nil {
			return out, nil
		}
		if withThinking && strings.Contains(strings.ToLower(err.Error()), "thinking") {
			withThinking = false
			continue
		}
		if wait <= 0 || attempt >= maxRetries {
			return nil, err
		}
		if wait > 2*time.Minute {
			next := g.markExhausted(model, wait)
			if next == "" {
				return nil, fmt.Errorf("gemini: квота исчерпана (ждать %s)", wait)
			}
			model = next
			continue
		}
		if dl, okd := ctx.Deadline(); okd && time.Until(dl) < wait+time.Second {
			return nil, fmt.Errorf("gemini rate limit: нужно ждать %s, не укладываемся", wait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait + 500*time.Millisecond):
		}
	}
}

func (g *GeminiClient) once(ctx context.Context, joined string, n int, model string, withThinking bool) ([]*ParsedAd, time.Duration, error) {
	sys := systemPrompt() + batchSuffix
	body := gemReq{
		Contents:          []gemContent{{Parts: []gemPart{{Text: joined}}}},
		SystemInstruction: &gemContent{Parts: []gemPart{{Text: sys}}},
		GenerationConfig: gemGenCfg{
			Temperature:      0,
			ResponseMIMEType: "application/json",
			MaxOutputTokens:  400*n + 800,
		},
	}
	if withThinking {
		body.GenerationConfig.ThinkingConfig = &gemThinking{ThinkingLevel: "minimal"}
	}
	buf, _ := json.Marshal(body)

	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.apiKey)

	resp, err := g.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusTooManyRequests {
		// Gemini обычно просит подождать; если срок не указан — считаем,
		// что кончилась суточная квота, и уходим к следующему провайдеру.
		return nil, retryAfter(resp, string(raw)), fmt.Errorf("gemini rate limit")
	}

	var gr gemResp
	if err := json.Unmarshal(raw, &gr); err != nil {
		return nil, 0, fmt.Errorf("gemini decode: %w", err)
	}
	if gr.Error != nil {
		if gr.Error.Code == 429 || strings.Contains(strings.ToUpper(gr.Error.Status), "RESOURCE_EXHAUSTED") {
			return nil, 5 * time.Minute, fmt.Errorf("gemini rate limit")
		}
		return nil, 0, fmt.Errorf("gemini error: %s", gr.Error.Message)
	}
	if len(gr.Candidates) == 0 || len(gr.Candidates[0].Content.Parts) == 0 {
		return nil, 0, errModelFailed
	}

	content := strings.TrimSpace(gr.Candidates[0].Content.Parts[0].Text)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var br batchResp
	if err := json.Unmarshal([]byte(content), &br); err != nil {
		return nil, 0, fmt.Errorf("gemini parse json: %w", err)
	}
	out := make([]*ParsedAd, n)
	for _, item := range br.Ads {
		idx := item.Index - 1
		if idx < 0 || idx >= n {
			continue
		}
		ad := item.ParsedAd
		out[idx] = &ad
	}
	return out, 0, nil
}
