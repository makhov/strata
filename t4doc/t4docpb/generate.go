package t4docpb

//go:generate protoc -I../.. --go_out=../.. --go_opt=module=github.com/t4db/t4 --go-grpc_out=../.. --go-grpc_opt=module=github.com/t4db/t4 ../../proto/t4doc/v1/document.proto
