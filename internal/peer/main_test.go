package peer_test

import (
	"os"
	"testing"

	"github.com/strata-db/strata/internal/metrics"
)

func TestMain(m *testing.M) {
	metrics.Register(nil)
	os.Exit(m.Run())
}
