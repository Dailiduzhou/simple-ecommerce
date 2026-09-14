package biz

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	communityv1 "github.com/Dailiduzhou/simple-ecommerce/api/community/v1"
	"github.com/go-kratos/kratos/v2/errors"
)

// All new write paths take the actor from trusted claims. Administrators cannot
// impersonate another author, browse another user's history, or edit their posts.
type Actor struct {
	ID    int64
	Admin bool
}

func (a Actor) Validate() error {
	if a.ID <= 0 {
		return errors.Unauthorized("UNAUTHORIZED", "authentication is required")
	}
	return nil
}

func (a Actor) Owns(id int64, allowAdmin bool) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if a.ID != id && !(allowAdmin && a.Admin) {
		return communityv1.ErrorForbidden("resource belongs to another user")
	}
	return nil
}

type CommunityPolicy struct {
	HistoryRetention time.Duration
	CleanupBatch     int32
}

type Page struct {
	Scope     string
	Time      time.Time
	ID        int64
	Limit     int32
	HasCursor bool
}
type cursor struct {
	Version int       `json:"v"`
	Scope   string    `json:"s"`
	Time    time.Time `json:"t"`
	ID      int64     `json:"i"`
}

func ParsePage(raw, scope string, size int32) (Page, error) {
	p := Page{Scope: scope, Limit: size, Time: time.Unix(0, 0).UTC()}
	if size < 0 || size > 50 {
		return p, communityv1.ErrorInvalidCursor("page_size must be between 0 and 50")
	}
	if size == 0 {
		p.Limit = 20
	}
	if raw == "" {
		return p, nil
	}
	if len(raw) > 512 {
		return p, communityv1.ErrorInvalidCursor("cursor too long")
	}
	b, e := base64.RawURLEncoding.Strict().DecodeString(raw)
	if e != nil {
		return p, communityv1.ErrorInvalidCursor("malformed cursor")
	}
	var c cursor
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return p, communityv1.ErrorInvalidCursor("malformed cursor")
	}
	if d.Decode(new(any)) != io.EOF || c.Version != 1 || c.Scope != scope || c.ID <= 0 || c.Time.IsZero() || c.Time.Year() < 1970 || c.Time.Year() > 9999 {
		return p, communityv1.ErrorInvalidCursor("invalid cursor scope or key")
	}
	p.Time = c.Time
	p.ID = c.ID
	p.HasCursor = true
	return p, nil
}

func NextCursor(p Page, t time.Time, id int64) string {
	b, _ := json.Marshal(cursor{Version: 1, Scope: p.Scope, Time: t.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(b)
}

func ValidText(s string, max int) (string, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) < 1 || utf8.RuneCountInString(s) > max || strings.ContainsRune(s, 0) || strings.ContainsAny(s, "<>") {
		return "", communityv1.ErrorInvalidContent("plain text must contain 1 to %d Unicode characters; HTML is not accepted", max)
	}
	return s, nil
}

func PositiveIDs(ids ...int64) error {
	for _, id := range ids {
		if id <= 0 {
			return errors.BadRequest("INVALID_ID", "IDs must be positive")
		}
	}
	return nil
}

func Scope(kind string, ids ...int64) string {
	s := kind
	for _, id := range ids {
		s += fmt.Sprintf(":%d", id)
	}
	return s
}
