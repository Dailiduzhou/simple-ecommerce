package service

import (
	"context"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/community/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type CommunityService struct {
	pb.UnimplementedCommunityServer
	posts    *biz.PostUsecase
	comments *biz.CommentUsecase
}

func NewCommunityService(p *biz.PostUsecase, c *biz.CommentUsecase) *CommunityService {
	return &CommunityService{posts: p, comments: c}
}

func actor(ctx context.Context) (biz.Actor, error) {
	c, e := authenticatedClaims(ctx)
	if e != nil {
		return biz.Actor{}, e
	}
	return biz.Actor{ID: c.UserID, Admin: c.Role == "admin"}, nil
}
func authorProto(a biz.Author) *pb.Author { return &pb.Author{Id: a.ID, Nickname: a.Nickname} }
func postProto(p *biz.Post) *pb.Post {
	if p == nil {
		return nil
	}
	out := &pb.Post{Id: p.ID, Author: authorProto(p.Author), Title: p.Title, Content: p.Content, Version: p.Version, CreatedAt: timestamppb.New(p.CreatedAt), UpdatedAt: timestamppb.New(p.UpdatedAt), LikeCount: p.LikeCount, CommentCount: p.CommentCount, LikedByMe: p.LikedByMe}
	for _, m := range p.Images {
		out.Images = append(out.Images, &pb.Image{Id: m.ID, Url: m.URL, ContentType: m.ContentType, SizeBytes: m.SizeBytes, Width: m.Width, Height: m.Height})
	}
	return out
}

func commentProto(c *biz.Comment) *pb.Comment {
	if c == nil {
		return nil
	}
	out := &pb.Comment{Id: c.ID, PostId: c.PostID, Content: c.Content, RootCommentId: c.RootID, ReplyToCommentId: c.ReplyToID, Deleted: c.Deleted, CreatedAt: timestamppb.New(c.CreatedAt), ReplyCount: c.ReplyCount, ReplyToDeleted: c.ReplyToDeleted}
	if !c.Deleted {
		out.Author = authorProto(c.Author)
	}
	if c.ReplyToAuthor != nil && !c.ReplyToDeleted {
		out.ReplyToAuthor = authorProto(*c.ReplyToAuthor)
	}
	return out
}

func (s *CommunityService) CreatePost(ctx context.Context, r *pb.CreatePostRequest) (*pb.Post, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	p, e := s.posts.Create(ctx, a, biz.PostInput{Title: r.Title, Content: r.Content, ImageIDs: r.ImageIds})
	return postProto(p), e
}

func (s *CommunityService) GetPost(ctx context.Context, r *pb.PostRequest) (*pb.Post, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	p, e := s.posts.Get(ctx, a, r.Id)
	return postProto(p), e
}

func (s *CommunityService) ListPosts(ctx context.Context, r *pb.ListPostsRequest) (*pb.ListPostsReply, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	ps, next, e := s.posts.List(ctx, a, r.AuthorId, r.Cursor, r.PageSize)
	if e != nil {
		return nil, e
	}
	out := &pb.ListPostsReply{NextCursor: next}
	for _, p := range ps {
		out.Items = append(out.Items, postProto(&p))
	}
	return out, nil
}

func (s *CommunityService) UpdatePost(ctx context.Context, r *pb.UpdatePostRequest) (*pb.Post, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	p, e := s.posts.Update(ctx, a, r.Id, biz.PostInput{Title: r.Title, Content: r.Content, ImageIDs: r.ImageIds, ExpectedVersion: r.ExpectedVersion})
	return postProto(p), e
}

func (s *CommunityService) DeletePost(ctx context.Context, r *pb.PostRequest) (*pb.EmptyReply, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	e = s.posts.Delete(ctx, a, r.Id)
	return &pb.EmptyReply{}, e
}

func (s *CommunityService) LikePost(ctx context.Context, r *pb.PostRequest) (*pb.EmptyReply, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	e = s.posts.SetLike(ctx, a, r.Id, true)
	return &pb.EmptyReply{}, e
}

func (s *CommunityService) UnlikePost(ctx context.Context, r *pb.PostRequest) (*pb.EmptyReply, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	e = s.posts.SetLike(ctx, a, r.Id, false)
	return &pb.EmptyReply{}, e
}

func (s *CommunityService) CreateComment(ctx context.Context, r *pb.CreateCommentRequest) (*pb.Comment, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	c, e := s.comments.Create(ctx, a, r.PostId, r.Content, r.ReplyToCommentId)
	return commentProto(c), e
}

func (s *CommunityService) listComments(ctx context.Context, post, root int64, raw string, size int32) (*pb.ListCommentsReply, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	cs, next, e := s.comments.List(ctx, a, post, root, raw, size)
	if e != nil {
		return nil, e
	}
	out := &pb.ListCommentsReply{NextCursor: next}
	for _, c := range cs {
		out.Items = append(out.Items, commentProto(&c))
	}
	return out, nil
}

func (s *CommunityService) ListComments(ctx context.Context, r *pb.ListCommentsRequest) (*pb.ListCommentsReply, error) {
	return s.listComments(ctx, r.PostId, 0, r.Cursor, r.PageSize)
}

func (s *CommunityService) ListCommentReplies(ctx context.Context, r *pb.ListCommentRepliesRequest) (*pb.ListCommentsReply, error) {
	if e := biz.PositiveIDs(r.RootCommentId); e != nil {
		return nil, e
	}
	return s.listComments(ctx, r.PostId, r.RootCommentId, r.Cursor, r.PageSize)
}

func (s *CommunityService) DeleteComment(ctx context.Context, r *pb.DeleteCommentRequest) (*pb.EmptyReply, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	e = s.comments.Delete(ctx, a, r.PostId, r.Id)
	return &pb.EmptyReply{}, e
}
