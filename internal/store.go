package internal

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Channel — активный канал из board_channels.
type Channel struct {
	Channel string
	Title   string
}

// ActiveChannels — какие каналы слушать.
func (s *Store) ActiveChannels(ctx context.Context) ([]Channel, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT channel, COALESCE(title,'') FROM board_channels WHERE is_active = TRUE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.Channel, &c.Title); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AdInput — что писать в board_ads.
type AdInput struct {
	Kind            string
	FromRegion      string
	ToRegion        string
	FromCountry     string
	ToCountry       string
	CargoDesc       string
	WeightKg        *float64
	VehicleType     string
	PriceText       string
	DateText        string
	ContactPhone    string
	ContactUsername string
	Lang            string
	OriginalText    string
	SourceChannel   string
	SourceMsgID     int64
	SourceLink      string
	PostedAt        time.Time
}

// InsertAd — вставить объявление. Дубли (канал+сообщение) игнорируются.
// Возвращает true, если реально вставлено (не дубль).
func (s *Store) InsertAd(ctx context.Context, in AdInput) (bool, error) {
	q := `INSERT INTO board_ads
		(id, kind, from_region, to_region, from_country, to_country,
		 cargo_desc, weight_kg, vehicle_type, price_text, date_text,
		 contact_phone, contact_username, lang, original_text,
		 source_channel, source_msg_id, source_link, posted_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,NOW())
		ON CONFLICT (source_channel, source_msg_id) WHERE source_msg_id IS NOT NULL
		DO NOTHING`
	tag, err := s.pool.Exec(ctx, q,
		uuid.New(), in.Kind, ns(in.FromRegion), ns(in.ToRegion), ns(in.FromCountry), ns(in.ToCountry),
		ns(in.CargoDesc), in.WeightKg, ns(in.VehicleType), ns(in.PriceText), ns(in.DateText),
		ns(in.ContactPhone), ns(in.ContactUsername), ns(in.Lang), in.OriginalText,
		in.SourceChannel, in.SourceMsgID, ns(in.SourceLink), in.PostedAt,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func ns(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// AdExists — объявление из этого сообщения уже сохранено?
// Проверяем ДО вызова ИИ, чтобы при перезапуске не тратить лимит Groq
// на посты, которые уже разобраны.
func (s *Store) AdExists(ctx context.Context, channel string, msgID int64) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM board_ads WHERE source_channel=$1 AND source_msg_id=$2)`,
		channel, msgID).Scan(&exists)
	return exists, err
}

// TextExists — такой же текст уже разобран? Каналы часто перепубликуют
// одно объявление, а платить ИИ за повтор незачем.
func (s *Store) TextExists(ctx context.Context, text string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM board_ads WHERE md5(original_text)=md5($1))`,
		text).Scan(&exists)
	return exists, err
}
