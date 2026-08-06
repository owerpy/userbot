package internal

import (
	"context"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Parser — общий интерфейс для провайдеров разбора объявлений.
type Parser interface {
	Parse(ctx context.Context, text string) (*ParsedAd, error)
	ParseBatch(ctx context.Context, texts []string) ([]*ParsedAd, error)
	MaxBatchTokens() int
	Models() []string
	Name() string
}

// Chain — цепочка провайдеров. Когда у одного кончается суточная квота,
// запросы идут к следующему; к исчерпанному возвращаемся после сброса.
// Это позволяет складывать бесплатные лимиты разных сервисов законно,
// вместо заведения нескольких аккаунтов у одного и того же.
type Chain struct {
	providers []Parser
	log       *zap.Logger

	mu        sync.Mutex
	current   int
	exhausted map[string]time.Time
}

func NewChain(log *zap.Logger, providers ...Parser) *Chain {
	var live []Parser
	for _, p := range providers {
		if p != nil {
			live = append(live, p)
		}
	}
	return &Chain{providers: live, log: log, exhausted: map[string]time.Time{}}
}

func (c *Chain) active() (Parser, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for i := 0; i < len(c.providers); i++ {
		idx := (c.current + i) % len(c.providers)
		p := c.providers[idx]
		if until, bad := c.exhausted[p.Name()]; bad && now.Before(until) {
			continue
		}
		delete(c.exhausted, p.Name())
		c.current = idx
		return p, true
	}
	return nil, false
}

func (c *Chain) markExhausted(p Parser) {
	c.mu.Lock()
	c.exhausted[p.Name()] = time.Now().Add(30 * time.Minute)
	c.current = (c.current + 1) % len(c.providers)
	c.mu.Unlock()
}

// isQuotaError — у провайдера кончился лимит, есть смысл идти к следующему.
func isQuotaError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "квота") ||
		strings.Contains(s, "rate limit") ||
		strings.Contains(s, "не укладываемся") ||
		strings.Contains(s, "исчерпана")
}

func (c *Chain) Name() string { return "chain" }

func (c *Chain) Models() []string {
	var out []string
	for _, p := range c.providers {
		for _, m := range p.Models() {
			out = append(out, p.Name()+":"+m)
		}
	}
	return out
}

// MaxBatchTokens — предел текущего провайдера (у Gemini он много больше).
func (c *Chain) MaxBatchTokens() int {
	if p, ok := c.active(); ok {
		return p.MaxBatchTokens()
	}
	return 1500
}

func (c *Chain) Parse(ctx context.Context, text string) (*ParsedAd, error) {
	out, err := c.ParseBatch(ctx, []string{text})
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return out[0], nil
}

func (c *Chain) ParseBatch(ctx context.Context, texts []string) ([]*ParsedAd, error) {
	var lastErr error
	for attempt := 0; attempt < len(c.providers); attempt++ {
		p, ok := c.active()
		if !ok {
			break
		}
		out, err := p.ParseBatch(ctx, texts)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !isQuotaError(err) {
			return nil, err // не про лимиты — незачем дёргать другой сервис
		}
		c.log.Warn("лимит провайдера исчерпан — переключаюсь",
			zap.String("provider", p.Name()), zap.Error(err))
		c.markExhausted(p)
	}
	if lastErr == nil {
		lastErr = context.DeadlineExceeded
	}
	return nil, lastErr
}
