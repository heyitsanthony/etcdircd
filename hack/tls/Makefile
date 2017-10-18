CA= etcdclient etcdcrypto ircd
CERTS=server1-key.pem server2-key.pem server3-key.pem

CFSSL	= @env PATH=$(GOPATH)/bin:$(PATH) cfssl
JSON	= env PATH=$(GOPATH)/bin:$(PATH) cfssljson

all: $(addsuffix -certs, $(CA))

clean:
	rm -rf certs

cfssl:
	go get -u -tags nopkcs11 github.com/cloudflare/cfssl/cmd/cfssl
	go get -u github.com/cloudflare/cfssl/cmd/cfssljson

.PRECIOUS: $(addsuffix /ca.pem,$(addprefix certs/, $(CA)))
certs/%/ca.pem: config/%-ca-csr.json
	mkdir -p certs/$*
	$(CFSSL) gencert -initca config/$*-ca-csr.json | $(JSON) -bare certs/$*/ca

etcdclient-certs: $(addprefix certs/etcdclient/,$(CERTS))
etcdcrypto-certs: $(addprefix certs/etcdcrypto/,$(CERTS))
ircd-certs: $(addprefix certs/ircd/,$(CERTS))

certs/etcdclient/%-key.pem: certs/etcdclient/ca.pem
	$(CFSSL) gencert \
		-ca certs/etcdclient/ca.pem -ca-key certs/etcdclient/ca-key.pem \
		-config config/ca-config.json \
		config/etcdclient-csr.json | $(JSON) -bare `echo $@ | cut -f1 -d'-'`

certs/etcdcrypto/%-key.pem: certs/etcdcrypto/ca.pem
	$(CFSSL) gencert \
		-ca certs/etcdcrypto/ca.pem -ca-key certs/etcdcrypto/ca-key.pem \
		-config config/ca-config.json \
		config/etcdcrypto-csr.json | $(JSON) -bare `echo $@ | cut -f1 -d'-'`

certs/ircd/%-key.pem: certs/ircd/ca.pem
	$(CFSSL) gencert \
		-ca certs/ircd/ca.pem -ca-key certs/ircd/ca-key.pem \
		-config config/ca-config.json \
		config/ircd-csr.json | $(JSON) -bare `echo $@ | cut -f1 -d'-'`

.PHONY: all clean cfssl $(addsuffix -certs, $(CA))
