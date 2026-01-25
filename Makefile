.PHONY: build run

build:
	go build -o arshes ./cmd/arshes

run:
	go run ./cmd/arshes serve
