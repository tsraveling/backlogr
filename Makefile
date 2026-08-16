BINARY := backlogr

# No key is baked in — backlogr prompts for the user's own on first run,
# so `go install ./...` produces exactly what a release does.
LDFLAGS := -s -w

.PHONY: build install run clean

build:
	go build -ldflags '$(LDFLAGS)' -o $(BINARY) .

install:
	go install -ldflags '$(LDFLAGS)' .

run: build
	./$(BINARY)

clean:
	rm -f $(BINARY)
