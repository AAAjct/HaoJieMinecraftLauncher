package engine

import (
	"os"
	"slices"
	"testing"
)

func TestForgeBuildsMatchMinecraftVersion(t *testing.T) {
	builds := forgeBuildsForGame("1.21.1", []string{
		"1.21.1-52.0.9", "1.21.1-52.0.10", "1.21.1-52.1.2", "1.21.10-60.0.0", "1.21.1-14.23.5",
	})
	if len(builds) != 3 || builds[0].Version != "52.1.2" || builds[1].Version != "52.0.10" {
		t.Fatalf("wrong Forge build list: %+v", builds)
	}
}

func TestLiveLoaderCatalogs(t *testing.T) {
	if os.Getenv("HJML_INTEGRATION") != "1" {
		t.Skip("set HJML_INTEGRATION=1 to query the loader catalogs")
	}
	for _, item := range []struct{ loader, known string }{
		{"forge", "52.1.20"}, {"fabric", "0.16.10"}, {"quilt", "0.29.2-beta.5"},
	} {
		builds, err := LoaderBuilds(t.Context(), "1.21.1", item.loader)
		if err != nil || len(builds) < 2 || !slices.ContainsFunc(builds, func(b LoaderBuild) bool { return b.Version == item.known }) {
			t.Fatalf("%s did not return known alternatives: %d builds, %v", item.loader, len(builds), err)
		}
		t.Logf("%s: %d builds, first %s", item.loader, len(builds), builds[0].Version)
	}
}
