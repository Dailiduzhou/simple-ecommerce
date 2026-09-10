package biz

import (
	"context"
	"time"

	communityv1 "github.com/Dailiduzhou/simple-ecommerce/api/community/v1"
)

type Author struct {
	ID       int64
	Nickname string
}
type Post struct {
	ID                      int64
	Author                  Author
	Title, Content          string
	Version                 int64
	CreatedAt, UpdatedAt    time.Time
	Images                  []MediaAsset
	LikeCount, CommentCount int64
	LikedByMe               bool
}
type PostInput struct {
	Title, Content string
	// ImageIDs is optional (0-9). On update, nil/empty removes all images.
	ImageIDs        []int64
	ExpectedVersion int64
}

func (i PostInput) Validate() (PostInput, error) {
	var e error
	i.Title, e = ValidText(i.Title, 100)
	if e != nil {
		return i, e
	}
	i.Content, e = ValidText(i.Content, 5000)
	if e != nil {
		return i, e
	}
	if len(i.ImageIDs) > 9 {
		return i, communityv1.ErrorInvalidContent("posts allow at most 9 images")
	}
	seen := map[int64]bool{}
	for _, id := range i.ImageIDs {
		if id <= 0 || seen[id] {
			return i, communityv1.ErrorInvalidContent("invalid or duplicate image ID")
		}
		seen[id] = true
	}
	return i, nil
}

type PostRepo interface {
	Create(context.Context, Actor, PostInput) (*Post, error)
	Get(context.Context, int64, int64) (*Post, error)
	List(context.Context, int64, int64, Page) ([]Post, string, error)
	Update(context.Context, Actor, int64, PostInput) (*Post, error)
	Delete(context.Context, Actor, int64) error
	SetLike(context.Context, Actor, int64, bool) error
}
type PostUsecase struct {
	repo    PostRepo
	storage ObjectStorage
	policy  MediaPolicy
}

func NewPostUsecase(r PostRepo, s ObjectStorage, p MediaPolicy) *PostUsecase {
	return &PostUsecase{r, s, p}
}

func (u *PostUsecase) images(ctx context.Context, p *Post) error {
	for i := range p.Images {
		url, e := u.storage.ReadURL(ctx, p.Images[i].Object, u.policy.ReadTTL)
		if e != nil {
			return e
		}
		p.Images[i].URL = url
	}
	return nil
}

func (u *PostUsecase) Create(ctx context.Context, a Actor, i PostInput) (*Post, error) {
	if e := a.Validate(); e != nil {
		return nil, e
	}
	i, e := i.Validate()
	if e != nil {
		return nil, e
	}
	p, e := u.repo.Create(ctx, a, i)
	if e != nil {
		return nil, e
	}
	e = u.images(ctx, p)
	return p, e
}

func (u *PostUsecase) Get(ctx context.Context, a Actor, id int64) (*Post, error) {
	if e := a.Validate(); e != nil {
		return nil, e
	}
	if e := PositiveIDs(id); e != nil {
		return nil, e
	}
	p, e := u.repo.Get(ctx, a.ID, id)
	if e != nil {
		return nil, e
	}
	e = u.images(ctx, p)
	return p, e
}

func (u *PostUsecase) List(ctx context.Context, a Actor, author int64, raw string, size int32) ([]Post, string, error) {
	if e := a.Validate(); e != nil {
		return nil, "", e
	}
	if author < 0 {
		return nil, "", communityv1.ErrorInvalidContent("invalid author")
	}
	page, e := ParsePage(raw, Scope("posts", author), size)
	if e != nil {
		return nil, "", e
	}
	ps, next, e := u.repo.List(ctx, a.ID, author, page)
	if e != nil {
		return nil, "", e
	}
	for i := range ps {
		if e = u.images(ctx, &ps[i]); e != nil {
			return nil, "", e
		}
	}
	return ps, next, nil
}

func (u *PostUsecase) Update(ctx context.Context, a Actor, id int64, i PostInput) (*Post, error) {
	if e := a.Validate(); e != nil {
		return nil, e
	}
	if e := PositiveIDs(id, i.ExpectedVersion); e != nil {
		return nil, e
	}
	i, e := i.Validate()
	if e != nil {
		return nil, e
	}
	p, e := u.repo.Update(ctx, a, id, i)
	if e != nil {
		return nil, e
	}
	e = u.images(ctx, p)
	return p, e
}

func (u *PostUsecase) Delete(ctx context.Context, a Actor, id int64) error {
	if e := a.Validate(); e != nil {
		return e
	}
	if e := PositiveIDs(id); e != nil {
		return e
	}
	return u.repo.Delete(ctx, a, id)
}

func (u *PostUsecase) SetLike(ctx context.Context, a Actor, id int64, on bool) error {
	if e := a.Validate(); e != nil {
		return e
	}
	if e := PositiveIDs(id); e != nil {
		return e
	}
	return u.repo.SetLike(ctx, a, id, on)
}
