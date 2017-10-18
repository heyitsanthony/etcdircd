package keyencode

import (
	"sync"

	etcd "github.com/coreos/etcd/clientv3"
	"golang.org/x/net/context"
)

type watcherKeyEncode struct {
	etcd.Watcher

	xfm KeyEncoder

	wg       sync.WaitGroup
	stopc    chan struct{}
	stopOnce sync.Once
}

func NewWatcher(w etcd.Watcher, xfm KeyEncoder) etcd.Watcher {
	return &watcherKeyEncode{Watcher: w, xfm: xfm, stopc: make(chan struct{})}
}

func (w *watcherKeyEncode) Watch(ctx context.Context, key string, opts ...etcd.OpOption) etcd.WatchChan {
	encKey := w.xfm.Encode(key)
	// TODO: handle ranges
	wch := w.Watcher.Watch(ctx, encKey, opts...)
	// translate watch events from prefixed to unprefixed
	plaintextWch := make(chan etcd.WatchResponse)
	w.wg.Add(1)
	go func() {
		defer func() {
			close(plaintextWch)
			w.wg.Done()
		}()
		for wr := range wch {
			for i := range wr.Events {
				if kv := wr.Events[i].Kv; len(kv.Key) > 0 {
					v, err := w.xfm.Decode(string(kv.Key))
					if err != nil {
						panic(err)
					}
					kv.Key = []byte(v)
				}
				if wr.Events[i].PrevKv != nil && len(wr.Events[i].PrevKv.Key) > 0 {
					v, err := w.xfm.Decode(string(wr.Events[i].PrevKv.Key))
					if err != nil {
						panic(err)
					}
					wr.Events[i].PrevKv.Key = []byte(v)
				}
			}
			select {
			case plaintextWch <- wr:
			case <-ctx.Done():
				return
			case <-w.stopc:
				return
			}
		}
	}()
	return plaintextWch
}

func (w *watcherKeyEncode) Close() error {
	w.stopOnce.Do(func() { close(w.stopc) })
	w.wg.Wait()
	return nil
}
