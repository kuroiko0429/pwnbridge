.PHONY: build cross test check install-user clean

build:
	mkdir -p bin
	go build -trimpath -o bin/pwnbridge ./cmd/pwnbridge
	go build -trimpath -o bin/pwnbridge-pan-pair ./tools/pan-pair

cross:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bin/pwnbridge-agent.arm64 ./cmd/pwnbridge-agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bin/pwnbridge-pan.arm64 ./cmd/pwnbridge-pan
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bin/pwnbridge-pan-pair.arm64 ./tools/pan-pair

test:
	go test -race ./...
	python3 -m unittest discover -s device/tests -p 'test*.py' -v
	python3 -m unittest discover -s scripts/tests -p 'test*.py' -v

check:
	go vet ./...

# Atomic user installation; preserves private config. Never enables/restarts services.
install-user: build
	python3 scripts/install-user.py
	systemctl --user daemon-reload

clean:
	rm -f bin/pwnbridge bin/pwnbridge-pan-pair bin/pwnbridge-agent.arm64 bin/pwnbridge-pan.arm64 bin/pwnbridge-pan-pair.arm64
