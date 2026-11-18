package engine

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestCatalogUsesMirrorWithoutWaitingForOfficial(t *testing.T) {
	officialCancelled := make(chan struct{})
	result, err := firstCatalog(t.Context(),
		func(ctx context.Context) (string, error) {
			<-ctx.Done()
			close(officialCancelled)
			return "", ctx.Err()
		},
		func(context.Context) (string, error) { return "mirror", nil },
	)
	if err != nil || result != "mirror" {
		t.Fatalf("mirror result: %q, %v", result, err)
	}
	select {
	case <-officialCancelled:
	case <-time.After(time.Second):
		t.Fatal("official request was still running")
	}
}

func TestLiveCacheRoundTrip(t *testing.T) {
	if os.Getenv("HJML_INTEGRATION") != "1" {
		t.Skip("set HJML_INTEGRATION=1 to query version catalogs")
	}
	core := Engine{DataDir: t.TempDir()}
	versions, err := core.RefreshVersions(t.Context())
	if err != nil || len(versions) == 0 {
		t.Fatalf("refresh Mojang list: %v", err)
	}
	if cached, stamp := core.CachedVersions(); len(cached) != len(versions) || cached[0].ID != versions[0].ID || time.Since(stamp) >= CatalogMaxAge {
		t.Fatal("Minecraft version cache was not persisted")
	}
	t.Logf("saved %d Minecraft versions", len(versions))
	for _, loader := range []string{"fabric", "forge", "neoforge"} {
		start := time.Now()
		builds, err := core.RefreshBuilds(t.Context(), "1.21.1", loader)
		if err != nil || len(builds) < 2 {
			t.Fatalf("refresh %s list: %v", loader, err)
		}
		t.Logf("%s network: %d builds in %s", loader, len(builds), time.Since(start).Round(time.Millisecond))
		start = time.Now()
		cached, stamp, found := core.CachedBuilds("1.21.1", loader)
		if !found || len(cached) != len(builds) || cached[0].Version != builds[0].Version || time.Since(stamp) >= CatalogMaxAge {
			t.Fatalf("%s build cache was not persisted", loader)
		}
		t.Logf("%s disk: %s", loader, time.Since(start).Round(time.Microsecond))
	}
}
