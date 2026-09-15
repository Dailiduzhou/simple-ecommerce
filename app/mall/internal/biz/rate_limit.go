package biz

import (
	"context"

	communityv1 "github.com/Dailiduzhou/simple-ecommerce/api/community/v1"
	mediav1 "github.com/Dailiduzhou/simple-ecommerce/api/media/v1"
	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
)

type WriteLimiter interface {
	Allow(ctx context.Context, actorID int64, ip, operation string) error
}

// RateLimitCategory returns the quota bucket for a rate-limited write
// operation, or "" for read-only operations. Transport middleware uses it so
// reads never depend on the limiter or Redis availability. The "auth" bucket
// also applies to the JWT-whitelisted login/register/refresh endpoints, which
// arrive without claims and are throttled on the IP dimension alone.
func RateLimitCategory(operation string) string {
	switch operation {
	case communityv1.OperationCommunityCreatePost, communityv1.OperationCommunityUpdatePost:
		return "posts"
	case communityv1.OperationCommunityCreateComment:
		return "comments"
	case mediav1.OperationMediaCreateImageUpload, mediav1.OperationMediaCompleteImageUpload:
		return "uploads"
	case userv1.OperationUserRecordProductView, userv1.OperationUserDeleteBrowsingHistoryItem, userv1.OperationUserClearBrowsingHistory,
		communityv1.OperationCommunityLikePost, communityv1.OperationCommunityUnlikePost, communityv1.OperationCommunityDeletePost, communityv1.OperationCommunityDeleteComment:
		return "interactions"
	case userv1.OperationUserLogin, userv1.OperationUserRegister, userv1.OperationUserRefreshToken:
		return "auth"
	}
	return ""
}
