package biz

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"time"

	mediav1 "github.com/Dailiduzhou/simple-ecommerce/api/media/v1"
	_ "golang.org/x/image/webp"
)

const (
	MaxImageBytes     int64 = 10 << 20
	MaxImagePixels    int64 = 20_000_000
	MaxImageDimension       = 10000
)

type MediaAsset struct {
	ID, OwnerID                          int64
	Object                               ObjectRef
	StagingKey, ContentType, Status, URL string
	SizeBytes                            int64
	Width, Height                        int32
	ExpiresAt, UploadExpiresAt           time.Time
}
type VerifiedImage struct {
	ContentType   string
	Size          int64
	Width, Height int32
}
type MediaRepo interface {
	Create(context.Context, int64, MediaAsset) (*MediaAsset, error)
	Complete(context.Context, int64, int64, func(context.Context, MediaAsset) (VerifiedImage, error)) (*MediaAsset, error)
	Read(context.Context, int64, int64) (*MediaAsset, error)
	Sweep(context.Context) (int, error)
	Remove(context.Context, int64, func(context.Context, MediaAsset) error) error
}
type MediaUsecase struct {
	repo    MediaRepo
	storage ObjectStorage
	policy  MediaPolicy
}

func NewMediaUsecase(r MediaRepo, s ObjectStorage, p MediaPolicy) *MediaUsecase {
	return &MediaUsecase{r, s, p}
}
func supportedImage(m string) bool { return m == "image/jpeg" || m == "image/png" || m == "image/webp" }
func ValidateImage(b []byte, declared string, size int64) (VerifiedImage, error) {
	var v VerifiedImage
	if !supportedImage(declared) || int64(len(b)) != size || size < 1 || size > MaxImageBytes {
		return v, mediav1.ErrorInvalidImage("image type or size mismatch")
	}
	cfg, format, e := image.DecodeConfig(bytes.NewReader(b))
	if e != nil {
		return v, mediav1.ErrorInvalidImage("invalid image")
	}
	mime := map[string]string{"jpeg": "image/jpeg", "png": "image/png", "webp": "image/webp"}[format]
	if mime != declared || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxImageDimension || cfg.Height > MaxImageDimension || int64(cfg.Width)*int64(cfg.Height) > MaxImagePixels {
		return v, mediav1.ErrorInvalidImage("unsupported image type or dimensions")
	}
	// Decode the full payload after the dimension guard; headers alone are not proof.
	decoded, f, e := image.Decode(bytes.NewReader(b))
	if e != nil || f != format || decoded.Bounds().Dx() != cfg.Width || decoded.Bounds().Dy() != cfg.Height {
		return v, mediav1.ErrorInvalidImage("corrupt image data")
	}
	return VerifiedImage{mime, size, int32(cfg.Width), int32(cfg.Height)}, nil
}

func (u *MediaUsecase) CreateUpload(ctx context.Context, a Actor, mime string, size int64) (*MediaAsset, UploadGrant, error) {
	if e := a.Validate(); e != nil {
		return nil, UploadGrant{}, e
	}
	if !supportedImage(mime) || size < 1 || size > MaxImageBytes {
		return nil, UploadGrant{}, mediav1.ErrorInvalidImage("JPEG/PNG/WebP, maximum 10 MiB")
	}
	provider, bucket := u.storage.Location()
	if provider == "disabled" {
		return nil, UploadGrant{}, mediav1.ErrorStorageUnavailable("object storage is disabled")
	}
	key := make([]byte, 32)
	if _, e := rand.Read(key); e != nil {
		return nil, UploadGrant{}, e
	}
	name := hex.EncodeToString(key)
	now := time.Now().UTC()
	asset := MediaAsset{Object: ObjectRef{provider, bucket, "images/" + name}, StagingKey: "uploads/" + name, ContentType: mime, SizeBytes: size, ExpiresAt: now.Add(u.policy.UnusedRetention), UploadExpiresAt: now.Add(u.policy.UploadTTL)}
	assetPtr, e := u.repo.Create(ctx, a.ID, asset)
	if e != nil {
		return nil, UploadGrant{}, e
	}
	ref := asset.Object
	ref.Key = asset.StagingKey
	grant, e := u.storage.CreateUpload(ctx, ref, mime, size, asset.UploadExpiresAt)
	return assetPtr, grant, e
}

func (u *MediaUsecase) Complete(ctx context.Context, a Actor, id int64) (*MediaAsset, error) {
	if e := a.Validate(); e != nil {
		return nil, e
	}
	if e := PositiveIDs(id); e != nil {
		return nil, e
	}
	asset, e := u.repo.Complete(ctx, a.ID, id, func(ctx context.Context, m MediaAsset) (VerifiedImage, error) {
		ref := m.Object
		ref.Key = m.StagingKey
		b, e := u.storage.ReadObject(ctx, ref, MaxImageBytes)
		if e != nil {
			return VerifiedImage{}, e
		}
		v, e := ValidateImage(b, m.ContentType, m.SizeBytes)
		if e != nil {
			return v, e
		}
		created, e := u.storage.PutImmutable(ctx, m.Object, b, v.ContentType)
		if e != nil {
			return v, e
		}
		if !created {
			b, e = u.storage.ReadObject(ctx, m.Object, MaxImageBytes)
			if e != nil {
				return v, e
			}
			return ValidateImage(b, m.ContentType, m.SizeBytes)
		}
		return v, nil
	})
	if e != nil {
		return nil, e
	}
	asset.URL, e = u.storage.ReadURL(ctx, asset.Object, u.policy.ReadTTL)
	return asset, e
}

func (u *MediaUsecase) Get(ctx context.Context, a Actor, id int64) (*MediaAsset, error) {
	if e := a.Validate(); e != nil {
		return nil, e
	}
	if e := PositiveIDs(id); e != nil {
		return nil, e
	}
	m, e := u.repo.Read(ctx, a.ID, id)
	if e != nil {
		return nil, e
	}
	m.URL, e = u.storage.ReadURL(ctx, m.Object, u.policy.ReadTTL)
	return m, e
}
func (u *MediaUsecase) Sweep(ctx context.Context) (int, error) { return u.repo.Sweep(ctx) }
func (u *MediaUsecase) Remove(ctx context.Context, id int64) error {
	return u.repo.Remove(ctx, id, func(ctx context.Context, m MediaAsset) error {
		if e := u.storage.DeleteObject(ctx, m.Object); e != nil {
			return e
		}
		m.Object.Key = m.StagingKey
		return u.storage.DeleteObject(ctx, m.Object)
	})
}
