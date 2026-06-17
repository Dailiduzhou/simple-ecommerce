//go:build integration
// +build integration

package data

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPaymentMQRepo_EnqueueCheckPay_Dedup verifies that PaymentMQRepo enqueues
// River jobs and deduplicates them by OutTradeNo via river.UniqueOpts.
func TestPaymentMQRepo_EnqueueCheckPay_Dedup(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, testDB, cleanup := createTestDatabase(ctx, t, dsn)
	defer cleanup()

	t.Logf("using test database: %s", testDB)

	workers := river.NewWorkers()
	riverClient, err := NewRiverClient(pool, workers)
	require.NoError(t, err)
	defer riverClient.Stop(ctx)

	repo := NewPaymentMQRepo(riverClient, log.DefaultLogger)

	args := biz.CheckPayArgs{
		PaymentID:           1,
		OrderID:             2,
		OutTradeNo:          "otn-dedup-1",
		Channel:             "wechat",
		MaxPolls:            10,
		PollIntervalSeconds: 5,
		Source:              "integration_test",
	}

	// First enqueue should succeed.
	job1, err := repo.EnqueueCheckPay(ctx, args, time.Time{})
	require.NoError(t, err)
	require.NotNil(t, job1)
	assert.Equal(t, "check_pay", job1.Kind)
	assert.Equal(t, "payments", job1.Queue)
	assert.Equal(t, "available", job1.State)
	assert.Equal(t, int32(10), job1.MaxAttempts)

	// Second enqueue with the same OutTradeNo should be skipped as duplicate.
	args2 := args
	args2.MaxPolls = 99 // different polling params should not affect dedup
	job2, err := repo.EnqueueCheckPay(ctx, args2, time.Time{})
	require.NoError(t, err)
	require.NotNil(t, job2)
	assert.Equal(t, job1.ID, job2.ID, "duplicate job should return the existing job")

	// A different OutTradeNo should create a new job.
	args3 := args
	args3.OutTradeNo = "otn-dedup-2"
	job3, err := repo.EnqueueCheckPay(ctx, args3, time.Time{})
	require.NoError(t, err)
	require.NotNil(t, job3)
	assert.NotEqual(t, job1.ID, job3.ID)

	// GetMQJob should retrieve the job by ID.
	got, err := repo.GetMQJob(ctx, job1.ID)
	require.NoError(t, err)
	assert.Equal(t, job1.ID, got.ID)
	assert.Equal(t, job1.Kind, got.Kind)
}

func createTestDatabase(ctx context.Context, t *testing.T, dsn string) (*pgxpool.Pool, string, func()) {
	t.Helper()

	testDB := fmt.Sprintf("ecommerce_test_%d", time.Now().UnixNano())

	// Connect to the database from the DSN to create the test DB.
	adminPool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer adminPool.Close()

	_, err = adminPool.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s", testDB))
	require.NoError(t, err)

	// Build a DSN that points to the new test database.
	testDSN := replaceDBName(dsn, testDB)

	cfg := &conf.Data{
		Database: &conf.Data_Database{
			Source: testDSN,
		},
	}

	if err := RunMigrations(cfg); err != nil {
		_, _ = adminPool.Exec(ctx, fmt.Sprintf("DROP DATABASE %s WITH (FORCE)", testDB))
		t.Fatalf("run migrations: %v", err)
	}

	pool, err := pgxpool.New(ctx, testDSN)
	require.NoError(t, err)

	cleanup := func() {
		pool.Close()
		adminPool2, err := pgxpool.New(ctx, dsn)
		if err == nil {
			_, _ = adminPool2.Exec(ctx, fmt.Sprintf("DROP DATABASE %s WITH (FORCE)", testDB))
			adminPool2.Close()
		}
	}

	return pool, testDB, cleanup
}

func replaceDBName(dsn, dbName string) string {
	// Handle postgres://user:pass@host:port/dbname?params
	if idx := strings.LastIndex(dsn, "/"); idx != -1 {
		before := dsn[:idx+1]
		after := dsn[idx+1:]
		if qIdx := strings.Index(after, "?"); qIdx != -1 {
			return before + dbName + after[qIdx:]
		}
		return before + dbName
	}
	return dsn
}
