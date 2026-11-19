package main

import (
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"hjml/internal/engine"
)

func TestDroppedPacksHaveSeparateTaskRows(t *testing.T) {
	dir := t.TempDir()
	a := newApp(engine.Engine{DataDir: dir})
	a.window = &mygo.Window{}
	a.receiveFiles([]string{filepath.Join(dir, "first.mrpack"), filepath.Join(dir, "second.zip")})
	if a.downloadCount() != 2 {
		t.Fatalf("two packs created %d download tasks", a.downloadCount())
	}
	t.Cleanup(func() {
		for _, task := range a.jobs {
			task.cancel()
		}
	})
	test := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Gap(8).Children(func() { a.taskView(c) })
	}, 1020, 675)
	if !test.HasText("first.mrpack") || !test.HasText("second.zip") {
		t.Fatalf("missing pack task rows: %v", test.Texts())
	}
	if err := test.Click("取消 first.mrpack"); err != nil {
		t.Fatal(err)
	}
	if !a.jobs[0].cancelling || a.jobs[1].cancelling {
		t.Fatal("cancelling the first pack affected the second pack")
	}
}
