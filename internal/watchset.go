package internal

import (
	"strings"
	"sync"
)

// WatchSet — потокобезопасное множество отслеживаемых каналов.
// Матчим и по @username, и на случай приватных — по числовому id ("channel_123").
type WatchSet struct {
	mu        sync.RWMutex
	usernames map[string]struct{} // без @, в нижнем регистре
	ids       map[int64]struct{}
}

func NewWatchSet() *WatchSet {
	return &WatchSet{
		usernames: map[string]struct{}{},
		ids:       map[int64]struct{}{},
	}
}

func (w *WatchSet) Set(chans []Channel) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.usernames = map[string]struct{}{}
	// ID, полученные резолвом (Bind), НЕ стираем — иначе после обновления
	// списка каналов перестанут матчиться посты без Entities.
	if w.ids == nil {
		w.ids = map[int64]struct{}{}
	}
	for _, c := range chans {
		v := strings.TrimSpace(c.Channel)
		if v == "" {
			continue
		}
		if strings.HasPrefix(v, "@") || !isNumeric(v) {
			w.usernames[normUser(v)] = struct{}{}
		} else {
			var id int64
			for _, r := range v {
				id = id*10 + int64(r-'0')
			}
			w.ids[id] = struct{}{}
		}
	}
}

func (w *WatchSet) Match(channelID int64, username string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if username != "" {
		if _, ok := w.usernames[normUser(username)]; ok {
			return true
		}
	}
	if _, ok := w.ids[channelID]; ok {
		return true
	}
	return false
}


// Bind — запомнить соответствие @username → channelID (после резолва через API).
// Матчинг по ID надёжнее: в апдейтах Telegram часто нет данных о канале.
func (w *WatchSet) Bind(username string, id int64, accessHash int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if username != "" {
		w.usernames[normUser(username)] = struct{}{}
	}
	if id != 0 {
		w.ids[id] = struct{}{}
	}
}

func normUser(s string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "@"))
}

func isNumeric(s string) bool {
	s = strings.TrimPrefix(s, "-")
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
