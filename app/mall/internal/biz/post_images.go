package biz

import (
	"context"

	mediav1 "github.com/Dailiduzhou/simple-ecommerce/api/media/v1"
)

// All fallible signing occurs before the write transaction. Nothing is returned
// to the caller until commit, and the transaction must revalidate every ref.
func (u *PostUsecase) prepareImages(ctx context.Context, a Actor, postID int64, input *PostInput) (map[int64]string, error) {
	input.PreparedImages = nil
	if len(input.ImageIDs) == 0 {
		return nil, nil
	}
	assets, err := u.repo.PrepareImages(ctx, a, postID, *input)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]MediaAsset, len(assets))
	for _, asset := range assets {
		byID[asset.ID] = asset
	}
	prepared := make(map[int64]string, len(input.ImageIDs))
	for _, id := range input.ImageIDs {
		asset, ok := byID[id]
		if !ok {
			return nil, mediav1.ErrorMediaNotReady("image not found")
		}
		link, err := u.storage.ReadURL(ctx, asset.Object, u.policy.ReadTTL)
		if err != nil {
			return nil, err
		}
		prepared[id] = link
		input.PreparedImages = append(input.PreparedImages, PostImageRef{ID: id, Object: asset.Object})
	}
	return prepared, nil
}

// No external I/O or new failure boundary is allowed after a successful write.
func applyPreparedPostURLs(post *Post, prepared map[int64]string) {
	for i := range post.Images {
		post.Images[i].URL = prepared[post.Images[i].ID]
	}
}
