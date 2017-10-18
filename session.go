package etcdircd

import (
	"context"
	"sync"
	"time"

	etcd "github.com/coreos/etcd/clientv3"
	v3sync "github.com/coreos/etcd/clientv3/concurrency"
	"github.com/golang/glog"
)

// etcdSession maintains a single session. If the session is lost, a new one is generated.
type etcdSession struct {
	cli *etcd.Client

	mu     sync.RWMutex
	readyc chan struct{}
	s      *v3sync.Session

	cancel context.CancelFunc
	ctx    context.Context
	donec  chan struct{}
}

func newEtcdSession(cli *etcd.Client) *etcdSession {
	ctx, cancel := context.WithCancel(cli.Ctx())
	es := &etcdSession{
		cli:    cli,
		readyc: make(chan struct{}),
		ctx:    ctx,
		cancel: cancel,
		donec:  make(chan struct{}),
	}
	go func() {
		defer func() {
			if es.s != nil {
				es.s.Close()
			}
			close(es.donec)
		}()
		// TODO use rate package
		for {
			s, err := v3sync.NewSession(cli, v3sync.WithTTL(5), v3sync.WithContext(ctx))
			if err != nil {
				glog.V(9).Infof("session: %v", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
				continue
			}
			es.mu.Lock()
			es.s = s
			es.mu.Unlock()
			close(es.readyc)
			select {
			case <-ctx.Done():
				return
			case <-s.Done():
			}
			es.mu.Lock()
			es.readyc = make(chan struct{})
			es.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
	return es
}

func (es *etcdSession) Session() (*v3sync.Session, error) {
	es.mu.RLock()
	rc := es.readyc
	es.mu.RUnlock()
	select {
	case <-rc:
	case <-es.ctx.Done():
		return nil, es.ctx.Err()
	}
	es.mu.RLock()
	s := es.s
	es.mu.RUnlock()
	return s, es.ctx.Err()
}

func (es *etcdSession) Close() {
	es.cancel()
	<-es.donec
}
