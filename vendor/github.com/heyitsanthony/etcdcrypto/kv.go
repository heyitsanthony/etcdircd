package etcdcrypto

import (
	"github.com/coreos/etcd/clientv3"
	pb "github.com/coreos/etcd/etcdserver/etcdserverpb"
	"golang.org/x/net/context"
)

type kvCipher struct {
	clientv3.KV
	Cipher
}

func NewKV(kv clientv3.KV, cipher Cipher) clientv3.KV {
	return &kvCipher{kv, cipher}
}

func (kv *kvCipher) Put(ctx context.Context, key, val string, opts ...clientv3.OpOption) (*clientv3.PutResponse, error) {
	r, err := kv.Do(ctx, clientv3.OpPut(key, val, opts...))
	if err != nil {
		return nil, err
	}
	return r.Put(), nil
}

func (kv *kvCipher) Get(ctx context.Context, key string, opts ...clientv3.OpOption) (*clientv3.GetResponse, error) {
	r, err := kv.Do(ctx, clientv3.OpGet(key, opts...))
	if err != nil {
		return nil, err
	}
	return r.Get(), nil
}

func (kv *kvCipher) Delete(ctx context.Context, key string, opts ...clientv3.OpOption) (*clientv3.DeleteResponse, error) {
	r, err := kv.Do(ctx, clientv3.OpDelete(key, opts...))
	if err != nil {
		return nil, err
	}
	return r.Del(), nil
}

func (kv *kvCipher) Do(ctx context.Context, op clientv3.Op) (clientv3.OpResponse, error) {
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

type txnCipher struct {
	clientv3.Txn
	kv *kvCipher
}

func (kv *kvCipher) Txn(ctx context.Context) clientv3.Txn {
	return &txnCipher{kv.KV.Txn(ctx), kv}
}

func (txn *txnCipher) If(cs ...clientv3.Cmp) clientv3.Txn {
	newCmps := make([]clientv3.Cmp, len(cs))
	for i := range cs {
		newCmps[i] = cs[i]
	}
	txn.Txn = txn.Txn.If(newCmps...)
	return txn
}

func (txn *txnCipher) Then(ops ...clientv3.Op) clientv3.Txn {
	newOps := make([]clientv3.Op, len(ops))
	for i := range ops {
		newOps[i] = txn.kv.encOp(ops[i])
	}
	txn.Txn = txn.Txn.Then(newOps...)
	return txn
}

func (txn *txnCipher) Else(ops ...clientv3.Op) clientv3.Txn {
	newOps := make([]clientv3.Op, len(ops))
	for i := range ops {
		newOps[i] = txn.kv.encOp(ops[i])
	}
	txn.Txn = txn.Txn.Else(newOps...)
	return txn
}

func (txn *txnCipher) Commit() (*clientv3.TxnResponse, error) {
	resp, err := txn.Txn.Commit()
	if err != nil {
		return nil, err
	}
	txn.kv.decTxnResponse(resp)
	return resp, nil
}

func (kv *kvCipher) decGetResponse(resp *clientv3.GetResponse) {
	for i := range resp.Kvs {
		v, err := kv.Decrypt(resp.Kvs[i].Value)
		if err != nil {
			panic(err)
		}
		resp.Kvs[i].Value = v
	}
}

func (kv *kvCipher) decPutResponse(resp *clientv3.PutResponse) {
	if resp.PrevKv != nil {
		v, err := kv.Decrypt(resp.PrevKv.Value)
		if err != nil {
			panic(err)
		}
		resp.PrevKv.Value = v
	}
}

func (kv *kvCipher) decDeleteResponse(resp *clientv3.DeleteResponse) {
	for i := range resp.PrevKvs {
		v, err := kv.Decrypt(resp.PrevKvs[i].Value)
		if err != nil {
			panic(err)
		}
		resp.PrevKvs[i].Value = v
	}
}

func (kv *kvCipher) decTxnResponse(resp *clientv3.TxnResponse) {
	for _, r := range resp.Responses {
		switch tv := r.Response.(type) {
		case *pb.ResponseOp_ResponseRange:
			if tv.ResponseRange != nil {
				kv.decGetResponse((*clientv3.GetResponse)(tv.ResponseRange))
			}
		case *pb.ResponseOp_ResponsePut:
			if tv.ResponsePut != nil {
				kv.decPutResponse((*clientv3.PutResponse)(tv.ResponsePut))
			}
		case *pb.ResponseOp_ResponseDeleteRange:
			if tv.ResponseDeleteRange != nil {
				kv.decDeleteResponse((*clientv3.DeleteResponse)(tv.ResponseDeleteRange))
			}
		default:
		}
	}
}

func (kv *kvCipher) encOp(op clientv3.Op) clientv3.Op {
	if b := op.ValueBytes(); b != nil {
		op.WithValueBytes(kv.Encrypt(b))
	}
	return op
}
