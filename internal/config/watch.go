package config

import (
	"context"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// kubeDataLink is the symlink Kubernetes swaps atomically when it refreshes a
// mounted ConfigMap or Secret volume.
const kubeDataLink = "..data"

// Watch calls onChange (debounced) whenever the file at path is written,
// created, renamed or removed, until ctx is cancelled. It watches the file's
// directory because editors usually replace files via rename. It also reacts to
// the "..data" entry of that directory: Kubernetes updates mounted ConfigMaps
// and Secrets by atomically re-pointing the "..data" symlink, and the file
// itself (a symlink to ..data/<name>) never emits an event. It does nothing
// and returns nil when path is empty. onChange runs on a single goroutine, so
// calls never overlap.
func Watch(ctx context.Context, path string, debounce time.Duration, onChange func()) error {
	if path == "" {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := w.Add(filepath.Dir(abs)); err != nil {
		_ = w.Close()
		return err
	}
	base := filepath.Base(abs)

	go func() {
		defer w.Close()
		timer := time.NewTimer(time.Hour)
		if !timer.Stop() {
			<-timer.C
		}
		pending := false
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if name := filepath.Base(ev.Name); name != base && name != kubeDataLink {
					continue
				}
				if !ev.Has(fsnotify.Write) && !ev.Has(fsnotify.Create) && !ev.Has(fsnotify.Rename) && !ev.Has(fsnotify.Remove) {
					continue
				}
				if pending && !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(debounce)
				pending = true
			case <-w.Errors:
				// Transient watcher errors are ignored; the next event retriggers.
			case <-timer.C:
				pending = false
				onChange()
			}
		}
	}()
	return nil
}
