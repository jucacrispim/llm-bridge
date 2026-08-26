GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test -v ./...
BIN_NAME=llm-bridge
BUILD_DIR=build
BIN_PATH=./$(BUILD_DIR)/$(BIN_NAME)
OUTFLAG=-o $(BIN_PATH)

SCRIPTS_DIR=./scripts/


.PHONY: build # - Creates the binary under the build/ directory
build:
	$(GOBUILD) $(OUTFLAG) cmd/bridge/main.go

.PHONY: build-kb # - Build with knowledge base (ONNX) enabled
build-kb:
	CGO_ENABLED=1 $(GOBUILD) -tags knowledge_onnx $(OUTFLAG) cmd/bridge/main.go

.PHONY: test # - Run all tests
test:
	$(GOBUILD) ./...
	$(GOTEST)

.PHONY: setupenv # - Install needed tools for tests/docs
setupenv:
	$(SCRIPTS_DIR)/env.sh setup-env

.PHONY: docs # - Build documentation
docs:
	$(SCRIPTS_DIR)/env.sh build-docs

.PHONY: coverage # - Run all tests and check coverage
coverage:
	$(SCRIPTS_DIR)/check_coverage.sh

.PHONY: run # - Run the program. You can use `make run ARGS="-host :9090 -root=/"`
run:
	$(GOBUILD) $(OUTFLAG)
	$(BIN_PATH) $(ARGS)

.PHONY: clean # - Remove the files created during build
clean:
	rm -rf $(BUILD_DIR)

.PHONY: install # - Copy the binary to the path
install: build
	go install

.PHONY: uninstall # - Remove the binary from path
uninstall:
	go clean -i llm-bridge/$(BIN_NAME)


all: build build-kb test install

.PHONY: help  # - Show this help text
help:
	@grep '^.PHONY: .* #' Makefile | sed 's/\.PHONY: \(.*\) # \(.*\)/\1 \2/' | expand -t20
