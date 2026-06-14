package postgres_test

import (
	"context"
	"os"
	"testing"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/memory/memtest"
	"github.com/nevindra/oasis/store/postgres"
)

func TestPostgres_ItemStoreConformance(t *testing.T) {
	dsn := os.Getenv("OASIS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set OASIS_TEST_POSTGRES_DSN to run")
	}
	memtest.ConformanceTest(t, func(t *testing.T) core.MemoryItemStore {
		ctx := context.Background()
		s, err := postgres.Open(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s.Memory()
	})
}
