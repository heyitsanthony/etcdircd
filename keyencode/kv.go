package keyencode

import (
	etcd "github.com/coreos/etcd/clientv3"
	pb "github.com/coreos/etcd/etcdserver/etcdserverpb"
	"golang.org/x/net/context"
)

type kvKeyEncoder struct {
	etcd.KV
	KeyEncoder
}

func NewKV(kv etcd.KV, xfm KeyEncoder) etcd.KV {
	return &kvKeyEncoder{kv, xfm}
}

func (kv *kvKeyEncoder) Put(ctx context.Context, key, val string, opts ...etcd.OpOption) (*etcd.PutResponse, error) {
	r, err := kv.Do(ctx, etcd.OpPut(key, val, opts...))
	if err != nil {
		return nil, err
	}
	return r.Put(), nil
}

func (kv *kvKeyEncoder) Get(ctx context.Context, key string, opts ...etcd.OpOption) (*etcd.GetResponse, error) {
	r, err := kv.Do(ctx, etcd.OpGet(key, opts...))
	if err != nil {
		return nil, err
	}
	return r.Get(), nil
}

func (kv *kvKeyEncoder) Delete(ctx context.Context, key string, opts ...etcd.OpOption) (*etcd.DeleteResponse, error) {
	r, err := kv.Do(ctx, etcd.OpDelete(key, opts...))
	if err != nil {
		return nil, err
	}
	return r.Del(), nil
}

func (kv *kvKeyEncoder) Do(ctx context.Context, op etcd.Op) (etcd.OpResponse, error) {
	r, err := kv.KV.Do(ctx, kv.encOp(op))
	if err != nil {
		return r, err
	}
	switch {
	case r.Get() != nil:
		kv.decGetResponse(r.Get())
	case r.Put() != nil:
		kv.decPutResponse(r.Put())
	case r.Del() != nil:
		kv.decDeleteResponse(r.Del())
	}
	return r, nil
}

type txnKeyEncoder struct {
	etcd.Txn
	kv *kvKeyEncoder
}

func (kv *kvKeyEncoder) Txn(ctx context.Context) etcd.Txn {
	return &txnKeyEncoder{kv.KV.Txn(ctx), kv}
}

func (txn *txnKeyEncoder) If(cs ...etcd.Cmp) etcd.Txn {
	newCmps := make([]etcd.Cmp, len(cs))
	for i := range cs {
		newCmps[i] = cs[i]
		newCmps[i].Key = []byte(txn.kv.Encode(string(cs[i].Key)))
	}
	txn.Txn = txn.Txn.If(newCmps...)
	return txn
}

func (txn *txnKeyEncoder) Then(ops ...etcd.Op) etcd.Txn {
	newOps := make([]etcd.Op, len(ops))
	for i := range ops {
		newOps[i] = txn.kv.encOp(ops[i])
	}
	txn.Txn = txn.Txn.Then(newOps...)
	return txn
}

func (txn *txnKeyEncoder) Else(ops ...etcd.Op) etcd.Txn {
	newOps := make([]etcd.Op, len(ops))
	for i := range ops {
		newOps[i] = txn.kv.encOp(ops[i])
	}
	txn.Txn = txn.Txn.Else(newOps...)
	return txn
}

func (txn *txnKeyEncoder) Commit() (*etcd.TxnResponse, error) {
	resp, err := txn.Txn.Commit()
	if err != nil {
		return nil, err
	}
	txn.kv.decTxnResponse(resp)
	return resp, nil
}

func (kv *kvKeyEncoder) decGetResponse(resp *etcd.GetResponse) {
	for i := range resp.Kvs {
		k, err := kv.Decode(string(resp.Kvs[i].Key))
		if err != nil {
			panic(err)
		}
		resp.Kvs[i].Key = []byte(k)
	}
}

func (kv *kvKeyEncoder) decPutResponse(resp *etcd.PutResponse) {
	if resp.PrevKv != nil {
		k, err := kv.Decode(string(resp.PrevKv.Key))
		if err != nil {
			panic(err)
		}
		resp.PrevKv.Key = []byte(k)
	}
}

func (kv *kvKeyEncoder) decDeleteResponse(resp *etcd.DeleteResponse) {
	for i := range resp.PrevKvs {
		k, err := kv.Decode(string(resp.PrevKvs[i].Key))
		if err != nil {
			panic(err)
		}
		resp.PrevKvs[i].Key = []byte(k)
	}
}

func (kv *kvKeyEncoder) decTxnResponse(resp *etcd.TxnResponse) {
	for _, r := range resp.Responses {
		switch tv := r.Response.(type) {
		case *pb.ResponseOp_ResponseRange:
			if tv.ResponseRange != nil {
				kv.decGetResponse((*etcd.GetResponse)(tv.ResponseRange))
			}
		case *pb.ResponseOp_ResponsePut:
			if tv.ResponsePut != nil {
				kv.decPutResponse((*etcd.PutResponse)(tv.ResponsePut))
			}
		case *pb.ResponseOp_ResponseDeleteRange:
			if tv.ResponseDeleteRange != nil {
				kv.decDeleteResponse((*etcd.DeleteResponse)(tv.ResponseDeleteRange))
			}
		default:
			panic("unknown query")
		}
	}
}

func (kv *kvKeyEncoder) encOp(op etcd.Op) etcd.Op {
	key, end := op.KeyBytes(), op.RangeBytes()
	if key == nil {
		return op
	}
	skey := string(key)
	encKey := kv.Encode(skey)
	op.WithKeyBytes([]byte(encKey))
	if end != nil {
		send := string(end)
		switch send {
		case etcd.GetPrefixRangeEnd(skey):
			op.WithRangeBytes([]byte(etcd.GetPrefixRangeEnd(encKey)))
		case "\x00":
		default:
			panic("don't know how to handle range xfm " + send)
		}
	}
	return op
}
