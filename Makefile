.PHONY: build test server client zip

build: server client

server:
	go build -o bin/leak-server ./server

client:
	go build -o bin/leak-client ./client

test:
	go test ./...

zip:
	cd .. && zip -r proxy-leak-lab.zip proxy-leak-lab -x 'proxy-leak-lab/data/*' 'proxy-leak-lab/bin/*'
