package biz

import "errors"

var ErrMediaCleanupNotDue = errors.New("upload grant may still be live")

const (
	HistoryCleanupKind = "cleanup_browsing_history"
	MediaSweepKind     = "sweep_media"
	MediaDeleteKind    = "delete_media"
)

type HistoryCleanupArgs struct{}

func (HistoryCleanupArgs) Kind() string { return HistoryCleanupKind }

type MediaSweepArgs struct{}

func (MediaSweepArgs) Kind() string { return MediaSweepKind }

type MediaDeleteArgs struct {
	MediaID int64 `json:"media_id"`
}

func (MediaDeleteArgs) Kind() string { return MediaDeleteKind }
