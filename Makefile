all:
	go build ./cmd/etcdircd/
	go build ./cmd/ircdctl/


# go get honnef.co/go/tools/cmd/{gosimple,staticcheck,unused}
FMT = fmt-gosimple fmt-unused fmt-staticcheck fmt-fmt fmt-vet
.PHONY: fmt $(FMT)
fmt: $(FMT)

fmt-gosimple:
	! ( gosimple . 2>&1 | grep -v vendor | grep : )
fmt-unused:
	! ( unused . 2>&1 | grep -v vendor | grep : )
fmt-staticcheck:
	! ( staticcheck . 2>&1 | grep -v vendor | grep : )
fmt-vet:
	go vet -shadow ./...
	go vet ./...
fmt-fmt:
	! ( gofmt -l -s . 2>&1 | grep -v vendor)