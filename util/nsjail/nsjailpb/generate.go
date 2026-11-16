// Package nsjailpb contains Go types for nsjail's configuration file.
//
// config.proto is copied verbatim from https://github.com/google/nsjail at commit d1d9af3.
// To regenerate config.pb.go, install protoc and protoc-gen-go, then run go generate.
package nsjailpb

//go:generate protoc --go_out=. --go_opt=paths=source_relative --go_opt=Mconfig.proto=github.com/fumin/httproxy/util/nsjail/nsjailpb config.proto
