package biz

import (
	"context"
	"time"

	communityv1 "github.com/Dailiduzhou/simple-ecommerce/api/community/v1"
)

type Comment struct {
	ID, PostID, RootID, ReplyToID int64
	Author                        Author
	Content                       string
	CreatedAt                     time.Time
	Deleted                       bool
	ReplyCount                    int64
	ReplyToDeleted                bool
	ReplyToAuthor                 *Author
}
type CommentRepo interface {
	Create(context.Context, Actor, int64, string, int64) (*Comment, error)
	List(context.Context, int64, int64, Page) ([]Comment, string, error)
	Delete(context.Context, Actor, int64, int64) error
}
type CommentUsecase struct{ repo CommentRepo }

func NewCommentUsecase(r CommentRepo) *CommentUsecase { return &CommentUsecase{r} }
func (u *CommentUsecase) Create(ctx context.Context, a Actor, postID int64, text string, target int64) (*Comment, error) {
	if e := a.Validate(); e != nil {
		return nil, e
	}
	if e := PositiveIDs(postID); e != nil {
		return nil, e
	}
	if target < 0 {
		return nil, communityv1.ErrorInvalidReplyTarget("invalid reply target")
	}
	text, e := ValidText(text, 1000)
	if e != nil {
		return nil, e
	}
	return u.repo.Create(ctx, a, postID, text, target)
}

func (u *CommentUsecase) List(ctx context.Context, a Actor, postID, rootID int64, raw string, size int32) ([]Comment, string, error) {
	if e := a.Validate(); e != nil {
		return nil, "", e
	}
	if e := PositiveIDs(postID); e != nil {
		return nil, "", e
	}
	if rootID < 0 {
		return nil, "", communityv1.ErrorInvalidReplyTarget("invalid root")
	}
	p, e := ParsePage(raw, Scope("comments", postID, rootID), size)
	if e != nil {
		return nil, "", e
	}
	return u.repo.List(ctx, postID, rootID, p)
}

func (u *CommentUsecase) Delete(ctx context.Context, a Actor, postID, id int64) error {
	if e := a.Validate(); e != nil {
		return e
	}
	if e := PositiveIDs(postID, id); e != nil {
		return e
	}
	return u.repo.Delete(ctx, a, postID, id)
}
