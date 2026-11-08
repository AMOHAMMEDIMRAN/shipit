VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/amohammedimran/shipit/internal/cli.Version=$(VERSION)

.PHONY: build test e2e check dist clean

build:            ## build ./shipit for this computer
	go build -ldflags "$(LDFLAGS)" -o shipit ./cmd/shipit

test:             ## unit tests
	go vet ./...
	go test ./...

e2e:              ## end-to-end test against this computer
	./test/e2e-local.sh

check: test e2e   ## everything

dist:             ## binaries for Linux, macOS and Windows in ./dist
	@mkdir -p dist
	@for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do \
	  os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
	  GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o dist/shipit-$$os-$$arch$$ext ./cmd/shipit && echo "built dist/shipit-$$os-$$arch$$ext"; \
	done

clean:
	rm -rf shipit dist
