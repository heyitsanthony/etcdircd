# Security guide

This guide covers the tedious and arduous details for securing etcdircd. At minimum, the system's goal is to protect information about an IRC network from leaking to an untrusted party. Four classes of untrusted access are considered:

* Network; peer and client traffic
* Storage; data at rest
* Machine; running process
* Server APIs

## Demo secure mode

This section describes how to set up test certificates for a single local instance of etcdircd. The intent is to confirm that the system works in a simple encrypted configuration.

### Generate certificates

Public keys are necessary to secure etcdircd. Generate keys with `cfssl`:

```sh
$ cd hack/tls/
$ make cfssl
$ make
```

Certificates and keys should now be in `hack/tls/certs/` under `ircd`, `etcdclient`, and `etcdcrypto`. These files secure network traffic and data at rest.

### Run server

Build the binaries:

```sh
$ go get github.com/coreos/etcd/cmd/etcd
$ go get github.com/heyitsanthony/etcdircd/cmd/etcdircd
$ go get github.com/mattn/goreman
```

Use the [Procfile](Procfile) to launch `etcdircd` with the demo configuration:

```sh
$ goreman -f Procfile start
```

### Confirm encryption

Check etcd TLS works by trying to access the etcd server in plaintext:

```sh
$ ETCDCTL_API=3 etcdctl --endpoints=http://localhost:2379 get --prefix /
```

Check IRC data is encrypted at rest by inspecting etcd. First log in as `someuser` on the ircd and join `#somechannel`. Next, check if any keys or values contain `someuser` or `somechannel`:

```sh
$ ETCDCTL_API=3 etcdctl --endpoints=https://localhost:2379 -w fields get --prefix / | grep -E "(someuser|somechannel)"
```

If there matches, then the encryption isn't active.

Alternatively, try snooping IRC messages by watching all keys prefixed with `/`:

```sh
$ ETCDCTL_API=3 etcdctl --endpoints=https://localhost -w fields get --prefix /
```

## etcd traffic

Protect traffic between the ircd and etcd with TLS. The etcd backend should be encrypted, so etcd TLS is not needed to conceal IRC data. However, if using etcd's role-based access control without common name certificates, it's a good idea to have TLS for protecting etcd user passwords.

Pass `--etcdclient-yaml` a file with the yaml for an etcd3 client:

```yaml
endpoints: ['https://localhost:2379']
ca-file: 'hack/tls/certs/etcdclient/ca.pem'
```

Other client security features supported by etcd's yaml client configuration, such as client authentication, should also work.

## IRC traffic

Protect IRC protocol traffic from eavesdropping with TLS. Pass `--etcdircd-yaml` a file to set it up:

```yaml
IrcdKeyFile: 'hack/tls/certs/ircd/server1-key.pem'
IrcdCertFile: 'hack/tls/certs/ircd/server1.pem'
IrcdCAFile: 'hack/tls/certs/ircd/ca.pem'
```

Confirm the ircd is serving TLS:

```
$ openssl s_client -CAFile hack/tls/certs/ircd/ca.pem -showcerts -verify_return_error -debug -connect localhost:6667 
```

Test the TLS with the following `irssi.config`:

```
servers = (
	{
		address = "localhost";
		port = "6667";
		use_ssl = "yes";
		ssl_verify = "yes";
		ssl_cafile = "hack/tls/certs/ircd/ca.pem";
	},
);
```

Feed the configuration file into irssi and connect:

```
$ irssi --config irssi.config -connect localhost
```

## etcd backend

To avoid interception through etcd, all IRC data is AES-GCM encrypted. The AES key is negotiated over etcd by etcdircd servers using RSA key exchange.

Set up the key exchange files in the etcdircd yaml file:

```yaml
EtcdCryptoKeyFile: 'hack/tls/certs/etcdcrypto/server1-key.pem'
EtcdCryptoCertFile: 'hack/tls/certs/etcdcrypto/server1.pem'
EtcdCryptoCAFile: 'hack/tls/certs/etcdcrypto/ca.pem'
```

Important: the private keys should not be shared with etcd.

## Machine

This section describes ways etcdircd can mitigate compromise of a server machine.

TODO: CRLs, key rotation, physical attestation, mlock keys

### Enforced OTR mode

Off-the-record (OTR) communication provides end-to-end encryption between clients. OTR prevents eavesdropping by the IRC server since the messages can only be decrypted by the the end user. Many IRC clients support OTR functionality through [libOTR](https://otr.cypherpunks.ca/). By using etcdircd's `E` (i.e., Encrypted) mode, the server will reject any non-OTR private message sent to or from the user.

A client dismisses non-OTR private message by setting the mode:

```
/umode +E
```

To force OTR regardless of user configuration, pin the mode in the etcdircd yaml file:

```yaml
PinnedUserModes: 'E'
```

## APIs

This section describes protection mechanisms supported by etcdircd when communicating over the IRC protocol.

TODO: channel keys

### Client certificate authentication

Restrict users that can connect to the ircd by enabling client certificate authentication in the etcdircd yaml:

```yaml
IrcdClientCerts: true
```

