package v1

import (
	"testing"

	"buf.build/go/protovalidate"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestPostImageValidation(t *testing.T) {
	requests := []struct {
		name string
		new  func([]int64) proto.Message
	}{
		{"create", func(ids []int64) proto.Message {
			return &CreatePostRequest{Title: "标题", Content: "正文", ImageIds: ids}
		}},
		{"update", func(ids []int64) proto.Message {
			return &UpdatePostRequest{Id: 1, ExpectedVersion: 1, Title: "标题", Content: "正文", ImageIds: ids}
		}},
	}
	cases := []struct {
		name string
		ids  []int64
		fail bool
	}{
		{"omitted", nil, false},
		{"empty", []int64{}, false},
		{"one", []int64{1}, false},
		{"nine", []int64{1, 2, 3, 4, 5, 6, 7, 8, 9}, false},
		{"ten", []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, true},
		{"duplicate", []int64{1, 1}, true},
		{"zero_id", []int64{0}, true},
		{"negative_id", []int64{-1}, true},
	}
	for _, request := range requests {
		for _, tc := range cases {
			t.Run(request.name+"/"+tc.name, func(t *testing.T) {
				err := protovalidate.Validate(request.new(tc.ids))
				if tc.fail {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
			})
		}
	}
}

func TestPostJSONOptionalImages(t *testing.T) {
	for _, imageField := range []string{"", `,"image_ids":[]`} {
		t.Run(imageField, func(t *testing.T) {
			var create CreatePostRequest
			require.NoError(t, protojson.Unmarshal([]byte(`{"title":"标题","content":"正文"`+imageField+`}`), &create))
			require.NoError(t, protovalidate.Validate(&create))
			require.Empty(t, create.ImageIds)

			var update UpdatePostRequest
			require.NoError(t, protojson.Unmarshal([]byte(`{"id":"1","expected_version":"1","title":"标题","content":"正文"`+imageField+`}`), &update))
			require.NoError(t, protovalidate.Validate(&update))
			require.Empty(t, update.ImageIds)
		})
	}
}
