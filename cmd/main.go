package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"

	"github.com/owerpy/board-userbot/internal"
)

// Userbot слушает чужие TG-каналы (под личным аккаунтом-подписчиком),
// разбирает новые посты через Groq и складывает в board_ads.
func main() {
	log, _ := zap.NewProduction()
	defer log.Sync()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// ── конфиг из окружения ──
	appIDStr := os.Getenv("TG_APP_ID")       // https://my.telegram.org -> API_ID
	appHash := os.Getenv("TG_APP_HASH")      // API_HASH
	phone := os.Getenv("TG_PHONE")           // номер аккаунта-подписчика (+998...)
	sessionDir := os.Getenv("TG_SESSION_DIR") // где хранить сессию (том!)
	dsn := os.Getenv("DATABASE_URL")         // postgres://...
	groqKey := os.Getenv("GROQ_API_KEY")
	groqModel := os.Getenv("GROQ_MODEL")

	if appIDStr == "" || appHash == "" || phone == "" || dsn == "" || groqKey == "" {
		log.Fatal("missing env: need TG_APP_ID, TG_APP_HASH, TG_PHONE, DATABASE_URL, GROQ_API_KEY")
	}
	if sessionDir == "" {
		sessionDir = "/data/session"
	}
	var appID int
	fmt.Sscanf(appIDStr, "%d", &appID)

	// ── зависимости ──
	store, err := internal.NewStore(ctx, dsn)
	if err != nil {
		log.Fatal("db connect", zap.Error(err))
	}
	defer store.Close()
	groq := internal.NewGroqClient(groqKey, groqModel)

	// ── обработчик новых сообщений в каналах ──
	d := tg.NewUpdateDispatcher()

	// множество отслеживаемых каналов (обновляется периодически из БД)
	watched := internal.NewWatchSet()
	refreshWatched(ctx, store, watched, log)
	go func() {
		t := time.NewTicker(2 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				refreshWatched(ctx, store, watched, log)
			}
		}
	}()

	handleNew := func(peerChannelID int64, username string, msg *tg.Message) {
		text := msg.Message
		if strings.TrimSpace(text) == "" {
			return
		}
		if !watched.Match(peerChannelID, username) {
			return
		}
		// разбираем через ИИ
		// Запас на повторы при лимите Groq (он просит ждать по 3-6 сек).
		pctx, pcancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer pcancel()
		parsed, err := groq.Parse(pctx, text)
		if err != nil {
			log.Warn("groq parse", zap.Error(err))
			return
		}
		if !parsed.IsAd {
			return // не объявление — пропускаем
		}
		src := username
		if src == "" {
			src = fmt.Sprintf("channel_%d", peerChannelID)
		}
		link := ""
		if username != "" {
			link = fmt.Sprintf("https://t.me/%s/%d", username, msg.ID)
		}
		kind := parsed.Kind
		if kind != "truck" {
			kind = "cargo"
		}
		// Приводим регионы и кузов к справочнику приложения: в объявлениях
		// пишут «Samarqand», «Тошкент вилояти», а фильтры ищут «Самарканд».
		fromRegion, fromCountry := internal.NormalizeRegion(parsed.FromRegion)
		toRegion, toCountry := internal.NormalizeRegion(parsed.ToRegion)
		if fromCountry == "" {
			fromCountry = parsed.FromCountry
		}
		if toCountry == "" {
			toCountry = parsed.ToCountry
		}
		vehType := internal.NormalizeVehicle(parsed.VehicleType)

		ins := internal.AdInput{
			Kind:            kind,
			FromRegion:      fromRegion,
			ToRegion:        toRegion,
			FromCountry:     fromCountry,
			ToCountry:       toCountry,
			CargoDesc:       parsed.CargoDesc,
			WeightKg:        parsed.WeightKg,
			VehicleType:     vehType,
			PriceText:       parsed.PriceText,
			DateText:        parsed.DateText,
			ContactPhone:    parsed.ContactPhone,
			ContactUsername: parsed.ContactUsername,
			Lang:            parsed.Lang,
			OriginalText:    text,
			SourceChannel:   "@" + strings.TrimPrefix(src, "@"),
			SourceMsgID:     int64(msg.ID),
			SourceLink:      link,
			PostedAt:        time.Unix(int64(msg.Date), 0),
		}
		ictx, icancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer icancel()
		ok, err := store.InsertAd(ictx, ins)
		if err != nil {
			log.Warn("insert ad", zap.Error(err))
			return
		}
		if ok {
			log.Info("ad added", zap.String("route", fromRegion+"→"+toRegion),
				zap.String("kind", kind), zap.String("src", ins.SourceChannel))
		}
	}

	// новые посты в каналах приходят как UpdateNewChannelMessage
	d.OnNewChannelMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewChannelMessage) error {
		msg, ok := u.Message.(*tg.Message)
		if !ok {
			return nil
		}
		peer, ok := msg.PeerID.(*tg.PeerChannel)
		if !ok {
			return nil
		}
		// Entities не всегда содержат канал — тогда username пустой,
		// и матчим по ID (он резолвится при старте из @username).
		username := ""
		if ch, okc := e.Channels[peer.ChannelID]; okc {
			username = ch.Username
		}
		log.Info("post received",
			zap.Int64("channel_id", peer.ChannelID),
			zap.String("username", username),
			zap.Int("msg_id", msg.ID),
			zap.Bool("watched", watched.Match(peer.ChannelID, username)))
		handleNew(peer.ChannelID, username, msg)
		return nil
	})

	// ── клиент Telegram под личным аккаунтом ──
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		log.Fatal("session dir", zap.Error(err))
	}
	gaps := updates.New(updates.Config{Handler: d})

	client := telegram.NewClient(appID, appHash, telegram.Options{
		SessionStorage: &telegram.FileSessionStorage{Path: sessionDir + "/session.json"},
		UpdateHandler:  gaps,
		Logger:         log.Named("td"),
	})

	flow := auth.NewFlow(
		termAuth{phone: phone},
		auth.SendCodeOptions{},
	)

	if err := client.Run(ctx, func(ctx context.Context) error {
		if err := client.Auth().IfNecessary(ctx, flow); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
		self, err := client.Self(ctx)
		if err != nil {
			return err
		}
		log.Info("logged in", zap.String("user", self.Username), zap.Int64("id", self.ID))

		api := client.API()

		// Резолвим @username каналов в их числовые ID — так матчинг постов
		// работает даже когда Telegram не прислал Entities.
		resolveWatched(ctx, api, store, watched, log)
		go func() {
			t := time.NewTicker(5 * time.Minute)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					resolveWatched(ctx, api, store, watched, log)
				}
			}
		}()

		// Первичная загрузка: читаем последние посты каналов, чтобы доска
		// не была пустой до появления новых сообщений.
		if os.Getenv("BOARD_BACKFILL") != "0" {
			go backfill(ctx, api, store, log, handleNew)
		}

		return gaps.Run(ctx, api, self.ID, updates.AuthOptions{
			OnStart: func(ctx context.Context) {
				log.Info("listening for channel posts...")
			},
		})
	}); err != nil {
		log.Fatal("client run", zap.Error(err))
	}
	_ = uuid.Nil
}

func refreshWatched(ctx context.Context, store *internal.Store, ws *internal.WatchSet, log *zap.Logger) {
	chans, err := store.ActiveChannels(ctx)
	if err != nil {
		log.Warn("load channels", zap.Error(err))
		return
	}
	ws.Set(chans)
	log.Info("watching channels", zap.Int("count", len(chans)))
}

// termAuth — ввод телефона/кода/пароля при первом входе (сессия потом сохраняется в томе).
type termAuth struct {
	phone string
}

func (a termAuth) Phone(_ context.Context) (string, error) { return a.phone, nil }

func (a termAuth) Password(_ context.Context) (string, error) {
	// если на аккаунте включён 2FA — задать через переменную окружения TG_2FA
	if p := os.Getenv("TG_2FA"); p != "" {
		return p, nil
	}
	fmt.Print("Enter 2FA password: ")
	var pw string
	fmt.Scanln(&pw)
	return pw, nil
}

func (a termAuth) Code(_ context.Context, _ *tg.AuthSentCode) (string, error) {
	fmt.Print("Enter code from Telegram: ")
	var code string
	fmt.Scanln(&code)
	return strings.TrimSpace(code), nil
}

func (a termAuth) AcceptTermsOfService(_ context.Context, _ tg.HelpTermsOfService) error { return nil }
func (a termAuth) SignUp(_ context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, fmt.Errorf("signup not supported")
}

// resolveWatched — превращает @username каналов в числовые ID и запоминает их.
// Нужно потому, что в апдейтах Telegram часто не присылает данные о канале,
// и сопоставить пост с каналом можно только по ID.
func resolveWatched(ctx context.Context, api *tg.Client, store *internal.Store,
	ws *internal.WatchSet, log *zap.Logger) {

	chans, err := store.ActiveChannels(ctx)
	if err != nil {
		log.Warn("resolve: load channels", zap.Error(err))
		return
	}
	for _, c := range chans {
		uname := strings.TrimPrefix(strings.TrimSpace(c.Channel), "@")
		if uname == "" {
			continue
		}
		res, err := api.ContactsResolveUsername(ctx, uname)
		if err != nil {
			log.Warn("resolve username", zap.String("channel", c.Channel), zap.Error(err))
			continue
		}
		for _, ch := range res.Chats {
			if full, ok := ch.(*tg.Channel); ok {
				ws.Bind(full.Username, full.ID, full.AccessHash)
				log.Info("channel resolved",
					zap.String("username", full.Username), zap.Int64("id", full.ID))
			}
		}
	}
}

// backfill — разовая подгрузка последних постов каналов при старте,
// чтобы доска сразу наполнилась, а не ждала новых сообщений.
func backfill(ctx context.Context, api *tg.Client, store *internal.Store,
	log *zap.Logger, handle func(int64, string, *tg.Message)) {

	time.Sleep(3 * time.Second) // дать резолву отработать

	chans, err := store.ActiveChannels(ctx)
	if err != nil {
		return
	}
	limit := 15
	if v := os.Getenv("BOARD_BACKFILL_LIMIT"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}

	for _, c := range chans {
		uname := strings.TrimPrefix(strings.TrimSpace(c.Channel), "@")
		if uname == "" {
			continue
		}
		res, err := api.ContactsResolveUsername(ctx, uname)
		if err != nil {
			log.Warn("backfill resolve", zap.String("channel", c.Channel), zap.Error(err))
			continue
		}
		var peer *tg.InputPeerChannel
		var username string
		for _, ch := range res.Chats {
			if full, ok := ch.(*tg.Channel); ok {
				peer = &tg.InputPeerChannel{ChannelID: full.ID, AccessHash: full.AccessHash}
				username = full.Username
			}
		}
		if peer == nil {
			continue
		}

		hist, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:  peer,
			Limit: limit,
		})
		if err != nil {
			log.Warn("backfill history", zap.String("channel", c.Channel), zap.Error(err))
			continue
		}
		msgs, ok := hist.(*tg.MessagesChannelMessages)
		if !ok {
			continue
		}
		log.Info("backfill start", zap.String("channel", c.Channel), zap.Int("messages", len(msgs.Messages)))
		for _, m := range msgs.Messages {
			msg, ok := m.(*tg.Message)
			if !ok || strings.TrimSpace(msg.Message) == "" {
				continue
			}
			handle(peer.ChannelID, username, msg)
			time.Sleep(4 * time.Second) // ~12k токенов/мин на бесплатном тарифе: держим темп
		}
		log.Info("backfill done", zap.String("channel", c.Channel))
	}
}
