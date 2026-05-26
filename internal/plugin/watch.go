package plugin

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// reloadDebounce coalesces editor save-storms (several write events for one
// save) into a single reload per file.
const reloadDebounce = 200 * time.Millisecond

// watch consumes fsnotify events and hot-reloads changed scripts without
// touching IRC connections. It runs on its own goroutine.
func (h *Host) watch() {
	timers := map[string]*time.Timer{}
	for {
		select {
		case <-h.quit:
			return
		case ev, ok := <-h.watcher.Events:
			if !ok {
				return
			}
			if !strings.HasSuffix(ev.Name, ".lua") {
				continue
			}
			path := ev.Name
			if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				name := scriptName(path)
				h.do(func() { h.unloadScript(name) })
				continue
			}
			if ev.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}
			// Debounce per file.
			if t := timers[path]; t != nil {
				t.Stop()
			}
			timers[path] = time.AfterFunc(reloadDebounce, func() {
				if _, err := filepath.Abs(path); err == nil {
					h.do(func() { h.loadScript(path) })
				}
			})
		case err, ok := <-h.watcher.Errors:
			if !ok {
				return
			}
			h.log.Warn("plugin watcher error", "err", err)
		}
	}
}
