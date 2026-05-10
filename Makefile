.PHONY: proto tla tla-go tla-all

proto:
	go generate ./t4doc/t4docpb

tla:
	./hack/tla.sh

tla-go:
	go test ./tests/tla

tla-all: tla tla-go
