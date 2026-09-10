APP_DIR := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
ROOT_DIR := $(abspath $(APP_DIR)/../..)
GOBIN := $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)
VERSION := $(shell git -C $(ROOT_DIR) describe --tags --always)
API_PROTO_FILES := $(shell cd $(ROOT_DIR)/api && find . -name '*.proto' | LC_ALL=C sort)
KRATOS_TOOLS_VERSION := v2.0.0-20260404020628-f149714c1d54

.NOTPARALLEL:
.PHONY: init check-protoc api config db wire generate all build help
init:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.1
	go install github.com/go-kratos/kratos/cmd/kratos/v2@$(KRATOS_TOOLS_VERSION)
	go install github.com/go-kratos/kratos/cmd/protoc-gen-go-http/v2@$(KRATOS_TOOLS_VERSION)
	go install github.com/go-kratos/kratos/cmd/protoc-gen-go-errors/v2@$(KRATOS_TOOLS_VERSION)
	go install github.com/google/gnostic/cmd/protoc-gen-openapi@v0.7.1
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
	go install github.com/golang/mock/mockgen@v1.6.0
	go install github.com/google/wire/cmd/wire@v0.7.0

check-protoc:
	@test "$$(protoc --version)" = "libprotoc 36.1" || (echo 'protoc 36.1 is required'; exit 1)

api: check-protoc
	cd $(ROOT_DIR)/api && protoc --proto_path=. --proto_path=$(ROOT_DIR)/third_party \
		--go_out=paths=source_relative:. --go-http_out=paths=source_relative:. \
		--go-grpc_out=paths=source_relative:. --go-errors_out=paths=source_relative:. \
		--openapi_out=fq_schema_naming=true,default_response=false:$(ROOT_DIR) $(API_PROTO_FILES)
	cp $(ROOT_DIR)/openapi.yaml $(APP_DIR)/openapi.yaml

config: check-protoc
	cd $(APP_DIR) && protoc --proto_path=internal --proto_path=$(ROOT_DIR)/third_party \
		--go_out=paths=source_relative:internal conf/conf.proto

db:
	cd $(APP_DIR) && sqlc generate
	cd $(APP_DIR) && mockgen -source=internal/data/db/querier.go -destination=internal/data/db/mock/querier_mock.go

wire:
	cd $(APP_DIR)/cmd/mall && wire

# Explicit submakes keep the generator dependency order even under make -j.
generate:
	$(MAKE) -f $(APP_DIR)/Makefile api
	$(MAKE) -f $(APP_DIR)/Makefile config
	$(MAKE) -f $(APP_DIR)/Makefile db
	$(MAKE) -f $(APP_DIR)/Makefile wire

all: generate
build:
	cd $(ROOT_DIR) && go build -ldflags "-X main.Version=$(VERSION)" -o $(APP_DIR)/bin/mall ./app/mall/cmd/mall
help:
	@echo 'Targets: init generate api config db wire build'
.DEFAULT_GOAL := help
