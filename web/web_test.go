package web

import (
	"testing"
	"testing/fstest"
)

func TestAssetVersionChangesWithSettingsDependencies(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"index.html", "app.js", "cloud-storage.js", "workspace-tools.js", "api.js", "style.css"} {
		assets := fstest.MapFS{"index.html": {Data: []byte("index")}, "app.js": {Data: []byte("app")}, "cloud-storage.js": {Data: []byte("cloud")}, "api.js": {Data: []byte("api")}, "style.css": {Data: []byte("styles")}}
		assets["workspace-tools.js"] = &fstest.MapFile{Data: []byte("workspace")}
		before, err := bundleVersion(assets)
		if err != nil {
			t.Fatal(err)
		}
		again, err := bundleVersion(assets)
		if err != nil || before != again {
			t.Fatal("asset version is unstable")
		}
		assets[name].Data = append(assets[name].Data, []byte("changed")...)
		after, err := bundleVersion(assets)
		if err != nil {
			t.Fatal(err)
		}
		if before == after {
			t.Fatalf("changing %s reused an old asset version", name)
		}
	}
}
