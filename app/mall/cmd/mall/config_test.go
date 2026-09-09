package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/go-kratos/kratos/v2/config"
	"github.com/go-kratos/kratos/v2/config/env"
	"github.com/go-kratos/kratos/v2/config/file"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestExampleConfigurationLoadsEnvironmentWithoutCoercingSecrets(t *testing.T) {
	settings := map[string]string{
		"AUTH_ACCESS_TOKEN_SECRET": strings.Repeat("a", 32), "AUTH_REFRESH_TOKEN_SECRET": strings.Repeat("b", 32), "AUTH_PHONE_SECRET": strings.Repeat("1", 32),
		"SNOWFLAKE_NODE_ID": "1", "PAYMENT_ALIPAY_ENABLED": "false", "ALIPAY_IS_PRODUCTION": "false", "ALIPAY_APP_ID": "2026012345678901", "PAYMENT_WECHAT_ENABLED": "false", "WECHAT_IS_PRODUCTION": "false",
		"STORAGE_PROVIDER": "s3", "STORAGE_ENDPOINT": "minio:9000", "STORAGE_PUBLIC_ENDPOINT": "images.example.test", "STORAGE_BUCKET": "private-images", "STORAGE_USE_TLS": "false", "STORAGE_PUBLIC_USE_TLS": "true",
	}
	for k, v := range settings {
		t.Setenv(k, v)
	}
	load := func() (*conf.Bootstrap, error) {
		c := config.New(config.WithSource(env.NewSource(""), file.NewSource("../../configs/config.yaml")))
		defer c.Close()
		if e := c.Load(); e != nil {
			return nil, e
		}
		var bc conf.Bootstrap
		e := scanBootstrap(c, &bc)
		return &bc, e
	}
	bc, e := load()
	require.NoError(t, e)
	require.NoError(t, validateBootstrap(bc))
	require.Equal(t, settings["AUTH_PHONE_SECRET"], bc.Auth.PhoneSecret)
	require.Equal(t, settings["ALIPAY_APP_ID"], bc.Payment.Alipay.AppId)
	require.False(t, bc.Payment.Alipay.Enabled)
	require.True(t, bc.Storage.PublicUseTls)
	require.EqualValues(t, 90, bc.Community.HistoryRetentionDays)
	t.Setenv("STORAGE_PUBLIC_USE_TLS", "invalid")
	_, e = load()
	require.ErrorContains(t, e, "storage.public_use_tls must be a boolean")
}

// New bool config fields must not require a hand-kept normalization list: this
// walks the descriptor, feeds every bool as an environment-style string and
// fails if protojson still sees an unnormalized token.
func TestNormalizeBoolsCoversEveryBooleanField(t *testing.T) {
	md := (&conf.Bootstrap{}).ProtoReflect().Descriptor()
	values := map[string]any{}
	var paths []string
	var build func(protoreflect.MessageDescriptor, map[string]any, string)
	build = func(md protoreflect.MessageDescriptor, node map[string]any, prefix string) {
		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			path := string(fd.Name())
			if prefix != "" {
				path = prefix + "." + path
			}
			switch {
			case fd.IsMap() || fd.IsList():
			case fd.Message() != nil:
				child := map[string]any{}
				build(fd.Message(), child, path)
				if len(child) > 0 {
					node[string(fd.Name())] = child
				}
			case fd.Kind() == protoreflect.BoolKind:
				node[string(fd.Name())] = "true"
				paths = append(paths, path)
			}
		}
	}
	build(md, values, "")
	require.NotEmpty(t, paths)
	require.NoError(t, normalizeBools(values, md, ""))

	raw, err := json.Marshal(values)
	require.NoError(t, err)
	var bc conf.Bootstrap
	require.NoError(t, protojson.Unmarshal(raw, &bc))

	var check func(protoreflect.Message) error
	check = func(msg protoreflect.Message) error {
		var first error
		msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
			switch {
			case fd.IsMap() || fd.IsList():
			case fd.Message() != nil:
				first = check(v.Message())
				return first == nil
			case fd.Kind() == protoreflect.BoolKind && !v.Bool():
				first = fmt.Errorf("%s was not normalized", fd.FullName())
				return false
			}
			return true
		})
		return first
	}
	require.NoError(t, check(bc.ProtoReflect()))
}
