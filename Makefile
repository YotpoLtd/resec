# build config
BUILD_DIR 		?= $(abspath build)
GET_GOARCH 		 = $(word 2,$(subst -, ,$1))
GET_GOOS   		 = $(word 1,$(subst -, ,$1))
GOBUILD   		?= $(shell go env GOOS)-$(shell go env GOARCH)
GIT_COMMIT 		:= $(shell git describe --tags)
GIT_DIRTY 		:= $(if $(shell git status --porcelain),+CHANGES)
GO_LDFLAGS 		:= "-X main.Version=$(GIT_COMMIT)$(GIT_DIRTY)"

$(BUILD_DIR):
	mkdir -p $@

# Run Go tests
.PHONY: test
test: requirements
	@echo "=> go test ./..."
	@go test ./...

# Install requirements needed for the project to build
.PHONY: requirements
requirements:
	@echo "=> go get"
	@go get

# Lazy alias for "go install" with custom GO_LDFLAGS enabled
.PHONY: install
install: requirements
	@echo "=> go install"
	@go install

# Shortcut for `go build` with custom GO_LDFLAGS enabled
.PHONY: build
build: requirements
	$(MAKE) -j $(BINARIES)

BINARIES = $(addprefix $(BUILD_DIR)/resec-, $(GOBUILD))
$(BINARIES): $(BUILD_DIR)/resec-%: $(BUILD_DIR)
	@echo "=> building $@ ..."
	GOOS=$(call GET_GOOS,$*) GOARCH=$(call GET_GOARCH,$*) CGO_ENABLED=0 go build -o $@ -ldflags $(GO_LDFLAGS)
