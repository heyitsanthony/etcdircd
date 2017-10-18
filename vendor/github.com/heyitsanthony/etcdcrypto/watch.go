package etcdcrypto

import (
	"sync"

	"github.com/coreos/etcd/clientv3"
	"golang.org/x/net/context"
)

type watcherCipher struct {
	clientv3.Watcher
	Cipher

	wg       sync.WaitGroup
	stopc    chan struct{}
	stopOnce sync.Once
}

func NewWatcher(w clientv3.Watcher, cipher Cipher) clientv3.Watcher {
	return &watcherCipher{Watcher: w, Cipher: cipher, stopc: make(chan struct{})}
}

func (w *watcherCipher) Watch(ctx context.Context, key string, opts ...clientv3.OpOption) clientv3.WatchChan {
	wch := w.Watcher.Watch(ctx, key, opts...)
	// translate watch events from prefixed to unprefixed
	plaintextWch := make(chan clientv3.WatchResponse)
	w.wg.Add(1)
	go func() {
		defer func() {
			close(plaintextWch)
			w.wg.Done()
		}()
		for wr := range wch {
			for i := range wr.Events {
				kv := wr.Events[i].Kv
				if len(kv.Value) > 0 {
					v, err := w.Decrypt(kv.Value)
					if err != nil {
						panic(err)
					}
					kv.Value = v
				}
				if wr.Events[i].PrevKv != nil && len(wr.Events[i].PrevKv.Value) > 0 {
					v, err := w.Decrypt(wr.Events[i].PrevKv.Value)
					if err != nil {
						panic(err)
					}
					wr.Events[i].PrevKv.Value = v
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

func (w *watcherCipher) Close() error {
	err := w.Watcher.Close()
	w.stopOnce.Do(func() { close(w.stopc) })
	w.wg.Wait()
	return err
}
