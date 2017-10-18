# etcdircd

An encrypted irc daemon backed by [etcd](https://github.com/coreos/etcd).
 
## Running etcdircd

Install `go`, then build etcd and etcdircd:

```sh
$ go get github.com/heyitsanthony/etcdircd/cmd/etcdircd
$ go get github.com/coreos/etcd/cmd/etcd
```

Launch an etcd server and an etcdircd server:

```sh
$ etcd &
$ etcdircd &
```

Connect with an irc client:

```sh
$ irssi -n mynick --connect localhost
```

If it all worked, the client will connect and display the welcome banner. See the [security guide](security.md) to encrypt everything.

## How it works

An etcdircd server communicates with other etcdircd servers through etcd. Each server receives messages by watching on etcd keys; network disconnection is tolerated by resuming the watches. If an etcdircd server permanently loses contact with etcd, its resources are automatically released through session expiration. 

User information is stored under `/user/` with a given `<nick>`. The key `/user/ctl/<nick>` holds user metadata including modes, name, and joined channels. The key `/user/msg/<nick>` forwards IRC messages to `<nick>`'s connection. For example, writing a `PRIVMSG` message to `/user/msg/<nick>` will send a private message to `<nick>`.

Channel information is similar; its keys are prefixed with `/chan/` and take a given `<channel>`. The key `/chan/ctl/<channel>` holds metadata such as the channel topic. The key `/chan/msg/<nick>` forwards IRC messages to all users on the channel. To notify channels when users disconnect, joined users are written to per-server keys `/chan/nicks/<channel>/<session>`; when the session expires, the key deletion event signals server loss and all the server's users are removed from the channel.

## Limitations

etcdircd is still under active development. It's enough to chat, but not much else!

etcd backend encryption is missing:

* Key revocation
* Key rotation (2^32 messages before risking repeat IV)
* Replay attack protection
* Certificates other than 4096-bit RSA
* Physical attestation

The irc frontend:

* Many IRC commands and modes
* Services
* Server linking

## Contact

Use the [issue tracker](https://github.com/heyitsanthony/etcdircd/issues)

Or try the etcd channel on freenode.

## License

etcdircd is under the AGPLv3 license. See [LICENSE](LICENSE) for details.
